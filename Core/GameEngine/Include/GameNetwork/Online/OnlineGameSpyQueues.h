/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#pragma once

#include <string>

class GameSpyBuddyMessageQueueInterface;
class GameSpyPeerMessageQueueInterface;
class GameSpyPSMessageQueueInterface;

namespace GeneralsOnline
{

// GeneralsX @bugfix Codex 01/08/2026 Keep idle pre-auth transport closes from surfacing as stale login failures.
constexpr bool ShouldReportOnlineConnectionLoss(bool expectedClose, bool authenticated, bool authenticating)
{
	return !expectedClose && (authenticated || authenticating);
}

enum class OnlineBuddyStatusKind
{
	Other,
	Online,
	Playing,
};

// GeneralsX @bugfix OpenAI 02/08/2026 Preserve the retail Loading presence across normal post-launch staging teardown.
class OnlineBuddyStatusPolicy
{
public:
	template<typename ApplyStatus>
	bool apply(OnlineBuddyStatusKind status, const char *statusString, ApplyStatus applyStatus)
	{
		const std::string incomingStatusString = statusString != nullptr ? statusString : "";
		if (m_lastStatus == OnlineBuddyStatusKind::Playing && m_lastStatusString == "Loading" &&
			status == OnlineBuddyStatusKind::Online)
		{
			return false;
		}

		m_lastStatus = status;
		m_lastStatusString = incomingStatusString;
		applyStatus();
		return true;
	}

	void reset()
	{
		m_lastStatus = OnlineBuddyStatusKind::Other;
		m_lastStatusString.clear();
	}

	OnlineBuddyStatusKind lastStatus() const { return m_lastStatus; }
	const std::string &lastStatusString() const { return m_lastStatusString; }

private:
	OnlineBuddyStatusKind m_lastStatus = OnlineBuddyStatusKind::Other;
	std::string m_lastStatusString;
};

// GeneralsX @bugfix OpenAI 02/08/2026 Preserve relay state only for an explicitly successful launch detach.
template<typename ClearGameState, typename SendLeave>
void ApplyOnlineStagingRoomExit(
	bool gameStarted,
	bool gameLaunchCommitted,
	ClearGameState clearGameState,
	SendLeave sendLeave)
{
	if (gameStarted && gameLaunchCommitted)
		return;
	clearGameState();
	sendLeave();
}

// GeneralsX @feature Codex 01/08/2026 Adapt the retail Online UI to the GeneralsX service protocol.
GameSpyBuddyMessageQueueInterface *CreateOnlineBuddyMessageQueue();
GameSpyPeerMessageQueueInterface *CreateOnlinePeerMessageQueue();
GameSpyPSMessageQueueInterface *CreateOnlinePersistentStorageMessageQueue();

} // namespace GeneralsOnline
