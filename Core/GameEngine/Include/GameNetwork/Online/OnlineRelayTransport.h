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

class Transport;

namespace GeneralsOnline
{

/** Create the platform implementation that relays complete Generals UDP
 * datagrams while preserving the retail transport's queues and validation.
 * Relay authentication does not encrypt the native gameplay payload.
 */
// GeneralsX @refactor Codex 01/08/2026 Expose a native-free factory to game-layer transport selection.
Transport *CreateOnlineRelayTransport();

} // namespace GeneralsOnline
