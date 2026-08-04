//go:build windows

package launch

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// GeneralsX @feature Codex 04/08/2026 Isolate the Online server in a console process group so Ctrl-Break reaches only that sidecar.
func configureSidecarProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// GeneralsX @feature Codex 04/08/2026 Let the Go server close listeners and SQLite before os/exec applies its five-second kill fallback.
func terminateSidecarProcess(process *os.Process, _ context.Context) error {
	if process == nil {
		return os.ErrProcessDone
	}
	return signalSidecarProcessGroup(process.Pid, windows.GenerateConsoleCtrlEvent)
}

type consoleControlEventFunc func(ctrlEvent uint32, processGroupID uint32) error

func signalSidecarProcessGroup(processGroupID int, generate consoleControlEventFunc) error {
	return generate(windows.CTRL_BREAK_EVENT, uint32(processGroupID))
}
