//go:build !darwin && !linux && !windows

package main

import (
	"context"
	"errors"
)

func launchTerminalJob(context.Context, string, string) error {
	return errors.New("native terminal handoff is unsupported on this platform")
}
