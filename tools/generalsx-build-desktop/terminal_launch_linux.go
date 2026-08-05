//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func launchTerminalJob(ctx context.Context, desktopExecutable, specPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("no graphical display is available; run generalsx-build --headless in a terminal")
	}
	type candidate struct {
		name string
		args []string
	}
	candidates := []candidate{
		{name: "x-terminal-emulator", args: []string{"-e", desktopExecutable, "--terminal-job", specPath}},
		{name: "gnome-terminal", args: []string{"--", desktopExecutable, "--terminal-job", specPath}},
		{name: "konsole", args: []string{"-e", desktopExecutable, "--terminal-job", specPath}},
		{name: "xfce4-terminal", args: []string{"--disable-server", "--execute", desktopExecutable, "--terminal-job", specPath}},
		{name: "mate-terminal", args: []string{"--", desktopExecutable, "--terminal-job", specPath}},
		{name: "xterm", args: []string{"-e", desktopExecutable, "--terminal-job", specPath}},
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		command := exec.Command(path, candidate.args...)
		if err := command.Start(); err != nil {
			continue
		}
		if err := command.Process.Release(); err != nil {
			return fmt.Errorf("release %s launcher: %w", candidate.name, err)
		}
		return nil
	}
	return errors.New("no supported terminal emulator found (x-terminal-emulator, gnome-terminal, konsole, xfce4-terminal, mate-terminal, or xterm)")
}
