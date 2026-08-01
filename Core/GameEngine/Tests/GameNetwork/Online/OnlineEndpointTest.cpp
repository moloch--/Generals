/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "GameNetwork/Online/OnlineEndpoint.h"
#include "GameNetwork/Online/OnlineControlClient.h"
#include "GameNetwork/Online/OnlineGameOptions.h"
#include "GameNetwork/Online/OnlineSessionState.h"

#include <array>
#include <cstdlib>
#include <chrono>
#include <condition_variable>
#include <cstring>
#include <iostream>
#include <mutex>
#include <string>
#include <thread>

#ifdef _WIN32
#include <winsock2.h>
#else
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>
#endif

namespace
{

#if !defined(_WIN32) || defined(_MSC_VER)
#ifdef _WIN32
using TestSocket = SOCKET;
constexpr TestSocket kInvalidTestSocket = INVALID_SOCKET;
using TestSocketLength = int;

class TestSocketRuntime
{
public:
	TestSocketRuntime()
	{
		WSADATA data{};
		m_started = WSAStartup(MAKEWORD(2, 2), &data) == 0;
	}
	~TestSocketRuntime()
	{
		if (m_started)
			WSACleanup();
	}
	bool started() const { return m_started; }

private:
	bool m_started = false;
};

void CloseTestSocket(TestSocket socket)
{
	if (socket != kInvalidTestSocket)
		closesocket(socket);
}
#else
using TestSocket = int;
constexpr TestSocket kInvalidTestSocket = -1;
using TestSocketLength = socklen_t;

class TestSocketRuntime
{
public:
	bool started() const { return true; }
};

void CloseTestSocket(TestSocket socket)
{
	if (socket != kInvalidTestSocket)
		close(socket);
}
#endif
#endif

void Expect(bool condition, const char *message)
{
	if (!condition)
	{
		std::cerr << "FAIL: " << message << '\n';
		std::exit(1);
	}
}

void TestEndpointParser()
{
	GeneralsOnline::OnlineEndpoint endpoint;
	std::string error;
	Expect(GeneralsOnline::ParseOnlineEndpoint("online.example.org", endpoint, &error), error.c_str());
	Expect(endpoint.configured, "valid endpoint is not explicitly configured");
	Expect(endpoint.host == "online.example.org", "DNS hostname changed unexpectedly");
	Expect(endpoint.controlPort == GeneralsOnline::kDefaultControlPort, "default control port is wrong");
	Expect(!endpoint.useTLS, "bare endpoint unexpectedly enabled TLS");

	Expect(GeneralsOnline::ParseOnlineEndpoint("192.0.2.10:65535", endpoint, &error), error.c_str());
	Expect(endpoint.host == "192.0.2.10" && endpoint.controlPort == 65535 && !endpoint.useTLS,
		"IPv4 endpoint did not parse");

	Expect(GeneralsOnline::ParseOnlineEndpoint("tls://online.example.org", endpoint, &error), error.c_str());
	Expect(endpoint.host == "online.example.org" && endpoint.useTLS,
		"TLS DNS endpoint did not parse");
	Expect(endpoint.controlPort == GeneralsOnline::kDefaultControlPort, "TLS endpoint default port is wrong");

	Expect(GeneralsOnline::ParseOnlineEndpoint("tls://192.0.2.10:65535", endpoint, &error), error.c_str());
	Expect(endpoint.host == "192.0.2.10" && endpoint.controlPort == 65535 && endpoint.useTLS,
		"TLS IPv4 endpoint did not parse");

	const std::array<const char *, 17> invalid = {
		"", "tls://", "TLS://online.example.org", "https://online.example.org",
		"tls://online.example.org/path", "online.example.org/path", "online example.org",
		"online.example.org:0", "online.example.org:65536", "online.example.org:notaport",
		"tls://online.example.org:1:2", "999.0.0.1", "2001:db8::1", "-online.example.org",
		"online..example.org", "example_.org", "tls://2001:db8::1"
	};
	for (const char *value : invalid)
	{
		Expect(!GeneralsOnline::ParseOnlineEndpoint(value, endpoint, &error), value);
		Expect(!error.empty(), "invalid endpoint did not explain the failure");
	}
}

void TestSessionState()
{
	GeneralsOnline::ClearOnlineSession();
	std::array<std::uint8_t, GeneralsOnline::kOnlineRelayTokenSize> token{};
	token[0] = 1;
	std::string error;
	Expect(GeneralsOnline::ConfigureOnlineRelaySession(
		"relay.example.org", 29901, 0x0123456789abcdefULL, 2, token, &error), error.c_str());
	Expect(!GeneralsOnline::SetOnlineSessionReady(true, &error), "session became ready without local mapping");

	const std::uint32_t quickmatchAddress = GeneralsOnline::OnlineVirtualIPv4HostOrder(2);
	Expect(quickmatchAddress == 0x0aff0003U, "quickmatch virtual address is not in retail host order");
	const std::uint32_t localAddress = htonl(quickmatchAddress);
	Expect(GeneralsOnline::SetOnlineVirtualIPv4(2, localAddress, &error), error.c_str());
	Expect(GeneralsOnline::SetOnlineSessionReady(true, &error), error.c_str());

	const GeneralsOnline::OnlineSessionSnapshot snapshot = GeneralsOnline::GetOnlineSessionSnapshot();
	Expect(snapshot.ready && snapshot.localServiceSlot == 2, "ready session snapshot is inconsistent");
	std::uint8_t serviceSlot = GeneralsOnline::kInvalidOnlineServiceSlot;
	Expect(GeneralsOnline::GetOnlineServiceSlot(localAddress, serviceSlot) && serviceSlot == 2,
		"reverse virtual address mapping failed");
	Expect(GeneralsOnline::GetOnlineServiceSlot(htonl(quickmatchAddress), serviceSlot) && serviceSlot == 2,
		"retail host-order quickmatch address did not round-trip through relay lookup conversion");

	GeneralsOnline::ClearOnlineSession();
	Expect(!GeneralsOnline::GetOnlineSessionSnapshot().ready, "session clear retained readiness");
}

void TestReadyKey()
{
	const std::string options =
		"US=1;M=00Maps/Test;MC=1234;MS=2048;SD=42;C=30;SR=0;SC=10000;O=N;"
		"S=HHost,AFF0001,8086,TT,0,-1,0,0,1:HPeer,AFF0002,8087,FT,1,2,1,1,1:O:X:O:X:O:X:;";
	const std::string readyKey = GeneralsOnline::BuildOnlineReadyKey(options);
	Expect(readyKey.size() == 22U && readyKey.rfind("gxrk1:", 0) == 0, "ready key format changed");

	std::string accepted = options;
	accepted.replace(accepted.find("HPeer,AFF0002,8087,FT") + 19U, 1U, "T");
	Expect(GeneralsOnline::BuildOnlineReadyKey(accepted) == readyKey,
		"human accepted-state echo changed the ready key");

	std::string mapAvailability = options;
	mapAvailability.replace(mapAvailability.find("HPeer,AFF0002,8087,FT") + 20U, 1U, "F");
	Expect(GeneralsOnline::BuildOnlineReadyKey(mapAvailability) != readyKey,
		"human map availability was omitted from the ready key");

	std::string color = options;
	color.replace(color.find("HPeer,AFF0002,8087,FT,1") + 22U, 1U, "2");
	Expect(GeneralsOnline::BuildOnlineReadyKey(color) != readyKey,
		"human color was omitted from the ready key");

	std::string cash = options;
	cash.replace(cash.find("SC=10000"), 8U, "SC=20000");
	Expect(GeneralsOnline::BuildOnlineReadyKey(cash) != readyKey,
		"global game options were omitted from the ready key");

	std::string slotState = options;
	slotState.replace(slotState.find(":O:X:"), 3U, ":X:");
	Expect(GeneralsOnline::BuildOnlineReadyKey(slotState) != readyKey,
		"non-human slot state was omitted from the ready key");

	GeneralsOnline::OnlineReadyPlayerStates readyStates{
		{"Peer", 0x0aff0002U, 1},
	};
	const std::string projected = GeneralsOnline::ApplyOnlineReadyStates(options, readyStates);
	Expect(projected.find("HPeer,AFF0002,8087,TT") != std::string::npos,
		"service readiness was not projected into the matching human slot");
	Expect(projected.find("HHost,AFF0001,8086,TT") != std::string::npos,
		"an unspecified slot's accepted state was changed");
	readyStates[0].ready = 0;
	const std::string reset = GeneralsOnline::ApplyOnlineReadyStates(projected, readyStates);
	Expect(reset.find("HPeer,AFF0002,8087,FT") != std::string::npos,
		"service readiness reset did not clear the accepted flag");

	const std::string aiBeforePeer =
		"US=1;M=00Maps/Test;MC=1234;MS=2048;SD=42;C=30;SR=0;SC=10000;O=N;"
		"S=HHost,AFF0001,8086,TT,0,-1,0,0,1:CE,1,-1,1,0:HPeer,AFF0002,8087,FT,2,2,2,1,1:O:X:O:X:O:;";
	readyStates[0].ready = 1;
	const std::string aiProjected = GeneralsOnline::ApplyOnlineReadyStates(aiBeforePeer, readyStates);
	Expect(aiProjected.find("CE,1,-1,1,0:HPeer,AFF0002,8087,TT") != std::string::npos,
		"service slot 1 readiness did not match a peer in retail slot 2 after an AI");

	std::string closedBeforePeer = aiBeforePeer;
	const std::string aiRecord = "CE,1,-1,1,0:";
	closedBeforePeer.replace(closedBeforePeer.find(aiRecord), aiRecord.size(), "X:");
	const std::string closedProjected = GeneralsOnline::ApplyOnlineReadyStates(closedBeforePeer, readyStates);
	Expect(closedProjected.find("X:HPeer,AFF0002,8087,TT") != std::string::npos,
		"service slot 1 readiness did not match a peer in retail slot 2 after a closed slot");
}

void TestAuthenticationSelection()
{
	using GeneralsOnline::OnlineAuthenticationKind;
	Expect(GeneralsOnline::SelectOnlineAuthentication(false, false) == OnlineAuthenticationKind::Guest,
		"plaintext login selected password authentication");
	Expect(GeneralsOnline::SelectOnlineAuthentication(false, true) == OnlineAuthenticationKind::Guest,
		"plaintext account creation selected password authentication");
	Expect(GeneralsOnline::SelectOnlineAuthentication(true, false) == OnlineAuthenticationKind::Login,
		"TLS login did not select persistent authentication");
	Expect(GeneralsOnline::SelectOnlineAuthentication(true, true) == OnlineAuthenticationKind::Register,
		"TLS account creation did not select registration");
}

void TestGameEndDisposition()
{
	Expect(!GeneralsOnline::ShouldDisconnectForOnlineGameEnd(false, "host_left"),
		"staged host departure was treated as an in-game disconnect");
	Expect(!GeneralsOnline::ShouldDisconnectForOnlineGameEnd(true, "host_ended"),
		"normal host score-screen completion was treated as a connection loss");
	Expect(GeneralsOnline::ShouldDisconnectForOnlineGameEnd(true, "player_left"),
		"started-game player departure did not terminate the relay session visibly");
	Expect(GeneralsOnline::ShouldDisconnectForOnlineGameEnd(true, "connection_closed"),
		"started-game connection loss did not terminate the relay session visibly");
}

void TestGameCompatibility()
{
	const GeneralsOnline::OnlineGameCompatibility local{"zerohour", 1, UINT32_C(0x12345678)};
	const GeneralsOnline::OnlineGameCompatibility same{"zerohour", 1, UINT32_C(0x12345678)};
	Expect(GeneralsOnline::IsOnlineGameCompatible(local, same),
		"an exact Online compatibility tuple was rejected");
	Expect(GeneralsOnline::ProjectOnlineExeCRC(UINT32_C(0x13579bdf), local, same) == UINT32_C(0x13579bdf),
		"an exact compatibility tuple did not retain the local EXE CRC");

	auto mismatch = same;
	mismatch.product = "generals";
	Expect(!GeneralsOnline::IsOnlineGameCompatible(local, mismatch),
		"a cross-product Online game was accepted");
	Expect(GeneralsOnline::ProjectOnlineExeCRC(UINT32_C(0x13579bdf), local, mismatch) == UINT32_C(0xeca86420),
		"a cross-product game did not enter the retail EXE-CRC rejection path");
	mismatch = same;
	mismatch.version = 2;
	Expect(!GeneralsOnline::IsOnlineGameCompatible(local, mismatch),
		"a different Online protocol generation was accepted");
	mismatch = same;
	mismatch.iniCRC ^= UINT32_C(1);
	Expect(!GeneralsOnline::IsOnlineGameCompatible(local, mismatch),
		"different gameplay data was accepted");
}

void TestGameBrowserDiff()
{
	std::vector<GeneralsOnline::OnlineGameBrowserSummary> games;
	for (int index = 0; index < 64; ++index)
	{
		GeneralsOnline::OnlineGameBrowserSummary game;
		const std::string id = std::to_string(index);
		game.gameId = std::string(16U - id.size(), '0') + id;
		game.name = "Game " + std::to_string(index);
		game.map = "Maps/Test/Test.map";
		game.hostName = "Host " + std::to_string(index);
		game.state = "open";
		game.players = 1;
		game.maximumPlayers = 8;
		games.push_back(game);
	}
	Expect(GeneralsOnline::DiffOnlineGameBrowserSummaries({}, games).size() == 64U,
		"initial 64-game browser list did not produce one add per game");
	Expect(GeneralsOnline::DiffOnlineGameBrowserSummaries(games, games).empty(),
		"an identical repeated 64-game browser list produced queue-flooding updates");

	auto changed = games;
	changed[17].players = 2;
	const auto update = GeneralsOnline::DiffOnlineGameBrowserSummaries(games, changed);
	Expect(update.size() == 1U && update.front().type == GeneralsOnline::OnlineGameBrowserChangeType::Update,
		"one material browser change did not produce exactly one update");
	changed.pop_back();
	const auto updateAndRemove = GeneralsOnline::DiffOnlineGameBrowserSummaries(games, changed);
	Expect(updateAndRemove.size() == 2U,
		"browser diff did not retain independent update and remove deltas");
	changed = games;
	changed[0].compatibility = {"zerohour", 1, UINT32_C(0x12345678)};
	const auto compatibilityUpdate = GeneralsOnline::DiffOnlineGameBrowserSummaries(games, changed);
	Expect(compatibilityUpdate.size() == 1U &&
		compatibilityUpdate.front().type == GeneralsOnline::OnlineGameBrowserChangeType::Update,
		"a compatibility tuple change was omitted from the browser delta");
}

void TestProfileIdentityMapping()
{
	Expect(GeneralsOnline::OnlineProfileIdForUserId(1) == 1,
		"small persistent service identity did not retain its global profile ID");
	Expect(GeneralsOnline::OnlineProfileIdForUserId(42) == 42,
		"persistent service identity mapping is not stable");
	const std::uint64_t guest = UINT64_C(0x800000000000002a);
	const int guestProfile = GeneralsOnline::OnlineProfileIdForUserId(guest);
	Expect(guestProfile >= 0x40000000 && guestProfile > 0,
		"guest service identity escaped its separated legacy profile domain");
	Expect(guestProfile == GeneralsOnline::OnlineProfileIdForUserId(guest),
		"guest service identity mapping is not deterministic");
}

void TestLegacyResultsParser()
{
	const std::string packet =
		"\\seed\\42\\hostname\\Host\\numplayers\\3"
		"\\player_0\\Host\\pid_0\\1000\\result_0\\win"
		"\\player_1\\Peer\\pid_1\\1001\\result_1\\discon"
		"\\player_2\\AIPlayer\\pid_2\\0\\result_2\\loss";
	std::vector<GeneralsOnline::OnlineLegacyGameResult> results;
	std::string error;
	Expect(GeneralsOnline::ParseOnlineLegacyGameResults(packet, results, &error), error.c_str());
	Expect(results.size() == 2U, "legacy results parser did not discard the AI profile");
	Expect(results[0].profileId == 1000 && results[0].outcome == "win",
		"legacy winner translation is incorrect");
	Expect(results[1].profileId == 1001 && results[1].outcome == "disconnect",
		"legacy disconnect translation is incorrect");

	const std::string missingOutcome = "\\pid_0\\1000\\player_0\\Host";
	Expect(!GeneralsOnline::ParseOnlineLegacyGameResults(missingOutcome, results, &error),
		"legacy results parser accepted a missing outcome");
	const std::string duplicateProfile =
		"\\pid_0\\1000\\result_0\\win\\pid_1\\1000\\result_1\\loss";
	Expect(!GeneralsOnline::ParseOnlineLegacyGameResults(duplicateProfile, results, &error),
		"legacy results parser accepted a duplicate human profile");
	const std::string unmatchedOutcome = "\\pid_0\\1000\\result_0\\win\\result_1\\loss";
	Expect(!GeneralsOnline::ParseOnlineLegacyGameResults(unmatchedOutcome, results, &error),
		"legacy results parser accepted an outcome without a profile");
}

#if !defined(_WIN32) || defined(_MSC_VER)
void TestControlClientFraming()
{
	// GeneralsX @feature Codex 01/08/2026 Exercise the native Win32 worker and Winsock framing path under MSVC.
	TestSocketRuntime socketRuntime;
	Expect(socketRuntime.started(), "could not initialize control test sockets");
	const TestSocket listener = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
	Expect(listener != kInvalidTestSocket, "could not create control test listener");
	sockaddr_in address{};
	address.sin_family = AF_INET;
	address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	address.sin_port = 0;
	Expect(bind(listener, reinterpret_cast<const sockaddr *>(&address), sizeof(address)) == 0,
		"could not bind control test listener");
	Expect(listen(listener, 1) == 0, "could not listen for control test client");
	TestSocketLength addressSize = sizeof(address);
	Expect(getsockname(listener, reinterpret_cast<sockaddr *>(&address), &addressSize) == 0,
		"could not read control test port");

	std::string receivedByServer;
	std::thread server([&]() {
		const TestSocket connection = accept(listener, nullptr, nullptr);
		if (connection == kInvalidTestSocket)
			return;
		char bytes[1024];
		while (receivedByServer.find('\n') == std::string::npos)
		{
			const int count = recv(connection, bytes, sizeof(bytes), 0);
			if (count <= 0)
				break;
			receivedByServer.append(bytes, static_cast<std::size_t>(count));
		}
		const char reply[] = "{\"v\":1,\"type\":\"test.event\",\"data\":{}}\r\n";
		send(connection, reply, sizeof(reply) - 1U, 0);
		CloseTestSocket(connection);
	});

	std::mutex callbackMutex;
	std::condition_variable callbackReady;
	std::string receivedByClient;
	GeneralsOnline::OnlineControlClient client;
	std::string error;
	Expect(client.start(
		"127.0.0.1",
		ntohs(address.sin_port),
		false,
		[&](const std::string &line) {
			std::lock_guard<std::mutex> lock(callbackMutex);
			receivedByClient = line;
			callbackReady.notify_one();
		},
		[](bool, const std::string &) {},
		&error), error.c_str());
	Expect(client.sendLine("{\"v\":1,\"type\":\"test\",\"id\":\"1\",\"data\":{}}", &error), error.c_str());
	Expect(!client.sendLine("{}\n{}", &error), "raw newline was accepted as one control frame");
	{
		std::unique_lock<std::mutex> lock(callbackMutex);
		Expect(callbackReady.wait_for(lock, std::chrono::seconds(3), [&]() { return !receivedByClient.empty(); }),
			"control client did not deliver an NDJSON line");
	}
	client.stop();
	server.join();
	CloseTestSocket(listener);
	Expect(receivedByServer.ends_with("\n"), "control client did not append LF framing");
	Expect(receivedByClient == "{\"v\":1,\"type\":\"test.event\",\"data\":{}}",
		"control client did not strip CRLF framing");
}

#ifdef SAGE_ONLINE_TLS
// GeneralsX @feature Codex 01/08/2026 Exercise the real libcurl TLS path and prove it never leaks queued JSON via plaintext fallback.
void TestTLSNeverFallsBackToPlaintext()
{
	TestSocketRuntime socketRuntime;
	Expect(socketRuntime.started(), "could not initialize TLS-negative test sockets");
	const TestSocket listener = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
	Expect(listener != kInvalidTestSocket, "could not create TLS-negative test listener");
	sockaddr_in address{};
	address.sin_family = AF_INET;
	address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	address.sin_port = 0;
	Expect(bind(listener, reinterpret_cast<const sockaddr *>(&address), sizeof(address)) == 0,
		"could not bind TLS-negative test listener");
	Expect(listen(listener, 1) == 0, "could not listen for TLS-negative test client");
	TestSocketLength addressSize = sizeof(address);
	Expect(getsockname(listener, reinterpret_cast<sockaddr *>(&address), &addressSize) == 0,
		"could not read TLS-negative test port");

	std::string receivedByServer;
	std::thread server([&]() {
		const TestSocket connection = accept(listener, nullptr, nullptr);
		if (connection == kInvalidTestSocket)
			return;
		char bytes[4096];
		const int count = recv(connection, bytes, sizeof(bytes), 0);
		if (count > 0)
			receivedByServer.append(bytes, static_cast<std::size_t>(count));
		CloseTestSocket(connection);
	});

	std::mutex callbackMutex;
	std::condition_variable callbackReady;
	bool failed = false;
	std::string failureDetail;
	GeneralsOnline::OnlineControlClient client;
	std::string error;
	Expect(client.start(
		"127.0.0.1",
		ntohs(address.sin_port),
		true,
		[](const std::string &) {},
		[&](bool connected, const std::string &detail) {
			if (connected)
				return;
			std::lock_guard<std::mutex> lock(callbackMutex);
			failed = true;
			failureDetail = detail;
			callbackReady.notify_one();
		},
		&error), error.c_str());
	Expect(client.sendLine("{\"v\":1,\"type\":\"must.not.be.plaintext\",\"data\":{}}", &error), error.c_str());
	{
		std::unique_lock<std::mutex> lock(callbackMutex);
		Expect(callbackReady.wait_for(lock, std::chrono::seconds(5), [&]() { return failed; }),
			"TLS-negative connection did not terminate");
	}
	client.stop();
	server.join();
	CloseTestSocket(listener);
	Expect(!failureDetail.empty(), "TLS-negative connection did not report an error");
	Expect(!receivedByServer.empty() && static_cast<unsigned char>(receivedByServer.front()) == 0x16,
		"TLS endpoint did not begin with a TLS handshake record");
	Expect(receivedByServer.find("must.not.be.plaintext") == std::string::npos,
		"TLS failure exposed queued control JSON through a plaintext fallback");
}
#endif
#endif

} // namespace

// GeneralsX @feature Codex 01/08/2026 Cover Online endpoint validation and synchronized relay session publication.
int main()
{
	TestEndpointParser();
	TestSessionState();
	TestReadyKey();
	TestAuthenticationSelection();
	TestGameEndDisposition();
	TestGameCompatibility();
	TestGameBrowserDiff();
	TestProfileIdentityMapping();
	TestLegacyResultsParser();
#if !defined(_WIN32) || defined(_MSC_VER)
	TestControlClientFraming();
#ifdef SAGE_ONLINE_TLS
	TestTLSNeverFallsBackToPlaintext();
#endif
#endif
	return 0;
}
