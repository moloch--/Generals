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

#include <cstdint>
#include <string>

namespace GeneralsOnline
{

// GeneralsX @feature Codex 01/08/2026 Provide an explicit, cross-platform Online service endpoint override.
constexpr std::uint16_t kDefaultControlPort = 29900;

struct OnlineEndpoint
{
	std::string host;
	std::uint16_t controlPort = kDefaultControlPort;
	bool useTLS = false;
	bool configured = false;
};

/** Parse an optional tls:// prefix followed by a DNS hostname or IPv4 address and optional control port.
 *
 * Accepted examples are "tls://online.example.org", "online.example.org:30000",
 * and "192.0.2.10". The tls:// form requires verified TLS and never falls back
 * to plaintext. Bare endpoints are an explicit guest-only development mode.
 * IPv6 literals, other URL schemes, paths, and whitespace are intentionally
 * rejected because the current game transport is IPv4-only.
 */
bool ParseOnlineEndpoint(const char *value, OnlineEndpoint &endpoint, std::string *error = nullptr);

/** Parse and publish the process-wide command-line override. */
bool ConfigureOnlineEndpoint(const char *value, std::string *error = nullptr);

/** Return the process-wide endpoint configuration.
 *
 * Command-line parsing finishes before Online worker threads start, so this
 * immutable-after-startup value does not require synchronization.
 */
const OnlineEndpoint &GetOnlineEndpoint();

/** Clear the explicit override, restoring the legacy Online path. */
void ClearOnlineEndpoint();

} // namespace GeneralsOnline
