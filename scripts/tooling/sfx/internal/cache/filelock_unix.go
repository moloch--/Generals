//go:build darwin || linux

package cache

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

type platformLockState struct{}

func tryPlatformLock(
	file *os.File,
	mode guardMode,
	_ *platformLockState,
) (bool, error) {
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if mode == guardExclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	err := syscall.Flock(int(file.Fd()), operation)
	runtime.KeepAlive(file)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockPlatformLock(file *os.File, _ *platformLockState) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	runtime.KeepAlive(file)
	return err
}
