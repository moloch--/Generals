package main

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moloch--/Generals/internal/buildcli"
)

// BuildRequest mirrors the public generalsx-build flags without accepting any secret.
type BuildRequest struct {
	RepoRoot           string `json:"repoRoot"`
	SourceRepo         string `json:"sourceRepo"`
	SourceRef          string `json:"sourceRef"`
	Target             string `json:"target"`
	AssetsDir          string `json:"assetsDir"`
	SteamUser          string `json:"steamUser"`
	CacheDir           string `json:"cacheDir"`
	SteamCMDDir        string `json:"steamCMDDir"`
	Output             string `json:"output"`
	AppOutput          string `json:"appOutput"`
	InstallDeps        bool   `json:"installDeps"`
	AcceptSDKLicenses  bool   `json:"acceptSDKLicenses"`
	WithOnlineServer   bool   `json:"withOnlineServer"`
	OnlineServerSource string `json:"onlineServerSource"`
	OnlineServerRepo   string `json:"onlineServerRepo"`
	OnlineServerRef    string `json:"onlineServerRef"`
	OnlineEndpoint     string `json:"onlineEndpoint"`
	SkipAssets         bool   `json:"skipAssets"`
	SkipGameBuild      bool   `json:"skipGameBuild"`
	DryRun             bool   `json:"dryRun"`
	NonInteractive     bool   `json:"nonInteractive"`
	KeepWindowsStage   bool   `json:"keepWindowsStage"`
}

type DesktopDefaults struct {
	HostOS   string       `json:"hostOS"`
	HostArch string       `json:"hostArch"`
	Request  BuildRequest `json:"request"`
}

type ValidationIssue struct {
	Field    string `json:"field"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// GeneralsX @build Codex 05/08/2026 Present the exact CLI defaults through the desktop form.
func (a *App) GetDefaults() (DesktopDefaults, error) {
	defaults, err := a.dependencies.loadDefaults()
	if err != nil {
		return DesktopDefaults{}, err
	}
	request := BuildRequest{
		RepoRoot:           defaults.RepositoryRoot,
		SourceRepo:         defaults.SourceRepository,
		SourceRef:          defaults.SourceReference,
		Target:             defaults.Target,
		AssetsDir:          defaults.AssetsDirectory,
		SteamUser:          defaults.SteamUser,
		CacheDir:           defaults.CacheDirectory,
		SteamCMDDir:        defaults.SteamCMDDirectory,
		Output:             defaults.OutputPath,
		AppOutput:          defaults.AppOutputPath,
		InstallDeps:        defaults.InstallDependencies,
		AcceptSDKLicenses:  defaults.AcceptSDKLicenses,
		WithOnlineServer:   defaults.IncludeOnlineServer,
		OnlineServerSource: defaults.OnlineServerSource,
		OnlineServerRepo:   defaults.OnlineServerRepository,
		OnlineServerRef:    defaults.OnlineServerReference,
		OnlineEndpoint:     defaults.OnlineEndpoint,
		SkipAssets:         defaults.SkipAssets,
		SkipGameBuild:      defaults.SkipGameBuild,
		DryRun:             defaults.DryRun,
		NonInteractive:     defaults.NonInteractive,
		KeepWindowsStage:   defaults.KeepWindowsStage,
	}
	return DesktopDefaults{
		HostOS:   a.dependencies.hostOS,
		HostArch: a.dependencies.hostArch,
		Request:  request,
	}, nil
}

func targetForHost(hostOS string) string {
	switch hostOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// GeneralsX @bugfix Codex 05/08/2026 Keep the hidden macOS raw launcher on its safe deterministic intermediate path.
func normalizeDesktopBuildRequest(request BuildRequest, hostOS string) BuildRequest {
	target, err := resolveTarget(request.Target, hostOS)
	if err == nil && target == "macos" {
		request.Output = filepath.Join(request.RepoRoot, "build", "sfx", "GeneralsXZH-macos-arm64-sfx")
	}
	return request
}

// GeneralsX @build Codex 05/08/2026 Validate desktop input before starting irreversible or interactive host setup.
func (a *App) ValidateBuild(request BuildRequest) []ValidationIssue {
	request = normalizeDesktopBuildRequest(request, a.dependencies.hostOS)
	issues := make([]ValidationIssue, 0)
	errorIssue := func(field, message string) {
		issues = append(issues, ValidationIssue{Field: field, Message: message, Severity: "error"})
	}
	warningIssue := func(field, message string) {
		issues = append(issues, ValidationIssue{Field: field, Message: message, Severity: "warning"})
	}

	for _, required := range []struct {
		field string
		value string
	}{
		{field: "repoRoot", value: request.RepoRoot},
		{field: "sourceRepo", value: request.SourceRepo},
		{field: "sourceRef", value: request.SourceRef},
		{field: "target", value: request.Target},
		{field: "assetsDir", value: request.AssetsDir},
		{field: "cacheDir", value: request.CacheDir},
		{field: "onlineServerRepo", value: request.OnlineServerRepo},
		{field: "onlineServerRef", value: request.OnlineServerRef},
	} {
		if strings.TrimSpace(required.value) == "" {
			errorIssue(required.field, "This value is required.")
		}
	}

	resolvedTarget, targetErr := resolveTarget(request.Target, a.dependencies.hostOS)
	if targetErr != nil {
		errorIssue("target", targetErr.Error())
	} else if err := validateHostTarget(resolvedTarget, a.dependencies.hostOS, a.dependencies.hostArch); err != nil {
		errorIssue("target", err.Error())
	}
	if resolvedTarget == "macos" && request.AppOutput != "" && !strings.HasSuffix(strings.ToLower(request.AppOutput), ".app") {
		errorIssue("appOutput", "macOS app output must end in .app.")
	}
	if resolvedTarget == "windows" && request.Output != "" && !strings.HasSuffix(strings.ToLower(request.Output), ".exe") {
		errorIssue("output", "Windows SFX output must end in .exe.")
	}

	if err := validateRemoteAndRef("source", request.SourceRepo, request.SourceRef); err != nil {
		errorIssue("sourceRepo", err.Error())
	}
	if err := validateRemoteAndRef("Online server", request.OnlineServerRepo, request.OnlineServerRef); err != nil {
		errorIssue("onlineServerRepo", err.Error())
	}
	if request.OnlineEndpoint != "" {
		if err := validateEndpoint(request.OnlineEndpoint); err != nil {
			errorIssue("onlineEndpoint", err.Error())
		}
	}
	if request.SkipGameBuild && (request.OnlineEndpoint != "" || request.WithOnlineServer) {
		errorIssue("skipGameBuild", "A reused game build cannot compile an Online endpoint or bundled server endpoint.")
	}
	if !request.SkipAssets && request.NonInteractive && !request.DryRun {
		warningIssue("nonInteractive", "The build will fail if the selected retail assets are incomplete because Steam authentication is interactive.")
	}
	if request.AcceptSDKLicenses && !request.InstallDeps {
		warningIssue("acceptSDKLicenses", "SDK license acceptance has no effect while dependency installation is disabled.")
	}

	addPathOverlapIssues(request, errorIssue)
	if err := buildcli.ValidateArguments(buildRequestArguments(request)); err != nil && !hasValidationMessage(issues, err.Error()) {
		errorIssue("request", err.Error())
	}
	return issues
}

func hasValidationMessage(issues []ValidationIssue, message string) bool {
	for _, issue := range issues {
		if issue.Message == message {
			return true
		}
	}
	return false
}

func resolveTarget(value, hostOS string) (string, error) {
	if value == "auto" {
		switch hostOS {
		case "darwin", "linux", "windows":
			return targetForHost(hostOS), nil
		default:
			return "", fmt.Errorf("host OS %q has no automatic target", hostOS)
		}
	}
	switch value {
	case "macos", "linux", "windows":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported target %q; use auto, macos, linux, or windows", value)
	}
}

// GeneralsX @bugfix Codex 05/08/2026 Record the platform-native artifact exposed by the GUI.
func effectiveArtifactPath(request BuildRequest, hostOS string) (string, error) {
	target, err := resolveTarget(request.Target, hostOS)
	if err != nil {
		return "", err
	}
	if target == "macos" {
		output := request.AppOutput
		if output == "" {
			output = filepath.Join(request.RepoRoot, "build", "sfx", "GeneralsXZH.app")
		}
		return filepath.Abs(output)
	}
	output := request.Output
	if output == "" {
		name := "GeneralsXZH-linux-amd64-sfx"
		switch target {
		case "windows":
			name = "GeneralsXZH-windows-amd64-sfx.exe"
		}
		output = filepath.Join(request.RepoRoot, "build", "sfx", name)
	}
	return filepath.Abs(output)
}

func validateHostTarget(target, hostOS, hostArch string) error {
	switch target {
	case "macos":
		if hostOS != "darwin" || hostArch != "arm64" {
			return fmt.Errorf("macOS SFX builds require a native darwin/arm64 host; current host is %s/%s", hostOS, hostArch)
		}
	case "windows":
		if hostOS != "windows" || hostArch != "amd64" {
			return fmt.Errorf("the supported Windows game build requires native Windows/amd64 with MSVC; current host is %s/%s", hostOS, hostArch)
		}
	case "linux":
		if hostOS != "linux" && hostOS != "darwin" {
			return fmt.Errorf("Linux SFX orchestration requires Linux or macOS with Docker; current host is %s/%s", hostOS, hostArch)
		}
		if hostOS == "linux" && hostArch != "amd64" {
			return fmt.Errorf("native Linux SFX orchestration requires linux/amd64; current host is %s/%s", hostOS, hostArch)
		}
	}
	return nil
}

func validateRemoteAndRef(label, remote, ref string) error {
	if remote == "" || ref == "" {
		return fmt.Errorf("%s repository and ref must not be empty", label)
	}
	for name, value := range map[string]string{"repository": remote, "ref": ref} {
		if value != strings.TrimSpace(value) || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s %s %q contains unsupported syntax", label, name, value)
		}
	}
	return nil
}

func validateEndpoint(value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("online endpoint %q contains unsupported syntax", value)
	}
	hostPort := strings.TrimPrefix(value, "tls://")
	if strings.ContainsAny(hostPort, " /\\\t\r\n@") || strings.Contains(hostPort, "://") || hostPort == "" {
		return fmt.Errorf("online endpoint %q must be [tls://]HOST[:PORT]", value)
	}
	host := hostPort
	if colon := strings.LastIndexByte(hostPort, ':'); colon >= 0 {
		host = hostPort[:colon]
		port, err := strconv.Atoi(hostPort[colon+1:])
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("online endpoint %q has an invalid port", value)
		}
	}
	if host == "" || strings.Contains(host, ":") {
		return fmt.Errorf("online endpoint %q must use a DNS name or IPv4 address", value)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return fmt.Errorf("online endpoint %q does not support IPv6 literals", value)
		}
		return nil
	}
	if strings.Count(host, ".") == 3 {
		numeric := true
		for _, character := range host {
			if (character < '0' || character > '9') && character != '.' {
				numeric = false
				break
			}
		}
		if numeric {
			return fmt.Errorf("online endpoint %q has an invalid IPv4 address", value)
		}
	}
	if len(host) > 253 {
		return fmt.Errorf("online endpoint %q has an oversized DNS name", value)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("online endpoint %q has an invalid DNS label", value)
		}
		for _, character := range label {
			if (character < 'A' || character > 'Z') &&
				(character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return fmt.Errorf("online endpoint %q has an invalid DNS character", value)
			}
		}
	}
	return nil
}

func addPathOverlapIssues(request BuildRequest, addIssue func(string, string)) {
	if request.AssetsDir == "" {
		return
	}
	checks := []struct {
		field string
		path  string
		text  string
	}{
		{field: "repoRoot", path: request.RepoRoot, text: "Source checkout must not overlap the retail asset tree."},
		{field: "cacheDir", path: request.CacheDir, text: "Builder cache must not overlap the retail asset tree."},
		{field: "steamCMDDir", path: request.SteamCMDDir, text: "SteamCMD cache must not overlap the retail asset tree."},
		{field: "appOutput", path: request.AppOutput, text: "macOS app output must not overlap the retail asset tree."},
		{field: "onlineServerSource", path: request.OnlineServerSource, text: "Online server source must not overlap the retail asset tree."},
	}
	if request.Output != "" {
		checks = append(checks, struct {
			field string
			path  string
			text  string
		}{field: "output", path: filepath.Dir(request.Output), text: "SFX output directory must not overlap the retail asset tree."})
	}
	for _, check := range checks {
		if check.path != "" && desktopPathsOverlap(request.AssetsDir, check.path) {
			addIssue(check.field, check.text)
		}
	}
}

func desktopPathsOverlap(first, second string) bool {
	first = resolveDesktopPath(first)
	second = resolveDesktopPath(second)
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(first, second) || within(second, first)
}

func resolveDesktopPath(path string) string {
	path = filepath.Clean(path)
	candidate := path
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return path
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func buildRequestArguments(request BuildRequest) []string {
	return []string{
		"--repo=" + request.RepoRoot,
		"--source-repo=" + request.SourceRepo,
		"--source-ref=" + request.SourceRef,
		"--target=" + request.Target,
		"--assets-dir=" + request.AssetsDir,
		"--steam-user=" + request.SteamUser,
		"--cache-dir=" + request.CacheDir,
		"--steamcmd-dir=" + request.SteamCMDDir,
		"--output=" + request.Output,
		"--app-output=" + request.AppOutput,
		"--install-deps=" + strconv.FormatBool(request.InstallDeps),
		"--accept-sdk-licenses=" + strconv.FormatBool(request.AcceptSDKLicenses),
		"--with-online-server=" + strconv.FormatBool(request.WithOnlineServer),
		"--online-server-source=" + request.OnlineServerSource,
		"--online-server-repo=" + request.OnlineServerRepo,
		"--online-server-ref=" + request.OnlineServerRef,
		"--online-endpoint=" + request.OnlineEndpoint,
		"--skip-assets=" + strconv.FormatBool(request.SkipAssets),
		"--skip-game-build=" + strconv.FormatBool(request.SkipGameBuild),
		"--dry-run=" + strconv.FormatBool(request.DryRun),
		"--non-interactive=" + strconv.FormatBool(request.NonInteractive),
		"--keep-windows-stage=" + strconv.FormatBool(request.KeepWindowsStage),
	}
}

func validationError(issues []ValidationIssue) error {
	count := 0
	first := ""
	for _, issue := range issues {
		if issue.Severity != "error" {
			continue
		}
		count++
		if first == "" {
			first = issue.Message
		}
	}
	if count == 0 {
		return nil
	}
	if count == 1 {
		return errors.New(first)
	}
	return fmt.Errorf("build request has %d errors; first: %s", count, first)
}
