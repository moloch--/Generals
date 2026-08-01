/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "PreRTS.h" // This must go first in EVERY cpp file in the GameEngine

// GeneralsX @refactor Codex 01/08/2026 Keep relay resolver and socket operations in the platform layer.
#include "GameNetwork/Online/OnlineRelayTransport.h"

#include "GameNetwork/Online/OnlineSessionState.h"
#include "GameNetwork/Transport.h"
#include "GameNetwork/networkutil.h"

#include <algorithm>
#include <array>
#include <cstring>

namespace
{

// GeneralsX @feature OpenAI 01/08/2026 Relay opaque Online gameplay datagrams without changing their inner wire format.
constexpr std::size_t kRelayHeaderSize = 32;
constexpr UnsignedByte kRelayVersion = 1;
constexpr UnsignedByte kRelayBind = 1;
constexpr UnsignedByte kRelayData = 2;
constexpr UnsignedByte kRelayKeepalive = 3;
constexpr UnsignedByte kRelayBindAck = 4;
constexpr UnsignedByte kRelayDataOut = 5;
constexpr UnsignedByte kRelayBroadcast = 0xff;
constexpr UnsignedInt kKeepaliveIntervalMs = 5000;
constexpr UnsignedInt kBindRetryIntervalMs = 500;
constexpr UnsignedInt kBindTimeoutMs = 3000;
constexpr UnsignedShort kVirtualPeerPort = 8888;
constexpr std::array<UnsignedByte, 4> kRelayMagic = {'G', 'X', 'R', 'L'};

void WriteBigEndian64(UnsignedByte *destination, std::uint64_t value)
{
	for (int index = 7; index >= 0; --index)
	{
		destination[index] = static_cast<UnsignedByte>(value & 0xffU);
		value >>= 8U;
	}
}

std::uint64_t ReadBigEndian64(const UnsignedByte *source)
{
	std::uint64_t value = 0;
	for (int index = 0; index < 8; ++index)
	{
		value = (value << 8U) | source[index];
	}
	return value;
}

void BuildRelayHeader(
	UnsignedByte *frame,
	UnsignedByte kind,
	UnsignedByte source,
	UnsignedByte destination,
	const GeneralsOnline::OnlineSessionSnapshot &session)
{
	std::copy(kRelayMagic.begin(), kRelayMagic.end(), frame);
	frame[4] = kRelayVersion;
	frame[5] = kind;
	frame[6] = source;
	frame[7] = destination;
	WriteBigEndian64(frame + 8, session.gameId);
	std::copy(session.token.begin(), session.token.end(), frame + 16);
}

bool HasValidRelayHeader(
	const UnsignedByte *frame,
	Int length,
	const GeneralsOnline::OnlineSessionSnapshot &session)
{
	return length >= static_cast<Int>(kRelayHeaderSize) &&
		std::equal(kRelayMagic.begin(), kRelayMagic.end(), frame) &&
		frame[4] == kRelayVersion &&
		ReadBigEndian64(frame + 8) == session.gameId &&
		std::equal(session.token.begin(), session.token.end(), frame + 16);
}

class OnlineRelayTransport final : public Transport
{
public:
	OnlineRelayTransport();
	~OnlineRelayTransport() override = default;

	Bool init(AsciiString ip, UnsignedShort port) override;
	Bool init(UnsignedInt ip, UnsignedShort port) override;
	void reset() override;
	Bool doRecv() override;
	Bool allowBroadcasts(Bool) override { return false; }

protected:
	Int readDatagram(UnsignedByte *buffer, UnsignedInt length, TransportDatagramSource *from) override;
	Int writeDatagram(const UnsignedByte *buffer, UnsignedInt length, UnsignedInt address, UnsignedShort port) override;

private:
	Bool resolveRelay();
	Bool sendControlFrame(UnsignedByte kind);
	Bool waitForBindAck();

	UnsignedInt m_relayAddress;
	UnsignedInt m_lastKeepalive;
	Bool m_bound;
};

} // namespace

OnlineRelayTransport::OnlineRelayTransport() :
	m_relayAddress(0),
	m_lastKeepalive(0),
	m_bound(false)
{
}

Bool OnlineRelayTransport::init(AsciiString, UnsignedShort)
{
	return init(0, 0);
}

Bool OnlineRelayTransport::init(UnsignedInt, UnsignedShort)
{
	const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
	if (!session.ready)
	{
		DEBUG_LOG(("OnlineRelayTransport::init - relay session is not ready"));
		return false;
	}

	// Bind an ephemeral local socket. The virtual staging address must never be
	// passed to the operating system because it only identifies a relay slot.
	if (!Transport::init(0, 0) || !resolveRelay())
	{
		Transport::reset();
		return false;
	}

	m_lastKeepalive = timeGetTime();
	m_bound = false;
	if (!sendControlFrame(kRelayBind) || !waitForBindAck())
	{
		DEBUG_LOG(("OnlineRelayTransport::init - authenticated UDP relay bind timed out"));
		Transport::reset();
		return false;
	}
	return true;
}

void OnlineRelayTransport::reset()
{
	m_relayAddress = 0;
	m_lastKeepalive = 0;
	m_bound = false;
	Transport::reset();
}

Bool OnlineRelayTransport::doRecv()
{
	const UnsignedInt now = timeGetTime();
	if (now - m_lastKeepalive >= kKeepaliveIntervalMs)
	{
		if (!sendControlFrame(m_bound ? kRelayKeepalive : kRelayBind))
		{
			return false;
		}
		m_lastKeepalive = now;
	}
	return Transport::doRecv();
}

Bool OnlineRelayTransport::resolveRelay()
{
	const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
	// GeneralsX @bugfix OpenAI 01/08/2026 Resolve the validated relay hostname as DNS even when its first label starts with a digit.
	// The legacy ResolveIP helper treats every leading digit as an IPv4 literal;
	// keep that LAN behavior unchanged and use the socket resolver only here.
	hostent *resolved = gethostbyname(session.relayHost.c_str());
	if (resolved == nullptr || resolved->h_addrtype != AF_INET ||
		resolved->h_length != static_cast<Int>(sizeof(in_addr)) || resolved->h_addr_list[0] == nullptr)
	{
		DEBUG_LOG(("OnlineRelayTransport::resolveRelay - could not resolve %s", session.relayHost.c_str()));
		return false;
	}

	in_addr address{};
	std::memcpy(&address, resolved->h_addr_list[0], sizeof(address));
	m_relayAddress = ntohl(address.s_addr);
	if (m_relayAddress == 0 || m_relayAddress == 0xffffffffU)
	{
		DEBUG_LOG(("OnlineRelayTransport::resolveRelay - invalid unicast address for %s", session.relayHost.c_str()));
		m_relayAddress = 0;
		return false;
	}
	return true;
}

Bool OnlineRelayTransport::sendControlFrame(UnsignedByte kind)
{
	if (m_udpsock == nullptr || m_relayAddress == 0)
	{
		return false;
	}

	const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
	if (!session.ready)
	{
		return false;
	}

	std::array<UnsignedByte, kRelayHeaderSize> frame{};
	BuildRelayHeader(frame.data(), kind, session.localServiceSlot, session.localServiceSlot, session);
	return m_udpsock->Write(frame.data(), frame.size(), m_relayAddress, session.relayPort) ==
		static_cast<Int>(frame.size());
}

Bool OnlineRelayTransport::waitForBindAck()
{
	const UnsignedInt started = timeGetTime();
	UnsignedInt lastBind = started;
	std::array<UnsignedByte, kRelayHeaderSize + MAX_NETWORK_MESSAGE_LEN> frame{};
	while (timeGetTime() - started < kBindTimeoutMs)
	{
		sockaddr_in relaySource{};
		const Int frameLength = m_udpsock->Read(frame.data(), frame.size(), &relaySource);
		const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
		if (!session.ready)
			return false;
		if (frameLength == static_cast<Int>(kRelayHeaderSize) &&
			ntohl(relaySource.sin_addr.s_addr) == m_relayAddress &&
			ntohs(relaySource.sin_port) == session.relayPort &&
			HasValidRelayHeader(frame.data(), frameLength, session) &&
			frame[5] == kRelayBindAck && frame[6] == session.localServiceSlot &&
			frame[7] == session.localServiceSlot)
		{
			m_bound = true;
			m_lastKeepalive = timeGetTime();
			return true;
		}

		const UnsignedInt now = timeGetTime();
		if (now - lastBind >= kBindRetryIntervalMs)
		{
			if (!sendControlFrame(kRelayBind))
				return false;
			lastBind = now;
		}
		Sleep(10);
	}
	return false;
}

Int OnlineRelayTransport::writeDatagram(
	const UnsignedByte *buffer,
	UnsignedInt length,
	UnsignedInt address,
	UnsignedShort)
{
	if (buffer == nullptr || length > MAX_NETWORK_MESSAGE_LEN || m_udpsock == nullptr || m_relayAddress == 0)
	{
		return -1;
	}

	const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
	if (!session.ready)
	{
		return -1;
	}

	UnsignedByte destination = GeneralsOnline::kInvalidOnlineServiceSlot;
	if (address == 0xffffffffU)
	{
		destination = kRelayBroadcast;
	}
	else if (!GeneralsOnline::GetOnlineServiceSlot(
			 GeneralsOnline::OnlineIPv4HostToNetwork(address), destination) ||
		destination == session.localServiceSlot)
	{
		DEBUG_LOG(("OnlineRelayTransport::writeDatagram - no relay slot for virtual address %08x", address));
		return -1;
	}

	std::array<UnsignedByte, kRelayHeaderSize + MAX_NETWORK_MESSAGE_LEN> frame{};
	BuildRelayHeader(frame.data(), kRelayData, session.localServiceSlot, destination, session);
	std::memcpy(frame.data() + kRelayHeaderSize, buffer, length);
	const UnsignedInt frameLength = static_cast<UnsignedInt>(kRelayHeaderSize) + length;
	const Int sent = m_udpsock->Write(frame.data(), frameLength, m_relayAddress, session.relayPort);
	return sent == static_cast<Int>(frameLength) ? static_cast<Int>(length) : sent;
}

Int OnlineRelayTransport::readDatagram(
	UnsignedByte *buffer,
	UnsignedInt length,
	TransportDatagramSource *from)
{
	if (buffer == nullptr || from == nullptr || m_udpsock == nullptr || m_relayAddress == 0)
	{
		return -1;
	}

	std::array<UnsignedByte, kRelayHeaderSize + MAX_NETWORK_MESSAGE_LEN> frame{};
	for (;;)
	{
		sockaddr_in relaySource{};
		const Int frameLength = m_udpsock->Read(frame.data(), frame.size(), &relaySource);
		if (frameLength <= 0)
		{
			return frameLength;
		}

		const GeneralsOnline::OnlineSessionSnapshot session = GeneralsOnline::GetOnlineSessionSnapshot();
		if (!session.ready || ntohl(relaySource.sin_addr.s_addr) != m_relayAddress ||
			ntohs(relaySource.sin_port) != session.relayPort ||
			!HasValidRelayHeader(frame.data(), frameLength, session))
		{
			continue;
		}

		const UnsignedByte kind = frame[5];
		if (kind == kRelayBindAck && frameLength == static_cast<Int>(kRelayHeaderSize) &&
			frame[6] == session.localServiceSlot && frame[7] == session.localServiceSlot)
		{
			m_bound = true;
			continue;
		}

		const Int payloadLength = frameLength - static_cast<Int>(kRelayHeaderSize);
		if (kind != kRelayDataOut || frame[6] >= GeneralsOnline::kOnlineServiceSlotCount ||
			frame[7] != session.localServiceSlot || payloadLength <= 0 ||
			static_cast<UnsignedInt>(payloadLength) > length)
		{
			continue;
		}

		std::uint32_t sourceAddress = 0;
		if (!GeneralsOnline::GetOnlineVirtualIPv4(frame[6], sourceAddress))
		{
			continue;
		}

		std::memcpy(buffer, frame.data() + kRelayHeaderSize, payloadLength);
		from->address = GeneralsOnline::OnlineIPv4NetworkToHost(sourceAddress);
		from->port = kVirtualPeerPort;
		return payloadLength;
	}
}

Transport *GeneralsOnline::CreateOnlineRelayTransport()
{
	// GeneralsX @refactor Codex 01/08/2026 Hide the native relay implementation behind a platform-neutral factory.
	return new OnlineRelayTransport;
}
