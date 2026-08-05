package buildcli

import (
	"context"
	"errors"
	"os/exec"
)

type managedProcess interface {
	terminate() error
	close() error
}

// GeneralsX @build Codex 05/08/2026 Cancel complete external-command trees instead of leaving compiler and packaging descendants running.
func runManagedCommand(ctx context.Context, command *exec.Cmd) error {
	if ctx == nil {
		return errors.New("external command context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	process, err := startManagedProcess(command)
	if err != nil {
		return err
	}

	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
	}()

	select {
	case err := <-waited:
		return errors.Join(err, process.close())
	case <-ctx.Done():
		terminateErr := process.terminate()
		<-waited
		return errors.Join(ctx.Err(), terminateErr, process.close())
	}
}
