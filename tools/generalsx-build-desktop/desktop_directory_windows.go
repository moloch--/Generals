//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// GeneralsX @feature Codex 05/08/2026 Honor redirected and synchronized Windows Desktop known folders.
func systemDesktopDirectory() (string, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("resolve Windows known Desktop folder: %w", err)
	}
	return validateDesktopDirectory(desktop)
}
