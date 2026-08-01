/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

// GeneralsX @refactor Codex 01/08/2026 Keep native synchronization in the platform layer.
#include "GameNetwork/Online/OnlineSessionState.h"

#include "GameNetwork/Online/OnlineEndpoint.h"

#include <algorithm>
#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <winsock2.h>
#include <windows.h>
#else
#include <arpa/inet.h>
#include <mutex>
#endif

namespace GeneralsOnline
{
namespace
{

#ifdef _WIN32
class SessionMutex
{
public:
	SessionMutex() { InitializeCriticalSection(&m_section); }
	~SessionMutex() { DeleteCriticalSection(&m_section); }
	SessionMutex(const SessionMutex &) = delete;
	SessionMutex &operator=(const SessionMutex &) = delete;
	void lock() { EnterCriticalSection(&m_section); }
	void unlock() { LeaveCriticalSection(&m_section); }

private:
	CRITICAL_SECTION m_section;
};

class SessionLock
{
public:
	explicit SessionLock(SessionMutex &mutex) : m_mutex(mutex) { m_mutex.lock(); }
	~SessionLock() { m_mutex.unlock(); }
	SessionLock(const SessionLock &) = delete;
	SessionLock &operator=(const SessionLock &) = delete;

private:
	SessionMutex &m_mutex;
};
#else
using SessionMutex = std::mutex;
using SessionLock = std::lock_guard<SessionMutex>;
#endif

// GeneralsX @bugfix Codex 01/08/2026 Avoid std::mutex on the win32-thread-model MinGW toolchain.
SessionMutex s_onlineSessionMutex;
OnlineSessionSnapshot s_onlineSession;

void SetError(std::string *error, const char *message)
{
	if (error != nullptr)
	{
		*error = message;
	}
}

bool HasNonzeroToken(const std::array<std::uint8_t, kOnlineRelayTokenSize> &token)
{
	return std::any_of(token.begin(), token.end(), [](std::uint8_t byte) { return byte != 0; });
}

bool HasRelayCredentials(const OnlineSessionSnapshot &session)
{
	return !session.relayHost.empty() && session.relayPort != 0 && session.gameId != 0 &&
		session.localServiceSlot < kOnlineServiceSlotCount && HasNonzeroToken(session.token);
}

} // namespace

std::uint32_t OnlineIPv4HostToNetwork(std::uint32_t address)
{
	return htonl(address);
}

std::uint32_t OnlineIPv4NetworkToHost(std::uint32_t address)
{
	return ntohl(address);
}

bool ConfigureOnlineRelaySession(
	const char *relayHost,
	std::uint16_t relayPort,
	std::uint64_t gameId,
	std::uint8_t localServiceSlot,
	const std::array<std::uint8_t, kOnlineRelayTokenSize> &token,
	std::string *error)
{
	if (error != nullptr)
	{
		error->clear();
	}

	OnlineEndpoint parsedRelay;
	if (!ParseOnlineEndpoint(relayHost, parsedRelay, error) || parsedRelay.host != relayHost)
	{
		if (error != nullptr && error->empty())
		{
			*error = "Relay host must not include a port";
		}
		return false;
	}
	if (relayPort == 0)
	{
		SetError(error, "Relay port must be between 1 and 65535");
		return false;
	}
	if (gameId == 0)
	{
		SetError(error, "Relay game ID must not be zero");
		return false;
	}
	if (localServiceSlot >= kOnlineServiceSlotCount)
	{
		SetError(error, "Local Online service slot must be between 0 and 7");
		return false;
	}
	if (!HasNonzeroToken(token))
	{
		SetError(error, "Relay token must not be empty");
		return false;
	}

	OnlineSessionSnapshot configured;
	configured.relayHost = parsedRelay.host;
	configured.relayPort = relayPort;
	configured.gameId = gameId;
	configured.localServiceSlot = localServiceSlot;
	configured.token = token;

	SessionLock lock(s_onlineSessionMutex);
	s_onlineSession = configured;
	return true;
}

bool SetOnlineVirtualIPv4(std::uint8_t serviceSlot, std::uint32_t virtualIPv4, std::string *error)
{
	if (error != nullptr)
	{
		error->clear();
	}
	if (serviceSlot >= kOnlineServiceSlotCount)
	{
		SetError(error, "Online service slot must be between 0 and 7");
		return false;
	}
	if (virtualIPv4 == 0)
	{
		SetError(error, "Virtual IPv4 address must not be zero");
		return false;
	}

	SessionLock lock(s_onlineSessionMutex);
	if (!HasRelayCredentials(s_onlineSession))
	{
		SetError(error, "Relay session is not configured");
		return false;
	}
	for (std::size_t index = 0; index < s_onlineSession.virtualIPv4ByServiceSlot.size(); ++index)
	{
		if (index != serviceSlot && s_onlineSession.virtualIPv4ByServiceSlot[index] == virtualIPv4)
		{
			SetError(error, "Virtual IPv4 address is already assigned to another service slot");
			return false;
		}
	}

	s_onlineSession.virtualIPv4ByServiceSlot[serviceSlot] = virtualIPv4;
	s_onlineSession.ready = false;
	return true;
}

bool SetOnlineSessionReady(bool ready, std::string *error)
{
	if (error != nullptr)
	{
		error->clear();
	}

	SessionLock lock(s_onlineSessionMutex);
	if (ready && (!HasRelayCredentials(s_onlineSession) ||
		s_onlineSession.virtualIPv4ByServiceSlot[s_onlineSession.localServiceSlot] == 0))
	{
		SetError(error, "Relay credentials and the local virtual IPv4 mapping are required before readiness");
		return false;
	}

	s_onlineSession.ready = ready;
	return true;
}

OnlineSessionSnapshot GetOnlineSessionSnapshot()
{
	SessionLock lock(s_onlineSessionMutex);
	return s_onlineSession;
}

bool GetOnlineVirtualIPv4(std::uint8_t serviceSlot, std::uint32_t &virtualIPv4)
{
	if (serviceSlot >= kOnlineServiceSlotCount)
	{
		return false;
	}

	SessionLock lock(s_onlineSessionMutex);
	virtualIPv4 = s_onlineSession.virtualIPv4ByServiceSlot[serviceSlot];
	return virtualIPv4 != 0;
}

bool GetOnlineServiceSlot(std::uint32_t virtualIPv4, std::uint8_t &serviceSlot)
{
	if (virtualIPv4 == 0)
	{
		return false;
	}

	SessionLock lock(s_onlineSessionMutex);
	for (std::size_t index = 0; index < s_onlineSession.virtualIPv4ByServiceSlot.size(); ++index)
	{
		if (s_onlineSession.virtualIPv4ByServiceSlot[index] == virtualIPv4)
		{
			serviceSlot = static_cast<std::uint8_t>(index);
			return true;
		}
	}
	return false;
}

void ClearOnlineSession()
{
	SessionLock lock(s_onlineSessionMutex);
	s_onlineSession = OnlineSessionSnapshot();
}

} // namespace GeneralsOnline
