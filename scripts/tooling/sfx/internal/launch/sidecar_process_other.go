//go:build !windows

package launch

import (
	"context"
	"os"
	"os/exec"
)

func configureSidecarProcess(_ *exec.Cmd) {}

func terminateSidecarProcess(process *os.Process, ctx context.Context) error {
	return terminateProcess(process, ctx)
}
