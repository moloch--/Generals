//go:build !windows

package signalctx

import (
	"os"
	"syscall"
)

func platformSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func platformSignalExitCode(received os.Signal) (int, bool) {
	native, ok := received.(syscall.Signal)
	if !ok || native <= 0 {
		return 0, false
	}
	return 128 + int(native), true
}
