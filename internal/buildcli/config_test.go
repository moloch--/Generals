package buildcli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		host  string
		want  target
	}{
		{value: "auto", host: "darwin", want: targetMacOS},
		{value: "auto", host: "linux", want: targetLinux},
		{value: "auto", host: "windows", want: targetWindows},
		{value: "windows", host: "darwin", want: targetWindows},
	}
	for _, test := range tests {
		test := test
		t.Run(test.value+"-"+test.host, func(t *testing.T) {
			t.Parallel()
			got, err := parseTarget(test.value, test.host)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("parseTarget() = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := parseTarget("all", "linux"); err == nil {
		t.Fatal("parseTarget(all) succeeded")
	}
}

func TestParseConfigOnlineServerDefaultsToLoopback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	assets := filepath.Join(t.TempDir(), "assets")
	output := filepath.Join(t.TempDir(), "game-sfx")
	var stderr bytes.Buffer
	cfg, err := parseConfig([]string{
		"--target", "linux",
		"--repo", root,
		"--assets-dir", assets,
		"--cache-dir", filepath.Join(t.TempDir(), "cache"),
		"--output", output,
		"--with-online-server",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseConfig() error = %v, stderr = %s", err, stderr.String())
	}
	if cfg.onlineEndpoint != "127.0.0.1:29900" {
		t.Fatalf("onlineEndpoint = %q", cfg.onlineEndpoint)
	}
}

func TestLoadConfigurationDefaults(t *testing.T) {
	t.Parallel()
	defaults, err := LoadConfigurationDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string]string{
		"repository root":          defaults.RepositoryRoot,
		"source repository":        defaults.SourceRepository,
		"source reference":         defaults.SourceReference,
		"asset directory":          defaults.AssetsDirectory,
		"SteamCMD directory":       defaults.SteamCMDDirectory,
		"cache directory":          defaults.CacheDirectory,
		"output path":              defaults.OutputPath,
		"Online server repository": defaults.OnlineServerRepository,
		"Online server reference":  defaults.OnlineServerReference,
	} {
		if value == "" {
			t.Errorf("%s is empty", label)
		}
	}
	if defaults.Target != "auto" {
		t.Fatalf("Target = %q, want auto", defaults.Target)
	}
	if !defaults.InstallDependencies {
		t.Fatal("InstallDependencies = false, want true")
	}
	if defaults.SteamCMDDirectory != filepath.Join(defaults.CacheDirectory, "steamcmd") {
		t.Fatalf("SteamCMDDirectory = %q", defaults.SteamCMDDirectory)
	}
	if runtime.GOOS == "darwin" && defaults.AppOutputPath == "" {
		t.Fatal("AppOutputPath is empty on macOS")
	}
}

func TestValidateArgumentsAcceptsHeadless(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"--headless", "--help"},
		{"--headless=true", "--help"},
		{"--headless=false", "--help"},
	} {
		if err := ValidateArguments(arguments); err != nil {
			t.Errorf("ValidateArguments(%q) = %v", arguments, err)
		}
	}
	if err := ValidateArguments([]string{"--target", "unsupported"}); err == nil {
		t.Fatal("unsupported target passed argument validation")
	}
}

func TestValidateOnlineEndpoint(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"127.0.0.1:29900",
		"online.example.net",
		"tls://online.example.net:29900",
	} {
		if err := validateOnlineEndpoint(value); err != nil {
			t.Errorf("validateOnlineEndpoint(%q) = %v", value, err)
		}
	}
	for _, value := range []string{
		"https://online.example.net",
		"user@online.example.net",
		"online.example.net:0",
		"online.example.net:65536",
		"[::1]:29900",
		"bad_label.example",
		"999.999.1.1:29900",
	} {
		if err := validateOnlineEndpoint(value); err == nil {
			t.Errorf("validateOnlineEndpoint(%q) succeeded", value)
		}
	}
}

func TestPathsOverlap(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "tmp", "assets")
	if !pathsOverlap(root, filepath.Join(root, "output")) {
		t.Fatal("child path did not overlap")
	}
	if pathsOverlap(root, filepath.Join(string(filepath.Separator), "tmp", "build")) {
		t.Fatal("sibling paths overlap")
	}
}

func TestPathsOverlapResolvesExistingSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.Mkdir(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(assets, alias); err != nil {
		t.Fatal(err)
	}
	if !pathsOverlap(assets, filepath.Join(alias, "cache", "downloads")) {
		t.Fatal("symlinked child path did not overlap")
	}
}

func TestValidateGitRemoteAndRefRejectsOptionInjection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		remote string
		ref    string
	}{
		{remote: "--upload-pack=helper", ref: "main"},
		{remote: "https://example.invalid/repo.git", ref: "--help"},
		{remote: "https://example.invalid/repo.git\nmalicious", ref: "main"},
	} {
		if err := validateGitRemoteAndRef("source", test.remote, test.ref); err == nil {
			t.Errorf("validateGitRemoteAndRef(%q, %q) succeeded", test.remote, test.ref)
		}
	}
	if err := validateGitRemoteAndRef("source", "git@github.com:moloch--/Generals.git", "feature/builder"); err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigRejectsSkipBuildWithCompiledEndpoint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	arguments := []string{
		"--target", "linux",
		"--repo", filepath.Join(root, "source"),
		"--assets-dir", filepath.Join(root, "assets"),
		"--cache-dir", filepath.Join(root, "cache"),
		"--output", filepath.Join(root, "output", "game-sfx"),
		"--skip-game-build",
		"--online-endpoint", "127.0.0.1:29900",
	}
	if _, err := parseConfig(arguments, &bytes.Buffer{}); err == nil {
		t.Fatal("skip game build with compiled endpoint succeeded")
	}
}
