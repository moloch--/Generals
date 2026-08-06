package buildcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

type application struct {
	cfg               config
	runner            runner
	hostOS            string
	hostArch          string
	http              *http.Client
	reporter          Reporter
	interactiveRunner InteractiveCommandRunner
}

// Main runs the build command and returns a process exit code.
func Main(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithReporter(ctx, arguments, stdin, stdout, stderr, nil)
}

// GeneralsX @feature Codex 05/08/2026 Allow graphical frontends to observe builds without changing terminal output.
// MainWithReporter runs the build command, reports structured progress, and
// returns the same process exit codes as Main.
func MainWithReporter(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, reporter Reporter) int {
	return MainWithOptions(ctx, arguments, stdin, stdout, stderr, RunOptions{Reporter: reporter})
}

// GeneralsX @feature Codex 05/08/2026 Let desktop frontends provide a real terminal for private interactive prompts.
// MainWithOptions runs the command with optional progress reporting and an
// interactive-command runner while preserving Main's arguments and exit codes.
func MainWithOptions(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, options RunOptions) int {
	cfg, err := parseConfig(arguments, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "generalsx-build: %v\n", err)
		return 2
	}
	app := application{
		cfg: cfg,
		runner: runner{
			dryRun: cfg.dryRun,
			stdin:  stdin,
			stdout: stdout,
			stderr: stderr,
		},
		hostOS:            runtime.GOOS,
		hostArch:          runtime.GOARCH,
		http:              http.DefaultClient,
		reporter:          options.Reporter,
		interactiveRunner: options.InteractiveRunner,
	}
	if err := app.run(ctx); err != nil {
		fmt.Fprintf(stderr, "generalsx-build: %v\n", err)
		return 1
	}
	return 0
}

// GeneralsX @build Codex 04/08/2026 Coordinate an owned-asset single-file build without publishing retail content.
func (app application) run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("build context is nil")
	}
	reportProgress(app.reporter, ProgressPhasePreflight, "Validating the build host and configuration")
	if err := app.validateHostTarget(); err != nil {
		return err
	}
	fmt.Fprintln(app.runner.stdout, "GeneralsX single-file SFX builder")
	fmt.Fprintf(app.runner.stdout, "Target: %s (host %s/%s)\n", app.cfg.target, app.hostOS, app.hostArch)
	fmt.Fprintln(app.runner.stdout, "NOTICE: the output contains copyrighted retail game data for personal use.")
	fmt.Fprintln(app.runner.stdout, "Do not redistribute it without permission for every embedded asset.")
	if app.cfg.dryRun {
		fmt.Fprintln(app.runner.stdout, "Dry run: external commands, downloads, and filesystem staging are not executed.")
	}

	if !app.cfg.dryRun {
		if err := ensurePrivateDirectory(app.cfg.cacheDir); err != nil {
			return err
		}
	}
	reportProgress(app.reporter, ProgressPhaseSource, "Preparing the GeneralsX source checkout")
	gitPath, err := app.ensureGit(ctx)
	if err != nil {
		return err
	}
	if err := app.ensureSource(ctx, gitPath); err != nil {
		return err
	}
	if !app.cfg.dryRun {
		if err := validateRepository(app.cfg.repoRoot); err != nil {
			return err
		}
	}
	reportProgress(app.reporter, ProgressPhaseToolchain, "Preparing the target build toolchain")
	buildEnv, err := app.bootstrap(ctx, gitPath)
	if err != nil {
		return err
	}
	reportProgress(app.reporter, ProgressPhaseAssets, "Preparing owned Zero Hour retail assets")
	if err := app.acquireAssets(ctx); err != nil {
		return err
	}

	serverBinary := ""
	reportProgress(app.reporter, ProgressPhaseOnlineServer, "Preparing the optional Online server")
	if app.cfg.withOnlineServer {
		serverBinary, err = app.buildOnlineServer(ctx, buildEnv)
		if err != nil {
			return err
		}
	}
	buildMessage := "Building the self-extracting game executable"
	if app.cfg.target == targetMacOS {
		buildMessage = "Building the Finder-ready game application"
	}
	reportProgress(app.reporter, ProgressPhaseBuild, buildMessage)
	if err := app.buildSFX(ctx, buildEnv, serverBinary); err != nil {
		return err
	}
	fmt.Fprintln(app.runner.stdout)
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "Dry run complete; planned output: %s\n", app.cfg.primaryArtifactPath())
	} else {
		fmt.Fprintf(app.runner.stdout, "Build complete: %s\n", app.cfg.primaryArtifactPath())
	}
	if app.cfg.target == targetMacOS {
		fmt.Fprintf(app.runner.stdout, "Terminal launcher: %s\n", filepath.Join(app.cfg.appOutput, "Contents", "MacOS", "GeneralsXZH"))
	}
	if app.cfg.withOnlineServer {
		fmt.Fprintln(app.runner.stdout, "Bundled backend: launch with --sfx-server; it is never started or exposed automatically.")
	}
	reportProgress(app.reporter, ProgressPhaseComplete, "Automated build complete")
	return nil
}

func (app application) ensureSource(ctx context.Context, gitPath string) error {
	if err := validateRepository(app.cfg.repoRoot); err == nil {
		fmt.Fprintf(app.runner.stdout, "Using existing source checkout: %s\n", app.cfg.repoRoot)
		return nil
	}
	if info, err := os.Lstat(app.cfg.repoRoot); err == nil {
		return fmt.Errorf("source destination %q already exists but is not a complete GeneralsX checkout (mode %s)", app.cfg.repoRoot, info.Mode())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect source destination: %w", err)
	}
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] clone source %s at %s into %s\n", app.cfg.sourceRepo, app.cfg.sourceRef, app.cfg.repoRoot)
	} else if err := os.MkdirAll(filepath.Dir(app.cfg.repoRoot), 0o755); err != nil {
		return fmt.Errorf("create source checkout parent: %w", err)
	}
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"clone", "--no-checkout", "--", app.cfg.sourceRepo, app.cfg.repoRoot},
	}); err != nil {
		return fmt.Errorf("clone GeneralsX source: %w", err)
	}
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"fetch", "--force", "--", "origin", app.cfg.sourceRef},
		dir:  app.cfg.repoRoot,
	}); err != nil {
		return fmt.Errorf("fetch GeneralsX source ref %q: %w", app.cfg.sourceRef, err)
	}
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"checkout", "--detach", "FETCH_HEAD"},
		dir:  app.cfg.repoRoot,
	}); err != nil {
		return fmt.Errorf("check out fetched GeneralsX source ref %q: %w", app.cfg.sourceRef, err)
	}
	return nil
}

func validateRepository(root string) error {
	for _, relative := range []string{
		"CMakePresets.json",
		filepath.Join("scripts", "tooling", "sfx", "go.mod"),
		filepath.Join("scripts", "build"),
	} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			return fmt.Errorf("repository root %q is missing %s: %w", root, relative, err)
		}
	}
	return nil
}

func (app application) validateHostTarget() error {
	switch app.cfg.target {
	case targetMacOS:
		if app.hostOS != "darwin" || app.hostArch != "arm64" {
			return fmt.Errorf("macOS SFX builds require a native darwin/arm64 host; current host is %s/%s", app.hostOS, app.hostArch)
		}
	case targetWindows:
		if app.hostOS != "windows" || app.hostArch != "amd64" {
			return fmt.Errorf("the supported Windows game build requires native Windows/amd64 with MSVC; current host is %s/%s", app.hostOS, app.hostArch)
		}
	case targetLinux:
		if app.hostOS != "linux" && app.hostOS != "darwin" {
			return fmt.Errorf("Linux SFX orchestration requires Linux or macOS with Docker; current host is %s/%s", app.hostOS, app.hostArch)
		}
		if app.hostOS == "linux" && app.hostArch != "amd64" {
			return fmt.Errorf("native Linux SFX orchestration requires linux/amd64; current host is %s/%s", app.hostOS, app.hostArch)
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory %q: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private path %q is not a real directory", path)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("private directory %q is accessible by other users (mode %#o)", path, info.Mode().Perm())
		}
	}
	return nil
}
