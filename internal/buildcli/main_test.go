package buildcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationDryRunPlansSourceCloneAndBuild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository := filepath.Join(root, "source")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := application{
		cfg: config{
			repoRoot:      repository,
			sourceRepo:    "https://example.invalid/Generals.git",
			sourceRef:     "deadbeef",
			target:        targetLinux,
			assetsDir:     filepath.Join(root, "assets"),
			cacheDir:      filepath.Join(root, "cache"),
			steamCMDDir:   filepath.Join(root, "cache", "steamcmd"),
			output:        filepath.Join(root, "output", "game-sfx"),
			skipAssets:    true,
			skipGameBuild: true,
			dryRun:        true,
			installDeps:   true,
		},
		runner: runner{
			dryRun: true,
			stdout: &stdout,
			stderr: &stderr,
		},
		hostOS:   "linux",
		hostArch: "amd64",
	}
	if err := app.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v, stderr = %s", err, stderr.String())
	}
	output := stdout.String()
	for _, required := range []string{
		"https://example.invalid/Generals.git",
		"fetch --force -- origin deadbeef",
		"checkout --detach FETCH_HEAD",
		"submodule update --init --recursive",
		"build-sfx-linux-zh.sh",
		"Dry run complete; planned output:",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("dry-run output missing %q:\n%s", required, output)
		}
	}
	if _, err := os.Lstat(repository); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created repository path or returned unexpected error: %v", err)
	}
}

func TestValidateHostTargetRejectsUnsupportedNativeLinuxArchitecture(t *testing.T) {
	t.Parallel()
	app := application{
		cfg:      config{target: targetLinux},
		hostOS:   "linux",
		hostArch: "arm64",
	}
	if err := app.validateHostTarget(); err == nil {
		t.Fatal("linux/arm64 host unexpectedly accepted")
	}
}

func TestEnsurePrivateDirectoryRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("symlinked private directory was accepted")
	}
}

func TestAcquireAssetsUsesCompleteTreeNonInteractively(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	var stdout bytes.Buffer
	app := application{
		cfg: config{
			assetsDir:      root,
			target:         targetLinux,
			steamUser:      "configured-user",
			nonInteractive: true,
		},
		runner: runner{stdout: &stdout},
	}
	if err := app.acquireAssets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Using existing retail assets") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestAcquireAssetsRejectsInteractiveLoginInNonInteractiveMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := application{
		cfg: config{
			assetsDir:      root,
			target:         targetLinux,
			steamUser:      "configured-user",
			nonInteractive: true,
		},
		runner: runner{stdout: &bytes.Buffer{}},
	}
	err := app.acquireAssets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SteamCMD authentication is interactive") {
		t.Fatalf("acquireAssets() error = %v", err)
	}
}

func TestEnsureSourceCreatesMissingParentsAndClones(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream")
	if err := os.MkdirAll(filepath.Join(upstream, "scripts", "tooling", "sfx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(upstream, "scripts", "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	for relative, contents := range map[string]string{
		"CMakePresets.json": "{}\n",
		filepath.Join("scripts", "tooling", "sfx", "go.mod"): "module example.invalid/sfx\n",
		filepath.Join("scripts", "build", ".keep"):           "fixture\n",
	} {
		if err := os.WriteFile(filepath.Join(upstream, relative), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runTestCommand(t, upstream, "git", "init", "--initial-branch=main")
	runTestCommand(t, upstream, "git", "add", ".")
	runTestCommand(t, upstream, "git", "-c", "user.name=GeneralsX Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	runTestCommand(t, upstream, "git", "checkout", "-b", "remote-only")
	if err := os.WriteFile(filepath.Join(upstream, "REMOTE_ONLY"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, upstream, "git", "add", "REMOTE_ONLY")
	runTestCommand(t, upstream, "git", "-c", "user.name=GeneralsX Test", "-c", "user.email=test@example.invalid", "commit", "-m", "remote branch")
	runTestCommand(t, upstream, "git", "checkout", "main")

	destination := filepath.Join(root, "missing", "parents", "source")
	var stdout bytes.Buffer
	app := application{
		cfg: config{
			repoRoot:   destination,
			sourceRepo: upstream,
			sourceRef:  "remote-only",
		},
		runner: runner{stdout: &stdout, stderr: &stdout},
	}
	if err := app.ensureSource(context.Background(), "git"); err != nil {
		t.Fatalf("ensureSource() error = %v, output = %s", err, stdout.String())
	}
	if err := validateRepository(destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "REMOTE_ONLY")); err != nil {
		t.Fatalf("remote-only branch was not checked out: %v", err)
	}
	command := exec.Command("git", "status", "--short")
	command.Dir = destination
	status, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 0 {
		t.Fatalf("cloned checkout is dirty: %s", status)
	}
}

func runTestCommand(t *testing.T, directory, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
}
