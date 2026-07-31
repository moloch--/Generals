//go:build !darwin && !linux && !windows

package cache

import (
	"errors"
	"os"
)

type platformLockState struct{}

func tryPlatformLock(
	_ *os.File,
	_ guardMode,
	_ *platformLockState,
) (bool, error) {
	return false, errors.New("cache file locking is unsupported on this platform")
}

func unlockPlatformLock(_ *os.File, _ *platformLockState) error {
	return nil
}
