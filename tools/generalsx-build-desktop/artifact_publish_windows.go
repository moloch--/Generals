//go:build windows

package main

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

// GeneralsX @feature Codex 05/08/2026 Publish a complete Windows Desktop copy without replacing an existing file.
func publishTemporaryArtifact(temporaryPath, destinationPath string) error {
	temporary, err := windows.UTF16PtrFromString(extendedWindowsPath(temporaryPath))
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(extendedWindowsPath(destinationPath))
	if err != nil {
		return err
	}
	if err := windows.MoveFile(temporary, destination); err != nil {
		if err == windows.ERROR_ALREADY_EXISTS || err == windows.ERROR_FILE_EXISTS {
			return fs.ErrExist
		}
		return err
	}
	return nil
}
