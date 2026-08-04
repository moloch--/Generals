// Package buildcli implements the GeneralsX host bootstrap and SFX build command.
package buildcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	zeroHourSteamAppID = "2732960"
	defaultSourceRepo  = "https://github.com/moloch--/Generals.git"
	defaultServerRepo  = "https://github.com/moloch--/generals-server.git"
)

type target string

const (
	targetMacOS   target = "macos"
	targetLinux   target = "linux"
	targetWindows target = "windows"
)

type config struct {
	repoRoot           string
	sourceRepo         string
	sourceRef          string
	target             target
	assetsDir          string
	steamUser          string
	steamCMDDir        string
	cacheDir           string
	output             string
	appOutput          string
	installDeps        bool
	acceptSDKLicenses  bool
	withOnlineServer   bool
	onlineServerSource string
	onlineServerRepo   string
	onlineServerRef    string
	onlineEndpoint     string
	skipAssets         bool
	skipGameBuild      bool
	dryRun             bool
	nonInteractive     bool
	keepWindowsStage   bool
}

// GeneralsX @build Codex 04/08/2026 Keep automation flags explicit and non-secret.
func parseConfig(arguments []string, stderr io.Writer) (config, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return config{}, fmt.Errorf("resolve working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return config{}, fmt.Errorf("resolve user home: %w", err)
	}
	userCache, err := os.UserCacheDir()
	if err != nil {
		return config{}, fmt.Errorf("resolve user cache: %w", err)
	}

	var cfg config
	var requestedTarget string
	flags := flag.NewFlagSet("generalsx-build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultRepoRoot := workingDirectory
	if validateRepository(workingDirectory) != nil {
		defaultRepoRoot = filepath.Join(home, "GeneralsX", "source")
	}
	flags.StringVar(&cfg.repoRoot, "repo", defaultRepoRoot, "existing checkout or destination for an automatic clone")
	flags.StringVar(&cfg.sourceRepo, "source-repo", defaultSourceRepo, "GeneralsX Git URL used when --repo does not exist")
	flags.StringVar(&cfg.sourceRef, "source-ref", "main", "game source branch, tag, or commit to check out after cloning")
	flags.StringVar(&requestedTarget, "target", "auto", "target: auto, macos, linux, or windows")
	flags.StringVar(&cfg.assetsDir, "assets-dir", filepath.Join(home, "GeneralsX", "GeneralsZH"), "owned Zero Hour retail-data directory")
	flags.StringVar(&cfg.steamUser, "steam-user", os.Getenv("STEAM_USER"), "Steam account name; SteamCMD prompts for secrets")
	flags.StringVar(&cfg.cacheDir, "cache-dir", filepath.Join(userCache, "GeneralsX", "builder"), "private dependency/download cache")
	flags.StringVar(&cfg.steamCMDDir, "steamcmd-dir", "", "SteamCMD installation directory (default: CACHE/steamcmd)")
	flags.StringVar(&cfg.output, "output", "", "raw SFX output path")
	flags.StringVar(&cfg.appOutput, "app-output", "", "macOS .app output path")
	flags.BoolVar(&cfg.installDeps, "install-deps", true, "install missing host build dependencies")
	flags.BoolVar(&cfg.acceptSDKLicenses, "accept-sdk-licenses", false, "accept required SDK/tool licenses during automatic installation")
	flags.BoolVar(&cfg.withOnlineServer, "with-online-server", false, "embed a target-native generals-server sidecar")
	flags.StringVar(&cfg.onlineServerSource, "online-server-source", "", "existing generals-server checkout (default: ./generals-server or managed clone)")
	flags.StringVar(&cfg.onlineServerRepo, "online-server-repo", defaultServerRepo, "server Git URL used when no local checkout exists")
	flags.StringVar(&cfg.onlineServerRef, "online-server-ref", "main", "server Git ref used for a managed clone")
	flags.StringVar(&cfg.onlineEndpoint, "online-endpoint", "", "default Online endpoint compiled into the game")
	flags.BoolVar(&cfg.skipAssets, "skip-assets", false, "do not run SteamCMD; require an existing asset tree")
	flags.BoolVar(&cfg.skipGameBuild, "skip-game-build", false, "reuse the current native game build")
	flags.BoolVar(&cfg.dryRun, "dry-run", false, "print planned external actions without changing the host")
	flags.BoolVar(&cfg.nonInteractive, "non-interactive", false, "fail instead of requesting Steam Guard or installer interaction")
	flags.BoolVar(&cfg.keepWindowsStage, "keep-windows-stage", false, "retain the generated Windows runtime stage for diagnosis")
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	resolvedTarget, err := parseTarget(requestedTarget, runtime.GOOS)
	if err != nil {
		return config{}, err
	}
	cfg.target = resolvedTarget
	if cfg.steamCMDDir == "" {
		cfg.steamCMDDir = filepath.Join(cfg.cacheDir, "steamcmd")
	}
	if cfg.output == "" {
		cfg.output = defaultOutput(cfg.repoRoot, cfg.target)
	}
	if cfg.appOutput == "" && cfg.target == targetMacOS {
		cfg.appOutput = filepath.Join(cfg.repoRoot, "build", "sfx", "GeneralsXZH.app")
	}
	if cfg.withOnlineServer && cfg.onlineEndpoint == "" {
		cfg.onlineEndpoint = "127.0.0.1:29900"
	}

	for label, value := range map[string]string{
		"repository root":    cfg.repoRoot,
		"asset directory":    cfg.assetsDir,
		"cache directory":    cfg.cacheDir,
		"SteamCMD directory": cfg.steamCMDDir,
		"SFX output":         cfg.output,
	} {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return config{}, fmt.Errorf("resolve %s: %w", label, err)
		}
		switch label {
		case "repository root":
			cfg.repoRoot = absolute
		case "asset directory":
			cfg.assetsDir = absolute
		case "cache directory":
			cfg.cacheDir = absolute
		case "SteamCMD directory":
			cfg.steamCMDDir = absolute
		case "SFX output":
			cfg.output = absolute
		}
	}
	if cfg.appOutput != "" {
		cfg.appOutput, err = filepath.Abs(cfg.appOutput)
		if err != nil {
			return config{}, fmt.Errorf("resolve app output: %w", err)
		}
	}
	if cfg.onlineServerSource != "" {
		cfg.onlineServerSource, err = filepath.Abs(cfg.onlineServerSource)
		if err != nil {
			return config{}, fmt.Errorf("resolve Online server source: %w", err)
		}
	}
	if err := validateConfig(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func parseTarget(value, hostOS string) (target, error) {
	if value == "auto" {
		switch hostOS {
		case "darwin":
			return targetMacOS, nil
		case "linux":
			return targetLinux, nil
		case "windows":
			return targetWindows, nil
		default:
			return "", fmt.Errorf("host OS %q has no automatic target", hostOS)
		}
	}
	switch target(value) {
	case targetMacOS, targetLinux, targetWindows:
		return target(value), nil
	default:
		return "", fmt.Errorf("unsupported target %q; use auto, macos, linux, or windows", value)
	}
}

func defaultOutput(repoRoot string, targetOS target) string {
	name := "GeneralsXZH-linux-amd64-sfx"
	switch targetOS {
	case targetMacOS:
		name = "GeneralsXZH-macos-arm64-sfx"
	case targetWindows:
		name = "GeneralsXZH-windows-amd64-sfx.exe"
	}
	return filepath.Join(repoRoot, "build", "sfx", name)
}

func validateConfig(cfg config) error {
	if cfg.repoRoot == "" || cfg.assetsDir == "" || cfg.cacheDir == "" || cfg.output == "" {
		return errors.New("repository, assets, cache, and output paths must not be empty")
	}
	if cfg.target == targetMacOS && !strings.HasSuffix(strings.ToLower(cfg.appOutput), ".app") {
		return errors.New("macOS app output must end in .app")
	}
	if cfg.target == targetWindows && !strings.HasSuffix(strings.ToLower(cfg.output), ".exe") {
		return errors.New("Windows SFX output must end in .exe")
	}
	if cfg.skipGameBuild && cfg.onlineEndpoint != "" {
		return errors.New("--skip-game-build cannot compile --online-endpoint or the bundled server's loopback endpoint")
	}
	if cfg.onlineEndpoint != "" {
		if err := validateOnlineEndpoint(cfg.onlineEndpoint); err != nil {
			return err
		}
	}
	if cfg.withOnlineServer && cfg.onlineServerRepo == "" && cfg.onlineServerSource == "" {
		return errors.New("Online server repository or source must not be empty")
	}
	if cfg.sourceRepo == "" || cfg.sourceRef == "" {
		return errors.New("source repository and ref must not be empty")
	}
	if err := validateGitRemoteAndRef("source", cfg.sourceRepo, cfg.sourceRef); err != nil {
		return err
	}
	if err := validateGitRemoteAndRef("Online server", cfg.onlineServerRepo, cfg.onlineServerRef); err != nil {
		return err
	}
	if pathsOverlap(cfg.assetsDir, filepath.Dir(cfg.output)) {
		return errors.New("SFX output directory must not overlap the retail asset tree")
	}
	if pathsOverlap(cfg.assetsDir, cfg.repoRoot) {
		return errors.New("source checkout must not overlap the retail asset tree")
	}
	if pathsOverlap(cfg.assetsDir, cfg.cacheDir) || pathsOverlap(cfg.assetsDir, cfg.steamCMDDir) {
		return errors.New("builder and SteamCMD caches must not overlap the retail asset tree")
	}
	if cfg.appOutput != "" && pathsOverlap(cfg.assetsDir, cfg.appOutput) {
		return errors.New("macOS app output must not overlap the retail asset tree")
	}
	if cfg.onlineServerSource != "" && pathsOverlap(cfg.assetsDir, cfg.onlineServerSource) {
		return errors.New("Online server source must not overlap the retail asset tree")
	}
	return nil
}

func validateGitRemoteAndRef(label, remote, ref string) error {
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

func validateOnlineEndpoint(value string) error {
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
		port := hostPort[colon+1:]
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
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

func pathsOverlap(first, second string) bool {
	first = resolveForPathComparison(first)
	second = resolveForPathComparison(second)
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(first, second) || within(second, first)
}

func resolveForPathComparison(path string) string {
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
