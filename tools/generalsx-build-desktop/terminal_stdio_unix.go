//go:build !windows

package main

import (
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
	return terminalStreams{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
		close:  func() {},
	}, nil
}
