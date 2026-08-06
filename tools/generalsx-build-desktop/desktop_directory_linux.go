//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// GeneralsX @feature Codex 05/08/2026 Honor the current Linux user's configured XDG Desktop directory.
func systemDesktopDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	} else if !filepath.IsAbs(configHome) {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
	}
	contents, err := os.ReadFile(filepath.Join(configHome, "user-dirs.dirs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read user-dirs.dirs: %w", err)
	}
	if err == nil {
		if desktop, found, parseErr := parseXDGDesktopDirectory(home, contents); parseErr != nil {
			return "", parseErr
		} else if found {
			return validateDesktopDirectory(desktop)
		}
	}
	return validateDesktopDirectory(filepath.Join(home, "Desktop"))
}
