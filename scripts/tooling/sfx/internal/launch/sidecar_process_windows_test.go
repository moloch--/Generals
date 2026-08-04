//go:build windows

package launch

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureSidecarProcessCreatesDedicatedConsoleGroup(t *testing.T) {
	command := exec.Command("cmd.exe", "/c", "exit", "0")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT}
	configureSidecarProcess(command)
	if command.SysProcAttr == nil {
		t.Fatal("sidecar command has no Windows process attributes")
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("creation flags %#x do not include CREATE_NEW_PROCESS_GROUP", command.SysProcAttr.CreationFlags)
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_UNICODE_ENVIRONMENT == 0 {
		t.Fatalf("creation flags %#x discarded existing flags", command.SysProcAttr.CreationFlags)
	}
}

func TestSignalSidecarProcessGroupSendsCtrlBreakToChildGroup(t *testing.T) {
	wantErr := errors.New("sentinel")
	var gotEvent uint32
	var gotGroup uint32
	err := signalSidecarProcessGroup(4242, func(event, group uint32) error {
		gotEvent = event
		gotGroup = group
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("signalSidecarProcessGroup error = %v, want sentinel", err)
	}
	if gotEvent != windows.CTRL_BREAK_EVENT {
		t.Fatalf("console event = %d, want CTRL_BREAK_EVENT", gotEvent)
	}
	if gotGroup != 4242 {
		t.Fatalf("process group = %d, want 4242", gotGroup)
	}
}
