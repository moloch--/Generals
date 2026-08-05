//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var (
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	freeConsole   = kernel32.NewProc("FreeConsole")
	attachConsole = kernel32.NewProc("AttachConsole")
	allocConsole  = kernel32.NewProc("AllocConsole")
)

// prepareDesktopGUI detaches the console allocated by a -windowsconsole build
// only for the no-argument graphical launch. CLI and terminal-job modes retain it.
func prepareDesktopGUI() {
	_, _, _ = freeConsole.Call()
}

// prepareArgumentConsole makes CLI and hidden terminal-job modes work even if
// a local build accidentally uses the Windows GUI subsystem. Existing pipes
// and file redirections remain untouched.
func prepareArgumentConsole() error {
	const attachParentProcess = uintptr(0xffffffff)
	attached, _, attachErr := attachConsole.Call(attachParentProcess)
	if attached == 0 && !errors.Is(attachErr, windows.ERROR_ACCESS_DENIED) {
		allocated, _, allocateErr := allocConsole.Call()
		if allocated == 0 && !errors.Is(allocateErr, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("attach to parent console: %v; allocate console: %w", attachErr, allocateErr)
		}
	}

	var reopenErrors []error
	if standardStreamNeedsReopen(os.Stdin) {
		stream, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
		if err != nil {
			reopenErrors = append(reopenErrors, fmt.Errorf("open CONIN$: %w", err))
		} else {
			os.Stdin = stream
		}
	}
	if standardStreamNeedsReopen(os.Stdout) {
		stream, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
		if err != nil {
			reopenErrors = append(reopenErrors, fmt.Errorf("open stdout CONOUT$: %w", err))
		} else {
			os.Stdout = stream
		}
	}
	if standardStreamNeedsReopen(os.Stderr) {
		stream, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
		if err != nil {
			reopenErrors = append(reopenErrors, fmt.Errorf("open stderr CONOUT$: %w", err))
		} else {
			os.Stderr = stream
		}
	}
	return errors.Join(reopenErrors...)
}

func standardStreamNeedsReopen(stream *os.File) bool {
	if stream == nil {
		return true
	}
	_, err := stream.Stat()
	return err != nil
}
