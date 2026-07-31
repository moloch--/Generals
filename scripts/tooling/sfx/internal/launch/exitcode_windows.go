//go:build windows

package launch

import "os/exec"

func platformSignalExitCode(_ *exec.ExitError) (int, bool) {
	return 0, false
}
