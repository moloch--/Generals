/*
** Command & Conquer Generals Zero Hour(tm)
** Copyright 2025 Electronic Arts Inc.
**
** This program is free software: you can redistribute it and/or modify
** it under the terms of the GNU General Public License as published by
** the Free Software Foundation, either version 3 of the License, or
** (at your option) any later version.
*/

#pragma once

#include <string>

namespace GeneralsPlatform
{

// GeneralsX @bugfix Codex 10/10/2026 Decode service UTF-8 independently of the process locale and wchar_t width.
// Malformed input returns an empty string; line breaks become spaces for single-line controls.
inline std::wstring DecodeUtf8SingleLine(const char *text)
{
    std::wstring result;
    if (!text)
        return result;

    const unsigned char *cursor = reinterpret_cast<const unsigned char *>(text);
    while (*cursor)
    {
        const unsigned char lead = *cursor++;
        unsigned long codePoint = lead;
        unsigned long minimum = 0;
        unsigned int remaining = 0;
        if (lead >= 0xC2 && lead <= 0xDF)
        {
            codePoint = lead & 0x1F;
            minimum = 0x80;
            remaining = 1;
        }
        else if (lead >= 0xE0 && lead <= 0xEF)
        {
            codePoint = lead & 0x0F;
            minimum = 0x800;
            remaining = 2;
        }
        else if (lead >= 0xF0 && lead <= 0xF4)
        {
            codePoint = lead & 0x07;
            minimum = 0x10000;
            remaining = 3;
        }
        else if (lead >= 0x80)
        {
            return std::wstring();
        }

        for (unsigned int i = 0; i < remaining; ++i)
        {
            if ((*cursor & 0xC0) != 0x80)
                return std::wstring();
            codePoint = (codePoint << 6) | (*cursor++ & 0x3F);
        }
        if (codePoint < minimum || codePoint > 0x10FFFF ||
            (codePoint >= 0xD800 && codePoint <= 0xDFFF))
            return std::wstring();

        if (codePoint == '\r' || codePoint == '\n')
            codePoint = ' ';
        if (sizeof(wchar_t) == 2 && codePoint > 0xFFFF)
        {
            codePoint -= 0x10000;
            result += static_cast<wchar_t>(0xD800 + (codePoint >> 10));
            result += static_cast<wchar_t>(0xDC00 + (codePoint & 0x3FF));
        }
        else
        {
            result += static_cast<wchar_t>(codePoint);
        }
    }
    return result;
}

} // namespace GeneralsPlatform
