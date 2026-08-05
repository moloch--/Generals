//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func launchTerminalJob(ctx context.Context, desktopExecutable, specPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	launcherPath := filepath.Join(filepath.Dir(specPath), "launch.command")
	launcher := "#!/bin/sh\nexec " + quotePOSIXShell(desktopExecutable) + " --terminal-job " + quotePOSIXShell(specPath) + "\n"
	if err := os.WriteFile(launcherPath, []byte(launcher), 0o700); err != nil {
		return fmt.Errorf("write private terminal launcher: %w", err)
	}
	command := exec.CommandContext(ctx, "/usr/bin/open", "-a", "Terminal", launcherPath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("open Terminal.app: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func quotePOSIXShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
