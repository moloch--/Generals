//go:build !windows

package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/signalctx"
)

func terminateProcess(process *os.Process, ctx context.Context) error {
	if process == nil {
		return os.ErrProcessDone
	}
	terminationSignal := os.Signal(syscall.SIGTERM)
	if received, ok := signalctx.ReceivedSignal(ctx); ok {
		terminationSignal = received
	}
	nativeSignal, ok := terminationSignal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported process termination signal %T", terminationSignal)
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	return signalProcessGroup(process.Pid, nativeSignal)
}

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}
