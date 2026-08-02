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

namespace GeneralsOnline
{

// GeneralsX @bugfix Codex 01/08/2026 Transform the current byte before advancing pointers
// so cached passwords are safe on every compiler.
inline void ObfuscateLoginPassword(char *value)
{
	if (!value)
		return;

	static const char xorWord[] = "1337Munkee";
	char *current = value;
	const char *key = xorWord;
	while (*current)
	{
		if (!*key)
			key = xorWord;
		if (*current != *key)
			*current ^= *key;
		++current;
		++key;
	}
}

} // namespace GeneralsOnline
