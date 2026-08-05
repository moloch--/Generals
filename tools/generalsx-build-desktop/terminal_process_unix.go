//go:build darwin || linux

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const terminalProcessGracePeriod = 750 * time.Millisecond

type terminalProcess struct {
	command *exec.Cmd
}

func startTerminalProcess(command *exec.Cmd) (*terminalProcess, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &terminalProcess{command: command}, nil
}

func (process *terminalProcess) wait() error {
	return process.command.Wait()
}

// terminate signals the whole process group, then force-kills descendants that
// ignore SIGTERM. The helper itself is outside this newly created group.
func (process *terminalProcess) terminate() error {
	if process.command.Process == nil {
		return nil
	}
	groupID := -process.command.Process.Pid
	if err := syscall.Kill(groupID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(terminalProcessGracePeriod)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(groupID, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(groupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (*terminalProcess) close() error {
	return nil
}
