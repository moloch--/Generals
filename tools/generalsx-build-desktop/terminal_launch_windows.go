//go:build windows

package main

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func launchTerminalJob(ctx context.Context, desktopExecutable, specPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	applicationName, err := windows.UTF16PtrFromString(desktopExecutable)
	if err != nil {
		return fmt.Errorf("encode Windows executable path: %w", err)
	}
	commandLine := windows.ComposeCommandLine(windowsTerminalArguments(desktopExecutable, specPath))
	commandLinePointer, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return fmt.Errorf("encode Windows terminal command: %w", err)
	}
	startupInfo := windowsTerminalStartupInfo()
	var processInfo windows.ProcessInformation
	if err := windows.CreateProcess(
		applicationName,
		commandLinePointer,
		nil,
		nil,
		false,
		windowsTerminalLaunchCreationFlags(),
		nil,
		nil,
		&startupInfo,
		&processInfo,
	); err != nil {
		return fmt.Errorf("create Windows console: %w", err)
	}
	_ = windows.CloseHandle(processInfo.Thread)
	_ = windows.CloseHandle(processInfo.Process)
	return nil
}

// GeneralsX @bugfix Codex 05/08/2026 Keep the explicit SteamCMD handoff visible while background children stay hidden.
func windowsTerminalLaunchCreationFlags() uint32 {
	return windows.CREATE_NEW_CONSOLE
}

func windowsTerminalArguments(desktopExecutable, specPath string) []string {
	return []string{desktopExecutable, "--terminal-job", specPath}
}

func windowsTerminalStartupInfo() windows.StartupInfo {
	// Flags intentionally remains zero. In particular, omitting
	// STARTF_USESTDHANDLES lets CREATE_NEW_CONSOLE provide CONIN$/CONOUT$.
	return windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
}
