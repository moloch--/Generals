// GeneralsX @build Codex 05/08/2026 Add a native desktop shell without changing the automated builder's CLI contract.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"os"

	"github.com/moloch--/Generals/internal/buildcli"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed frontend/dist
var assets embed.FS

// GeneralsX @build Codex 05/08/2026 Embed verifiable release provenance because Wails v2 disables Go VCS stamping.
var (
	desktopVersion = "development"
	desktopCommit  = "unknown"
)

func main() {
	if len(os.Args) == 1 {
		prepareDesktopGUI()
	} else if err := prepareArgumentConsole(); err != nil {
		fmt.Fprintf(os.Stderr, "generalsx-build-desktop: prepare console: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, runDesktop))
}

// GeneralsX @build Codex 05/08/2026 Keep every argument-bearing invocation on the exact existing CLI path.
func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer, desktop func() error) int {
	if len(arguments) == 2 && arguments[0] == "--terminal-job" {
		return runTerminalJob(arguments[1])
	}
	if len(arguments) == 1 && arguments[0] == "--desktop-build-info" {
		writeDesktopBuildInfo(stdout, desktopVersion, desktopCommit)
		return 0
	}
	if len(arguments) != 0 {
		return buildcli.Main(context.Background(), arguments, stdin, stdout, stderr)
	}
	if err := desktop(); err != nil {
		fmt.Fprintf(stderr, "generalsx-build-desktop: %v\n", err)
		return 1
	}
	return 0
}

func writeDesktopBuildInfo(output io.Writer, version, commit string) {
	fmt.Fprintf(output, "version=%s\ncommit=%s\n", version, commit)
}

func runDesktop() error {
	app := NewApp()
	return wails.Run(&options.App{
		Title:     "GeneralsX Automated Build Tool",
		Width:     1280,
		Height:    840,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 9, G: 14, B: 24, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
}
