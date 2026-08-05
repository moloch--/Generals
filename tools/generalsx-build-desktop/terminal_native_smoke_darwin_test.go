//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
)

// TestNativeTerminalHandoffSmoke is opt-in because it opens Terminal.app. It
// verifies the packaged executable's real launcher/helper round trip without
// invoking SteamCMD or requesting a credential.
func TestNativeTerminalHandoffSmoke(t *testing.T) {
	desktopBinary := os.Getenv("GENERALSX_DESKTOP_SMOKE_BINARY")
	if desktopBinary == "" {
		t.Skip("set GENERALSX_DESKTOP_SMOKE_BINARY to test the native terminal handoff")
	}
	desktopBinary, err := filepath.Abs(desktopBinary)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(desktopBinary); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("desktop smoke binary is unavailable: %v", err)
	}

	runner := newTerminalInteractiveRunner().(*terminalInteractiveRunner)
	runner.executablePath = func() (string, error) { return desktopBinary, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.RunInteractive(ctx, buildcli.InteractiveCommand{
		Purpose:    buildcli.InteractiveDependencyInstallation,
		Executable: "/usr/bin/true",
	}); err != nil {
		t.Fatalf("native terminal handoff = %v", err)
	}
}
