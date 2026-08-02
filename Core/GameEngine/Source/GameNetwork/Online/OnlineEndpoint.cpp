/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "GameNetwork/Online/OnlineEndpoint.h"
#include "GameNetwork/Online/OnlineBuildConfig.h"

#include <cctype>
#include <cstdio>
#include <limits>
#include <string_view>

namespace GeneralsOnline
{
namespace
{

constexpr std::string_view kTLSPrefix = "tls://";
constexpr char kBuiltInOnlineServerEndpoint[] = SAGE_ONLINE_SERVER_DEFAULT;

OnlineEndpoint BuildBuiltInOnlineEndpoint()
{
	OnlineEndpoint endpoint;
	if (kBuiltInOnlineServerEndpoint[0] == '\0')
	{
		return endpoint;
	}

	std::string error;
	if (!ParseOnlineEndpoint(kBuiltInOnlineServerEndpoint, endpoint, &error))
	{
		std::fprintf(stderr, "ERROR: Built-in Online server endpoint is invalid: %s\n", error.c_str());
		return OnlineEndpoint();
	}
	return endpoint;
}

OnlineEndpoint &MutableOnlineEndpoint()
{
	static OnlineEndpoint endpoint = BuildBuiltInOnlineEndpoint();
	return endpoint;
}

void SetError(std::string *error, const char *message)
{
	if (error != nullptr)
	{
		*error = message;
	}
}

bool IsAsciiAlphaNumeric(char value)
{
	const unsigned char character = static_cast<unsigned char>(value);
	return std::isalnum(character) != 0 && character < 0x80;
}

bool IsStrictIPv4(std::string_view host)
{
	std::size_t start = 0;
	for (int octet = 0; octet < 4; ++octet)
	{
		const std::size_t end = host.find('.', start);
		const bool finalOctet = octet == 3;
		if ((end == std::string_view::npos) != finalOctet)
		{
			return false;
		}

		const std::size_t stop = finalOctet ? host.size() : end;
		if (stop == start || stop - start > 3)
		{
			return false;
		}

		unsigned int value = 0;
		for (std::size_t index = start; index < stop; ++index)
		{
			const unsigned char character = static_cast<unsigned char>(host[index]);
			if (character < '0' || character > '9')
			{
				return false;
			}
			value = value * 10 + (character - '0');
		}
		if (value > 255)
		{
			return false;
		}

		start = stop + 1;
	}

	return start == host.size() + 1;
}

bool IsDNSHostname(std::string_view host)
{
	if (host.empty() || host.size() > 253 || host.front() == '.' || host.back() == '.')
	{
		return false;
	}

	std::size_t labelStart = 0;
	while (labelStart < host.size())
	{
		const std::size_t separator = host.find('.', labelStart);
		const std::size_t labelEnd = separator == std::string_view::npos ? host.size() : separator;
		const std::size_t labelLength = labelEnd - labelStart;
		if (labelLength == 0 || labelLength > 63 ||
			!IsAsciiAlphaNumeric(host[labelStart]) || !IsAsciiAlphaNumeric(host[labelEnd - 1]))
		{
			return false;
		}

		for (std::size_t index = labelStart + 1; index + 1 < labelEnd; ++index)
		{
			if (!IsAsciiAlphaNumeric(host[index]) && host[index] != '-')
			{
				return false;
			}
		}

		if (separator == std::string_view::npos)
		{
			break;
		}
		labelStart = separator + 1;
	}

	return true;
}

bool ParseControlPort(std::string_view text, std::uint16_t &port)
{
	if (text.empty() || text.size() > 5)
	{
		return false;
	}

	unsigned int value = 0;
	for (char character : text)
	{
		if (character < '0' || character > '9')
		{
			return false;
		}
		value = value * 10 + static_cast<unsigned int>(character - '0');
	}

	if (value == 0 || value > std::numeric_limits<std::uint16_t>::max())
	{
		return false;
	}

	port = static_cast<std::uint16_t>(value);
	return true;
}

} // namespace

bool ParseOnlineEndpoint(const char *value, OnlineEndpoint &endpoint, std::string *error)
{
	if (error != nullptr)
	{
		error->clear();
	}
	if (value == nullptr || value[0] == '\0')
	{
		SetError(error, "Online server must not be empty");
		return false;
	}

	const std::string_view input(value);
	if (input.size() > 265)
	{
		SetError(error, "Online server is too long");
		return false;
	}
	for (char character : input)
	{
		const unsigned char ascii = static_cast<unsigned char>(character);
		if (ascii <= 0x20 || ascii >= 0x7f)
		{
			SetError(error, "Online server must contain printable ASCII without whitespace");
			return false;
		}
	}

	bool useTLS = false;
	std::string_view address = input;
	if (address.size() >= kTLSPrefix.size() && address.substr(0, kTLSPrefix.size()) == kTLSPrefix)
	{
		useTLS = true;
		address.remove_prefix(kTLSPrefix.size());
	}
	else if (address.find("://") != std::string_view::npos)
	{
		SetError(error, "Online server supports only the tls:// scheme");
		return false;
	}

	const std::size_t separator = address.find(':');
	if (separator != std::string_view::npos && address.find(':', separator + 1) != std::string_view::npos)
	{
		SetError(error, "IPv6 Online server addresses are not supported");
		return false;
	}

	const std::string_view host = separator == std::string_view::npos ? address : address.substr(0, separator);
	std::uint16_t controlPort = kDefaultControlPort;
	if (separator != std::string_view::npos && !ParseControlPort(address.substr(separator + 1), controlPort))
	{
		SetError(error, "Online server control port must be between 1 and 65535");
		return false;
	}

	bool numericAddress = !host.empty();
	for (char character : host)
	{
		if ((character < '0' || character > '9') && character != '.')
		{
			numericAddress = false;
			break;
		}
	}

	if ((numericAddress && !IsStrictIPv4(host)) || (!numericAddress && !IsDNSHostname(host)))
	{
		SetError(error, numericAddress ? "Online server IPv4 address is invalid" : "Online server DNS hostname is invalid");
		return false;
	}

	OnlineEndpoint parsed;
	parsed.host.assign(host.data(), host.size());
	parsed.controlPort = controlPort;
	parsed.useTLS = useTLS;
	parsed.configured = true;
	endpoint = parsed;
	return true;
}

bool ConfigureOnlineEndpoint(const char *value, std::string *error)
{
	OnlineEndpoint parsed;
	if (!ParseOnlineEndpoint(value, parsed, error))
	{
		return false;
	}

	MutableOnlineEndpoint() = parsed;
	return true;
}

const OnlineEndpoint &GetOnlineEndpoint()
{
	return MutableOnlineEndpoint();
}

const char *GetBuiltInOnlineServerEndpoint()
{
	return kBuiltInOnlineServerEndpoint;
}

void ClearOnlineEndpoint()
{
	MutableOnlineEndpoint() = BuildBuiltInOnlineEndpoint();
}

} // namespace GeneralsOnline
