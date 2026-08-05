//go:build darwin || linux

package buildcli

import (
	"errors"
	"os/signal"
	"syscall"
)

func ignoreGracefulTermination() {
	signal.Ignore(syscall.SIGTERM)
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
