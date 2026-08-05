//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
)

type terminalStreams struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	close  func()
}

func openTerminalStreams() (terminalStreams, error) {
	stdin, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		return terminalStreams{}, fmt.Errorf("open CONIN$: %w", err)
	}
	stdout, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		stdin.Close()
		return terminalStreams{}, fmt.Errorf("open CONOUT$: %w", err)
	}
	return terminalStreams{
		stdin: stdin, stdout: stdout, stderr: stdout,
		close: func() {
			_ = stdin.Close()
			_ = stdout.Close()
		},
	}, nil
}
