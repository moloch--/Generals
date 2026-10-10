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

#include <array>
#include <cstdint>
#include <string>
#include <vector>

namespace GeneralsOnline
{

// GeneralsX @bugfix OpenAI 02/08/2026 Fail closed instead of advertising compatibility from an opt-out build.
#if defined(USE_DETERMINISTIC_MATH)
inline constexpr int kOnlineCompatibilityVersion = 2;
#else
inline constexpr int kOnlineCompatibilityVersion = 0;
#endif

// GeneralsX @feature Codex 01/08/2026 Fingerprint material game options without treating ready-state echoes as changes.
std::string BuildOnlineReadyKey(const std::string &gameOptions);

struct OnlineReadyPlayerState
{
	std::string name;
	// GameInfo serializes IPv4 addresses in host byte order inside the legacy slot list.
	std::uint32_t virtualIPv4HostOrder = 0;
	std::int8_t ready = -1;
};

using OnlineReadyPlayerStates = std::vector<OnlineReadyPlayerState>;

/** Project service readiness into human records matched by virtual address or name, not service-slot index. */
std::string ApplyOnlineReadyStates(const std::string &gameOptions, const OnlineReadyPlayerStates &readyStates);

/** Map a 64-bit service identity into the positive legacy GPProfile namespace deterministically. */
int OnlineProfileIdForUserId(std::uint64_t userId);

enum class OnlineAuthenticationKind
{
	Guest,
	Login,
	Register,
};

/** Select an authentication command without ever allowing password credentials on plaintext. */
OnlineAuthenticationKind SelectOnlineAuthentication(bool useTLS, bool createAccount);

/** Started matches end cleanly only when the host explicitly closes them from the score-screen seam. */
bool ShouldDisconnectForOnlineGameEnd(bool gameStarted, const std::string &reason);

struct OnlineGameCompatibility
{
	std::string product;
	int version = 0;
	std::uint32_t iniCRC = 0;
};

/** Online games are compatible only when their product, protocol generation, and gameplay data match exactly. */
bool IsOnlineGameCompatible(
	const OnlineGameCompatibility &local,
	const OnlineGameCompatibility &remote);

/** Project an incompatible service game into the existing retail EXE-CRC rejection path. */
std::uint32_t ProjectOnlineExeCRC(
	std::uint32_t localExeCRC,
	const OnlineGameCompatibility &local,
	const OnlineGameCompatibility &remote);

struct OnlineGameBrowserSummary
{
	std::string gameId;
	std::string name;
	std::string map;
	std::string hostName;
	std::string state;
	int players = 0;
	int maximumPlayers = 8;
	bool hasPassword = false;
	OnlineGameCompatibility compatibility;
};

struct OnlineGameMemberSummary
{
	std::uint64_t userId = 0;
	std::string name;
	bool host = false;
	bool ready = false;
	int slot = -1;
};

/** Player-info callbacks are needed for new identities/topology, not repeated snapshots or readiness alone. */
bool ShouldEmitOnlinePlayerInfo(
	const OnlineGameMemberSummary *previous,
	const OnlineGameMemberSummary &current);

enum class OnlineGameBrowserChangeType
{
	Add,
	Update,
	Remove,
};

struct OnlineGameBrowserChange
{
	OnlineGameBrowserChangeType type = OnlineGameBrowserChangeType::Add;
	OnlineGameBrowserSummary game;
};

/** Produce only material browser deltas so repeated full lists cannot flood the retail queue. */
std::vector<OnlineGameBrowserChange> DiffOnlineGameBrowserSummaries(
	const std::vector<OnlineGameBrowserSummary> &previous,
	const std::vector<OnlineGameBrowserSummary> &current);

struct OnlineLegacyGameResult
{
	int profileId = 0;
	std::string outcome;
};

/** Extract bounded human profile outcomes from the retail GameSpy final-results packet. */
bool ParseOnlineLegacyGameResults(
	const std::string &packet,
	std::vector<OnlineLegacyGameResult> &results,
	std::string *error = nullptr);

} // namespace GeneralsOnline
