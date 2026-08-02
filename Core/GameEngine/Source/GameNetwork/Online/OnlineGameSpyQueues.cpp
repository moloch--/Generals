/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "PreRTS.h"

// The precompiled/game compatibility headers may expose function macros that corrupt modern C++ headers on MinGW.
#ifdef min
#undef min
#endif
#ifdef max
#undef max
#endif
#ifdef snprintf
#undef snprintf
#endif

#include <nlohmann/json.hpp>

#include <algorithm>
#include <array>
#include <atomic>
#include <cctype>
#include <cstdint>
#include <cstring>
#include <deque>
#include <limits>
#include <map>
#include <mutex>
#include <set>
#include <string>
#include <unordered_map>
#include <utility>
#include <vector>

#include "Common/GlobalData.h"
#include "GameNetwork/FirewallHelper.h"
#include "GameNetwork/GameInfo.h"
#include "GameNetwork/GameSpy/BuddyThread.h"
#include "GameNetwork/GameSpy/PeerThread.h"
#include "GameNetwork/GameSpy/PersistentStorageThread.h"
#include "GameNetwork/GameSpy/ThreadUtils.h"
#include "GameNetwork/Online/OnlineControlClient.h"
#include "GameNetwork/Online/OnlineEndpoint.h"
#include "GameNetwork/Online/OnlineGameOptions.h"
#include "GameNetwork/Online/OnlineGameSpyQueues.h"
#include "GameNetwork/Online/OnlineSessionState.h"

#ifdef _WIN32
#include "WWLib/mutex.h"
#endif

// Keep the legacy macros out of this translation unit's implementation as well.
#ifdef min
#undef min
#endif
#ifdef max
#undef max
#endif
#ifdef snprintf
#undef snprintf
#endif

namespace GeneralsOnline
{
namespace
{

using Json = nlohmann::json;

#ifdef _WIN32
using OnlineMutex = CriticalSectionClass;
class OnlineLock
{
public:
	explicit OnlineLock(OnlineMutex &mutex) : m_lock(mutex) {}

private:
	CriticalSectionClass::LockClass m_lock;
};
#else
using OnlineMutex = std::mutex;
using OnlineLock = std::lock_guard<OnlineMutex>;
#endif

constexpr std::size_t kMaximumAdapterResponses = 512U;
constexpr std::size_t kMaximumPendingRequests = 256U;
constexpr std::size_t kMaximumNoncriticalPeerResponses = 480U;
constexpr std::size_t kMaximumChatLength = 512U;
constexpr std::size_t kMaximumGameOptionLength = 4096U;
constexpr const char *kUTMChatPrefix = "__GX_UTM1__";
#if RTS_ZEROHOUR
constexpr const char *kOnlineProduct = "zerohour";
#else
constexpr const char *kOnlineProduct = "generals";
#endif

OnlineGameCompatibility LocalCompatibility(std::uint32_t iniCRC)
{
	return OnlineGameCompatibility{kOnlineProduct, kOnlineCompatibilityVersion, iniCRC};
}

OnlineGameCompatibility LocalCompatibility()
{
	return LocalCompatibility(TheGlobalData ? static_cast<std::uint32_t>(TheGlobalData->m_iniCRC) : 0U);
}

Json CompatibilityRequest(std::uint32_t iniCRC)
{
	return Json{
		{"product", kOnlineProduct},
		{"compatibility_version", kOnlineCompatibilityVersion},
		{"ini_crc", iniCRC},
	};
}

template<typename T> class BoundedQueue
{
public:
	void push(const T &value)
	{
		OnlineLock lock(m_mutex);
		if (m_values.size() == kMaximumAdapterResponses)
			m_values.pop_front();
		m_values.push_back(value);
	}

	bool pop(T &value)
	{
		OnlineLock lock(m_mutex);
		if (m_values.empty())
			return false;
		value = m_values.front();
		m_values.pop_front();
		return true;
	}

private:
	OnlineMutex m_mutex;
	std::deque<T> m_values;
};

bool IsCriticalPeerResponse(const PeerResponse &response)
{
	if (response.peerResponseType == PeerResponse::PEERRESPONSE_PLAYERUTM && response.command == "KICK")
		return true;
	switch (response.peerResponseType)
	{
		case PeerResponse::PEERRESPONSE_DISCONNECT:
		case PeerResponse::PEERRESPONSE_GAMESTART:
		case PeerResponse::PEERRESPONSE_FAILEDTOHOST:
		case PeerResponse::PEERRESPONSE_PLAYERLEFT:
		case PeerResponse::PEERRESPONSE_LOGIN:
		case PeerResponse::PEERRESPONSE_JOINGROUPROOM:
		case PeerResponse::PEERRESPONSE_CREATESTAGINGROOM:
		case PeerResponse::PEERRESPONSE_JOINSTAGINGROOM:
		case PeerResponse::PEERRESPONSE_QUICKMATCHSTATUS:
			return true;
		default:
			return false;
	}
}

// GeneralsX @bugfix OpenAI 02/08/2026 Classify only the presence values needed by the retail Loading transition policy.
OnlineBuddyStatusKind ClassifyOnlineBuddyStatus(int status)
{
	if (status == GP_PLAYING)
		return OnlineBuddyStatusKind::Playing;
	if (status == GP_ONLINE)
		return OnlineBuddyStatusKind::Online;
	return OnlineBuddyStatusKind::Other;
}

class PeerResponseQueue
{
public:
	void push(const PeerResponse &value)
	{
		OnlineLock lock(m_mutex);
		if (!IsCriticalPeerResponse(value))
		{
			if (m_values.size() >= kMaximumNoncriticalPeerResponses)
				return;
		}
		else if (m_values.size() >= kMaximumAdapterResponses)
		{
			const auto expendable = std::find_if(
				m_values.begin(), m_values.end(), [](const PeerResponse &queued) {
					return !IsCriticalPeerResponse(queued);
				});
			if (expendable != m_values.end())
				m_values.erase(expendable);
			else
				m_values.pop_front();
		}
		m_values.push_back(value);
	}

	bool pop(PeerResponse &value)
	{
		OnlineLock lock(m_mutex);
		if (m_values.empty())
			return false;
		value = m_values.front();
		m_values.pop_front();
		return true;
	}

private:
	OnlineMutex m_mutex;
	std::deque<PeerResponse> m_values;
};

template<std::size_t Size> void CopyString(char (&destination)[Size], const std::string &source)
{
	static_assert(Size > 0, "A string destination must include a terminator");
	const std::size_t length = source.size() < Size - 1U ? source.size() : Size - 1U;
	std::memcpy(destination, source.data(), length);
	destination[length] = '\0';
}

template<std::size_t Size> void CopyWideString(WideChar (&destination)[Size], const std::wstring &source)
{
	static_assert(Size > 0, "A string destination must include a terminator");
	const std::size_t length = source.size() < Size - 1U ? source.size() : Size - 1U;
	std::copy_n(source.data(), length, destination);
	destination[length] = L'\0';
}

std::string Truncate(const std::string &value, std::size_t maximum)
{
	return value.size() <= maximum ? value : value.substr(0, maximum);
}

UnsignedInt JsonCounter(const Json &object, const char *key)
{
	const auto value = object.find(key);
	if (value == object.end())
		return 0;
	std::uint64_t counter = 0;
	if (value->is_number_unsigned())
		counter = value->get<std::uint64_t>();
	else if (value->is_number_integer())
	{
		const std::int64_t signedCounter = value->get<std::int64_t>();
		if (signedCounter <= 0)
			return 0;
		counter = static_cast<std::uint64_t>(signedCounter);
	}
	else
	{
		return 0;
	}
	return static_cast<UnsignedInt>(std::min<std::uint64_t>(
		counter, (std::numeric_limits<UnsignedInt>::max)()));
}

std::string NormalizeCommand(std::string command)
{
	while (!command.empty() && command.back() == '/')
		command.pop_back();
	return command;
}

std::uint32_t VirtualIPv4ForSlot(int slot)
{
	if (slot < 0 || slot >= static_cast<int>(kOnlineServiceSlotCount))
		return 0;
	return OnlineIPv4HostToNetwork(OnlineVirtualIPv4HostOrder(static_cast<std::uint8_t>(slot)));
}

bool DecodeHex(const std::string &encoded, std::array<std::uint8_t, kOnlineRelayTokenSize> &decoded)
{
	if (encoded.size() != decoded.size() * 2U)
		return false;
	auto nibble = [](char value) -> int {
		if (value >= '0' && value <= '9')
			return value - '0';
		if (value >= 'a' && value <= 'f')
			return value - 'a' + 10;
		return -1;
	};
	for (std::size_t index = 0; index < decoded.size(); ++index)
	{
		const int high = nibble(encoded[index * 2U]);
		const int low = nibble(encoded[index * 2U + 1U]);
		if (high < 0 || low < 0)
			return false;
		decoded[index] = static_cast<std::uint8_t>((high << 4) | low);
	}
	return true;
}

struct ServiceMember
{
	std::uint64_t userId = 0;
	std::string name;
	bool host = false;
	bool ready = false;
	int slot = -1;
};

struct ServiceGame
{
	std::string gameId;
	std::string name;
	std::string map;
	std::string hostName;
	int players = 0;
	int maximumPlayers = MAX_SLOTS;
	bool hasPassword = false;
	std::string state;
	OnlineGameCompatibility compatibility;
	Json options = Json::object();
	std::vector<ServiceMember> members;
};

ServiceMember ParseMember(const Json &value)
{
	ServiceMember member;
	member.userId = value.value("user_id", std::uint64_t{0});
	member.name = value.value("display_name", std::string());
	member.host = value.value("host", false);
	member.ready = value.value("ready", false);
	member.slot = value.value("slot", -1);
	return member;
}

OnlineGameMemberSummary MemberSummary(const ServiceMember &member)
{
	return OnlineGameMemberSummary{member.userId, member.name, member.host, member.ready, member.slot};
}

ServiceGame ParseGame(const Json &value)
{
	ServiceGame game;
	game.gameId = value.value("game_id", std::string());
	game.name = value.value("name", std::string());
	game.map = value.value("map", std::string());
	game.hostName = value.value("host_name", std::string());
	game.players = value.value("players", 0);
	game.maximumPlayers = value.value("max_players", MAX_SLOTS);
	game.hasPassword = value.value("has_password", false);
	game.state = value.value("state", std::string("open"));
	game.compatibility.product = value.value("product", std::string());
	game.compatibility.version = value.value("compatibility_version", 0);
	game.compatibility.iniCRC = static_cast<std::uint32_t>(JsonCounter(value, "ini_crc"));
	if (value.contains("options") && value["options"].is_object())
	{
		game.options = value["options"];
		if (game.map.empty())
			game.map = game.options.value("map", std::string());
	}
	if (value.contains("members") && value["members"].is_array())
	{
		for (const Json &member : value["members"])
			game.members.push_back(ParseMember(member));
	}
	return game;
}

OnlineGameBrowserSummary BrowserSummary(const ServiceGame &game)
{
	return OnlineGameBrowserSummary{
		game.gameId,
		game.name,
		game.map,
		game.hostName,
		game.state,
		game.players,
		game.maximumPlayers,
		game.hasPassword,
		game.compatibility,
	};
}

class OnlineBuddyMessageQueue;
class OnlinePeerMessageQueue;
class OnlinePersistentStorageMessageQueue;

class OnlineServiceSession
{
public:
	void attach(OnlineBuddyMessageQueue *queue);
	void attach(OnlinePeerMessageQueue *queue);
	void attach(OnlinePersistentStorageMessageQueue *queue);
	void detach(OnlineBuddyMessageQueue *queue);
	void detach(OnlinePeerMessageQueue *queue);
	void detach(OnlinePersistentStorageMessageQueue *queue);

	void handle(const BuddyRequest &request);
	void handle(const PeerRequest &request);
	void handle(const PSRequest &request);

	bool isConnected() const;
	bool isConnecting() const;
	GPProfile localProfile() const;

private:
	enum class Operation
	{
		Ignore,
		Authenticate,
		BuddyList,
		RoomList,
		RoomJoin,
		GameList,
		GameCreate,
		GameJoin,
		GameOptions,
		GameReady,
		GameStart,
		GameStartReady,
		StatsGet,
		Quickmatch,
	};

	struct Pending
	{
		Operation operation = Operation::Ignore;
		int integer = 0;
		bool registration = false;
	};

	void attachCommon();
	void detachCommon();
	bool startControl(const OnlineEndpoint &endpoint, std::string &error);
	void handleAuthentication(const BuddyRequest &request);
	void resetStateLocked();
	void clearGameStateLocked();
	bool sendRequestLocked(const std::string &type, const Json &data, Pending pending);
	bool sendRequestLocked(const std::string &type, const Json &data)
	{
		return sendRequestLocked(type, data, Pending{});
	}
	void onLine(const std::string &line);
	void onConnectionState(bool connected, const std::string &detail);
	void handleResponseLocked(const Pending &pending, bool ok, const std::string &code, const std::string &error, const Json &data);
	void handleEventLocked(const std::string &type, const Json &data);

	int profileIdLocked(std::uint64_t userId);
	std::uint64_t serviceUserIdLocked(int profileId) const;
	int roomIdLocked(const std::string &roomId);
	int gameIdLocked(const std::string &gameId);
	std::string serviceGameIdLocked(int gameId) const;
	std::string serviceRoomIdLocked(int roomId) const;
	std::string memberNameLocked(std::uint64_t userId) const;

	void emitBuddyLocked(const BuddyResponse &response);
	void emitPeerLocked(const PeerResponse &response);
	void emitPSLocked(const PSResponse &response);
	void emitDisconnectLocked(DisconnectReason reason, const std::string &detail);
	void emitFailedToHostLocked(const std::string &detail);
	void emitPreGameFailureLocked(const std::string &detail);
	void emitPeerLoginLocked();
	void emitBuddyListLocked(const Json &data);
	void emitRoomListLocked(const Json &rooms);
	void applyRoomSnapshotLocked(const Json &room);
	void emitGameListLocked(const Json &games, bool clearFirst);
	void emitGameSummaryLocked(const ServiceGame &game, int action, int percentComplete);
	void applyGameSnapshotLocked(const ServiceGame &game, bool suppressLocalJoin = false);
	void emitPlayerLocked(const ServiceMember &member, RoomType roomType, int responseType);
	void emitSlotListLocked(const ServiceGame &game);
	void emitChatLocked(const std::string &type, const Json &data);
	void applyRelayCredentialsLocked(const Json &data);
	void handleBuddyEventLocked(const std::string &type, const Json &data);
	void handleQuickmatchLocked(const ServiceGame &game);
	void emitQuickmatchLocked(const ServiceGame &game);
	void handleGameGoLocked(const Json &data);
	void sendGameResultsLocked(const std::string &packet);
	void sendUTMLocked(const PeerRequest &request);

	mutable OnlineMutex m_mutex;
	OnlineControlClient m_client;
	OnlineBuddyMessageQueue *m_buddy = nullptr;
	OnlinePeerMessageQueue *m_peer = nullptr;
	OnlinePersistentStorageMessageQueue *m_ps = nullptr;
	int m_attachmentCount = 0;
	bool m_authenticated = false;
	bool m_authenticating = false;
	bool m_expectedClose = false;
	bool m_connectionFailed = false;
	std::string m_lastConnectionError;
	std::uint64_t m_nextRequestId = 1;
	std::unordered_map<std::string, Pending> m_pending;
	std::uint64_t m_localUserId = 0;
	int m_localProfileId = 0;
	std::string m_localName;
	std::string m_authName;
	std::string m_authUsername;
	std::string m_authPassword;
	bool m_persistentProfile = false;
	std::unordered_map<std::uint64_t, int> m_profiles;
	std::unordered_map<int, std::uint64_t> m_serviceUsers;
	std::unordered_map<std::string, int> m_rooms;
	std::unordered_map<int, std::string> m_serviceRooms;
	int m_nextRoomId = 1;
	std::unordered_map<std::string, int> m_games;
	std::unordered_map<int, std::string> m_serviceGames;
	int m_nextGameId = 1;
	std::map<std::string, ServiceGame> m_publicGames;
	bool m_gameListActive = false;
	std::map<std::uint64_t, ServiceMember> m_roomMembers;
	ServiceGame m_currentGame;
	bool m_hasCurrentGame = false;
	std::vector<ServiceMember> m_startedRoster;
	ServiceGame m_pendingQuickmatch;
	bool m_hasPendingQuickmatch = false;
	std::string m_onlineRelayGameId;
	std::string m_emittedQuickmatchGameId;
	bool m_gameStarted = false;
	bool m_gameEnding = false;
	bool m_gameCredentialsReady = false;
	OnlineBuddyStatusPolicy m_buddyStatusPolicy;
};

OnlineServiceSession &Service()
{
	static OnlineServiceSession service;
	return service;
}

class OnlineBuddyMessageQueue final : public GameSpyBuddyMessageQueueInterface
{
public:
	~OnlineBuddyMessageQueue() override { endThread(); }
	void startThread() override
	{
		if (!m_attached.exchange(true))
			Service().attach(this);
	}
	void endThread() override
	{
		if (m_attached.exchange(false))
			Service().detach(this);
	}
	Bool isThreadRunning() override { return m_attached.load(); }
	Bool isConnected() override { return m_attached.load() && Service().isConnected(); }
	Bool isConnecting() override { return m_attached.load() && Service().isConnecting(); }
	void addRequest(const BuddyRequest &request) override { Service().handle(request); }
	Bool getRequest(BuddyRequest &) override { return false; }
	void addResponse(const BuddyResponse &response) override { m_responses.push(response); }
	Bool getResponse(BuddyResponse &response) override { return m_responses.pop(response); }
	GPProfile getLocalProfileID() override { return Service().localProfile(); }

private:
	std::atomic<bool> m_attached{false};
	BoundedQueue<BuddyResponse> m_responses;
};

class OnlinePeerMessageQueue final : public GameSpyPeerMessageQueueInterface
{
public:
	~OnlinePeerMessageQueue() override { endThread(); }
	void startThread() override
	{
		if (!m_attached.exchange(true))
			Service().attach(this);
	}
	void endThread() override
	{
		if (m_attached.exchange(false))
			Service().detach(this);
	}
	Bool isThreadRunning() override { return m_attached.load(); }
	Bool isConnected() override { return m_attached.load() && Service().isConnected(); }
	Bool isConnecting() override { return m_attached.load() && Service().isConnecting(); }
	void addRequest(const PeerRequest &request) override { Service().handle(request); }
	Bool getRequest(PeerRequest &) override { return false; }
	void addResponse(const PeerResponse &response) override { m_responses.push(response); }
	Bool getResponse(PeerResponse &response) override { return m_responses.pop(response); }
	SerialAuthResult getSerialAuthResult() override { return SERIAL_OK; }

private:
	std::atomic<bool> m_attached{false};
	PeerResponseQueue m_responses;
};

class OnlinePersistentStorageMessageQueue final : public GameSpyPSMessageQueueInterface
{
public:
	~OnlinePersistentStorageMessageQueue() override { endThread(); }
	void startThread() override
	{
		if (!m_attached.exchange(true))
			Service().attach(this);
	}
	void endThread() override
	{
		if (m_attached.exchange(false))
			Service().detach(this);
	}
	Bool isThreadRunning() override { return m_attached.load(); }
	void addRequest(const PSRequest &request) override { Service().handle(request); }
	Bool getRequest(PSRequest &) override { return false; }
	void addResponse(const PSResponse &response) override { m_responses.push(response); }
	Bool getResponse(PSResponse &response) override { return m_responses.pop(response); }
	void trackPlayerStats(PSPlayerStats stats) override
	{
		if (stats.id == 0)
			return;
		OnlineLock lock(m_statsMutex);
		auto found = m_stats.find(stats.id);
		if (found == m_stats.end())
			m_stats.emplace(stats.id, stats);
		else
			found->second.incorporate(stats);
	}
	PSPlayerStats findPlayerStatsByID(Int id) override
	{
		OnlineLock lock(m_statsMutex);
		auto found = m_stats.find(id);
		if (found != m_stats.end())
			return found->second;
		PSPlayerStats empty;
		empty.id = 0;
		return empty;
	}

private:
	std::atomic<bool> m_attached{false};
	BoundedQueue<PSResponse> m_responses;
	OnlineMutex m_statsMutex;
	std::map<Int, PSPlayerStats> m_stats;
};

void OnlineServiceSession::attachCommon()
{
	++m_attachmentCount;
}

bool OnlineServiceSession::startControl(const OnlineEndpoint &endpoint, std::string &error)
{
	// GeneralsX @bugfix Codex 01/08/2026 Connect only when authentication begins so the login form has no idle deadline.
	if (!endpoint.configured || !m_client.start(
			endpoint.host,
			endpoint.controlPort,
			endpoint.useTLS,
			[this](const std::string &line) { onLine(line); },
			[this](bool connected, const std::string &detail) { onConnectionState(connected, detail); },
			&error))
	{
		if (error.empty())
			error = "Online server is not configured";
		return false;
	}
	return true;
}

void OnlineServiceSession::detachCommon()
{
	if (m_attachmentCount > 0)
		--m_attachmentCount;
	if (m_attachmentCount == 0)
		resetStateLocked();
}

void OnlineServiceSession::resetStateLocked()
{
	m_client.setHeartbeatEnabled(false);
	m_authenticated = false;
	m_authenticating = false;
	m_expectedClose = false;
	m_connectionFailed = false;
	m_lastConnectionError.clear();
	m_nextRequestId = 1;
	m_pending.clear();
	m_localUserId = 0;
	m_localProfileId = 0;
	m_localName.clear();
	m_authName.clear();
	m_authUsername.clear();
	m_authPassword.clear();
	m_persistentProfile = false;
	m_profiles.clear();
	m_serviceUsers.clear();
	m_rooms.clear();
	m_serviceRooms.clear();
	m_nextRoomId = 1;
	m_games.clear();
	m_serviceGames.clear();
	m_nextGameId = 1;
	m_publicGames.clear();
	m_gameListActive = false;
	m_roomMembers.clear();
	m_buddyStatusPolicy.reset();
	clearGameStateLocked();
}

void OnlineServiceSession::clearGameStateLocked()
{
	ClearOnlineSession();
	m_currentGame = ServiceGame{};
	m_hasCurrentGame = false;
	m_startedRoster.clear();
	m_pendingQuickmatch = ServiceGame{};
	m_hasPendingQuickmatch = false;
	m_onlineRelayGameId.clear();
	m_emittedQuickmatchGameId.clear();
	m_gameStarted = false;
	m_gameEnding = false;
	m_gameCredentialsReady = false;
}

void OnlineServiceSession::attach(OnlineBuddyMessageQueue *queue)
{
	OnlineLock lock(m_mutex);
	m_buddy = queue;
	attachCommon();
}

void OnlineServiceSession::attach(OnlinePeerMessageQueue *queue)
{
	OnlineLock lock(m_mutex);
	m_peer = queue;
	attachCommon();
	if (m_connectionFailed)
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, m_lastConnectionError);
}

void OnlineServiceSession::attach(OnlinePersistentStorageMessageQueue *queue)
{
	OnlineLock lock(m_mutex);
	m_ps = queue;
	attachCommon();
}

void OnlineServiceSession::detach(OnlineBuddyMessageQueue *queue)
{
	bool stop = false;
	{
		OnlineLock lock(m_mutex);
		if (m_buddy == queue)
			m_buddy = nullptr;
		detachCommon();
		stop = m_attachmentCount == 0;
	}
	if (stop)
	{
		m_client.stop();
		OnlineLock lock(m_mutex);
		if (m_attachmentCount == 0)
			resetStateLocked();
	}
}

void OnlineServiceSession::detach(OnlinePeerMessageQueue *queue)
{
	bool stop = false;
	{
		OnlineLock lock(m_mutex);
		if (m_peer == queue)
			m_peer = nullptr;
		detachCommon();
		stop = m_attachmentCount == 0;
	}
	if (stop)
	{
		m_client.stop();
		OnlineLock lock(m_mutex);
		if (m_attachmentCount == 0)
			resetStateLocked();
	}
}

void OnlineServiceSession::detach(OnlinePersistentStorageMessageQueue *queue)
{
	bool stop = false;
	{
		OnlineLock lock(m_mutex);
		if (m_ps == queue)
			m_ps = nullptr;
		detachCommon();
		stop = m_attachmentCount == 0;
	}
	if (stop)
	{
		m_client.stop();
		OnlineLock lock(m_mutex);
		if (m_attachmentCount == 0)
			resetStateLocked();
	}
}

bool OnlineServiceSession::isConnected() const
{
	OnlineLock lock(m_mutex);
	return m_authenticated;
}

bool OnlineServiceSession::isConnecting() const
{
	OnlineLock lock(m_mutex);
	return m_authenticating || m_client.isConnecting();
}

GPProfile OnlineServiceSession::localProfile() const
{
	OnlineLock lock(m_mutex);
	return m_localProfileId;
}

bool OnlineServiceSession::sendRequestLocked(const std::string &type, const Json &data, Pending pending)
{
	if (m_pending.size() >= kMaximumPendingRequests)
		return false;
	const std::string id = std::to_string(m_nextRequestId++);
	Json envelope{{"v", 1}, {"type", type}, {"id", id}, {"data", data}};
	m_pending.emplace(id, pending);
	std::string error;
	const std::string encoded = envelope.dump(-1, ' ', false, Json::error_handler_t::replace);
	if (!m_client.sendLine(encoded, &error))
	{
		m_pending.erase(id);
		m_lastConnectionError = error;
		return false;
	}
	return true;
}

int OnlineServiceSession::profileIdLocked(std::uint64_t userId)
{
	if (userId == 0)
		return 0;
	auto found = m_profiles.find(userId);
	if (found != m_profiles.end())
		return found->second;
	int profile = OnlineProfileIdForUserId(userId);
	while (m_serviceUsers.count(profile) != 0 && m_serviceUsers.at(profile) != userId)
		profile = profile == (std::numeric_limits<int>::max)() ? 1 : profile + 1;
	m_profiles.emplace(userId, profile);
	m_serviceUsers.emplace(profile, userId);
	return profile;
}

std::uint64_t OnlineServiceSession::serviceUserIdLocked(int profileId) const
{
	auto found = m_serviceUsers.find(profileId);
	return found == m_serviceUsers.end() ? 0 : found->second;
}

int OnlineServiceSession::roomIdLocked(const std::string &roomId)
{
	auto found = m_rooms.find(roomId);
	if (found != m_rooms.end())
		return found->second;
	const int id = m_nextRoomId++;
	m_rooms.emplace(roomId, id);
	m_serviceRooms.emplace(id, roomId);
	return id;
}

int OnlineServiceSession::gameIdLocked(const std::string &gameId)
{
	auto found = m_games.find(gameId);
	if (found != m_games.end())
		return found->second;
	const int id = m_nextGameId++;
	m_games.emplace(gameId, id);
	m_serviceGames.emplace(id, gameId);
	return id;
}

std::string OnlineServiceSession::serviceGameIdLocked(int gameId) const
{
	auto found = m_serviceGames.find(gameId);
	return found == m_serviceGames.end() ? std::string() : found->second;
}

std::string OnlineServiceSession::serviceRoomIdLocked(int roomId) const
{
	auto found = m_serviceRooms.find(roomId);
	return found == m_serviceRooms.end() ? std::string() : found->second;
}

std::string OnlineServiceSession::memberNameLocked(std::uint64_t userId) const
{
	if (userId == m_localUserId)
		return m_localName;
	for (const ServiceMember &member : m_currentGame.members)
		if (member.userId == userId)
			return member.name;
	auto room = m_roomMembers.find(userId);
	return room == m_roomMembers.end() ? std::string() : room->second.name;
}

void OnlineServiceSession::emitBuddyLocked(const BuddyResponse &response)
{
	if (m_buddy)
		m_buddy->addResponse(response);
}

void OnlineServiceSession::emitPeerLocked(const PeerResponse &response)
{
	if (m_peer)
		m_peer->addResponse(response);
}

void OnlineServiceSession::emitPSLocked(const PSResponse &response)
{
	if (m_ps)
		m_ps->addResponse(response);
}

void OnlineServiceSession::emitDisconnectLocked(DisconnectReason reason, const std::string &detail)
{
	BuddyResponse buddy{};
	buddy.buddyResponseType = BuddyResponse::BUDDYRESPONSE_DISCONNECT;
	buddy.result = GP_NETWORK_ERROR;
	buddy.arg.error.errorCode = GP_LOGIN_CONNECTION_FAILED;
	buddy.arg.error.fatal = GP_FATAL;
	CopyString(buddy.arg.error.errorString, detail);
	emitBuddyLocked(buddy);

	PeerResponse peer{};
	peer.peerResponseType = PeerResponse::PEERRESPONSE_DISCONNECT;
	peer.discon.reason = reason;
	emitPeerLocked(peer);
}

void OnlineServiceSession::emitFailedToHostLocked(const std::string &detail)
{
	ClearOnlineSession();
	m_onlineRelayGameId.clear();
	m_gameStarted = false;
	m_gameEnding = false;
	m_gameCredentialsReady = false;
	if (m_hasCurrentGame)
		m_currentGame.state = "open";

	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_FAILEDTOHOST;
	response.commandOptions = detail;
	emitPeerLocked(response);
}

void OnlineServiceSession::emitPreGameFailureLocked(const std::string &detail)
{
	if (m_hasPendingQuickmatch)
	{
		PeerResponse response{};
		response.peerResponseType = PeerResponse::PEERRESPONSE_QUICKMATCHSTATUS;
		response.qmStatus.status = QM_COULDNOTNEGOTIATEFIREWALLS;
		emitPeerLocked(response);
		clearGameStateLocked();
		return;
	}

	const auto local = std::find_if(
		m_currentGame.members.begin(), m_currentGame.members.end(), [this](const ServiceMember &member) {
			return member.userId == m_localUserId;
		});
	if (local != m_currentGame.members.end() && local->host)
	{
		PeerResponse response{};
		response.peerResponseType = PeerResponse::PEERRESPONSE_FAILEDTOHOST;
		response.command = "TERMINAL";
		response.commandOptions = detail;
		emitPeerLocked(response);
	}
	else
	{
		const auto host = std::find_if(
			m_currentGame.members.begin(), m_currentGame.members.end(), [](const ServiceMember &member) {
				return member.host;
			});
		if (host != m_currentGame.members.end())
		{
			emitPlayerLocked(*host, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERLEFT);
		}
	}
	clearGameStateLocked();
}

void OnlineServiceSession::emitPeerLoginLocked()
{
	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_LOGIN;
	response.nick = m_localName;
	response.player.profileID = m_localProfileId;
	// The legacy login callback uniquely expects host byte order; staging player callbacks expect network order.
	response.player.internalIP = OnlineIPv4NetworkToHost(VirtualIPv4ForSlot(0));
	response.player.externalIP = OnlineIPv4NetworkToHost(VirtualIPv4ForSlot(0));
	emitPeerLocked(response);
}

void OnlineServiceSession::emitBuddyListLocked(const Json &data)
{
	const Json buddies = data.value("buddies", Json::array());
	if (buddies.is_array())
	{
		for (const Json &buddy : buddies)
		{
			const std::uint64_t userId = buddy.value("user_id", std::uint64_t{0});
			if (userId == 0)
				continue;
			const bool online = buddy.value("online", false);
			BuddyResponse response{};
			response.buddyResponseType = BuddyResponse::BUDDYRESPONSE_STATUS;
			response.profile = profileIdLocked(userId);
			CopyString(response.arg.status.nick, buddy.value("display_name", std::string()));
			CopyString(response.arg.status.countrycode, "US");
			CopyString(response.arg.status.statusString, online ? "online" : "offline");
			response.arg.status.status = online ? GP_ONLINE : GP_OFFLINE;
			emitBuddyLocked(response);
		}
	}

	const Json pending = data.value("pending", Json::array());
	if (pending.is_array())
	{
		for (const Json &requester : pending)
		{
			const std::uint64_t userId = requester.value("user_id", std::uint64_t{0});
			if (userId == 0)
				continue;
			BuddyResponse response{};
			response.buddyResponseType = BuddyResponse::BUDDYRESPONSE_REQUEST;
			response.profile = profileIdLocked(userId);
			CopyString(response.arg.request.nick, requester.value("display_name", std::string()));
			CopyString(response.arg.request.countrycode, "US");
			emitBuddyLocked(response);
		}
	}
}

void OnlineServiceSession::onConnectionState(bool connected, const std::string &detail)
{
	OnlineLock lock(m_mutex);
	if (connected)
	{
		m_connectionFailed = false;
		m_lastConnectionError.clear();
		return;
	}
	const bool expectedClose = m_expectedClose;
	m_expectedClose = false;
	m_connectionFailed = true;
	m_lastConnectionError = detail;
	const bool reportConnectionLoss =
		ShouldReportOnlineConnectionLoss(expectedClose, m_authenticated, m_authenticating);
	m_authenticated = false;
	m_authenticating = false;
	m_gameListActive = false;
	m_publicGames.clear();
	m_client.setHeartbeatEnabled(false);
	m_pending.clear();
	clearGameStateLocked();
	if (reportConnectionLoss)
		emitDisconnectLocked(DISCONNECT_LOSTCON, detail);
}

void OnlineServiceSession::onLine(const std::string &line)
{
	try
	{
		const Json envelope = Json::parse(line);
		if (envelope.value("v", 0) != 1)
			return;
		const std::string type = envelope.value("type", std::string());
		const Json data = envelope.contains("data") && envelope["data"].is_object() ? envelope["data"] : Json::object();
		OnlineLock lock(m_mutex);
		if (type == "response")
		{
			const std::string id = envelope.value("id", std::string());
			auto found = m_pending.find(id);
			if (found == m_pending.end())
				return;
			const Pending pending = found->second;
			m_pending.erase(found);
			handleResponseLocked(
				pending,
				envelope.value("ok", false),
				envelope.value("code", std::string()),
				envelope.value("error", std::string()),
				data);
		}
		else
		{
			handleEventLocked(type, data);
		}
	}
	catch (const std::exception &exception)
	{
		static_cast<void>(exception); // DEBUG_LOG compiles out in MSVC Release builds.
		DEBUG_LOG(("Online control message rejected: %s", exception.what()));
	}
}

void OnlineServiceSession::handleResponseLocked(
	const Pending &pending,
	bool ok,
	const std::string &code,
	const std::string &error,
	const Json &data)
{
	if (!ok)
	{
		if (pending.operation == Operation::Authenticate)
		{
			m_authenticating = false;
			DisconnectReason reason = pending.registration ? DISCONNECT_GP_NEWUSER_BAD_NICK : DISCONNECT_GP_LOGIN_BAD_PASSWORD;
			if (code == "tls_required")
				reason = DISCONNECT_GP_LOGIN_SERVER_AUTH_FAILED;
			else if (code == "invalid_credentials" || code == "bad_password" || code == "wrong_password")
				reason = DISCONNECT_GP_LOGIN_BAD_PASSWORD;
			else if (code == "username_taken")
				reason = DISCONNECT_GP_NEWUSER_BAD_NICK;
			emitDisconnectLocked(reason, error.empty() ? code : error);
		}
		else if (pending.operation == Operation::RoomJoin)
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_JOINGROUPROOM;
			response.joinGroupRoom.id = pending.integer;
			response.joinGroupRoom.ok = false;
			emitPeerLocked(response);
		}
		else if (pending.operation == Operation::GameCreate)
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_CREATESTAGINGROOM;
			response.createStagingRoom.result = PEERJoinFailed;
			emitPeerLocked(response);
		}
		else if (pending.operation == Operation::GameJoin)
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_JOINSTAGINGROOM;
			response.joinStagingRoom.id = pending.integer;
			response.joinStagingRoom.ok = false;
			response.joinStagingRoom.isHostPresent = false;
			response.joinStagingRoom.result = (code == "bad_password" || code == "wrong_password") ? PEERBadPassword :
				code == "game_full" ? PEERFullRoom : code == "already_in_game" ? PEERAlreadyInRoom : PEERJoinFailed;
			emitPeerLocked(response);
		}
		else if (pending.operation == Operation::StatsGet)
		{
			PSResponse response{};
			response.responseType = PSResponse::PSRESPONSE_COULDNOTCONNECT;
			response.player.id = pending.integer;
			emitPSLocked(response);
		}
		else if (pending.operation == Operation::Quickmatch)
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_QUICKMATCHSTATUS;
			response.qmStatus.status = code == "relay_unavailable" ? QM_COULDNOTNEGOTIATEFIREWALLS :
				QM_COULDNOTFINDCHANNEL;
			emitPeerLocked(response);
		}
		else if (pending.operation == Operation::GameStart && !m_gameCredentialsReady)
		{
			emitFailedToHostLocked(error.empty() ? code : error);
		}
		else if (pending.operation == Operation::GameStartReady && m_gameCredentialsReady && !m_gameStarted)
		{
			emitPreGameFailureLocked(error.empty() ? code : error);
		}
		return;
	}

	switch (pending.operation)
	{
		case Operation::Authenticate:
		{
			const Json profile = data.value("profile", Json::object());
			m_localUserId = profile.value("user_id", std::uint64_t{0});
			m_localName = profile.value("display_name", m_authName);
			m_localProfileId = profileIdLocked(m_localUserId);
			m_persistentProfile = !profile.value("guest", true);
			m_authenticated = true;
			m_authenticating = false;
			m_client.setHeartbeatEnabled(true);

			BuddyResponse buddy{};
			buddy.buddyResponseType = BuddyResponse::BUDDYRESPONSE_LOGIN;
			buddy.result = GP_NO_ERROR;
			buddy.profile = m_localProfileId;
			emitBuddyLocked(buddy);
			emitPeerLoginLocked();
			if (m_persistentProfile)
				sendRequestLocked("buddy.list", Json::object(), {Operation::BuddyList});
			sendRequestLocked("room.list", Json::object(), {Operation::RoomList});
			break;
		}
		case Operation::BuddyList:
			emitBuddyListLocked(data);
			break;
		case Operation::RoomList:
			emitRoomListLocked(data.value("rooms", Json::array()));
			break;
		case Operation::RoomJoin:
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_JOINGROUPROOM;
			response.joinGroupRoom.id = pending.integer;
			response.joinGroupRoom.ok = true;
			emitPeerLocked(response);
			applyRoomSnapshotLocked(data.value("room", Json::object()));
			break;
		}
		case Operation::GameList:
			if (m_gameListActive)
				emitGameListLocked(data.value("games", Json::array()), true);
			break;
		case Operation::GameCreate:
		{
			const ServiceGame game = ParseGame(data.value("game", Json::object()));
			gameIdLocked(game.gameId);
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_CREATESTAGINGROOM;
			response.createStagingRoom.result = PEERJoinSuccess;
			emitPeerLocked(response);
			applyGameSnapshotLocked(game, true);
			break;
		}
		case Operation::GameJoin:
		{
			const ServiceGame game = ParseGame(data.value("game", Json::object()));
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_JOINSTAGINGROOM;
			response.joinStagingRoom.id = pending.integer;
			response.joinStagingRoom.ok = true;
			response.joinStagingRoom.isHostPresent = false;
			response.joinStagingRoom.result = PEERJoinSuccess;
			for (const ServiceMember &member : game.members)
			{
				if (member.slot >= 0 && member.slot < MAX_SLOTS)
					response.stagingRoomPlayerNames[member.slot] = member.name;
				if (member.host)
					response.joinStagingRoom.isHostPresent = true;
			}
			emitPeerLocked(response);
			applyGameSnapshotLocked(game);
			break;
		}
		case Operation::GameOptions:
			if (data.contains("game"))
				applyGameSnapshotLocked(ParseGame(data["game"]));
			break;
		case Operation::StatsGet:
		{
			const Json stats = data.value("stats", Json::object());
			PSResponse response{};
			response.responseType = PSResponse::PSRESPONSE_PLAYERSTATS;
			response.player.id = pending.integer;
			response.player.wins[0] = JsonCounter(stats, "wins");
			response.player.losses[0] = JsonCounter(stats, "losses");
			response.player.discons[0] = JsonCounter(stats, "disconnects");
			response.player.games[0] = JsonCounter(stats, "games");
			emitPSLocked(response);
			if (m_ps)
				m_ps->trackPlayerStats(response.player);
			break;
		}
		case Operation::Quickmatch:
			if (data.value("matched", false) && data.contains("game"))
				handleQuickmatchLocked(ParseGame(data["game"]));
			else
			{
				PeerResponse response{};
				response.peerResponseType = PeerResponse::PEERRESPONSE_QUICKMATCHSTATUS;
				response.qmStatus.status = QM_WORKING;
				response.qmStatus.poolSize = 1;
				emitPeerLocked(response);
			}
			break;
		default:
			break;
	}
}

void OnlineServiceSession::handleEventLocked(const std::string &type, const Json &data)
{
	if (type == "session.ready")
	{
		if (m_gameListActive && data.contains("games"))
			emitGameListLocked(data["games"], false);
	}
	else if (type == "session.replaced")
	{
		emitDisconnectLocked(DISCONNECT_LOSTCON, "This Online account was connected from another client");
	}
	else if (type == "room.updated")
	{
		applyRoomSnapshotLocked(data.value("room", Json::object()));
	}
	else if (type == "room.chat" || type == "game.chat" || type == "player.chat")
	{
		emitChatLocked(type, data);
	}
	else if (type == "game.list")
	{
		if (m_gameListActive)
			emitGameListLocked(data.value("games", Json::array()), false);
	}
	else if (type == "game.updated")
	{
		const ServiceGame game = ParseGame(data.value("game", Json::object()));
		if (m_hasCurrentGame && game.gameId == m_currentGame.gameId)
			applyGameSnapshotLocked(game);
	}
	else if (type == "game.started")
	{
		const std::string gameId = data.value("game_id", std::string());
		if (m_hasCurrentGame && !gameId.empty() && gameId == m_currentGame.gameId)
			applyRelayCredentialsLocked(data);
	}
	else if (type == "game.go")
	{
		handleGameGoLocked(data);
	}
	else if (type == "game.peer_left")
	{
		const std::string gameId = data.value("game_id", std::string());
		if (m_gameStarted && gameId == m_currentGame.gameId)
		{
			const std::uint64_t userId = data.value("departed_user_id", std::uint64_t{0});
			auto departed = std::find_if(
				m_currentGame.members.begin(), m_currentGame.members.end(), [userId](const ServiceMember &member) {
					return member.userId == userId;
				});
			if (departed != m_currentGame.members.end())
			{
				emitPlayerLocked(*departed, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERLEFT);
				m_currentGame.members.erase(departed);
			}
		}
	}
	else if (type == "game.kicked")
	{
		const std::string gameId = data.value("game_id", std::string());
		if (!m_hasCurrentGame || gameId.empty() || gameId != m_currentGame.gameId)
			return;
		std::string hostName = m_currentGame.hostName;
		for (const ServiceMember &member : m_currentGame.members)
			if (member.host)
				hostName = member.name;
		PeerResponse response{};
		response.peerResponseType = PeerResponse::PEERRESPONSE_PLAYERUTM;
		response.nick = hostName;
		response.command = "KICK";
		response.commandOptions = data.value("reason", std::string("Kicked"));
		emitPeerLocked(response);
		clearGameStateLocked();
	}
	else if (type == "game.ended")
	{
		const std::string gameId = data.value("game_id", std::string());
		if (!m_hasCurrentGame || gameId.empty() || gameId != m_currentGame.gameId)
			return;
		const std::string reason = data.value("reason", std::string());
		if (!m_gameStarted)
		{
			emitPreGameFailureLocked(reason.empty() ? "Online game ended before launch" : reason);
			return;
		}
		if (ShouldDisconnectForOnlineGameEnd(m_gameStarted, reason))
		{
			PeerResponse response{};
			response.peerResponseType = PeerResponse::PEERRESPONSE_DISCONNECT;
			response.discon.reason = DISCONNECT_LOSTCON;
			emitPeerLocked(response);
		}
		clearGameStateLocked();
	}
	else if (type == "quickmatch.matched")
	{
		handleQuickmatchLocked(ParseGame(data.value("game", Json::object())));
	}
	else if (type.rfind("buddy.", 0) == 0)
	{
		handleBuddyEventLocked(type, data);
	}
}

void OnlineServiceSession::emitRoomListLocked(const Json &rooms)
{
	if (!rooms.is_array())
		return;
	for (const Json &room : rooms)
	{
		PeerResponse response{};
		response.peerResponseType = PeerResponse::PEERRESPONSE_GROUPROOM;
		response.groupRoom.id = roomIdLocked(room.value("room_id", std::string()));
		response.groupRoom.numWaiting = room.value("players", 0);
		response.groupRoom.maxWaiting = 1000;
		response.groupRoom.numGames = 0;
		response.groupRoom.numPlaying = 0;
		response.groupRoomName = room.value("name", std::string());
		emitPeerLocked(response);
	}
	PeerResponse complete{};
	complete.peerResponseType = PeerResponse::PEERRESPONSE_GROUPROOM;
	complete.groupRoom.id = 0;
	emitPeerLocked(complete);
}

void OnlineServiceSession::emitPlayerLocked(
	const ServiceMember &member,
	RoomType roomType,
	int responseType)
{
	PeerResponse response{};
	response.peerResponseType = static_cast<decltype(response.peerResponseType)>(responseType);
	response.nick = member.name;
	response.player.profileID = profileIdLocked(member.userId);
	response.player.roomType = roomType;
	response.player.flags = (roomType == StagingRoom ? PEER_FLAG_STAGING : 0) |
		(member.host ? PEER_FLAG_HOST | PEER_FLAG_OP : 0) | (member.ready ? PEER_FLAG_READY : 0);
	response.player.IP = roomType == StagingRoom ? VirtualIPv4ForSlot(member.slot) : 0;
	response.player.wins = 0;
	response.player.losses = 0;
	response.player.rankPoints = 0;
	response.player.side = 0;
	response.player.preorder = 0;
	response.locale = "US";
	emitPeerLocked(response);
}

void OnlineServiceSession::applyRoomSnapshotLocked(const Json &room)
{
	if (!room.is_object() || !room.contains("members") || !room["members"].is_array())
		return;
	std::map<std::uint64_t, ServiceMember> next;
	for (const Json &value : room["members"])
	{
		ServiceMember member = ParseMember(value);
		next.emplace(member.userId, member);
		auto previous = m_roomMembers.find(member.userId);
		if (previous == m_roomMembers.end())
			emitPlayerLocked(member, GroupRoom, PeerResponse::PEERRESPONSE_PLAYERJOIN);
		emitPlayerLocked(member, GroupRoom, PeerResponse::PEERRESPONSE_PLAYERINFO);
	}
	for (const auto &[userId, member] : m_roomMembers)
		if (next.count(userId) == 0)
			emitPlayerLocked(member, GroupRoom, PeerResponse::PEERRESPONSE_PLAYERLEFT);
	m_roomMembers = std::move(next);
}

void OnlineServiceSession::emitGameSummaryLocked(const ServiceGame &game, int action, int percentComplete)
{
	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_STAGINGROOM;
	response.stagingRoom.id = gameIdLocked(game.gameId);
	response.stagingRoom.action = action;
	response.stagingRoom.isStaging = true;
	response.stagingRoom.requiresPassword = game.hasPassword;
	response.stagingRoom.allowObservers = game.options.value("allow_observers", false);
	response.stagingRoom.useStats = game.options.value("use_stats", true);
	response.stagingRoom.version = 1;
	const std::uint32_t localExeCRC = TheGlobalData ? static_cast<std::uint32_t>(TheGlobalData->m_exeCRC) : 1U;
	response.stagingRoom.exeCRC = ProjectOnlineExeCRC(localExeCRC, LocalCompatibility(), game.compatibility);
	response.stagingRoom.iniCRC = game.compatibility.iniCRC;
	response.stagingRoom.numPlayers = game.players;
	response.stagingRoom.numObservers = 0;
	response.stagingRoom.maxPlayers = std::clamp(game.maximumPlayers, 2, MAX_SLOTS);
	response.stagingRoom.percentComplete = percentComplete;
	response.stagingServerName = MultiByteToWideCharSingleLine(game.name.c_str());
	response.stagingRoomMapName = game.map.empty() ? "Maps/Default/Default.map" : game.map;
	response.stagingServerPingString = "0000000000000000";
	response.stagingServerLadderIP = "0";
	response.stagingRoomPlayerNames[0] = game.hostName;
	if (m_hasCurrentGame && m_currentGame.gameId == game.gameId)
	{
		for (const ServiceMember &member : m_currentGame.members)
		{
			if (member.slot >= 0 && member.slot < MAX_SLOTS)
			{
				response.stagingRoomPlayerNames[member.slot] = member.name;
				response.stagingRoom.profileID[member.slot] = profileIdLocked(member.userId);
			}
		}
	}
	emitPeerLocked(response);
}

void OnlineServiceSession::emitGameListLocked(const Json &games, bool clearFirst)
{
	if (!games.is_array())
		return;
	std::map<std::string, ServiceGame> next;
	for (const Json &value : games)
	{
		ServiceGame game = ParseGame(value);
		if (game.gameId.empty())
			continue;
		next[game.gameId] = game;
	}
	if (clearFirst)
	{
		PeerResponse clear{};
		clear.peerResponseType = PeerResponse::PEERRESPONSE_STAGINGROOM;
		clear.stagingRoom.action = PEER_CLEAR;
		emitPeerLocked(clear);
		for (const auto &[gameId, game] : next)
			emitGameSummaryLocked(game, PEER_ADD, 100);
		m_publicGames = std::move(next);

		PeerResponse complete{};
		complete.peerResponseType = PeerResponse::PEERRESPONSE_STAGINGROOMLISTCOMPLETE;
		emitPeerLocked(complete);
		return;
	}
	std::vector<OnlineGameBrowserSummary> previousSummaries;
	std::vector<OnlineGameBrowserSummary> nextSummaries;
	previousSummaries.reserve(m_publicGames.size());
	nextSummaries.reserve(next.size());
	for (const auto &[gameId, game] : m_publicGames)
		previousSummaries.push_back(BrowserSummary(game));
	for (const auto &[gameId, game] : next)
		nextSummaries.push_back(BrowserSummary(game));
	for (const OnlineGameBrowserChange &change :
		DiffOnlineGameBrowserSummaries(previousSummaries, nextSummaries))
	{
		const bool removed = change.type == OnlineGameBrowserChangeType::Remove;
		const auto &source = removed ? m_publicGames : next;
		const auto game = source.find(change.game.gameId);
		if (game == source.end())
			continue;
		const int action = removed ? PEER_REMOVE :
			change.type == OnlineGameBrowserChangeType::Add ? PEER_ADD : PEER_UPDATE;
		emitGameSummaryLocked(game->second, action, 100);
	}
	m_publicGames = std::move(next);
}

void OnlineServiceSession::emitSlotListLocked(const ServiceGame &game)
{
	const std::string storedSlotList = game.options.value("slot_list", std::string());
	OnlineReadyPlayerStates readyStates;
	readyStates.reserve(game.members.size());
	for (const ServiceMember &member : game.members)
	{
		if (!member.host)
		{
			readyStates.push_back(OnlineReadyPlayerState{
				member.name,
				member.slot >= 0 && member.slot < static_cast<int>(kOnlineServiceSlotCount) ?
					OnlineVirtualIPv4HostOrder(static_cast<std::uint8_t>(member.slot)) : 0U,
				static_cast<std::int8_t>(member.ready ? 1 : 0),
			});
		}
	}
	const std::string slotList = ApplyOnlineReadyStates(storedSlotList, readyStates);
	if (slotList.empty())
		return;
	std::string host = game.hostName;
	for (const ServiceMember &member : game.members)
		if (member.host)
			host = member.name;
	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_ROOMUTM;
	response.nick = host;
	response.command = "SL";
	response.commandOptions = slotList;
	emitPeerLocked(response);
}

void OnlineServiceSession::applyGameSnapshotLocked(const ServiceGame &game, bool suppressLocalJoin)
{
	if (game.gameId.empty())
		return;
	const bool sameGame = m_hasCurrentGame && m_currentGame.gameId == game.gameId;
	std::map<std::uint64_t, ServiceMember> previous;
	if (sameGame)
		for (const ServiceMember &member : m_currentGame.members)
			previous.emplace(member.userId, member);

	m_currentGame = game;
	m_hasCurrentGame = true;
	gameIdLocked(game.gameId);
	for (const ServiceMember &member : game.members)
	{
		auto found = previous.find(member.userId);
		if (found == previous.end() && !(suppressLocalJoin && member.userId == m_localUserId))
			emitPlayerLocked(member, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERJOIN);
		const OnlineGameMemberSummary currentSummary = MemberSummary(member);
		OnlineGameMemberSummary previousSummary;
		const OnlineGameMemberSummary *previousSummaryPointer = nullptr;
		if (found != previous.end())
		{
			previousSummary = MemberSummary(found->second);
			previousSummaryPointer = &previousSummary;
		}
		// GeneralsX @bugfix Codex 01/08/2026 Do not turn echoed snapshots into host option/chat feedback loops.
		if (ShouldEmitOnlinePlayerInfo(previousSummaryPointer, currentSummary))
			emitPlayerLocked(member, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERINFO);
		if (found != previous.end() && found->second.ready != member.ready)
		{
			emitPlayerLocked(member, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERCHANGEDFLAGS);
			if (member.ready)
			{
				PeerResponse accepted{};
				accepted.peerResponseType = PeerResponse::PEERRESPONSE_PLAYERUTM;
				accepted.nick = member.name;
				accepted.command = "accept";
				accepted.commandOptions = "true";
				emitPeerLocked(accepted);
			}
		}
	}
	for (const auto &[userId, member] : previous)
	{
		const auto present = std::find_if(game.members.begin(), game.members.end(), [userId](const ServiceMember &value) {
			return value.userId == userId;
		});
		if (present == game.members.end())
			emitPlayerLocked(member, StagingRoom, PeerResponse::PEERRESPONSE_PLAYERLEFT);
	}
	emitSlotListLocked(game);
}

void OnlineServiceSession::emitChatLocked(const std::string &type, const Json &data)
{
	const std::uint64_t senderId = data.value("user_id", std::uint64_t{0});
	const std::string sender = data.value("display_name", memberNameLocked(senderId));
	const std::string message = data.value("message", std::string());
	if (type == "game.chat" && message.rfind(kUTMChatPrefix, 0) == 0)
	{
		try
		{
			const Json utm = Json::parse(message.substr(std::strlen(kUTMChatPrefix)));
			const std::uint64_t target = utm.value("target", std::uint64_t{0});
			if (target != 0 && target != m_localUserId)
				return;
			PeerResponse response{};
			response.peerResponseType = utm.value("room", false) ? PeerResponse::PEERRESPONSE_ROOMUTM :
				PeerResponse::PEERRESPONSE_PLAYERUTM;
			response.nick = sender;
			response.command = utm.value("command", std::string());
			response.commandOptions = utm.value("options", std::string());
			emitPeerLocked(response);
		}
		catch (const std::exception &exception)
		{
			static_cast<void>(exception); // DEBUG_LOG compiles out in MSVC Release builds.
			DEBUG_LOG(("Online UTM chat envelope rejected: %s", exception.what()));
		}
		return;
	}

	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_MESSAGE;
	response.nick = sender;
	response.text = MultiByteToWideCharSingleLine(message.c_str());
	response.message.profileID = profileIdLocked(senderId);
	response.message.isPrivate = type == "player.chat";
	response.message.isAction = data.value("action", false);
	emitPeerLocked(response);

	if (type == "player.chat")
	{
		BuddyResponse buddy{};
		buddy.buddyResponseType = BuddyResponse::BUDDYRESPONSE_MESSAGE;
		buddy.profile = profileIdLocked(senderId);
		CopyString(buddy.arg.message.nick, sender);
		CopyWideString(buddy.arg.message.text, MultiByteToWideCharSingleLine(message.c_str()));
		emitBuddyLocked(buddy);
	}
}

void OnlineServiceSession::applyRelayCredentialsLocked(const Json &data)
{
	const std::string gameIdText = data.value("game_id", std::string());
	const std::string relayHost = data.value("relay_host", std::string());
	const int relayPort = data.value("relay_port", 0);
	const int localSlot = data.value("slot", -1);
	std::array<std::uint8_t, kOnlineRelayTokenSize> token{};
	if (gameIdText.size() != 16U || gameIdText.find_first_not_of("0123456789abcdef") != std::string::npos ||
		relayHost.empty() || relayPort <= 0 || relayPort > 65535 ||
		localSlot < 0 || localSlot >= static_cast<int>(kOnlineServiceSlotCount) ||
		!DecodeHex(data.value("relay_token", std::string()), token))
	{
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, "Online server returned invalid relay credentials");
		return;
	}

	std::uint64_t numericGameId = 0;
	try
	{
		numericGameId = std::stoull(gameIdText, nullptr, 16);
	}
	catch (const std::exception &)
	{
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, "Online server returned an invalid relay game ID");
		return;
	}
	std::string stateError;
	if (!ConfigureOnlineRelaySession(
			relayHost.c_str(),
			static_cast<std::uint16_t>(relayPort),
			numericGameId,
			static_cast<std::uint8_t>(localSlot),
			token,
			&stateError))
	{
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, stateError);
		return;
	}
	for (std::uint8_t slot = 0; slot < kOnlineServiceSlotCount; ++slot)
		SetOnlineVirtualIPv4(slot, VirtualIPv4ForSlot(slot), &stateError);
	if (!SetOnlineSessionReady(true, &stateError))
	{
		ClearOnlineSession();
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, stateError);
		return;
	}
	m_onlineRelayGameId = gameIdText;
	m_gameCredentialsReady = true;
	m_gameStarted = false;
	m_gameEnding = false;
	if (!sendRequestLocked("game.start_ready", Json{{"game_id", gameIdText}}, {Operation::GameStartReady}))
		emitPreGameFailureLocked(m_lastConnectionError.empty() ?
			"Could not confirm Online relay credentials" : m_lastConnectionError);
}

void OnlineServiceSession::handleBuddyEventLocked(const std::string &type, const Json &data)
{
	const std::uint64_t userId = data.value("user_id", std::uint64_t{0});
	const int profile = profileIdLocked(userId);
	const std::string name = data.value("display_name", std::string());
	if (type == "buddy.requested")
	{
		BuddyResponse response{};
		response.buddyResponseType = BuddyResponse::BUDDYRESPONSE_REQUEST;
		response.profile = profile;
		CopyString(response.arg.request.nick, name);
		CopyString(response.arg.request.countrycode, "US");
		emitBuddyLocked(response);
	}
	else
	{
		BuddyResponse response{};
		response.buddyResponseType = BuddyResponse::BUDDYRESPONSE_STATUS;
		response.profile = profile;
		CopyString(response.arg.status.nick, name);
		CopyString(response.arg.status.countrycode, "US");
		CopyString(response.arg.status.statusString, data.value("status", std::string("online")));
		response.arg.status.status = data.value("online", true) ? GP_ONLINE : GP_OFFLINE;
		emitBuddyLocked(response);
	}
}

void OnlineServiceSession::handleQuickmatchLocked(const ServiceGame &game)
{
	if (game.gameId.empty() || m_emittedQuickmatchGameId == game.gameId ||
		(m_hasPendingQuickmatch && m_pendingQuickmatch.gameId == game.gameId))
	{
		return;
	}
	applyGameSnapshotLocked(game);
	if (m_onlineRelayGameId != game.gameId || !m_gameStarted)
	{
		m_pendingQuickmatch = game;
		m_hasPendingQuickmatch = true;
		return;
	}
	emitQuickmatchLocked(game);
	m_emittedQuickmatchGameId = game.gameId;
}

void OnlineServiceSession::handleGameGoLocked(const Json &data)
{
	const std::string gameId = data.value("game_id", std::string());
	if (!m_hasCurrentGame || !m_gameCredentialsReady || m_gameStarted || gameId.empty() ||
		gameId != m_currentGame.gameId || gameId != m_onlineRelayGameId)
		return;
	m_startedRoster = m_currentGame.members;
	m_gameStarted = true;
	if (m_hasPendingQuickmatch && m_pendingQuickmatch.gameId == gameId &&
		m_emittedQuickmatchGameId != gameId)
	{
		emitQuickmatchLocked(m_pendingQuickmatch);
		m_emittedQuickmatchGameId = gameId;
		m_pendingQuickmatch = ServiceGame{};
		m_hasPendingQuickmatch = false;
	}

	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_GAMESTART;
	emitPeerLocked(response);
}

void OnlineServiceSession::emitQuickmatchLocked(const ServiceGame &game)
{
	PeerResponse response{};
	response.peerResponseType = PeerResponse::PEERRESPONSE_QUICKMATCHSTATUS;
	response.qmStatus.status = QM_MATCHED;
	response.qmStatus.poolSize = static_cast<int>(game.members.size());
	response.qmStatus.mapIdx = 0;
	try
	{
		response.qmStatus.seed = static_cast<int>(std::stoull(game.gameId, nullptr, 16) & 0x7fffffffU);
	}
	catch (const std::exception &)
	{
		response.qmStatus.seed = 0;
	}
	for (const ServiceMember &member : game.members)
	{
		if (member.slot >= 0 && member.slot < MAX_SLOTS)
		{
			response.stagingRoomPlayerNames[member.slot] = member.name;
			// The quickmatch UI stores this directly in GameInfo; unlike staging callbacks it expects host order.
			response.qmStatus.IP[member.slot] =
				OnlineVirtualIPv4HostOrder(static_cast<std::uint8_t>(member.slot));
			response.qmStatus.side[member.slot] = PLAYERTEMPLATE_RANDOM;
			response.qmStatus.color[member.slot] = member.slot;
			response.qmStatus.nat[member.slot] = FirewallHelperClass::FIREWALL_TYPE_SIMPLE;
		}
	}
	emitPeerLocked(response);
}

void OnlineServiceSession::sendGameResultsLocked(const std::string &packet)
{
	if (!m_persistentProfile || !m_hasCurrentGame || m_currentGame.gameId.empty() ||
		!m_currentGame.options.value("use_stats", false))
	{
		return;
	}
	const auto local = std::find_if(
		m_currentGame.members.begin(), m_currentGame.members.end(), [this](const ServiceMember &member) {
			return member.userId == m_localUserId;
		});
	const bool liveHostPresent = std::any_of(
		m_currentGame.members.begin(), m_currentGame.members.end(), [](const ServiceMember &member) {
			return member.host;
		});
	if (local == m_currentGame.members.end() || (!local->host && liveHostPresent))
		return; // The host reports normally; a survivor takes over after the host disconnects.

	std::vector<OnlineLegacyGameResult> legacyResults;
	std::string error;
	if (!ParseOnlineLegacyGameResults(packet, legacyResults, &error))
	{
		DEBUG_LOG(("Online game results were not submitted: %s", error.c_str()));
		return;
	}
	std::map<int, std::string> outcomes;
	for (const OnlineLegacyGameResult &result : legacyResults)
		outcomes.emplace(result.profileId, result.outcome);

	const std::vector<ServiceMember> &roster = m_startedRoster.empty() ? m_currentGame.members : m_startedRoster;
	Json results = Json::array();
	for (const ServiceMember &member : roster)
	{
		const int profileId = profileIdLocked(member.userId);
		const auto outcome = outcomes.find(profileId);
		if (outcome == outcomes.end())
		{
			DEBUG_LOG(("Online game results omitted service player %llu", static_cast<unsigned long long>(member.userId)));
			return;
		}
		results.push_back(Json{{"user_id", member.userId}, {"outcome", outcome->second}});
	}
	if (!results.empty())
		sendRequestLocked("stats.results", Json{{"game_id", m_currentGame.gameId}, {"results", results}});
}

void OnlineServiceSession::sendUTMLocked(const PeerRequest &request)
{
	const std::string command = NormalizeCommand(request.id);
	if (command == "SL")
		return;
	if (command == "accept")
	{
		sendRequestLocked("game.ready", Json{{"ready", true}}, {Operation::GameReady});
		return;
	}
	if (command == "NAT")
		return; // The authenticated relay replaces retail NAT negotiation.

	std::uint64_t target = 0;
	if (!request.nick.empty())
	{
		for (const ServiceMember &member : m_currentGame.members)
			if (member.name == request.nick)
					target = member.userId;
	}
	if (command == "KICK")
	{
		if (target != 0)
			sendRequestLocked("game.kick", Json{{"user_id", target}});
		return;
	}
	Json payload{
		{"target", target},
		{"room", request.peerRequestType == PeerRequest::PEERREQUEST_UTMROOM},
		{"command", Truncate(command, 32)},
		{"options", Truncate(request.options, 384)},
	};
	const std::string encoded = std::string(kUTMChatPrefix) +
		payload.dump(-1, ' ', false, Json::error_handler_t::replace);
	if (encoded.size() <= kMaximumChatLength)
		sendRequestLocked("game.chat", Json{{"message", encoded}, {"action", false}});
}

void OnlineServiceSession::handleAuthentication(const BuddyRequest &request)
{
	const bool createAccount = request.buddyRequestType == BuddyRequest::BUDDYREQUEST_LOGINNEW;
	const OnlineEndpoint endpoint = GetOnlineEndpoint();
	if (!m_client.isRunning())
	{
		std::string error;
		// Joining a finished worker while holding m_mutex can deadlock its state callback.
		if (!startControl(endpoint, error))
		{
			OnlineLock lock(m_mutex);
			m_connectionFailed = true;
			m_lastConnectionError = error;
			m_authenticating = false;
			emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, m_lastConnectionError);
			return;
		}
	}

	OnlineLock lock(m_mutex);
	m_gameListActive = false;
	m_publicGames.clear();
	if (!m_client.isRunning())
	{
		m_authenticating = false;
		m_connectionFailed = true;
		if (m_lastConnectionError.empty())
			m_lastConnectionError = "Online control connection ended before authentication";
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, m_lastConnectionError);
		return;
	}
	m_connectionFailed = false;
	m_lastConnectionError.clear();
	if (request.buddyRequestType != BuddyRequest::BUDDYREQUEST_RELOGIN)
	{
		m_authName = request.arg.login.nick;
		m_authUsername = request.arg.login.nick;
		if (endpoint.useTLS)
			m_authPassword = Truncate(request.arg.login.password, 128);
		else
			m_authPassword.clear();
	}
	m_authenticating = true;
	m_expectedClose = false;
	bool sent = false;
	// GeneralsX @feature Codex 01/08/2026 Permit persistent credentials only when the configured control channel is TLS.
	switch (SelectOnlineAuthentication(endpoint.useTLS, createAccount))
	{
		case OnlineAuthenticationKind::Guest:
			sent = sendRequestLocked(
				"auth.guest", Json{{"display_name", m_authName}}, {Operation::Authenticate, 0, createAccount});
			break;
		case OnlineAuthenticationKind::Login:
			sent = sendRequestLocked("auth.login", Json{
				{"username", m_authUsername}, {"password", m_authPassword},
			}, {Operation::Authenticate, 0, false});
			break;
		case OnlineAuthenticationKind::Register:
			sent = sendRequestLocked("auth.register", Json{
				{"username", m_authUsername}, {"password", m_authPassword}, {"display_name", m_authName},
			}, {Operation::Authenticate, 0, true});
			break;
	}
	if (!sent)
	{
		m_authenticating = false;
		emitDisconnectLocked(DISCONNECT_COULDNOTCONNECT, m_lastConnectionError);
	}
}

void OnlineServiceSession::handle(const BuddyRequest &request)
{
	switch (request.buddyRequestType)
	{
		case BuddyRequest::BUDDYREQUEST_LOGIN:
		case BuddyRequest::BUDDYREQUEST_RELOGIN:
		case BuddyRequest::BUDDYREQUEST_LOGINNEW:
			handleAuthentication(request);
			return;
		default:
			break;
	}

	OnlineLock lock(m_mutex);
	switch (request.buddyRequestType)
	{
		case BuddyRequest::BUDDYREQUEST_LOGOUT:
			clearGameStateLocked();
			m_buddyStatusPolicy.reset();
			m_gameListActive = false;
			m_publicGames.clear();
			m_authenticated = false;
			m_client.setHeartbeatEnabled(false);
			m_expectedClose = true;
			sendRequestLocked("session.close", Json::object());
			break;
		case BuddyRequest::BUDDYREQUEST_MESSAGE:
		{
			const std::uint64_t recipient = serviceUserIdLocked(request.arg.message.recipient);
			if (recipient != 0)
				sendRequestLocked("player.chat", Json{
					{"user_id", recipient},
					{"message", Truncate(WideCharStringToMultiByte(request.arg.message.text), kMaximumChatLength)},
				});
			break;
		}
		case BuddyRequest::BUDDYREQUEST_ADDBUDDY:
		{
			const std::uint64_t userId = serviceUserIdLocked(request.arg.addbuddy.id);
			if (userId != 0)
				sendRequestLocked("buddy.request", Json{{"user_id", userId}});
			break;
		}
		case BuddyRequest::BUDDYREQUEST_OKADD:
		{
			const std::uint64_t userId = serviceUserIdLocked(request.arg.profile.id);
			if (userId != 0)
				sendRequestLocked("buddy.accept", Json{{"user_id", userId}});
			break;
		}
		case BuddyRequest::BUDDYREQUEST_DELBUDDY:
		case BuddyRequest::BUDDYREQUEST_DENYADD:
		{
			const std::uint64_t userId = serviceUserIdLocked(request.arg.profile.id);
			if (userId != 0)
				sendRequestLocked("buddy.remove", Json{{"user_id", userId}});
			break;
		}
		case BuddyRequest::BUDDYREQUEST_SETSTATUS:
		{
			m_buddyStatusPolicy.apply(
				ClassifyOnlineBuddyStatus(request.arg.status.status), request.arg.status.statusString, [this, &request]() {
					std::string status = request.arg.status.status == GP_AWAY ? "away" :
						request.arg.status.status == GP_PLAYING ? "in_game" : "online";
					if (std::string(request.arg.status.locationString).find("Staging") != std::string::npos)
						status = "in_game";
					if (status == "online" && m_gameStarted && !m_gameEnding)
					{
						const auto local = std::find_if(
							m_currentGame.members.begin(), m_currentGame.members.end(), [this](const ServiceMember &member) {
								return member.userId == m_localUserId;
							});
						const bool liveHostPresent = std::any_of(
							m_currentGame.members.begin(), m_currentGame.members.end(), [](const ServiceMember &member) {
								return member.host;
							});
						if (local != m_currentGame.members.end() && (local->host || !liveHostPresent) &&
							sendRequestLocked("game.end", Json::object()))
							m_gameEnding = true;
					}
					sendRequestLocked("buddy.status", Json{{"status", status}});
				});
			break;
		}
		default:
			break;
	}
}

void OnlineServiceSession::handle(const PeerRequest &request)
{
	OnlineLock lock(m_mutex);
	switch (request.peerRequestType)
	{
		case PeerRequest::PEERREQUEST_LOGIN:
			if (m_authenticated)
				emitPeerLoginLocked();
			break;
		case PeerRequest::PEERREQUEST_LOGOUT:
			clearGameStateLocked();
			m_buddyStatusPolicy.reset();
			m_gameListActive = false;
			m_publicGames.clear();
			m_authenticated = false;
			m_client.setHeartbeatEnabled(false);
			m_expectedClose = true;
			sendRequestLocked("session.close", Json::object());
			break;
		case PeerRequest::PEERREQUEST_MESSAGEPLAYER:
		{
			std::uint64_t userId = 0;
			for (const ServiceMember &member : m_currentGame.members)
				if (member.name == request.nick)
					userId = member.userId;
			if (userId != 0)
				sendRequestLocked("player.chat", Json{
					{"user_id", userId},
					{"message", Truncate(WideCharStringToMultiByte(request.text.c_str()), kMaximumChatLength)},
				});
			break;
		}
		case PeerRequest::PEERREQUEST_MESSAGEROOM:
		{
			const std::string type = request.UTM.isStagingRoom || m_hasCurrentGame ? "game.chat" : "room.chat";
			sendRequestLocked(type, Json{
				{"message", Truncate(WideCharStringToMultiByte(request.text.c_str()), kMaximumChatLength)},
				{"action", static_cast<bool>(request.message.isAction)},
			});
			break;
		}
		case PeerRequest::PEERREQUEST_JOINGROUPROOM:
		{
			const std::string room = serviceRoomIdLocked(request.groupRoom.id);
			if (!room.empty())
				sendRequestLocked("room.join", Json{{"room_id", room}}, {Operation::RoomJoin, request.groupRoom.id});
			break;
		}
		case PeerRequest::PEERREQUEST_LEAVEGROUPROOM:
			m_roomMembers.clear();
			sendRequestLocked("room.leave", Json::object());
			break;
		case PeerRequest::PEERREQUEST_STARTGAMELIST:
			m_gameListActive = true;
			sendRequestLocked("game.list", Json::object(), {Operation::GameList});
			break;
		case PeerRequest::PEERREQUEST_STOPGAMELIST:
			m_gameListActive = false;
			break;
		case PeerRequest::PEERREQUEST_CREATESTAGINGROOM:
		{
			const bool useStats = static_cast<bool>(request.stagingRoomCreation.useStats) && m_persistentProfile;
			Json options{
				{"map", ""},
				{"starting_cash", 10000},
				{"use_stats", useStats},
				{"allow_observers", static_cast<bool>(request.stagingRoomCreation.allowObservers)},
				{"opaque", ""},
				{"slot_list", ""},
			};
			Json create = CompatibilityRequest(static_cast<std::uint32_t>(request.stagingRoomCreation.iniCRC));
			create.update(Json{
				{"name", Truncate(WideCharStringToMultiByte(request.text.c_str()), 48)},
				{"password", Truncate(request.password, 64)},
				{"max_players", MAX_SLOTS},
				{"options", options},
			});
			sendRequestLocked("game.create", create, {Operation::GameCreate});
			break;
		}
		case PeerRequest::PEERREQUEST_SETGAMEOPTIONS:
		{
			Json options = m_hasCurrentGame && m_currentGame.options.is_object() ? m_currentGame.options : Json::object();
			const std::string legacyOptions = Truncate(request.options, kMaximumGameOptionLength);
			options["map"] = Truncate(request.gameOptsMapName, 128);
			options["opaque"] = legacyOptions;
			options["slot_list"] = legacyOptions;
			options["ready_key"] = BuildOnlineReadyKey(legacyOptions);
			options["use_stats"] = options.value("use_stats", true) && m_persistentProfile;
			options["allow_observers"] = options.value("allow_observers", false);
			options["starting_cash"] = options.value("starting_cash", 10000);
			sendRequestLocked("game.options", Json{
				{"max_players", std::clamp(request.gameOptions.maxPlayers, 2, MAX_SLOTS)},
				{"options", options},
			}, {Operation::GameOptions});
			break;
		}
		case PeerRequest::PEERREQUEST_JOINSTAGINGROOM:
		{
			const std::string gameId = serviceGameIdLocked(request.stagingRoom.id);
			if (!gameId.empty())
			{
				Json join = CompatibilityRequest(
					TheGlobalData ? static_cast<std::uint32_t>(TheGlobalData->m_iniCRC) : 0U);
				join.update(Json{{"game_id", gameId}, {"password", request.password}});
				sendRequestLocked("game.join", join, {Operation::GameJoin, request.stagingRoom.id});
			}
			break;
		}
		case PeerRequest::PEERREQUEST_LEAVESTAGINGROOM:
			ApplyOnlineStagingRoomExit(
				m_gameStarted,
				[this]() { clearGameStateLocked(); },
				[this]() { sendRequestLocked("game.leave", Json::object()); });
			break;
		case PeerRequest::PEERREQUEST_UTMPLAYER:
		case PeerRequest::PEERREQUEST_UTMROOM:
			sendUTMLocked(request);
			break;
		case PeerRequest::PEERREQUEST_STARTGAME:
			if (!sendRequestLocked("game.start", Json::object(), {Operation::GameStart}))
				emitFailedToHostLocked(m_lastConnectionError);
			break;
		case PeerRequest::PEERREQUEST_STARTQUICKMATCH:
		{
			Json quickmatch = CompatibilityRequest(static_cast<std::uint32_t>(request.QM.iniCRC));
			quickmatch["mode"] = "1v1";
			sendRequestLocked("quickmatch.enqueue", quickmatch, {Operation::Quickmatch});
			break;
		}
		case PeerRequest::PEERREQUEST_STOPQUICKMATCH:
			sendRequestLocked("quickmatch.cancel", Json::object());
			break;
		case PeerRequest::PEERREQUEST_PUSHSTATS:
			// Retail PUSHSTATS only advertises already-cumulative lobby display values; it is not a persistent delta.
			break;
		default:
			break;
	}
}

void OnlineServiceSession::handle(const PSRequest &request)
{
	OnlineLock lock(m_mutex);
	switch (request.requestType)
	{
		case PSRequest::PSREQUEST_READPLAYERSTATS:
		{
			Json data = Json::object();
			const std::uint64_t userId = serviceUserIdLocked(request.player.id);
			if (userId != 0 && userId != m_localUserId)
				data["user_id"] = userId;
			if (!sendRequestLocked("stats.get", data, {Operation::StatsGet, request.player.id}))
			{
				PSResponse response{};
				response.responseType = PSResponse::PSRESPONSE_COULDNOTCONNECT;
				response.player.id = request.player.id;
				emitPSLocked(response);
			}
			break;
		}
		case PSRequest::PSREQUEST_READCDKEYSTATS:
		{
			PSResponse response{};
			response.responseType = PSResponse::PSRESPONSE_PREORDER;
			response.preorder = false;
			emitPSLocked(response);
			break;
		}
		case PSRequest::PSREQUEST_UPDATEPLAYERSTATS:
			// This request contains cumulative legacy maps. Final per-game deltas are submitted by the host below.
			break;
		case PSRequest::PSREQUEST_SENDGAMERESTOGAMESPY:
			sendGameResultsLocked(request.results);
			break;
		case PSRequest::PSREQUEST_UPDATEPLAYERLOCALE:
		default:
			break;
	}
}

} // namespace

GameSpyBuddyMessageQueueInterface *CreateOnlineBuddyMessageQueue()
{
	return NEW OnlineBuddyMessageQueue;
}

GameSpyPeerMessageQueueInterface *CreateOnlinePeerMessageQueue()
{
	return NEW OnlinePeerMessageQueue;
}

GameSpyPSMessageQueueInterface *CreateOnlinePersistentStorageMessageQueue()
{
	return NEW OnlinePersistentStorageMessageQueue;
}

} // namespace GeneralsOnline
