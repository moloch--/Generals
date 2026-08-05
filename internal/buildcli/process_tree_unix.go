//go:build darwin || linux

package buildcli

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const processTerminationGracePeriod = 750 * time.Millisecond

type unixProcessGroup struct {
	id int
}

func startManagedProcess(command *exec.Cmd) (managedProcess, error) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.SysProcAttr.Pgid = 0
	if err := command.Start(); err != nil {
		return nil, err
	}
	return unixProcessGroup{id: command.Process.Pid}, nil
}

// GeneralsX @build Codex 05/08/2026 Give command trees a graceful shutdown window before forcibly removing the process group.
func (group unixProcessGroup) terminate() error {
	if err := signalProcessGroup(group.id, syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.NewTimer(processTerminationGracePeriod)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			if !processGroupExists(group.id) {
				return nil
			}
		case <-deadline.C:
			return signalProcessGroup(group.id, syscall.SIGKILL)
		}
	}
}

func (group unixProcessGroup) close() error {
	return nil
}

func signalProcessGroup(id int, signal syscall.Signal) error {
	err := syscall.Kill(-id, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(id int) bool {
	err := syscall.Kill(-id, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
