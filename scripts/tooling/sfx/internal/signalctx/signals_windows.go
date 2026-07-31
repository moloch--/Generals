//go:build windows

package signalctx

import "os"

func platformSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func platformSignalExitCode(received os.Signal) (int, bool) {
	if received != os.Interrupt {
		return 0, false
	}
	return 130, true
}
