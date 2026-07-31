//go:build windows

package cache

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32DLL.NewProc("LockFileEx")
	unlockFileExProc = kernel32DLL.NewProc("UnlockFileEx")
)

type platformLockState struct {
	overlapped syscall.Overlapped
}

func tryPlatformLock(
	file *os.File,
	mode guardMode,
	state *platformLockState,
) (bool, error) {
	flags := uintptr(lockfileFailImmediately)
	if mode == guardExclusive {
		flags |= lockfileExclusiveLock
	}
	result, _, callErr := lockFileExProc.Call(
		file.Fd(),
		flags,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&state.overlapped)),
	)
	runtime.KeepAlive(file)
	runtime.KeepAlive(state)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, errorLockViolation) {
		return false, nil
	}
	if callErr == syscall.Errno(0) {
		return false, errors.New("LockFileEx failed without a Windows error")
	}
	return false, fmt.Errorf("LockFileEx: %w", callErr)
}

func unlockPlatformLock(file *os.File, state *platformLockState) error {
	result, _, callErr := unlockFileExProc.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&state.overlapped)),
	)
	runtime.KeepAlive(file)
	runtime.KeepAlive(state)
	if result != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return errors.New("UnlockFileEx failed without a Windows error")
	}
	return fmt.Errorf("UnlockFileEx: %w", callErr)
}
