/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

// GeneralsX @refactor Codex 01/08/2026 Keep native control sockets and worker APIs in the platform layer.
#include "GameNetwork/Online/OnlineControlClient.h"

#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <process.h>
#include <winsock2.h>
#include <windows.h>
#else
#include <arpa/inet.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <unistd.h>
#endif

#ifdef SAGE_ONLINE_TLS
#include <curl/curl.h>
#endif

#include <atomic>
#include <cerrno>
#include <chrono>
#include <cstring>
#include <deque>
#ifndef _WIN32
#include <mutex>
#include <thread>
#endif
#include <utility>

namespace GeneralsOnline
{
namespace
{

constexpr std::size_t kMaximumControlLine = 64U * 1024U;
constexpr std::size_t kMaximumQueuedLines = 256U;
constexpr std::chrono::seconds kConnectTimeout(5);
constexpr std::chrono::seconds kHeartbeatInterval(25);
constexpr std::chrono::milliseconds kPollInterval(100);
constexpr const char *kHeartbeatLine = "{\"v\":1,\"type\":\"ping\",\"id\":\"gx-heartbeat\",\"data\":{}}\n";

// GeneralsX @bugfix Codex 01/08/2026 Use native Win32 synchronization when the MinGW runtime has no C++ thread model.
#ifdef _WIN32
class ControlMutex
{
public:
	ControlMutex() { InitializeCriticalSection(&m_section); }
	~ControlMutex() { DeleteCriticalSection(&m_section); }
	ControlMutex(const ControlMutex &) = delete;
	ControlMutex &operator=(const ControlMutex &) = delete;
	void lock() { EnterCriticalSection(&m_section); }
	void unlock() { LeaveCriticalSection(&m_section); }

private:
	CRITICAL_SECTION m_section;
};

class ControlLock
{
public:
	explicit ControlLock(ControlMutex &mutex) : m_mutex(mutex) { m_mutex.lock(); }
	~ControlLock() { m_mutex.unlock(); }
	ControlLock(const ControlLock &) = delete;
	ControlLock &operator=(const ControlLock &) = delete;

private:
	ControlMutex &m_mutex;
};
#else
using ControlMutex = std::mutex;
using ControlLock = std::lock_guard<ControlMutex>;
#endif

#ifdef _WIN32
using NativeSocket = SOCKET;
constexpr NativeSocket kInvalidSocket = INVALID_SOCKET;

int LastSocketError()
{
	return WSAGetLastError();
}

bool IsConnectPending(int error)
{
	return error == WSAEWOULDBLOCK || error == WSAEINPROGRESS || error == WSAEALREADY;
}

bool IsWouldBlock(int error)
{
	return error == WSAEWOULDBLOCK;
}

bool SetNonBlocking(NativeSocket socket)
{
	u_long enabled = 1;
	return ioctlsocket(socket, FIONBIO, &enabled) == 0;
}

void CloseSocket(NativeSocket socket)
{
	if (socket != kInvalidSocket)
		closesocket(socket);
}
#else
using NativeSocket = int;
constexpr NativeSocket kInvalidSocket = -1;

int LastSocketError()
{
	return errno;
}

bool IsConnectPending(int error)
{
	return error == EINPROGRESS || error == EALREADY;
}

bool IsWouldBlock(int error)
{
	return error == EAGAIN || error == EWOULDBLOCK;
}

bool SetNonBlocking(NativeSocket socket)
{
	const int flags = fcntl(socket, F_GETFL, 0);
	return flags >= 0 && fcntl(socket, F_SETFL, flags | O_NONBLOCK) == 0;
}

void CloseSocket(NativeSocket socket)
{
	if (socket != kInvalidSocket)
		close(socket);
}
#endif

std::string SocketErrorText(const char *operation, int error)
{
	return std::string(operation) + " failed (socket error " + std::to_string(error) + ")";
}

#ifdef SAGE_ONLINE_TLS
CURLcode InitializeCurl()
{
	// Process lifetime initialization avoids invalidating the update checker or another libcurl user.
	static const CURLcode result = curl_global_init(CURL_GLOBAL_DEFAULT);
	return result;
}
#endif

} // namespace

class OnlineControlClient::Impl
{
public:
	~Impl()
	{
		stop();
	}

	bool start(
		const std::string &host,
		std::uint16_t port,
		bool useTLS,
		LineHandler lineHandler,
		StateHandler stateHandler,
		std::string *error)
	{
		if (host.empty() || port == 0)
		{
			if (error)
				*error = "Online control endpoint is incomplete";
			return false;
		}
		if (m_running.load())
		{
			if (error)
				*error = "Online control client is already running";
			return false;
		}
#ifndef SAGE_ONLINE_TLS
		if (useTLS)
		{
			if (error)
				*error = "TLS Online connections are not available in this build";
			return false;
		}
#else
		if (useTLS)
		{
			const CURLcode initialization = InitializeCurl();
			if (initialization != CURLE_OK)
			{
				if (error)
					*error = std::string("Could not initialize TLS transport: ") + curl_easy_strerror(initialization);
				return false;
			}
		}
#endif
		if (workerJoinable())
		{
			if (isWorkerThread())
			{
				if (error)
					*error = "Online control client cannot restart from its socket callback";
				return false;
			}
			joinWorker();
		}

		m_host = host;
		m_port = port;
		m_useTLS = useTLS;
		m_lineHandler = std::move(lineHandler);
		m_stateHandler = std::move(stateHandler);
		{
			ControlLock lock(m_outgoingMutex);
			m_outgoing.clear();
		}
		m_heartbeatEnabled.store(false);
		m_stopping.store(false);
		m_running.store(true);
		m_connecting.store(true);
		m_connected.store(false);
		if (!launchWorker(error))
		{
			m_running.store(false);
			m_connecting.store(false);
			return false;
		}
		return true;
	}

	void stop()
	{
		m_stopping.store(true);
		if (workerJoinable() && !isWorkerThread())
			joinWorker();
		m_running.store(false);
		m_connecting.store(false);
		m_connected.store(false);
		ControlLock lock(m_outgoingMutex);
		m_outgoing.clear();
	}

	bool sendLine(const std::string &line, std::string *error)
	{
		if (line.empty() || line.size() > kMaximumControlLine || line.find('\n') != std::string::npos ||
			line.find('\r') != std::string::npos)
		{
			if (error)
				*error = "Online control line is empty, too large, or contains a raw newline";
			return false;
		}

		ControlLock lock(m_outgoingMutex);
		if (!m_running.load())
		{
			if (error)
				*error = "Online control client is not running";
			return false;
		}
		if (m_outgoing.size() >= kMaximumQueuedLines)
		{
			if (error)
				*error = "Online control send queue is full";
			return false;
		}
		m_outgoing.push_back(line + '\n');
		return true;
	}

	void setHeartbeatEnabled(bool enabled)
	{
		m_heartbeatEnabled.store(enabled);
	}

	bool isRunning() const { return m_running.load(); }
	bool isConnected() const { return m_connected.load(); }
	bool isConnecting() const { return m_connecting.load(); }

private:
	bool workerJoinable() const
	{
#ifdef _WIN32
		return m_worker != nullptr;
#else
		return m_worker.joinable();
#endif
	}

	bool isWorkerThread() const
	{
#ifdef _WIN32
		return m_workerThreadId != 0 && m_workerThreadId == GetCurrentThreadId();
#else
		return m_worker.joinable() && m_worker.get_id() == std::this_thread::get_id();
#endif
	}

	void joinWorker()
	{
#ifdef _WIN32
		WaitForSingleObject(m_worker, INFINITE);
		CloseHandle(m_worker);
		m_worker = nullptr;
		m_workerThreadId = 0;
#else
		m_worker.join();
#endif
	}

	bool launchWorker(std::string *error)
	{
#ifdef _WIN32
		const uintptr_t worker = _beginthreadex(nullptr, 0, &Impl::workerEntry, this, 0, &m_workerThreadId);
		m_worker = reinterpret_cast<HANDLE>(worker);
		if (m_worker == nullptr)
		{
			m_workerThreadId = 0;
			if (error)
				*error = "Could not create Online control worker (CRT error " + std::to_string(errno) + ")";
			return false;
		}
		return true;
#else
		try
		{
			m_worker = std::thread([this]() { run(); });
			return true;
		}
		catch (const std::exception &exception)
		{
			if (error)
				*error = exception.what();
			return false;
		}
#endif
	}

#ifdef _WIN32
	static unsigned __stdcall workerEntry(void *context)
	{
		static_cast<Impl *>(context)->run();
		return 0;
	}
#endif

	NativeSocket connectSocket(std::string &error)
	{
		// gethostbyname is retained for the same Winsock 1/POSIX compatibility surface used by the engine.
		hostent *resolved = gethostbyname(m_host.c_str());
		if (!resolved || resolved->h_addrtype != AF_INET || resolved->h_length != sizeof(in_addr) || !resolved->h_addr_list[0])
		{
			error = "Could not resolve Online server hostname";
			return kInvalidSocket;
		}

		NativeSocket socketHandle = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
		if (socketHandle == kInvalidSocket)
		{
			error = SocketErrorText("socket", LastSocketError());
			return kInvalidSocket;
		}
		if (!SetNonBlocking(socketHandle))
		{
			error = SocketErrorText("non-blocking setup", LastSocketError());
			CloseSocket(socketHandle);
			return kInvalidSocket;
		}

		sockaddr_in address{};
		address.sin_family = AF_INET;
		address.sin_port = htons(m_port);
		std::memcpy(&address.sin_addr, resolved->h_addr_list[0], sizeof(address.sin_addr));
		if (connect(socketHandle, reinterpret_cast<const sockaddr *>(&address), sizeof(address)) == 0)
			return socketHandle;

		const int connectError = LastSocketError();
		if (!IsConnectPending(connectError))
		{
			error = SocketErrorText("connect", connectError);
			CloseSocket(socketHandle);
			return kInvalidSocket;
		}

		const auto deadline = std::chrono::steady_clock::now() + kConnectTimeout;
		while (!m_stopping.load() && std::chrono::steady_clock::now() < deadline)
		{
			fd_set writable;
			fd_set exceptional;
			FD_ZERO(&writable);
			FD_ZERO(&exceptional);
			FD_SET(socketHandle, &writable);
			FD_SET(socketHandle, &exceptional);
			timeval wait{0, static_cast<long>(kPollInterval.count() * 1000)};
			const int selected = select(static_cast<int>(socketHandle + 1), nullptr, &writable, &exceptional, &wait);
			if (selected < 0)
			{
				error = SocketErrorText("select", LastSocketError());
				CloseSocket(socketHandle);
				return kInvalidSocket;
			}
			if (selected > 0)
			{
				int socketError = 0;
#ifdef _WIN32
				int errorLength = sizeof(socketError);
#else
				socklen_t errorLength = sizeof(socketError);
#endif
				if (getsockopt(socketHandle, SOL_SOCKET, SO_ERROR, reinterpret_cast<char *>(&socketError), &errorLength) != 0 ||
					socketError != 0)
				{
					error = SocketErrorText("connect", socketError ? socketError : LastSocketError());
					CloseSocket(socketHandle);
					return kInvalidSocket;
				}
				return socketHandle;
			}
		}

		error = m_stopping.load() ? "Online control connection cancelled" : "Online control connection timed out";
		CloseSocket(socketHandle);
		return kInvalidSocket;
	}

	bool writePending(NativeSocket socketHandle, std::string &current, std::size_t &offset, std::string &error)
	{
		if (current.empty())
		{
			ControlLock lock(m_outgoingMutex);
			if (m_outgoing.empty())
				return true;
			current = std::move(m_outgoing.front());
			m_outgoing.pop_front();
			offset = 0;
		}

		const std::size_t remaining = current.size() - offset;
#ifdef MSG_NOSIGNAL
		const int sent = send(socketHandle, current.data() + offset, static_cast<int>(remaining), MSG_NOSIGNAL);
#else
		const int sent = send(socketHandle, current.data() + offset, static_cast<int>(remaining), 0);
#endif
		if (sent > 0)
		{
			offset += static_cast<std::size_t>(sent);
			if (offset == current.size())
				current.clear();
			return true;
		}
		if (sent < 0 && IsWouldBlock(LastSocketError()))
			return true;
		error = sent == 0 ? "Online control connection closed while writing" : SocketErrorText("send", LastSocketError());
		return false;
	}

	bool processIncoming(const char *buffer, std::size_t received, std::string &incoming, std::string &error)
	{
		incoming.append(buffer, received);
		if (incoming.size() > kMaximumControlLine && incoming.find('\n') == std::string::npos)
		{
			error = "Online control server sent an oversized NDJSON line";
			return false;
		}

		for (;;)
		{
			const std::size_t newline = incoming.find('\n');
			if (newline == std::string::npos)
				break;
			if (newline > kMaximumControlLine)
			{
				error = "Online control server sent an oversized NDJSON line";
				return false;
			}
			std::string line = incoming.substr(0, newline);
			incoming.erase(0, newline + 1);
			if (!line.empty() && line.back() == '\r')
				line.pop_back();
			if (!line.empty() && m_lineHandler)
				m_lineHandler(line);
		}
		return true;
	}

	bool readAvailable(NativeSocket socketHandle, std::string &incoming, std::string &error)
	{
		char buffer[4096];
		const int received = recv(socketHandle, buffer, sizeof(buffer), 0);
		if (received == 0)
		{
			error = "Online control server closed the connection";
			return false;
		}
		if (received < 0)
		{
			if (IsWouldBlock(LastSocketError()))
				return true;
			error = SocketErrorText("recv", LastSocketError());
			return false;
		}
		return processIncoming(buffer, static_cast<std::size_t>(received), incoming, error);
	}

#ifdef SAGE_ONLINE_TLS
	static int curlProgress(
		void *context,
		curl_off_t,
		curl_off_t,
		curl_off_t,
		curl_off_t)
	{
		return static_cast<Impl *>(context)->m_stopping.load() ? 1 : 0;
	}

	bool writePendingTLS(CURL *curl, std::string &current, std::size_t &offset, std::string &error)
	{
		if (current.empty())
		{
			ControlLock lock(m_outgoingMutex);
			if (m_outgoing.empty())
				return true;
			current = std::move(m_outgoing.front());
			m_outgoing.pop_front();
			offset = 0;
		}

		std::size_t sent = 0;
		const CURLcode result = curl_easy_send(curl, current.data() + offset, current.size() - offset, &sent);
		if (result == CURLE_AGAIN)
			return true;
		if (result != CURLE_OK)
		{
			error = std::string("TLS send failed: ") + curl_easy_strerror(result);
			return false;
		}
		offset += sent;
		if (offset == current.size())
			current.clear();
		return true;
	}

	bool readAvailableTLS(CURL *curl, std::string &incoming, std::string &error)
	{
		for (;;)
		{
			char buffer[4096];
			std::size_t received = 0;
			const CURLcode result = curl_easy_recv(curl, buffer, sizeof(buffer), &received);
			if (result == CURLE_AGAIN)
				return true;
			if (result != CURLE_OK)
			{
				error = std::string("TLS receive failed: ") + curl_easy_strerror(result);
				return false;
			}
			if (received == 0)
			{
				error = "TLS Online control server closed the connection";
				return false;
			}
			if (!processIncoming(buffer, received, incoming, error))
				return false;
		}
	}

	void runTLS()
	{
		std::string detail;
		CURL *curl = curl_easy_init();
		if (curl == nullptr)
		{
			m_connecting.store(false);
			m_running.store(false);
			if (!m_stopping.load())
				notify(false, "Could not create TLS Online connection");
			return;
		}

		char curlError[CURL_ERROR_SIZE] = {};
		const std::string url = "https://" + m_host + ':' + std::to_string(m_port) + '/';
		auto requireOption = [&detail](CURLcode result, const char *name) {
			if (result == CURLE_OK)
				return true;
			detail = std::string("Could not configure TLS ") + name + ": " + curl_easy_strerror(result);
			return false;
		};
		const long minimumTLSVersion = static_cast<long>(CURL_SSLVERSION_TLSv1_2) |
			static_cast<long>(CURL_SSLVERSION_MAX_DEFAULT);
		const bool configured =
			requireOption(curl_easy_setopt(curl, CURLOPT_ERRORBUFFER, curlError), "error reporting") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_URL, url.c_str()), "server URL") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_CONNECT_ONLY, 1L), "connect-only transport") &&
			// GeneralsX @bugfix Codex 02/08/2026 Avoid curl 8.13 Schannel's TLS 1.3 ticket failure on non-reusable connections.
			requireOption(curl_easy_setopt(curl, CURLOPT_SSL_SESSIONID_CACHE, 0L), "connect-only session cache") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_HTTP_VERSION, CURL_HTTP_VERSION_1_1), "HTTP/1.1 ALPN") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_CONNECTTIMEOUT_MS,
				static_cast<long>(std::chrono::duration_cast<std::chrono::milliseconds>(kConnectTimeout).count())),
				"connection timeout") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L), "signal handling") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_PROXY, ""), "direct connection") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_SSL_VERIFYPEER, 1L), "certificate verification") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_SSL_VERIFYHOST, 2L), "hostname verification") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_SSLVERSION, minimumTLSVersion), "minimum TLS version") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_NOPROGRESS, 0L), "cancellation") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_XFERINFOFUNCTION, &Impl::curlProgress), "cancellation callback") &&
			requireOption(curl_easy_setopt(curl, CURLOPT_XFERINFODATA, this), "cancellation context");
		if (!configured)
		{
			curl_easy_cleanup(curl);
			m_connecting.store(false);
			m_running.store(false);
			if (!m_stopping.load())
				notify(false, detail);
			return;
		}

		const CURLcode connected = curl_easy_perform(curl);
		m_connecting.store(false);
		if (connected != CURLE_OK)
		{
			detail = curlError[0] != '\0' ? curlError : curl_easy_strerror(connected);
			curl_easy_cleanup(curl);
			m_running.store(false);
			if (!m_stopping.load())
				notify(false, "TLS Online connection failed: " + detail);
			return;
		}

		curl_socket_t socketHandle = CURL_SOCKET_BAD;
		const CURLcode socketResult = curl_easy_getinfo(curl, CURLINFO_ACTIVESOCKET, &socketHandle);
		if (socketResult != CURLE_OK || socketHandle == CURL_SOCKET_BAD)
		{
			detail = socketResult == CURLE_OK ? "TLS connection has no active socket" : curl_easy_strerror(socketResult);
			curl_easy_cleanup(curl);
			m_running.store(false);
			if (!m_stopping.load())
				notify(false, detail);
			return;
		}

		m_connected.store(true);
		notify(true, std::string());
		std::string incoming;
		std::string currentOutgoing;
		std::size_t outgoingOffset = 0;
		auto nextHeartbeat = std::chrono::steady_clock::now() + kHeartbeatInterval;
		while (!m_stopping.load())
		{
			const auto now = std::chrono::steady_clock::now();
			if (m_heartbeatEnabled.load() && now >= nextHeartbeat)
			{
				ControlLock lock(m_outgoingMutex);
				if (m_outgoing.size() < kMaximumQueuedLines)
					m_outgoing.emplace_back(kHeartbeatLine);
				nextHeartbeat = now + kHeartbeatInterval;
			}
			else if (!m_heartbeatEnabled.load())
			{
				nextHeartbeat = now + kHeartbeatInterval;
			}

			// libcurl may already hold decrypted bytes even when the native socket is not readable.
			if (!readAvailableTLS(curl, incoming, detail))
				break;

			fd_set readable;
			fd_set writable;
			FD_ZERO(&readable);
			FD_ZERO(&writable);
			FD_SET(socketHandle, &readable);
			{
				ControlLock lock(m_outgoingMutex);
				if (!currentOutgoing.empty() || !m_outgoing.empty())
					FD_SET(socketHandle, &writable);
			}
			timeval wait{0, static_cast<long>(kPollInterval.count() * 1000)};
#ifdef _WIN32
			const int selected = select(0, &readable, &writable, nullptr, &wait);
#else
			const int selected = select(static_cast<int>(socketHandle + 1), &readable, &writable, nullptr, &wait);
#endif
			if (selected < 0)
			{
				detail = SocketErrorText("TLS select", LastSocketError());
				break;
			}
			if (selected > 0 && FD_ISSET(socketHandle, &readable) && !readAvailableTLS(curl, incoming, detail))
				break;
			if (selected > 0 && FD_ISSET(socketHandle, &writable) &&
				!writePendingTLS(curl, currentOutgoing, outgoingOffset, detail))
				break;
		}

		curl_easy_cleanup(curl);
		m_connected.store(false);
		m_running.store(false);
		if (!m_stopping.load())
			notify(false, detail.empty() ? "TLS Online control connection ended" : detail);
	}
#endif

	void notify(bool connected, const std::string &detail)
	{
		if (m_stateHandler)
			m_stateHandler(connected, detail);
	}

	void run()
	{
#ifdef SAGE_ONLINE_TLS
		if (m_useTLS)
		{
			runTLS();
			return;
		}
#endif
		runPlain();
	}

	void runPlain()
	{
		std::string detail;
#ifdef _WIN32
		WSADATA winsockData{};
		const int startup = WSAStartup(MAKEWORD(2, 2), &winsockData);
		if (startup != 0)
		{
			detail = SocketErrorText("WSAStartup", startup);
			m_connecting.store(false);
			m_running.store(false);
			notify(false, detail);
			return;
		}
#endif

		NativeSocket socketHandle = connectSocket(detail);
		m_connecting.store(false);
		if (socketHandle == kInvalidSocket)
		{
			m_running.store(false);
			if (!m_stopping.load())
				notify(false, detail);
#ifdef _WIN32
			WSACleanup();
#endif
			return;
		}

		m_connected.store(true);
		notify(true, std::string());
		std::string incoming;
		std::string currentOutgoing;
		std::size_t outgoingOffset = 0;
		auto nextHeartbeat = std::chrono::steady_clock::now() + kHeartbeatInterval;
		while (!m_stopping.load())
		{
			const auto now = std::chrono::steady_clock::now();
			if (m_heartbeatEnabled.load() && now >= nextHeartbeat)
			{
				ControlLock lock(m_outgoingMutex);
				if (m_outgoing.size() < kMaximumQueuedLines)
					m_outgoing.emplace_back(kHeartbeatLine);
				nextHeartbeat = now + kHeartbeatInterval;
			}
			else if (!m_heartbeatEnabled.load())
			{
				nextHeartbeat = now + kHeartbeatInterval;
			}
			fd_set readable;
			fd_set writable;
			FD_ZERO(&readable);
			FD_ZERO(&writable);
			FD_SET(socketHandle, &readable);
			{
				ControlLock lock(m_outgoingMutex);
				if (!currentOutgoing.empty() || !m_outgoing.empty())
					FD_SET(socketHandle, &writable);
			}
			timeval wait{0, static_cast<long>(kPollInterval.count() * 1000)};
			const int selected = select(static_cast<int>(socketHandle + 1), &readable, &writable, nullptr, &wait);
			if (selected < 0)
			{
				detail = SocketErrorText("select", LastSocketError());
				break;
			}
			if (selected > 0 && FD_ISSET(socketHandle, &readable) && !readAvailable(socketHandle, incoming, detail))
				break;
			if (selected > 0 && FD_ISSET(socketHandle, &writable) && !writePending(socketHandle, currentOutgoing, outgoingOffset, detail))
				break;
		}

		CloseSocket(socketHandle);
		m_connected.store(false);
		m_running.store(false);
		if (!m_stopping.load())
			notify(false, detail.empty() ? "Online control connection ended" : detail);
#ifdef _WIN32
		WSACleanup();
#endif
	}

	std::string m_host;
	std::uint16_t m_port = 0;
	bool m_useTLS = false;
	LineHandler m_lineHandler;
	StateHandler m_stateHandler;
	std::atomic<bool> m_running{false};
	std::atomic<bool> m_connected{false};
	std::atomic<bool> m_connecting{false};
	std::atomic<bool> m_stopping{false};
	std::atomic<bool> m_heartbeatEnabled{false};
#ifdef _WIN32
	HANDLE m_worker = nullptr;
	unsigned m_workerThreadId = 0;
#else
	std::thread m_worker;
#endif
	ControlMutex m_outgoingMutex;
	std::deque<std::string> m_outgoing;
};

OnlineControlClient::OnlineControlClient() : m_impl(std::make_unique<Impl>()) {}
OnlineControlClient::~OnlineControlClient() = default;

bool OnlineControlClient::start(
	const std::string &host,
	std::uint16_t port,
	bool useTLS,
	LineHandler lineHandler,
	StateHandler stateHandler,
	std::string *error)
{
	return m_impl->start(host, port, useTLS, std::move(lineHandler), std::move(stateHandler), error);
}

void OnlineControlClient::stop()
{
	m_impl->stop();
}

bool OnlineControlClient::sendLine(const std::string &line, std::string *error)
{
	return m_impl->sendLine(line, error);
}

void OnlineControlClient::setHeartbeatEnabled(bool enabled)
{
	m_impl->setHeartbeatEnabled(enabled);
}

bool OnlineControlClient::isRunning() const
{
	return m_impl->isRunning();
}

bool OnlineControlClient::isConnected() const
{
	return m_impl->isConnected();
}

bool OnlineControlClient::isConnecting() const
{
	return m_impl->isConnecting();
}

} // namespace GeneralsOnline
