//go:build windows

package main

import (
	"os"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTerminalCommandLineRoundTripsWithoutShell(t *testing.T) {
	t.Parallel()
	want := windowsTerminalArguments(`C:\Program Files\GeneralsX & Tools\builder.exe`, `C:\Users\User Name\AppData\Local\Temp\job & one.json`)
	commandLine := windows.ComposeCommandLine(want)
	got, err := windows.DecomposeCommandLine(commandLine)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
}

func TestWindowsConsolePreservesUsableRedirectedStream(t *testing.T) {
	t.Parallel()
	stream, err := os.CreateTemp(t.TempDir(), "redirected-stdout")
	if err != nil {
		t.Fatal(err)
	}
	if standardStreamNeedsReopen(stream) {
		t.Fatal("usable redirected stream would be replaced")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !standardStreamNeedsReopen(stream) {
		t.Fatal("closed stream would not be reopened")
	}
}

func TestWindowsTerminalStartupUsesNewConsoleHandles(t *testing.T) {
	t.Parallel()
	startupInfo := windowsTerminalStartupInfo()
	if startupInfo.Cb == 0 {
		t.Fatal("StartupInfo.Cb is zero")
	}
	if startupInfo.Flags != 0 || startupInfo.StdInput != 0 || startupInfo.StdOutput != 0 || startupInfo.StdErr != 0 {
		t.Fatalf("StartupInfo unexpectedly overrides console handles: %#v", startupInfo)
	}
}

func TestWindowsInteractiveCommandStartsSuspendedInKillOnCloseJob(t *testing.T) {
	t.Parallel()
	flags := windowsTerminalProcessCreationFlags()
	if flags&windows.CREATE_SUSPENDED == 0 || flags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("interactive process creation flags = %#x", flags)
	}
	limits := windowsTerminalJobLimits()
	if limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatalf("interactive Job Object limit flags = %#x", limits.BasicLimitInformation.LimitFlags)
	}
	access := windowsTerminalJobAssignmentProcessAccess()
	if access&windows.PROCESS_SET_QUOTA == 0 || access&windows.PROCESS_TERMINATE == 0 {
		t.Fatalf("interactive job assignment process access = %#x", access)
	}
}
