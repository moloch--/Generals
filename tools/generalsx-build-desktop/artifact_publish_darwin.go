//go:build darwin

package main

import "golang.org/x/sys/unix"

// GeneralsX @feature Codex 05/08/2026 Publish a complete macOS Desktop copy without replacing an existing file.
func publishTemporaryArtifact(temporaryPath, destinationPath string) error {
	return unix.RenameatxNp(
		unix.AT_FDCWD,
		temporaryPath,
		unix.AT_FDCWD,
		destinationPath,
		unix.RENAME_EXCL,
	)
}
