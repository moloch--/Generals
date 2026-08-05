//go:build windows

package buildcli

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureSuspendedProcessPreservesCreationFlags(t *testing.T) {
	command := exec.Command("cmd.exe")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	configureSuspendedProcess(command)
	if command.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatalf("creation flags %#x do not suspend the process before job assignment", command.SysProcAttr.CreationFlags)
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("creation flags %#x discarded existing flags", command.SysProcAttr.CreationFlags)
	}
}

func TestJobAssignmentProcessAccessUsesRequiredRights(t *testing.T) {
	t.Parallel()
	access := windowsJobAssignmentProcessAccess()
	if access&windows.PROCESS_SET_QUOTA == 0 || access&windows.PROCESS_TERMINATE == 0 {
		t.Fatalf("job assignment process access = %#x", access)
	}
}
