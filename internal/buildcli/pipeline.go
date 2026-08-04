package buildcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GeneralsX @build Codex 04/08/2026 Cross-build the optional Online service as an authenticated SFX sidecar.
func (app application) buildOnlineServer(ctx context.Context, buildEnvironment map[string]string) (string, error) {
	source, managed, err := app.resolveOnlineServerSource(ctx)
	if err != nil {
		return "", err
	}
	if !app.cfg.dryRun {
		if err := validateServerSource(source); err != nil {
			return "", err
		}
	}
	if managed {
		fmt.Fprintf(app.runner.stdout, "Using managed Online server checkout: %s\n", source)
	} else {
		fmt.Fprintf(app.runner.stdout, "Using existing Online server checkout: %s\n", source)
	}

	targetOS, targetArch := serverTarget(app.cfg.target)
	fileName := "generals-server"
	if targetOS == "windows" {
		fileName += ".exe"
	}
	output := filepath.Join(app.cfg.repoRoot, "build", "bootstrap", "online-server", targetOS+"-"+targetArch, fileName)
	if !app.cfg.dryRun {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return "", fmt.Errorf("create Online server build directory: %w", err)
		}
		if info, err := os.Lstat(output); err == nil && !info.Mode().IsRegular() {
			return "", fmt.Errorf("Online server output %q is not a regular file", output)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("the Go toolchain running generalsx-build is no longer available on PATH")
	}
	environment := onlineServerBuildEnvironment(buildEnvironment, targetOS, targetArch)
	if err := app.runner.run(ctx, command{
		name: goExecutable,
		args: []string{
			"-C", source,
			"build",
			"-mod=readonly",
			"-buildvcs=false",
			"-trimpath",
			"-ldflags=-s -w -buildid=",
			"-o", output,
			"./cmd/generals-server",
		},
		env: environment,
	}); err != nil {
		return "", fmt.Errorf("build bundled Online server: %w", err)
	}
	if !app.cfg.dryRun {
		info, err := os.Stat(output)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("Online server build did not create a non-empty regular file at %s", output)
		}
	}
	return output, nil
}

func onlineServerBuildEnvironment(buildEnvironment map[string]string, targetOS, targetArch string) map[string]string {
	environment := cloneStringMap(buildEnvironment)
	mergeStringMap(environment, map[string]string{
		"CGO_ENABLED":  "0",
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFLAGS":      "",
		"GOOS":         targetOS,
		"GOARCH":       targetArch,
		"GOTOOLCHAIN":  "auto",
		"GOWORK":       "off",
	})
	return environment
}

func (app application) resolveOnlineServerSource(ctx context.Context) (string, bool, error) {
	if app.cfg.onlineServerSource != "" {
		return app.cfg.onlineServerSource, false, nil
	}
	adjacent := filepath.Join(app.cfg.repoRoot, "generals-server")
	if validateServerSource(adjacent) == nil {
		return adjacent, false, nil
	}
	managed := filepath.Join(app.cfg.cacheDir, "sources", "generals-server")
	if validateServerSource(managed) == nil {
		gitPath, err := app.ensureGit(ctx)
		if err != nil {
			return "", false, fmt.Errorf("prepare Git for the managed Online server: %w", err)
		}
		if err := app.fetchAndCheckoutRef(ctx, gitPath, managed, app.cfg.onlineServerRepo, app.cfg.onlineServerRef); err != nil {
			return "", false, fmt.Errorf("refresh managed Online server ref %q: %w", app.cfg.onlineServerRef, err)
		}
		return managed, true, nil
	}
	if info, err := os.Lstat(managed); err == nil {
		return "", false, fmt.Errorf("managed Online server path %q exists but is not a valid checkout (mode %s)", managed, info.Mode())
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	gitPath, err := app.ensureGit(ctx)
	if err != nil {
		return "", false, fmt.Errorf("prepare Git for the Online server clone: %w", err)
	}
	if !app.cfg.dryRun {
		if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
			return "", false, fmt.Errorf("create managed Online server parent: %w", err)
		}
	}
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"clone", "--no-checkout", "--", app.cfg.onlineServerRepo, managed},
	}); err != nil {
		return "", false, fmt.Errorf("clone Online server: %w", err)
	}
	if err := app.fetchAndCheckoutRef(ctx, gitPath, managed, app.cfg.onlineServerRepo, app.cfg.onlineServerRef); err != nil {
		return "", false, fmt.Errorf("check out fetched Online server ref %q: %w", app.cfg.onlineServerRef, err)
	}
	return managed, true, nil
}

func (app application) fetchAndCheckoutRef(ctx context.Context, gitPath, repository, remote, ref string) error {
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"fetch", "--force", "--", remote, ref},
		dir:  repository,
	}); err != nil {
		return err
	}
	return app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"checkout", "--detach", "FETCH_HEAD"},
		dir:  repository,
	})
}

func validateServerSource(root string) error {
	for _, relative := range []string{"go.mod", filepath.Join("cmd", "generals-server", "main.go")} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil {
			return fmt.Errorf("Online server source %q is missing %s: %w", root, relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Online server source entry %q is not a regular file", filepath.Join(root, relative))
		}
	}
	return nil
}

func serverTarget(targetOS target) (string, string) {
	switch targetOS {
	case targetMacOS:
		return "darwin", "arm64"
	case targetWindows:
		return "windows", "amd64"
	default:
		return "linux", "amd64"
	}
}

// GeneralsX @build Codex 04/08/2026 Reuse the target-native hardened staging and SFX packagers.
func (app application) buildSFX(ctx context.Context, buildEnvironment map[string]string, serverBinary string) error {
	environment := app.sfxEnvironment(buildEnvironment, serverBinary)
	var spec command
	switch app.cfg.target {
	case targetMacOS:
		arguments := []string{filepath.Join(app.cfg.repoRoot, "scripts", "build", "macos", "build-sfx-macos-zh.sh")}
		if app.cfg.skipGameBuild {
			arguments = append(arguments, "--skip-game-build")
		}
		spec = command{name: "/bin/bash", args: arguments, dir: app.cfg.repoRoot, env: environment}
	case targetLinux:
		arguments := []string{filepath.Join(app.cfg.repoRoot, "scripts", "build", "linux", "build-sfx-linux-zh.sh")}
		if app.cfg.skipGameBuild {
			arguments = append(arguments, "--skip-game-build")
		}
		spec = command{name: "/bin/bash", args: arguments, dir: app.cfg.repoRoot, env: environment}
	case targetWindows:
		powerShell, err := findPowerShell()
		if err != nil {
			return err
		}
		arguments := []string{
			"-NoLogo", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass",
			"-File", filepath.Join(app.cfg.repoRoot, "scripts", "build", "windows", "build-sfx-windows-zh.ps1"),
		}
		if app.cfg.skipGameBuild {
			arguments = append(arguments, "-SkipGameBuild")
		}
		if app.cfg.keepWindowsStage {
			arguments = append(arguments, "-KeepStage")
		}
		spec = command{name: powerShell, args: arguments, dir: app.cfg.repoRoot, env: environment}
	}
	if err := app.runner.run(ctx, spec); err != nil {
		return fmt.Errorf("build %s SFX: %w", app.cfg.target, err)
	}
	if app.cfg.dryRun {
		return nil
	}
	info, err := os.Stat(app.cfg.output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("SFX packager did not create a non-empty regular file at %s", app.cfg.output)
	}
	if app.cfg.target == targetMacOS {
		appInfo, err := os.Stat(app.cfg.appOutput)
		if err != nil || !appInfo.IsDir() {
			return fmt.Errorf("macOS packager did not create an app at %s", app.cfg.appOutput)
		}
	}
	return nil
}

func (app application) sfxEnvironment(buildEnvironment map[string]string, serverBinary string) map[string]string {
	environment := cloneStringMap(buildEnvironment)
	mergeStringMap(environment, map[string]string{
		"GX_SFX_ASSET_DIR":         app.cfg.assetsDir,
		"GX_SFX_OUTPUT":            app.cfg.output,
		"GX_ONLINE_SERVER_DEFAULT": app.cfg.onlineEndpoint,
		"GX_SFX_SERVER_BINARY":     serverBinary,
	})
	if app.cfg.appOutput != "" {
		environment["GX_SFX_APP_OUTPUT"] = app.cfg.appOutput
	}
	return environment
}

func findPowerShell() (string, error) {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("PowerShell is required for the native Windows SFX build")
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
