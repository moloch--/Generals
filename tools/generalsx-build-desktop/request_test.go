package main

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/moloch--/Generals/internal/buildcli"
)

func TestEffectiveArtifactPathMatchesCLIBlankOutputDefaults(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	for _, test := range []struct {
		name   string
		hostOS string
		target string
		file   string
	}{
		{name: "automatic macOS", hostOS: "darwin", target: "auto", file: "GeneralsXZH-macos-arm64-sfx"},
		{name: "explicit Linux on macOS", hostOS: "darwin", target: "linux", file: "GeneralsXZH-linux-amd64-sfx"},
		{name: "automatic Windows", hostOS: "windows", target: "auto", file: "GeneralsXZH-windows-amd64-sfx.exe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := effectiveArtifactPath(BuildRequest{RepoRoot: repository, Target: test.target}, test.hostOS)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(repository, "build", "sfx", test.file)
			if path != want {
				t.Fatalf("effective artifact = %q, want %q", path, want)
			}
		})
	}
}

func TestGetDefaultsMapsSharedBuilderDefaults(t *testing.T) {
	t.Parallel()
	wantRequest := BuildRequest{
		RepoRoot:           "/source",
		SourceRepo:         "https://example.invalid/source.git",
		SourceRef:          "v1.2.3",
		Target:             "auto",
		AssetsDir:          "/assets",
		SteamUser:          "commander",
		CacheDir:           "/cache",
		SteamCMDDir:        "/cache/steamcmd",
		Output:             "/output/game",
		AppOutput:          "/output/game.app",
		InstallDeps:        true,
		AcceptSDKLicenses:  true,
		WithOnlineServer:   true,
		OnlineServerSource: "/server",
		OnlineServerRepo:   "https://example.invalid/server.git",
		OnlineServerRef:    "stable",
		OnlineEndpoint:     "tls://example.invalid:29900",
		SkipAssets:         true,
		SkipGameBuild:      false,
		DryRun:             true,
		NonInteractive:     true,
		KeepWindowsStage:   true,
	}
	dependencies := defaultAppDependencies()
	dependencies.hostOS = "darwin"
	dependencies.hostArch = "arm64"
	dependencies.loadDefaults = func() (buildcli.ConfigurationDefaults, error) {
		return buildcli.ConfigurationDefaults{
			RepositoryRoot:         wantRequest.RepoRoot,
			SourceRepository:       wantRequest.SourceRepo,
			SourceReference:        wantRequest.SourceRef,
			Target:                 wantRequest.Target,
			AssetsDirectory:        wantRequest.AssetsDir,
			SteamUser:              wantRequest.SteamUser,
			SteamCMDDirectory:      wantRequest.SteamCMDDir,
			CacheDirectory:         wantRequest.CacheDir,
			OutputPath:             wantRequest.Output,
			AppOutputPath:          wantRequest.AppOutput,
			InstallDependencies:    wantRequest.InstallDeps,
			AcceptSDKLicenses:      wantRequest.AcceptSDKLicenses,
			IncludeOnlineServer:    wantRequest.WithOnlineServer,
			OnlineServerSource:     wantRequest.OnlineServerSource,
			OnlineServerRepository: wantRequest.OnlineServerRepo,
			OnlineServerReference:  wantRequest.OnlineServerRef,
			OnlineEndpoint:         wantRequest.OnlineEndpoint,
			SkipAssets:             wantRequest.SkipAssets,
			SkipGameBuild:          wantRequest.SkipGameBuild,
			DryRun:                 wantRequest.DryRun,
			NonInteractive:         wantRequest.NonInteractive,
			KeepWindowsStage:       wantRequest.KeepWindowsStage,
		}, nil
	}
	defaults, err := newApp(dependencies).GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.HostOS != "darwin" || defaults.HostArch != "arm64" {
		t.Fatalf("host defaults = %s/%s", defaults.HostOS, defaults.HostArch)
	}
	if !reflect.DeepEqual(defaults.Request, wantRequest) {
		t.Fatalf("request = %#v, want %#v", defaults.Request, wantRequest)
	}
}

func TestBuildRequestArgumentsMapsEveryField(t *testing.T) {
	t.Parallel()
	request := BuildRequest{
		RepoRoot:           "/source tree",
		SourceRepo:         "https://example.invalid/source.git",
		SourceRef:          "release",
		Target:             "linux",
		AssetsDir:          "/asset tree",
		SteamUser:          "commander",
		CacheDir:           "/cache",
		SteamCMDDir:        "/cache/steamcmd",
		Output:             "/output/game",
		AppOutput:          "/output/game.app",
		InstallDeps:        true,
		AcceptSDKLicenses:  true,
		WithOnlineServer:   true,
		OnlineServerSource: "/server source",
		OnlineServerRepo:   "https://example.invalid/server.git",
		OnlineServerRef:    "main",
		OnlineEndpoint:     "tls://online.example.invalid:29900",
		SkipAssets:         true,
		SkipGameBuild:      false,
		DryRun:             true,
		NonInteractive:     true,
		KeepWindowsStage:   true,
	}
	want := []string{
		"--repo=/source tree",
		"--source-repo=https://example.invalid/source.git",
		"--source-ref=release",
		"--target=linux",
		"--assets-dir=/asset tree",
		"--steam-user=commander",
		"--cache-dir=/cache",
		"--steamcmd-dir=/cache/steamcmd",
		"--output=/output/game",
		"--app-output=/output/game.app",
		"--install-deps=true",
		"--accept-sdk-licenses=true",
		"--with-online-server=true",
		"--online-server-source=/server source",
		"--online-server-repo=https://example.invalid/server.git",
		"--online-server-ref=main",
		"--online-endpoint=tls://online.example.invalid:29900",
		"--skip-assets=true",
		"--skip-game-build=false",
		"--dry-run=true",
		"--non-interactive=true",
		"--keep-windows-stage=true",
	}
	if got := buildRequestArguments(request); !slices.Equal(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
}

func TestValidateBuildUsesFieldAndAuthoritativeValidation(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defaults, err := app.GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	valid := defaults.Request
	valid.SkipAssets = true
	if issues := app.ValidateBuild(valid); hasErrorIssues(issues) {
		t.Fatalf("valid request issues = %#v", issues)
	}

	invalid := valid
	invalid.Target = "unsupported"
	invalid.OnlineEndpoint = "tls://bad host:70000"
	invalid.SkipGameBuild = true
	invalid.WithOnlineServer = true
	issues := app.ValidateBuild(invalid)
	for _, field := range []string{"target", "onlineEndpoint", "skipGameBuild"} {
		if !hasIssueField(issues, field) {
			t.Errorf("issues missing field %q: %#v", field, issues)
		}
	}
}

func TestValidateBuildAcceptsTerminalHandoffRequest(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defaults, err := app.GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	request := defaults.Request
	request.SkipAssets = false
	request.NonInteractive = false
	for _, issue := range app.ValidateBuild(request) {
		if issue.Field == "skipAssets" || issue.Message == "Steam acquisition needs a real terminal" {
			t.Fatalf("terminal handoff was rejected: %#v", issue)
		}
	}
}

func TestValidateBuildReportsUnsupportedHostTarget(t *testing.T) {
	t.Parallel()
	dependencies := defaultAppDependencies()
	dependencies.hostOS = "linux"
	dependencies.hostArch = "arm64"
	app := newApp(dependencies)
	defaults, err := app.GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	request := defaults.Request
	request.Target = "linux"
	request.SkipAssets = true
	issues := app.ValidateBuild(request)
	if !hasIssueField(issues, "target") {
		t.Fatalf("host target issue missing: %#v", issues)
	}
}

func hasErrorIssues(issues []ValidationIssue) bool {
	return slices.ContainsFunc(issues, func(issue ValidationIssue) bool { return issue.Severity == "error" })
}

func hasIssueField(issues []ValidationIssue, field string) bool {
	return slices.ContainsFunc(issues, func(issue ValidationIssue) bool { return issue.Field == field })
}

func TestTargetForHost(t *testing.T) {
	t.Parallel()
	want := map[string]string{"darwin": "macos", "linux": "linux", "windows": "windows"}
	for host, target := range want {
		if got := targetForHost(host); got != target {
			t.Errorf("targetForHost(%q) = %q, want %q", host, got, target)
		}
	}
}
