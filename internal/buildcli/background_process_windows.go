//go:build windows

package buildcli

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// GeneralsX @bugfix Codex 05/08/2026 Suppress GUI-owned console children without changing headless or interactive terminal behavior.
func configureBackgroundCommand(command *exec.Cmd, hideWindow bool) {
	if !hideWindow {
		return
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
