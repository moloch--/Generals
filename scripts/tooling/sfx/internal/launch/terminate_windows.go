//go:build windows

package launch

import (
	"context"
	"errors"
	"os"
)

func terminateProcess(process *os.Process, _ context.Context) error {
	if process == nil {
		return os.ErrProcessDone
	}
	err := process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}
