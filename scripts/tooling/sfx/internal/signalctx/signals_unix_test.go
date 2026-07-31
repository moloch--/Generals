//go:build !windows

package signalctx

import (
	"syscall"
	"testing"
)

func TestPlatformSignalExitCodeSIGTERM(t *testing.T) {
	code, ok := platformSignalExitCode(syscall.SIGTERM)
	if !ok || code != 143 {
		t.Fatalf("platformSignalExitCode(SIGTERM) = (%d, %v), want (143, true)", code, ok)
	}
}
