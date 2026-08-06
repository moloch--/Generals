//go:build !darwin && !linux && !windows

package main

import (
	"errors"
)

// GeneralsX @feature Codex 05/08/2026 Fail closed when a platform has no supported Desktop resolver.
func systemDesktopDirectory() (string, error) {
	return "", errors.New("copying to Desktop is unsupported on this platform")
}
