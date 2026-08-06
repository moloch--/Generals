//go:build !darwin && !linux && !windows

package main

import (
	"fmt"
	"os"
)

// GeneralsX @feature Codex 05/08/2026 Preserve no-overwrite publication semantics on fallback platforms.
func publishTemporaryArtifact(temporaryPath, destinationPath string) error {
	if err := os.Link(temporaryPath, destinationPath); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove temporary link after publishing: %w", err)
	}
	return nil
}
