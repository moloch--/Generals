package buildcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedOnlineServerRefreshesRequestedRef(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream")
	if err := os.MkdirAll(filepath.Join(upstream, "cmd", "generals-server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upstream, "go.mod"), []byte("module example.invalid/server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upstream, "cmd", "generals-server", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, upstream, "git", "init", "--initial-branch=main")
	runTestCommand(t, upstream, "git", "add", ".")
	runTestCommand(t, upstream, "git", "-c", "user.name=GeneralsX Test", "-c", "user.email=test@example.invalid", "commit", "-m", "main")
	runTestCommand(t, upstream, "git", "checkout", "-b", "alternate")
	if err := os.WriteFile(filepath.Join(upstream, "ALTERNATE"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, upstream, "git", "add", "ALTERNATE")
	runTestCommand(t, upstream, "git", "-c", "user.name=GeneralsX Test", "-c", "user.email=test@example.invalid", "commit", "-m", "alternate")
	runTestCommand(t, upstream, "git", "checkout", "main")

	var output bytes.Buffer
	app := application{
		cfg: config{
			repoRoot:         filepath.Join(root, "game"),
			cacheDir:         filepath.Join(root, "cache"),
			onlineServerRepo: upstream,
			onlineServerRef:  "alternate",
			installDeps:      false,
		},
		runner: runner{stdout: &output, stderr: &output},
	}
	managed, isManaged, err := app.resolveOnlineServerSource(context.Background())
	if err != nil {
		t.Fatalf("initial resolve: %v\n%s", err, output.String())
	}
	if !isManaged {
		t.Fatal("cloned server was not marked managed")
	}
	if _, err := os.Stat(filepath.Join(managed, "ALTERNATE")); err != nil {
		t.Fatalf("alternate ref was not checked out: %v", err)
	}

	app.cfg.onlineServerRef = "main"
	managedAgain, isManaged, err := app.resolveOnlineServerSource(context.Background())
	if err != nil {
		t.Fatalf("refresh resolve: %v\n%s", err, output.String())
	}
	if !isManaged || managedAgain != managed {
		t.Fatalf("refreshed managed path = %q, managed = %t", managedAgain, isManaged)
	}
	if _, err := os.Lstat(filepath.Join(managed, "ALTERNATE")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed checkout did not move back to main: %v", err)
	}

	secondUpstream := filepath.Join(root, "second-upstream")
	runTestCommand(t, root, "git", "clone", upstream, secondUpstream)
	if err := os.WriteFile(filepath.Join(secondUpstream, "SECOND_REMOTE"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, secondUpstream, "git", "add", "SECOND_REMOTE")
	runTestCommand(t, secondUpstream, "git", "-c", "user.name=GeneralsX Test", "-c", "user.email=test@example.invalid", "commit", "-m", "second remote")
	app.cfg.onlineServerRepo = secondUpstream
	if _, _, err := app.resolveOnlineServerSource(context.Background()); err != nil {
		t.Fatalf("changed remote resolve: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(filepath.Join(managed, "SECOND_REMOTE")); err != nil {
		t.Fatalf("managed checkout ignored changed remote: %v", err)
	}
}

func TestSFXEnvironmentClearsInheritedOptionalServer(t *testing.T) {
	t.Parallel()
	app := application{cfg: config{
		assetsDir:      "/owned/assets",
		output:         "/output/game",
		onlineEndpoint: "",
	}}
	environment := app.sfxEnvironment(map[string]string{
		"GX_SFX_SERVER_BINARY":     "/stale/server",
		"GX_ONLINE_SERVER_DEFAULT": "stale.example:29900",
	}, "")
	if value := environment["GX_SFX_SERVER_BINARY"]; value != "" {
		t.Fatalf("GX_SFX_SERVER_BINARY = %q, want empty", value)
	}
	if value := environment["GX_ONLINE_SERVER_DEFAULT"]; value != "" {
		t.Fatalf("GX_ONLINE_SERVER_DEFAULT = %q, want empty", value)
	}
}

func TestOnlineServerBuildEnvironmentSanitizesAmbientGoSettings(t *testing.T) {
	t.Parallel()
	environment := onlineServerBuildEnvironment(map[string]string{
		"GOENV":        "/tmp/host-goenv",
		"GOEXPERIMENT": "host-experiment",
		"GOFLAGS":      "-race -mod=mod",
		"GOTOOLCHAIN":  "host-toolchain",
		"GOWORK":       "/tmp/go.work",
		"PATH":         "/tool/path",
	}, "windows", "amd64")
	want := map[string]string{
		"CGO_ENABLED":  "0",
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFLAGS":      "",
		"GOOS":         "windows",
		"GOARCH":       "amd64",
		"GOTOOLCHAIN":  "auto",
		"GOWORK":       "off",
		"PATH":         "/tool/path",
	}
	for key, expected := range want {
		if got := environment[key]; got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}
