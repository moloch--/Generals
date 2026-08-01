/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "GameNetwork/Online/OnlineGameOptions.h"

#include <cstddef>
#include <cstdint>
#include <limits>
#include <map>
#include <set>
#include <string_view>

namespace GeneralsOnline
{
namespace
{

std::size_t FindSlotList(const std::string &options)
{
	std::size_t position = 0;
	while ((position = options.find("S=", position)) != std::string::npos)
	{
		if (position == 0 || options[position - 1U] == ';')
			return position + 2U;
		position += 2U;
	}
	return std::string::npos;
}

bool FindHumanAcceptedFlag(
	const std::string &options,
	std::size_t recordStart,
	std::size_t recordEnd,
	std::size_t &accepted)
{
	if (recordStart >= recordEnd || options[recordStart] != 'H')
		return false;
	std::size_t separator = recordStart;
	for (int field = 0; field < 3 && separator != std::string::npos; ++field)
	{
		separator = options.find(',', separator + 1U);
		if (separator != std::string::npos && separator >= recordEnd)
			separator = std::string::npos;
	}
	if (separator == std::string::npos)
		return false;
	accepted = separator + 1U;
	const std::size_t nextSeparator = options.find(',', accepted);
	return accepted < recordEnd && nextSeparator == accepted + 2U && nextSeparator < recordEnd &&
		(options[accepted] == 'T' || options[accepted] == 'F') &&
		(options[accepted + 1U] == 'T' || options[accepted + 1U] == 'F');
}

bool ParseHexIPv4(std::string_view value, std::uint32_t &address)
{
	if (value.empty() || value.size() > 8U)
		return false;
	std::uint32_t parsed = 0;
	for (const char character : value)
	{
		unsigned int nibble = 0;
		if (character >= '0' && character <= '9')
			nibble = static_cast<unsigned int>(character - '0');
		else if (character >= 'a' && character <= 'f')
			nibble = static_cast<unsigned int>(character - 'a' + 10);
		else if (character >= 'A' && character <= 'F')
			nibble = static_cast<unsigned int>(character - 'A' + 10);
		else
			return false;
		parsed = (parsed << 4U) | nibble;
	}
	address = parsed;
	return true;
}

bool ParseHumanIdentity(
	const std::string &options,
	std::size_t recordStart,
	std::size_t recordEnd,
	std::string_view &name,
	std::uint32_t &address)
{
	if (recordStart >= recordEnd || options[recordStart] != 'H')
		return false;
	const std::size_t nameEnd = options.find(',', recordStart + 1U);
	if (nameEnd == std::string::npos || nameEnd >= recordEnd || nameEnd == recordStart + 1U)
		return false;
	const std::size_t addressEnd = options.find(',', nameEnd + 1U);
	if (addressEnd == std::string::npos || addressEnd >= recordEnd)
		return false;
	name = std::string_view(options.data() + recordStart + 1U, nameEnd - recordStart - 1U);
	return ParseHexIPv4(
		std::string_view(options.data() + nameEnd + 1U, addressEnd - nameEnd - 1U), address);
}

const OnlineReadyPlayerState *FindReadyState(
	const OnlineReadyPlayerStates &readyStates,
	std::string_view name,
	std::uint32_t address)
{
	const OnlineReadyPlayerState *match = nullptr;
	if (address != 0)
	{
		for (const OnlineReadyPlayerState &state : readyStates)
		{
			if (state.ready < 0 || state.virtualIPv4HostOrder != address)
				continue;
			if (match != nullptr)
				return nullptr;
			match = &state;
		}
		if (match != nullptr)
			return match;
	}

	for (const OnlineReadyPlayerState &state : readyStates)
	{
		if (state.ready < 0 || state.name != name)
			continue;
		if (match != nullptr)
			return nullptr;
		match = &state;
	}
	return match;
}

void NormalizeHumanAcceptedFlags(std::string &options)
{
	const std::size_t slotListStart = FindSlotList(options);
	if (slotListStart == std::string::npos)
		return;
	const std::size_t slotListEnd = options.find(';', slotListStart);
	const std::size_t end = slotListEnd == std::string::npos ? options.size() : slotListEnd;
	std::size_t recordStart = slotListStart;
	while (recordStart < end)
	{
		std::size_t recordEnd = options.find(':', recordStart);
		if (recordEnd == std::string::npos || recordEnd > end)
			recordEnd = end;
		std::size_t accepted = std::string::npos;
		if (FindHumanAcceptedFlag(options, recordStart, recordEnd, accepted))
			options[accepted] = '-';
		if (recordEnd == end)
			break;
		recordStart = recordEnd + 1U;
	}
}

std::string FNV1a64(const std::string &value)
{
	std::uint64_t hash = UINT64_C(14695981039346656037);
	for (const unsigned char byte : value)
	{
		hash ^= byte;
		hash *= UINT64_C(1099511628211);
	}
	static constexpr char hex[] = "0123456789abcdef";
	std::string encoded(16U, '0');
	for (std::size_t index = 0; index < encoded.size(); ++index)
	{
		encoded[encoded.size() - index - 1U] = hex[hash & 0x0fU];
		hash >>= 4U;
	}
	return encoded;
}

void SetError(std::string *error, const char *message)
{
	if (error != nullptr)
		*error = message;
}

bool ParseIndex(std::string_view key, std::string_view prefix, int &index)
{
	if (!key.starts_with(prefix) || key.size() == prefix.size())
		return false;
	unsigned int parsed = 0;
	for (std::size_t position = prefix.size(); position < key.size(); ++position)
	{
		if (key[position] < '0' || key[position] > '9')
			return false;
		parsed = parsed * 10U + static_cast<unsigned int>(key[position] - '0');
		if (parsed >= 8U)
			return false;
	}
	index = static_cast<int>(parsed);
	return true;
}

bool ParseProfileId(std::string_view text, int &profileId)
{
	if (text.empty())
		return false;
	unsigned int parsed = 0;
	for (const char character : text)
	{
		if (character < '0' || character > '9')
			return false;
		const unsigned int digit = static_cast<unsigned int>(character - '0');
		if (parsed > (static_cast<unsigned int>(std::numeric_limits<int>::max()) - digit) / 10U)
			return false;
		parsed = parsed * 10U + digit;
	}
	profileId = static_cast<int>(parsed);
	return true;
}

} // namespace

std::string BuildOnlineReadyKey(const std::string &gameOptions)
{
	std::string normalized = gameOptions;
	NormalizeHumanAcceptedFlags(normalized);
	return std::string("gxrk1:") + FNV1a64(normalized);
}

std::string ApplyOnlineReadyStates(const std::string &gameOptions, const OnlineReadyPlayerStates &readyStates)
{
	std::string projected = gameOptions;
	const std::size_t slotListStart = FindSlotList(projected);
	if (slotListStart == std::string::npos)
		return projected;
	const std::size_t slotListEnd = projected.find(';', slotListStart);
	const std::size_t end = slotListEnd == std::string::npos ? projected.size() : slotListEnd;
	std::size_t recordStart = slotListStart;
	while (recordStart < end)
	{
		std::size_t recordEnd = projected.find(':', recordStart);
		if (recordEnd == std::string::npos || recordEnd > end)
			recordEnd = end;
		std::size_t accepted = std::string::npos;
		std::string_view name;
		std::uint32_t address = 0;
		if (FindHumanAcceptedFlag(projected, recordStart, recordEnd, accepted) &&
			ParseHumanIdentity(projected, recordStart, recordEnd, name, address))
		{
			const OnlineReadyPlayerState *state = FindReadyState(readyStates, name, address);
			if (state != nullptr)
				projected[accepted] = state->ready == 0 ? 'F' : 'T';
		}
		if (recordEnd == end)
			break;
		recordStart = recordEnd + 1U;
	}
	return projected;
}

int OnlineProfileIdForUserId(std::uint64_t userId)
{
	if (userId == 0)
		return 0;
	constexpr std::uint64_t profileDomain = UINT64_C(0x3fffffff);
	constexpr std::uint64_t guestBit = UINT64_C(1) << 63U;
	if ((userId & guestBit) == 0 && userId <= profileDomain)
		return static_cast<int>(userId);

	std::uint64_t mixed = userId;
	mixed ^= mixed >> 33U;
	mixed *= UINT64_C(0xff51afd7ed558ccd);
	mixed ^= mixed >> 33U;
	mixed *= UINT64_C(0xc4ceb9fe1a85ec53);
	mixed ^= mixed >> 33U;
	int profileId = static_cast<int>(mixed & profileDomain);
	if (profileId == 0)
		profileId = 1;
	if ((userId & guestBit) != 0)
		profileId |= 0x40000000;
	return profileId;
}

OnlineAuthenticationKind SelectOnlineAuthentication(bool useTLS, bool createAccount)
{
	if (!useTLS)
		return OnlineAuthenticationKind::Guest;
	return createAccount ? OnlineAuthenticationKind::Register : OnlineAuthenticationKind::Login;
}

bool ShouldDisconnectForOnlineGameEnd(bool gameStarted, const std::string &reason)
{
	return gameStarted && reason != "host_ended";
}

bool IsOnlineGameCompatible(
	const OnlineGameCompatibility &local,
	const OnlineGameCompatibility &remote)
{
	return !local.product.empty() && local.product == remote.product && local.version == remote.version &&
		local.iniCRC == remote.iniCRC;
}

std::uint32_t ProjectOnlineExeCRC(
	std::uint32_t localExeCRC,
	const OnlineGameCompatibility &local,
	const OnlineGameCompatibility &remote)
{
	return IsOnlineGameCompatible(local, remote) ? localExeCRC : localExeCRC ^ UINT32_C(0xffffffff);
}

std::vector<OnlineGameBrowserChange> DiffOnlineGameBrowserSummaries(
	const std::vector<OnlineGameBrowserSummary> &previous,
	const std::vector<OnlineGameBrowserSummary> &current)
{
	auto equal = [](const OnlineGameBrowserSummary &left, const OnlineGameBrowserSummary &right) {
		return left.gameId == right.gameId && left.name == right.name && left.map == right.map &&
			left.hostName == right.hostName && left.state == right.state && left.players == right.players &&
			left.maximumPlayers == right.maximumPlayers && left.hasPassword == right.hasPassword &&
			left.compatibility.product == right.compatibility.product &&
			left.compatibility.version == right.compatibility.version &&
			left.compatibility.iniCRC == right.compatibility.iniCRC;
	};
	std::map<std::string, OnlineGameBrowserSummary> oldById;
	std::map<std::string, OnlineGameBrowserSummary> newById;
	for (const OnlineGameBrowserSummary &game : previous)
		if (!game.gameId.empty())
			oldById[game.gameId] = game;
	for (const OnlineGameBrowserSummary &game : current)
		if (!game.gameId.empty())
			newById[game.gameId] = game;

	std::vector<OnlineGameBrowserChange> changes;
	changes.reserve(oldById.size() + newById.size());
	for (const auto &[gameId, game] : newById)
	{
		const auto old = oldById.find(gameId);
		if (old == oldById.end())
			changes.push_back({OnlineGameBrowserChangeType::Add, game});
		else if (!equal(old->second, game))
			changes.push_back({OnlineGameBrowserChangeType::Update, game});
	}
	for (const auto &[gameId, game] : oldById)
		if (newById.count(gameId) == 0)
			changes.push_back({OnlineGameBrowserChangeType::Remove, game});
	return changes;
}

bool ParseOnlineLegacyGameResults(
	const std::string &packet,
	std::vector<OnlineLegacyGameResult> &results,
	std::string *error)
{
	results.clear();
	if (error != nullptr)
		error->clear();
	if (packet.empty() || packet.size() > 16384U || packet.front() != '\\')
	{
		SetError(error, "legacy results packet is empty, oversized, or missing its leading separator");
		return false;
	}

	std::map<int, int> profiles;
	std::map<int, std::string> outcomes;
	std::size_t position = 1U;
	while (position < packet.size())
	{
		const std::size_t keyEnd = packet.find('\\', position);
		if (keyEnd == std::string::npos || keyEnd == position)
		{
			SetError(error, "legacy results packet contains an incomplete key");
			return false;
		}
		const std::size_t valueStart = keyEnd + 1U;
		const std::size_t valueEnd = packet.find('\\', valueStart);
		const std::string_view key(packet.data() + position, keyEnd - position);
		const std::string_view value(packet.data() + valueStart,
			(valueEnd == std::string::npos ? packet.size() : valueEnd) - valueStart);

		int index = -1;
		if (ParseIndex(key, "pid_", index))
		{
			int profileId = 0;
			if (!ParseProfileId(value, profileId) || !profiles.emplace(index, profileId).second)
			{
				SetError(error, "legacy results packet contains an invalid or duplicate profile ID");
				return false;
			}
		}
		else if (ParseIndex(key, "result_", index))
		{
			std::string outcome(value);
			if (outcome == "discon" || outcome == "desync")
				outcome = "disconnect";
			if ((outcome != "win" && outcome != "loss" && outcome != "disconnect") ||
				!outcomes.emplace(index, std::move(outcome)).second)
			{
				SetError(error, "legacy results packet contains an invalid or duplicate outcome");
				return false;
			}
		}

		if (valueEnd == std::string::npos)
			break;
		position = valueEnd + 1U;
	}

	std::set<int> seenProfiles;
	if (profiles.size() != outcomes.size())
	{
		SetError(error, "legacy results packet has mismatched profile and outcome fields");
		return false;
	}
	for (const auto &[index, profileId] : profiles)
	{
		const auto outcome = outcomes.find(index);
		if (outcome == outcomes.end())
		{
			SetError(error, "legacy results packet is missing a player outcome");
			return false;
		}
		if (profileId == 0)
			continue; // AI slots have no persistent profile.
		if (!seenProfiles.insert(profileId).second)
		{
			SetError(error, "legacy results packet repeats a human profile");
			return false;
		}
		results.push_back(OnlineLegacyGameResult{profileId, outcome->second});
	}
	if (results.empty())
	{
		SetError(error, "legacy results packet contains no human profiles");
		return false;
	}
	return true;
}

} // namespace GeneralsOnline
