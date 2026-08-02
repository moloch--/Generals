/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <winsock2.h>
#else
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>
#endif

#include "GameNetwork/Online/OnlineRelayTransport.h"
#include "GameNetwork/Online/OnlineSessionState.h"
#include "Common/AsciiString.h"
#include "GameNetwork/Transport.h"

#include <algorithm>
#include <array>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <iostream>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

namespace
{

constexpr std::size_t kRelayHeaderSize = 32;
constexpr std::uint8_t kRelayVersion = 1;
constexpr std::uint8_t kRelayBind = 1;
constexpr std::uint8_t kRelayData = 2;
constexpr std::uint8_t kRelayBindAck = 4;
constexpr std::uint8_t kRelayDataOut = 5;
constexpr std::uint16_t kVirtualPeerPort = 8888;
constexpr std::array<std::uint8_t, 4> kRelayMagic = {'G', 'X', 'R', 'L'};

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

void Fail(const char *message)
{
	std::cerr << "FAIL: " << message << '\n';
	std::exit(1);
}

void Expect(bool condition, const char *message)
{
	if (!condition)
		Fail(message);
}

bool SetReceiveTimeout(TestSocket socket, int milliseconds)
{
#ifdef _WIN32
	const DWORD timeout = static_cast<DWORD>(milliseconds);
	return setsockopt(socket, SOL_SOCKET, SO_RCVTIMEO,
		reinterpret_cast<const char *>(&timeout), sizeof(timeout)) == 0;
#else
	timeval timeout{};
	timeout.tv_sec = milliseconds / 1000;
	timeout.tv_usec = (milliseconds % 1000) * 1000;
	return setsockopt(socket, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout)) == 0;
#endif
}

void WriteBigEndian64(std::uint8_t *destination, std::uint64_t value)
{
	for (int index = 7; index >= 0; --index)
	{
		destination[index] = static_cast<std::uint8_t>(value & 0xffU);
		value >>= 8U;
	}
}

std::uint64_t ReadBigEndian64(const std::uint8_t *source)
{
	std::uint64_t value = 0;
	for (int index = 0; index < 8; ++index)
		value = (value << 8U) | source[index];
	return value;
}

std::vector<std::uint8_t> BuildRelayFrame(
	std::uint8_t kind,
	std::uint8_t source,
	std::uint8_t destination,
	std::uint64_t gameId,
	const std::array<std::uint8_t, GeneralsOnline::kOnlineRelayTokenSize> &token,
	const std::vector<std::uint8_t> &payload = {})
{
	std::vector<std::uint8_t> frame(kRelayHeaderSize + payload.size());
	std::copy(kRelayMagic.begin(), kRelayMagic.end(), frame.begin());
	frame[4] = kRelayVersion;
	frame[5] = kind;
	frame[6] = source;
	frame[7] = destination;
	WriteBigEndian64(frame.data() + 8, gameId);
	std::copy(token.begin(), token.end(), frame.begin() + 16);
	std::copy(payload.begin(), payload.end(), frame.begin() + kRelayHeaderSize);
	return frame;
}

bool SendRelayFrame(TestSocket socket, const sockaddr_in &destination, const std::vector<std::uint8_t> &frame)
{
	return sendto(socket,
		reinterpret_cast<const char *>(frame.data()),
		static_cast<int>(frame.size()),
		0,
		reinterpret_cast<const sockaddr *>(&destination),
		sizeof(destination)) == static_cast<int>(frame.size());
}

int ReceiveRelayFrame(
	TestSocket socket,
	std::array<std::uint8_t, kRelayHeaderSize + MAX_NETWORK_MESSAGE_LEN> &frame,
	sockaddr_in &source)
{
	TestSocketLength sourceLength = sizeof(source);
	return recvfrom(socket,
		reinterpret_cast<char *>(frame.data()),
		static_cast<int>(frame.size()),
		0,
		reinterpret_cast<sockaddr *>(&source),
		&sourceLength);
}

bool HasExpectedHeader(
	const std::uint8_t *frame,
	int length,
	std::uint8_t kind,
	std::uint8_t source,
	std::uint8_t destination,
	std::uint64_t gameId,
	const std::array<std::uint8_t, GeneralsOnline::kOnlineRelayTokenSize> &token)
{
	return length >= static_cast<int>(kRelayHeaderSize) &&
		std::equal(kRelayMagic.begin(), kRelayMagic.end(), frame) &&
		frame[4] == kRelayVersion && frame[5] == kind && frame[6] == source &&
		frame[7] == destination && ReadBigEndian64(frame + 8) == gameId &&
		std::equal(token.begin(), token.end(), frame + 16);
}

// GeneralsX @test Codex 01/08/2026 Verify authenticated bind filtering, slot routing, and opaque relay payloads.
void TestAuthenticatedRelayRoundTrip()
{
	TestSocketRuntime socketRuntime;
	Expect(socketRuntime.started(), "could not initialize relay test sockets");

	const TestSocket relaySocket = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
	const TestSocket attackerSocket = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
	Expect(relaySocket != kInvalidTestSocket && attackerSocket != kInvalidTestSocket,
		"could not create relay test sockets");
	Expect(SetReceiveTimeout(relaySocket, 2000), "could not set relay test receive timeout");

	sockaddr_in relayAddress{};
	relayAddress.sin_family = AF_INET;
	relayAddress.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	relayAddress.sin_port = 0;
	Expect(bind(relaySocket, reinterpret_cast<const sockaddr *>(&relayAddress), sizeof(relayAddress)) == 0,
		"could not bind loopback relay socket");
	TestSocketLength relayAddressLength = sizeof(relayAddress);
	Expect(getsockname(relaySocket, reinterpret_cast<sockaddr *>(&relayAddress), &relayAddressLength) == 0,
		"could not read loopback relay port");

	sockaddr_in attackerAddress{};
	attackerAddress.sin_family = AF_INET;
	attackerAddress.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	attackerAddress.sin_port = 0;
	Expect(bind(attackerSocket,
		reinterpret_cast<const sockaddr *>(&attackerAddress), sizeof(attackerAddress)) == 0,
		"could not bind wrong-source relay socket");

	constexpr std::uint64_t gameId = UINT64_C(0x0123456789abcdef);
	constexpr std::uint8_t localSlot = 2;
	constexpr std::uint8_t peerSlot = 5;
	std::array<std::uint8_t, GeneralsOnline::kOnlineRelayTokenSize> token{};
	for (std::size_t index = 0; index < token.size(); ++index)
		token[index] = static_cast<std::uint8_t>(index + 1U);
	const auto wrongToken = [&]() {
		auto value = token;
		value[7] ^= 0xffU;
		return value;
	}();

	GeneralsOnline::ClearOnlineSession();
	std::string sessionError;
	Expect(GeneralsOnline::ConfigureOnlineRelaySession(
		"127.0.0.1", ntohs(relayAddress.sin_port), gameId, localSlot, token, &sessionError),
		sessionError.c_str());
	const std::uint32_t localAddress = GeneralsOnline::OnlineVirtualIPv4HostOrder(localSlot);
	const std::uint32_t peerAddress = GeneralsOnline::OnlineVirtualIPv4HostOrder(peerSlot);
	Expect(GeneralsOnline::SetOnlineVirtualIPv4(
		localSlot, GeneralsOnline::OnlineIPv4HostToNetwork(localAddress), &sessionError), sessionError.c_str());
	Expect(GeneralsOnline::SetOnlineVirtualIPv4(
		peerSlot, GeneralsOnline::OnlineIPv4HostToNetwork(peerAddress), &sessionError), sessionError.c_str());
	Expect(GeneralsOnline::SetOnlineSessionReady(true, &sessionError), sessionError.c_str());

	std::mutex serverMutex;
	std::condition_variable serverCondition;
	bool responseFramesSent = false;
	std::string serverError;
	std::thread server([&]() {
		auto reportError = [&](const char *message) {
			serverError = message;
			std::lock_guard<std::mutex> lock(serverMutex);
			responseFramesSent = true;
			serverCondition.notify_one();
		};
		std::array<std::uint8_t, kRelayHeaderSize + MAX_NETWORK_MESSAGE_LEN> incoming{};
		sockaddr_in clientAddress{};
		int length = ReceiveRelayFrame(relaySocket, incoming, clientAddress);
		if (!HasExpectedHeader(incoming.data(), length, kRelayBind, localSlot, localSlot, gameId, token))
		{
			reportError("initial authenticated relay bind was malformed");
			return;
		}

		if (!SendRelayFrame(attackerSocket, clientAddress,
			BuildRelayFrame(kRelayBindAck, localSlot, localSlot, gameId, token)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayBindAck, localSlot, localSlot, gameId ^ UINT64_C(1), token)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayBindAck, localSlot, localSlot, gameId, wrongToken)))
		{
			reportError("could not send rejected relay bind acknowledgements");
			return;
		}

		length = ReceiveRelayFrame(relaySocket, incoming, clientAddress);
		if (!HasExpectedHeader(incoming.data(), length, kRelayBind, localSlot, localSlot, gameId, token))
		{
			reportError("relay transport accepted an unauthenticated bind acknowledgement");
			return;
		}
		if (!SendRelayFrame(relaySocket, clientAddress,
			BuildRelayFrame(kRelayBindAck, localSlot, localSlot, gameId, token)))
		{
			reportError("could not send authenticated relay bind acknowledgement");
			return;
		}

		length = ReceiveRelayFrame(relaySocket, incoming, clientAddress);
		if (!HasExpectedHeader(incoming.data(), length, kRelayData, localSlot, peerSlot, gameId, token) ||
			length <= static_cast<int>(kRelayHeaderSize))
		{
			reportError("relay transport did not map the virtual peer to its service slot");
			return;
		}
		const std::vector<std::uint8_t> opaquePayload(
			incoming.begin() + kRelayHeaderSize, incoming.begin() + length);

		if (!SendRelayFrame(attackerSocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, peerSlot, localSlot, gameId, token, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, peerSlot, localSlot, gameId ^ UINT64_C(1), token, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, peerSlot, localSlot, gameId, wrongToken, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, GeneralsOnline::kOnlineServiceSlotCount,
					localSlot, gameId, token, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, 4, localSlot, gameId, token, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, peerSlot, 0, gameId, token, opaquePayload)) ||
			!SendRelayFrame(relaySocket, clientAddress,
				BuildRelayFrame(kRelayDataOut, peerSlot, localSlot, gameId, token, opaquePayload)))
		{
			reportError("could not send relay data validation sequence");
			return;
		}

		std::lock_guard<std::mutex> lock(serverMutex);
		responseFramesSent = true;
		serverCondition.notify_one();
	});

	std::unique_ptr<Transport> transport(GeneralsOnline::CreateOnlineRelayTransport());
	Expect(transport != nullptr, "relay transport factory returned null");
	const bool initialized = transport->init(static_cast<UnsignedInt>(0), 0);
	if (!initialized)
	{
		server.join();
		CloseTestSocket(attackerSocket);
		CloseTestSocket(relaySocket);
		Fail("relay transport did not complete authenticated loopback bind");
	}

	const std::array<std::uint8_t, 11> payload = {
		0x00, 'G', 'X', 'R', 'L', 0xff, 0x7f, 0x80, 0x00, 0x42, 0x01,
	};
	Expect(transport->queueSend(peerAddress, 9999, payload.data(), static_cast<Int>(payload.size())),
		"relay transport did not queue an opaque binary payload");
	Expect(transport->doSend(), "relay transport did not send its queued payload");
	{
		std::unique_lock<std::mutex> lock(serverMutex);
		Expect(serverCondition.wait_for(lock, std::chrono::seconds(3), [&]() { return responseFramesSent; }),
			"loopback relay did not send its validation frames");
	}
	server.join();
	Expect(serverError.empty(), serverError.c_str());

	std::this_thread::sleep_for(std::chrono::milliseconds(30));
	Expect(transport->doRecv(), "relay transport reported a receive error");
	std::size_t receivedCount = 0;
	const TransportMessage *received = nullptr;
	for (const TransportMessage &message : transport->m_inBuffer)
	{
		if (message.length > 0)
		{
			++receivedCount;
			received = &message;
		}
	}
	Expect(receivedCount == 1 && received != nullptr,
		"relay transport accepted a forged or invalid data frame");
	Expect(received->addr == peerAddress && received->port == kVirtualPeerPort,
		"relay source slot did not map back to the peer virtual address");
	Expect(received->length == static_cast<Int>(payload.size()) &&
		std::equal(payload.begin(), payload.end(), received->data),
		"opaque relay payload changed during its loopback round trip");

	transport.reset();
	GeneralsOnline::ClearOnlineSession();
	CloseTestSocket(attackerSocket);
	CloseTestSocket(relaySocket);
}
#endif

} // namespace

// Transport::init(AsciiString, ...) is present in the production vtable but the
// Online relay always calls its numeric overload; keep the focused target from
// pulling the rest of networkutil.cpp into this narrow test.
UnsignedInt ResolveIP(AsciiString)
{
	return 0;
}

// The unused AsciiString overload above still contributes references through
// Transport's vtable. Keep its test-only copy isolated from the game allocator.
AsciiString::AsciiString(const AsciiString &) : m_data(nullptr)
{
}

void AsciiString::releaseBuffer()
{
	m_data = nullptr;
}

void RunOnlineRelayTransportTests()
{
#if !defined(_WIN32) || defined(_MSC_VER)
	TestAuthenticatedRelayRoundTrip();
#endif
}
