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

// GeneralsX @feature Codex 01/08/2026 Adapt the retail Online UI to the GeneralsX service protocol.
GameSpyBuddyMessageQueueInterface *CreateOnlineBuddyMessageQueue();
GameSpyPeerMessageQueueInterface *CreateOnlinePeerMessageQueue();
GameSpyPSMessageQueueInterface *CreateOnlinePersistentStorageMessageQueue();

} // namespace GeneralsOnline
