package main

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// GeneralsX @feature Codex 05/08/2026 Validate Desktop destinations and parse Linux XDG paths without shell evaluation.
func validateDesktopDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("Desktop directory must be an absolute path")
	}
	path = filepath.Clean(path)
	if path == "." || !filepath.IsAbs(path) {
		return "", errors.New("Desktop directory must be an absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Desktop directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Desktop path %q is not a directory", path)
	}
	return path, nil
}

func parseXDGDesktopDirectory(home string, contents []byte) (string, bool, error) {
	for lineNumber, rawLine := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rawValue, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "XDG_DESKTOP_DIR" {
			continue
		}
		desktop, err := parseXDGUserDirectory(strings.TrimSpace(rawValue), home)
		if err != nil {
			return "", true, fmt.Errorf("parse XDG_DESKTOP_DIR on line %d: %w", lineNumber+1, err)
		}
		return desktop, true, nil
	}
	return "", false, nil
}

func parseXDGUserDirectory(raw, home string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", errors.New("value must be double quoted")
	}
	contents := raw[1 : len(raw)-1]
	var value strings.Builder
	for index := 0; index < len(contents); index++ {
		character := contents[index]
		if character == '\\' {
			index++
			if index >= len(contents) {
				return "", errors.New("value ends with an incomplete escape")
			}
			escaped := contents[index]
			if escaped != '\\' && escaped != '"' && escaped != '$' && escaped != '`' {
				return "", fmt.Errorf("unsupported escape \\%c", escaped)
			}
			value.WriteByte(escaped)
			continue
		}
		if character == '`' {
			return "", errors.New("value contains an unescaped command substitution")
		}
		if character == '$' {
			remainder := contents[index:]
			switch {
			case index == 0 && (remainder == "$HOME" || strings.HasPrefix(remainder, "$HOME/")):
				value.WriteString(home)
				index += len("$HOME") - 1
				continue
			case index == 0 && (remainder == "${HOME}" || strings.HasPrefix(remainder, "${HOME}/")):
				value.WriteString(home)
				index += len("${HOME}") - 1
				continue
			default:
				return "", errors.New("value contains an unsupported variable or command substitution")
			}
		}
		value.WriteByte(character)
	}
	desktop := pathpkg.Clean(value.String())
	if !pathpkg.IsAbs(desktop) {
		return "", errors.New("value must be absolute or begin with $HOME")
	}
	if desktop == pathpkg.Clean(home) {
		return "", errors.New("the Desktop directory is disabled in user-dirs.dirs")
	}
	return desktop, nil
}
