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
#include <cstddef>
#include <cstdint>
#include <string>

namespace GeneralsOnline
{

// GeneralsX @feature Codex 01/08/2026 Share authenticated relay state safely between Online workers and transport.
constexpr std::size_t kOnlineServiceSlotCount = 8;
constexpr std::size_t kOnlineRelayTokenSize = 16;
constexpr std::uint8_t kInvalidOnlineServiceSlot = 0xff;

/** Return the deterministic virtual address exposed to retail game state, in host byte order. */
constexpr std::uint32_t OnlineVirtualIPv4HostOrder(std::uint8_t serviceSlot)
{
	return serviceSlot < kOnlineServiceSlotCount ?
		UINT32_C(0x0aff0000) | static_cast<std::uint32_t>(serviceSlot + 1U) : 0U;
}

// GeneralsX @refactor Codex 01/08/2026 Keep native byte-order APIs inside the Online platform implementation.
std::uint32_t OnlineIPv4HostToNetwork(std::uint32_t address);
std::uint32_t OnlineIPv4NetworkToHost(std::uint32_t address);

struct OnlineSessionSnapshot
{
	std::string relayHost;
	std::uint16_t relayPort = 0;
	std::uint64_t gameId = 0;
	std::uint8_t localServiceSlot = kInvalidOnlineServiceSlot;
	std::array<std::uint8_t, kOnlineRelayTokenSize> token{};
	// Addresses are stored in network byte order. Zero means that a slot is not mapped.
	std::array<std::uint32_t, kOnlineServiceSlotCount> virtualIPv4ByServiceSlot{};
	bool ready = false;
};

/** Replace relay credentials and clear all prior mappings/readiness. */
bool ConfigureOnlineRelaySession(
	const char *relayHost,
	std::uint16_t relayPort,
	std::uint64_t gameId,
	std::uint8_t localServiceSlot,
	const std::array<std::uint8_t, kOnlineRelayTokenSize> &token,
	std::string *error = nullptr);

/** Add or replace a service-slot to virtual-IPv4 mapping. */
bool SetOnlineVirtualIPv4(
	std::uint8_t serviceSlot,
	std::uint32_t virtualIPv4,
	std::string *error = nullptr);

/** Publish readiness after credentials and the local virtual address are present. */
bool SetOnlineSessionReady(bool ready, std::string *error = nullptr);

/** Return a consistent copy for a worker or transport operation. */
OnlineSessionSnapshot GetOnlineSessionSnapshot();

bool GetOnlineVirtualIPv4(std::uint8_t serviceSlot, std::uint32_t &virtualIPv4);
bool GetOnlineServiceSlot(std::uint32_t virtualIPv4, std::uint8_t &serviceSlot);

/** Remove credentials, mappings, and readiness. */
void ClearOnlineSession();

} // namespace GeneralsOnline
