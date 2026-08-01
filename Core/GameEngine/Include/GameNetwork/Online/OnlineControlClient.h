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
#include <functional>
#include <memory>
#include <string>

namespace GeneralsOnline
{

/** A bounded, IPv4 NDJSON client used by the custom Online service.
 *
 * Callbacks run on the socket worker. Consumers must enqueue work for the game
 * thread and must not call stop() from a callback. TLS connections verify the
 * certificate chain and hostname, require TLS 1.2 or newer, and never fall back
 * to plaintext.
 */
class OnlineControlClient
{
public:
	using LineHandler = std::function<void(const std::string &line)>;
	using StateHandler = std::function<void(bool connected, const std::string &detail)>;

	OnlineControlClient();
	~OnlineControlClient();

	OnlineControlClient(const OnlineControlClient &) = delete;
	OnlineControlClient &operator=(const OnlineControlClient &) = delete;

	// GeneralsX @feature Codex 01/08/2026 Connect the Online control plane without changing LAN networking.
	bool start(
		const std::string &host,
		std::uint16_t port,
		bool useTLS,
		LineHandler lineHandler,
		StateHandler stateHandler,
		std::string *error = nullptr);
	void stop();

	/** Queue one JSON object. The trailing LF is added by this class. */
	bool sendLine(const std::string &line, std::string *error = nullptr);
	/** Keep an authenticated session alive independently of menu activity. */
	void setHeartbeatEnabled(bool enabled);

	bool isRunning() const;
	bool isConnected() const;
	bool isConnecting() const;

private:
	class Impl;
	std::unique_ptr<Impl> m_impl;
};

} // namespace GeneralsOnline
