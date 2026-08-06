package main

import "strings"

// GeneralsX @feature Codex 05/08/2026 Use extended Windows paths when native APIs publish to redirected or deeply nested Desktops.
func extendedWindowsPath(path string) string {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(normalized, `\\?\`) || strings.HasPrefix(normalized, `\\.\`) {
		return normalized
	}
	if strings.HasPrefix(normalized, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(normalized, `\\`)
	}
	if len(normalized) >= 3 && isASCIIAlpha(normalized[0]) && normalized[1] == ':' && normalized[2] == '\\' {
		return `\\?\` + normalized
	}
	return path
}

func isASCIIAlpha(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}
