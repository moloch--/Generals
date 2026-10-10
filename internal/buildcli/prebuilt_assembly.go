package buildcli

import (
	"bufio"
	"context"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

// GeneralsX @feature Codex 10/10/2026 Assemble released engines with owned assets without preparing a source checkout.
func (app application) runPrebuilt(ctx context.Context) error {
	if err := requireRegularFile(app.cfg.output); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("invalid SFX output: %w", err)
	}
	reportProgress(app.reporter, ProgressPhaseRuntimeResolve, "Resolving the published game runtime")
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] resolve %s runtime %s for %s; download and require SHA-256 verification\n", app.cfg.runtimeRepository, app.cfg.runtimeVersion, runtimeTarget(app.cfg.target))
		for _, phase := range []ProgressPhase{ProgressPhaseRuntimeDownload, ProgressPhaseRuntimeVerify, ProgressPhaseToolchain, ProgressPhaseAssets, ProgressPhaseAssemble, ProgressPhasePackage, ProgressPhaseVerifyOutput} {
			reportProgress(app.reporter, phase, "Planning prebuilt runtime assembly")
		}
		fmt.Fprintln(app.runner.stdout, "[dry-run] require Go 1.25+ for the local SFX launcher; macOS also requires Command Line Tools for the progress helper")
		fmt.Fprintf(app.runner.stdout, "[dry-run] stage owned assets from %s; overlay the verified runtime; package and run --sfx-verify\n", app.cfg.assetsDir)
		fmt.Fprintf(app.runner.stdout, "Dry run complete; planned output: %s\n", app.cfg.primaryArtifactPath())
		reportProgress(app.reporter, ProgressPhaseComplete, "Prebuilt assembly dry run complete")
		return nil
	}
	if err := app.validatePrebuiltAppOutput(ctx); err != nil {
		return err
	}
	descriptor, archive, err := app.resolveRuntime(ctx)
	if err != nil {
		return err
	}
	reportProgress(app.reporter, ProgressPhaseToolchain, "Checking local packaging tools (the engine is already built)")
	goPath, environment, err := app.bootstrapPackaging(ctx)
	if err != nil {
		return err
	}
	reportProgress(app.reporter, ProgressPhaseRuntimeVerify, "Verifying and extracting the runtime and matching packaging kit")
	work, err := os.MkdirTemp(app.cfg.cacheDir, ".prebuilt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	packageRoot := filepath.Join(work, "package")
	if err := descriptor.Extract(ctx, archive, packageRoot); err != nil {
		return err
	}
	reportProgress(app.reporter, ProgressPhaseAssets, "Preparing owned Zero Hour retail assets")
	if err := app.acquireAssets(ctx); err != nil {
		return err
	}
	reportProgress(app.reporter, ProgressPhaseAssemble, "Combining the verified runtime with owned assets")
	stage := filepath.Join(work, "assembled")
	kit := filepath.Join(packageRoot, "kit")
	profile := filepath.Join(kit, "scripts", "tooling", "sfx", "profiles", string(app.cfg.target)+"-zh.exclude")
	if err := assembleRuntime(ctx, app.cfg.assetsDir, packageRoot, stage, profile, descriptor); err != nil {
		return err
	}
	// Include provenance inside the authenticated local payload, where it follows the game.
	receipt, err := json.MarshalIndent(struct {
		Version         string `json:"version"`
		Repository      string `json:"repository"`
		Commit          string `json:"source_commit"`
		Target          string `json:"target"`
		SHA256          string `json:"runtime_sha256"`
		PackagingSchema int    `json:"packaging_schema"`
	}{descriptor.Version, descriptor.Repository, descriptor.SourceCommit, descriptor.Target, descriptor.Archive.SHA256, 1}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "generalsx-runtime.json"), append(receipt, '\n'), 0o600); err != nil {
		return err
	}
	if err := app.packagePrebuilt(ctx, goPath, environment, kit, stage, descriptor); err != nil {
		return err
	}
	fmt.Fprintf(app.runner.stdout, "\nBuild complete: %s\nRuntime: %s (%s)\n", app.cfg.primaryArtifactPath(), descriptor.Version, descriptor.SourceCommit)
	reportProgress(app.reporter, ProgressPhaseComplete, "Prebuilt runtime assembly complete")
	return nil
}

func (app application) bootstrapPackaging(ctx context.Context) (string, map[string]string, error) {
	environment := map[string]string{"GOENV": "off", "GOFLAGS": "", "GOEXPERIMENT": "", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOOS": app.hostOS, "GOARCH": app.hostArch, "CGO_ENABLED": "0"}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return "", nil, errors.New("Go 1.25 or newer is required to package the local SFX launcher; install Go and rerun (no engine compiler is required)")
	}
	version, err := app.runner.output(ctx, command{name: goPath, args: []string{"version"}, env: environment})
	if err != nil {
		return "", nil, err
	}
	fields := strings.Fields(version)
	if len(fields) < 3 || !versionAtLeast(strings.TrimPrefix(fields[2], "go"), "1.25") {
		return "", nil, errors.New("local SFX packaging requires Go 1.25 or newer")
	}
	if _, err := exec.LookPath("xz"); err != nil {
		fmt.Fprintln(app.runner.stdout, "xz is unavailable; local packaging will use the slower Go compressor.")
	}
	if app.hostOS == "darwin" {
		for _, name := range []string{"xcrun", "codesign", "otool", "lipo", "iconutil", "sips", "plutil", "xattr", "file"} {
			if _, err := exec.LookPath(name); err != nil {
				return "", nil, fmt.Errorf("macOS app packaging requires %s; install Xcode Command Line Tools before retrying", name)
			}
		}
		if _, err := app.runner.output(ctx, command{name: "xcrun", args: []string{"--find", "clang"}}); err != nil {
			return "", nil, errors.New("macOS progress-helper packaging requires Xcode Command Line Tools; run xcode-select --install and retry")
		}
	}
	if app.hostOS == "linux" && app.needsSteamCMD() && !hasLinux32Runtime() {
		if !app.cfg.installDeps {
			return "", nil, errors.New("SteamCMD needs a 32-bit Linux runtime; enable --install-deps or provide complete assets with --skip-assets")
		}
		if err := app.installLinuxPackages(ctx, []string{"steamcmd-runtime"}); err != nil {
			return "", nil, err
		}
		if !hasLinux32Runtime() {
			return "", nil, errors.New("the SteamCMD 32-bit runtime remains unavailable after installation")
		}
	}
	return goPath, environment, nil
}

func versionAtLeast(actual, required string) bool {
	parse := func(value string) ([]int, bool) {
		var result []int
		for _, part := range strings.Split(strings.TrimSpace(value), ".") {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return nil, false
			}
			result = append(result, n)
		}
		return result, len(result) > 0
	}
	a, ok := parse(actual)
	if !ok {
		return false
	}
	b, ok := parse(required)
	if !ok {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func (app application) validateRuntimePlatform(ctx context.Context, descriptor runtimebundle.Descriptor) error {
	if app.hostOS == "darwin" && descriptor.MinimumOS != "" {
		version, err := app.runner.output(ctx, command{name: "/usr/bin/sw_vers", args: []string{"-productVersion"}})
		if err != nil {
			return err
		}
		if !versionAtLeast(strings.TrimSpace(version), descriptor.MinimumOS) {
			return fmt.Errorf("runtime requires macOS %s or newer", descriptor.MinimumOS)
		}
	}
	if app.hostOS == "linux" && descriptor.MinimumGLIBC != "" {
		version, err := app.runner.output(ctx, command{name: "getconf", args: []string{"GNU_LIBC_VERSION"}})
		if err != nil {
			return fmt.Errorf("cannot check required glibc %s: %w", descriptor.MinimumGLIBC, err)
		}
		fields := strings.Fields(version)
		if len(fields) != 2 || fields[0] != "glibc" || !versionAtLeast(fields[1], descriptor.MinimumGLIBC) {
			return fmt.Errorf("runtime requires glibc %s or newer", descriptor.MinimumGLIBC)
		}
	}
	return validateWindowsRuntimeOS(descriptor.MinimumOS)
}

type assetExclusion struct {
	value   string
	subtree bool
}

func readAssetExclusions(name string) ([]assetExclusion, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var rules []assetExclusion
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "\\\x00") || strings.HasPrefix(line, "/") {
			return nil, errors.New("unsafe asset exclusion rule")
		}
		rule := assetExclusion{value: strings.TrimSuffix(line, "/"), subtree: strings.HasSuffix(line, "/")}
		if rule.value == "." || rule.value == ".." || strings.HasPrefix(rule.value, "../") {
			return nil, errors.New("unsafe asset exclusion rule")
		}
		if _, err := path.Match(rule.value, ""); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, scanner.Err()
}

func excludedAsset(name string, rules []assetExclusion) bool {
	for _, rule := range rules {
		if rule.subtree && (name == rule.value || strings.HasPrefix(name, rule.value+"/")) {
			return true
		}
		if !rule.subtree {
			if match, _ := path.Match(rule.value, name); match {
				return true
			}
		}
	}
	return false
}

// Apply exclusions to retail inputs before overlaying runtime files. Runtime libraries
// and configuration must not accidentally be removed by retail exclusion profiles.
func assembleRuntime(ctx context.Context, assets, packageRoot, stage, profile string, descriptor runtimebundle.Descriptor) error {
	if err := descriptor.VerifyDirectory(packageRoot); err != nil {
		return err
	}
	rules, err := readAssetExclusions(profile)
	if err != nil {
		return err
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	runtimeFiles := map[string]bool{}
	runtimeDirectories := map[string]string{}
	for _, entry := range descriptor.Files {
		if strings.HasPrefix(entry.Path, "runtime/") {
			relative := strings.TrimPrefix(entry.Path, "runtime/")
			runtimeFiles[strings.ToLower(relative)] = true
			for directory := path.Dir(relative); directory != "."; directory = path.Dir(directory) {
				runtimeDirectories[strings.ToLower(directory)] = directory
			}
		}
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(assets, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(assets, source)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		folded := strings.ToLower(relative)
		if seen[folded] {
			return fmt.Errorf("case-colliding retail asset: %s", relative)
		}
		seen[folded] = true
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("prebuilt asset staging does not accept symlinks: %s", relative)
		}
		root, _, _ := strings.Cut(folded, "/")
		if root == "online-server" || root == "generalsx-runtime.json" || root == "lib" || root == "kit" || root == "fontconfig" {
			return fmt.Errorf("retail assets contain reserved runtime path %s", relative)
		}
		if excludedAsset(relative, rules) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if runtimeFiles[folded] {
			if entry.IsDir() {
				return fmt.Errorf("directory shadows runtime file %s", relative)
			}
			return nil
		}
		// GeneralsX @bugfix Codex 10/10/2026 Match retail ancestor casing to the
		// runtime overlay so Linux cannot create Window/ and window/ siblings.
		if !entry.IsDir() && runtimeDirectories[folded] != "" {
			return fmt.Errorf("retail file shadows runtime directory %s", relative)
		}
		ancestor := relative
		if !entry.IsDir() {
			ancestor = path.Dir(relative)
		}
		for ; ancestor != "."; ancestor = path.Dir(ancestor) {
			if canonical := runtimeDirectories[strings.ToLower(ancestor)]; canonical != "" {
				relative = canonical + strings.TrimPrefix(relative, ancestor)
				break
			}
		}
		if descriptor.Target == "windows-x86" && !strings.Contains(relative, "/") {
			ext := strings.ToLower(filepath.Ext(relative))
			if (ext == ".dll" || ext == ".exe") && folded != "binkw32.dll" && folded != "mss32.dll" {
				return nil
			}
			if folded == "binkw32.dll" || folded == "mss32.dll" {
				file, err := pe.Open(source)
				if err != nil {
					return fmt.Errorf("invalid retail x86 DLL %s: %w", relative, err)
				}
				machine := file.Machine
				_ = file.Close()
				if machine != pe.IMAGE_FILE_MACHINE_I386 {
					return fmt.Errorf("retail DLL %s must be x86", relative)
				}
				relative = folded
			}
		}
		destination := filepath.Join(stage, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		return copyAssemblyFile(ctx, source, destination, 0o600)
	})
	if err != nil {
		return err
	}
	for _, entry := range descriptor.Files {
		if !strings.HasPrefix(entry.Path, "runtime/") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative := strings.TrimPrefix(entry.Path, "runtime/")
		destination := filepath.Join(stage, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if entry.LinkTarget != "" {
			if err := os.Symlink(filepath.FromSlash(entry.LinkTarget), destination); err != nil {
				return err
			}
		} else if err := copyAssemblyFile(ctx, filepath.Join(packageRoot, filepath.FromSlash(entry.Path)), destination, os.FileMode(entry.Mode)); err != nil {
			return err
		}
	}
	if descriptor.Target == "windows-x86" {
		return runtimebundle.ValidateRetailWindowsLibraries(stage)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

func copyAssemblyFile(ctx context.Context, source, destination string, mode os.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("non-regular assembly input: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("assembly input changed while opening")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(output, contextReader{ctx, input})
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	after, err := input.Stat()
	if err != nil {
		return err
	}
	if n != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return fmt.Errorf("assembly input changed while copying: %s", source)
	}
	return os.Chmod(destination, mode.Perm())
}

func (app application) packagePrebuilt(ctx context.Context, goPath string, environment map[string]string, kit, stage string, descriptor runtimebundle.Descriptor) error {
	if err := os.MkdirAll(filepath.Dir(app.cfg.output), 0o755); err != nil {
		return err
	}
	outputParent, err := filepath.EvalSymlinks(filepath.Dir(app.cfg.output))
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(outputParent, ".prebuilt-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	outputName := "game-sfx"
	entry := "GeneralsXZH"
	if app.cfg.target == targetWindows {
		outputName += ".exe"
		entry = "generalszh.exe"
	}
	temporary := filepath.Join(work, outputName)
	module := filepath.Join(kit, "scripts", "tooling", "sfx")
	targetOS, targetArch := serverTarget(app.cfg.target)
	reportProgress(app.reporter, ProgressPhasePackage, "Compressing local assets and building the SFX launcher")
	if err := app.runner.run(ctx, command{name: goPath, args: []string{"-C", module, "run", "-mod=readonly", "./cmd/generalsx-sfx-pack", "-source", stage, "-output", temporary, "-target", targetOS + "/" + targetArch, "-entry", entry, "-workdir", ".", "-product", "GeneralsXZH", "-version", descriptor.Version, "-module", module, "-compression", "xz", "-max-embed-bytes", "1900000000"}, env: environment}); err != nil {
		return fmt.Errorf("package downloaded runtime: %w", err)
	}
	if app.cfg.target == targetMacOS {
		if err := app.runner.run(ctx, command{name: "codesign", args: []string{"--force", "--sign", "-", "--identifier", "com.generalsx.generalsxzh.sfx", "--timestamp=none", temporary}}); err != nil {
			return err
		}
	}
	reportProgress(app.reporter, ProgressPhaseVerifyOutput, "Verifying the complete locally assembled payload")
	if err := app.runner.run(ctx, command{name: temporary, args: []string{"--sfx-verify"}}); err != nil {
		return fmt.Errorf("verify assembled SFX: %w", err)
	}
	outputs := []prebuiltOutput{{staged: temporary, target: app.cfg.output}}
	if app.cfg.target == targetMacOS {
		if err := os.MkdirAll(filepath.Dir(app.cfg.appOutput), 0o755); err != nil {
			return err
		}
		appWork, err := os.MkdirTemp(filepath.Dir(app.cfg.appOutput), ".prebuilt-app-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(appWork)
		stagedApp := filepath.Join(appWork, filepath.Base(app.cfg.appOutput))
		appEnvironment := cloneStringMap(environment)
		appEnvironment["GX_SFX_APP_INPUT"] = temporary
		appEnvironment["GX_SFX_APP_OUTPUT"] = stagedApp
		appEnvironment["GX_SFX_APP_VERSION"] = strings.Split(strings.TrimPrefix(descriptor.Version, "runtime-v"), "-")[0]
		appEnvironment["GX_SFX_APP_BUILD_VERSION"] = "1"
		appEnvironment["GX_SFX_APP_CODESIGN_IDENTITY"] = "-"
		appEnvironment["GX_SFX_APP_ICON_SOURCE"] = filepath.Join(kit, "assets", "generalsx-zh_icon.png")
		if err := app.runner.run(ctx, command{name: "/bin/bash", args: []string{filepath.Join(kit, "scripts", "build", "macos", "package-sfx-macos-zh-app.sh")}, env: appEnvironment}); err != nil {
			return err
		}
		outputs = append(outputs, prebuiltOutput{staged: stagedApp, target: app.cfg.appOutput, directory: true})
	}
	if err := app.validatePrebuiltAppOutput(ctx); err != nil {
		return err
	}
	return publishPrebuiltOutputs(ctx, outputs, os.Rename)
}

func (app application) validatePrebuiltAppOutput(ctx context.Context) error {
	if app.cfg.target != targetMacOS {
		return nil
	}
	return validateExistingGameApp(app.cfg.appOutput, func(plist string) (string, error) {
		return app.runner.output(ctx, command{name: "/usr/bin/plutil", args: []string{"-extract", "CFBundleIdentifier", "raw", "-o", "-", plist}})
	})
}
