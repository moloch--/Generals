//go:build !darwin && !linux && !windows

package main

import "os/exec"

type terminalProcess struct {
	command *exec.Cmd
}

func startTerminalProcess(command *exec.Cmd) (*terminalProcess, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &terminalProcess{command: command}, nil
}

func (process *terminalProcess) wait() error {
	return process.command.Wait()
}

func (process *terminalProcess) terminate() error {
	return process.command.Process.Kill()
}

func (*terminalProcess) close() error {
	return nil
}
