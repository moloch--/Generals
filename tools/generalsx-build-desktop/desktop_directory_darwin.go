//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// GeneralsX @feature Codex 05/08/2026 Resolve the current macOS user's logical Desktop directory.
func systemDesktopDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return validateDesktopDirectory(filepath.Join(home, "Desktop"))
}
