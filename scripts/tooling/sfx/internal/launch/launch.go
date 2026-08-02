// Package launch prepares the native GeneralsX process after an SFX payload has
// been extracted.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	TargetDarwin  = "darwin"
	TargetLinux   = "linux"
	TargetWindows = "windows"
)

// Config describes an extracted payload and the native process to launch.
//
// Root is the extracted retail/runtime root. Entrypoint and WorkDir use
// slash-separated paths relative to Root, and an empty WorkDir means Root.
// RuntimeStateDir is an optional writable directory outside Root; when empty,
// Prepare creates a private, content-specific sibling. When Env is nil,
// Prepare inherits os.Environ. A non-nil Env, including an empty slice, is
// used as the complete base environment. Context controls the child lifetime;
// when nil, context.Background is used. On macOS and Linux, the native process
// runs from RuntimeStateDir while asset and library lookups remain rooted in
// the immutable payload; this contains legacy relative writes. Windows keeps
// the payload working directory for native asset and DLL compatibility but
// receives RuntimeStateDir through explicit writable-state variables.
type Config struct {
	Root            string
	RuntimeStateDir string
	TargetOS        string
	Entrypoint      string
	WorkDir         string
	Args            []string
	Env             []string
	Context         context.Context
}

// GeneralsX @feature Codex 30/07/2026 Prepare a path-safe native launch without a shell.
// Prepare validates the payload paths, builds the platform-specific
// environment, and returns a command with inherited standard streams.
func Prepare(config Config) (*exec.Cmd, error) {
	if err := validateTarget(config.TargetOS); err != nil {
		return nil, err
	}

	root, err := resolveRoot(config.Root)
	if err != nil {
		return nil, err
	}

	workDirPath := config.WorkDir
	if workDirPath == "" {
		workDirPath = "."
	}

	workDir, err := resolveRequiredPath(root, workDirPath, "work directory", true)
	if err != nil {
		return nil, err
	}

	executable, err := resolveRequiredPath(root, config.Entrypoint, "entrypoint", false)
	if err != nil {
		return nil, err
	}
	runtimeStateDir, err := resolveRuntimeStateDirectory(root, config.RuntimeStateDir)
	if err != nil {
		return nil, err
	}

	baseEnvironment := config.Env
	if baseEnvironment == nil {
		baseEnvironment = os.Environ()
	}

	environment, err := newEnvironment(baseEnvironment, config.TargetOS == TargetWindows)
	if err != nil {
		return nil, fmt.Errorf("prepare environment: %w", err)
	}

	if err = configureAssetPaths(environment, root); err != nil {
		return nil, err
	}

	switch config.TargetOS {
	case TargetDarwin:
		err = configureDarwin(environment, root, workDir, runtimeStateDir)
	case TargetLinux:
		err = configureLinux(environment, root, workDir, runtimeStateDir)
	case TargetWindows:
		err = configureWindows(environment, root, workDir, runtimeStateDir)
	}
	if err != nil {
		return nil, err
	}
	// GeneralsX @bugfix Codex 02/08/2026 Expose the stable writable-state directory on every packaged platform.
	environment.set("GENERALSX_SFX_RUNTIME_STATE", runtimeStateDir)

	nativeWorkDir := workDir
	if config.TargetOS == TargetDarwin || config.TargetOS == TargetLinux {
		nativeWorkDir = runtimeStateDir
		environment.set("PWD", runtimeStateDir)
	}
	arguments := append([]string(nil), config.Args...)
	commandContext := config.Context
	if commandContext == nil {
		commandContext = context.Background()
	}
	command := exec.CommandContext(commandContext, executable, arguments...)
	configureProcessGroup(command)
	command.Cancel = func() error {
		return terminateProcess(command.Process, commandContext)
	}
	command.WaitDelay = 5 * time.Second
	command.Dir = nativeWorkDir
	command.Env = environment.entriesCopy()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	return command, nil
}

// GeneralsX @feature Codex 30/07/2026 Preserve native child exit statuses in the SFX wrapper.
// ExitCode converts an exec.Cmd result into a process exit code. Wrapper-side
// failures and processes without a portable status map to 1. On Unix, signal
// termination maps to the conventional 128+signal status.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if code := exitError.ExitCode(); code >= 0 {
			return code
		}
		if code, ok := platformSignalExitCode(exitError); ok {
			return code
		}
	}

	return 1
}

func validateTarget(targetOS string) error {
	switch targetOS {
	case TargetDarwin, TargetLinux, TargetWindows:
		return nil
	default:
		return fmt.Errorf("unsupported target OS %q", targetOS)
	}
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("payload root is empty")
	}
	if strings.IndexByte(root, 0) >= 0 {
		return "", errors.New("payload root contains a NUL byte")
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve payload root: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve payload root %q: %w", absolute, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat payload root %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("payload root %q is not a directory", resolved)
	}

	return filepath.Clean(resolved), nil
}

func resolveRequiredPath(root, relativePath, label string, requireDirectory bool) (string, error) {
	cleaned, err := cleanPayloadPath(relativePath, label, !requireDirectory)
	if err != nil {
		return "", err
	}

	candidate := filepath.Join(root, filepath.FromSlash(cleaned))
	if !isWithin(root, candidate) {
		return "", fmt.Errorf("%s %q escapes payload root", label, relativePath)
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", label, relativePath, err)
	}
	if !isWithin(root, resolved) {
		return "", fmt.Errorf("%s %q resolves outside payload root", label, relativePath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %s %q: %w", label, relativePath, err)
	}
	if requireDirectory && !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory", label, relativePath)
	}
	if !requireDirectory && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s %q is not a regular file", label, relativePath)
	}

	return filepath.Clean(resolved), nil
}

func cleanPayloadPath(relativePath, label string, rejectDot bool) (string, error) {
	if relativePath == "" {
		return "", fmt.Errorf("%s is empty", label)
	}
	if strings.IndexByte(relativePath, 0) >= 0 {
		return "", fmt.Errorf("%s contains a NUL byte", label)
	}
	if strings.Contains(relativePath, `\`) {
		return "", fmt.Errorf("%s %q must use slash separators", label, relativePath)
	}
	if strings.Contains(relativePath, ":") {
		return "", fmt.Errorf("%s %q contains a volume or alternate stream separator", label, relativePath)
	}
	if path.IsAbs(relativePath) {
		return "", fmt.Errorf("%s %q must be relative", label, relativePath)
	}

	cleaned := path.Clean(relativePath)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%s %q escapes payload root", label, relativePath)
	}
	if rejectDot && cleaned == "." {
		return "", fmt.Errorf("%s %q does not name a file", label, relativePath)
	}

	return cleaned, nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolveRuntimeStateDirectory(root, value string) (string, error) {
	if value == "" {
		value = filepath.Join(filepath.Dir(root), ".runtime-state", filepath.Base(root))
		for _, directory := range []string{filepath.Dir(value), value} {
			if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create runtime state directory %q: %w", directory, err)
			}
			info, err := os.Lstat(directory)
			if err != nil {
				return "", fmt.Errorf("inspect runtime state directory %q: %w", directory, err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("runtime state path %q is not a real directory", directory)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				return "", fmt.Errorf("make runtime state directory private: %w", err)
			}
		}
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("runtime state directory contains a NUL byte")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve runtime state directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve runtime state directory %q: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat runtime state directory %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runtime state directory %q is not a directory", resolved)
	}
	if isWithin(root, resolved) {
		return "", fmt.Errorf("runtime state directory %q is inside the immutable payload root", resolved)
	}
	return filepath.Clean(resolved), nil
}

// GeneralsX @feature Codex 30/07/2026 Launch staged runtimes with explicit retail and library roots.
func configureAssetPaths(environment *environment, root string) error {
	environment.setDefault("CNC_GENERALS_ZH_PATH", root)
	environment.setDefault("GENERALSX_ASSET_PATH", root)
	environment.setDefault("CNC_ZH_INSTALLPATH", root)

	baseGenerals, present, err := optionalPayloadDirectory(root, root, "ZH_Generals")
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("CNC_GENERALS_PATH", baseGenerals)
		environment.setDefault("GENERALSX_GENERALS_ASSET_PATH", baseGenerals)
		environment.setDefault("CNC_GENERALS_INSTALLPATH", baseGenerals)
	}

	return nil
}

func configureRuntimeSearchPath(environment *environment, root, workDir, key, separator string) error {
	environment.prependList(key, workDir, separator)

	libraryDir, present, err := optionalPayloadDirectory(root, root, "lib")
	if err != nil {
		return err
	}
	if present {
		environment.prependList(key, libraryDir, separator)
	}

	return nil
}

func configureDarwin(environment *environment, root, workDir, runtimeStateDir string) error {
	if err := configurePOSIXUserDirectories(environment, runtimeStateDir, false); err != nil {
		return err
	}
	if err := configureRuntimeSearchPath(environment, root, workDir, "DYLD_LIBRARY_PATH", ":"); err != nil {
		return err
	}
	environment.setDefault("DXVK_WSI_DRIVER", "SDL3")
	environment.setDefault("DXVK_HUD", "0")
	configureDXVKWritableState(environment, runtimeStateDir)

	sagePatch, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: filepath.Join(root, "lib"), relativePath: "libsage_patch.dylib"},
		payloadFileLocation{baseDir: root, relativePath: "libsage_patch.dylib"},
		payloadFileLocation{baseDir: workDir, relativePath: "libsage_patch.dylib"},
	)
	if err != nil {
		return err
	}
	if present && environment.value("SAGE_PATCH_DISABLED") != "1" {
		environment.prependList("DYLD_INSERT_LIBRARIES", sagePatch, ":")
	}

	moltenVKManifest, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: root, relativePath: "MoltenVK_icd.json"},
		payloadFileLocation{baseDir: filepath.Join(root, "lib"), relativePath: "MoltenVK_icd.json"},
		payloadFileLocation{baseDir: workDir, relativePath: "MoltenVK_icd.json"},
	)
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("VK_ICD_FILENAMES", moltenVKManifest)
		environment.setDefault("VK_DRIVER_FILES", moltenVKManifest)
	}

	dxvkConfig, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: root, relativePath: "dxvk.conf"},
		payloadFileLocation{baseDir: workDir, relativePath: "dxvk.conf"},
	)
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("DXVK_CONFIG_FILE", dxvkConfig)
	}

	fontConfig, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: root, relativePath: "fontconfig/fonts.conf"},
		payloadFileLocation{baseDir: workDir, relativePath: "fontconfig/fonts.conf"},
	)
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("FONTCONFIG_FILE", fontConfig)
		environment.setDefault("FONTCONFIG_PATH", filepath.Dir(fontConfig))
	}

	return nil
}

func configureLinux(environment *environment, root, workDir, runtimeStateDir string) error {
	if err := configurePOSIXUserDirectories(environment, runtimeStateDir, true); err != nil {
		return err
	}
	if err := configureRuntimeSearchPath(environment, root, workDir, "LD_LIBRARY_PATH", ":"); err != nil {
		return err
	}
	environment.setDefault("DXVK_WSI_DRIVER", "SDL3")
	environment.setDefault("DXVK_LOG_LEVEL", "info")
	environment.setDefault("ALSOFT_DISABLE_CPU_EXTS", "all")
	environment.setDefault("ALSOFT_DRIVERS", "pulse,alsa,oss,jack,null,wave")
	configureDXVKWritableState(environment, runtimeStateDir)
	configureLinuxVulkanICDs(environment, []string{
		"/usr/share/vulkan/icd.d",
		"/etc/vulkan/icd.d",
	})

	sagePatch, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: filepath.Join(root, "lib"), relativePath: "libsage_patch.so"},
		payloadFileLocation{baseDir: root, relativePath: "libsage_patch.so"},
		payloadFileLocation{baseDir: workDir, relativePath: "libsage_patch.so"},
	)
	if err != nil {
		return err
	}
	if present && environment.value("SAGE_PATCH_DISABLED") != "1" {
		environment.prependList("LD_PRELOAD", sagePatch, ":")
		environment.setDefault("DXVK_HUD", "fps")
	} else {
		environment.setDefault("DXVK_HUD", "0")
	}

	dxvkConfig, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: root, relativePath: "dxvk.conf"},
		payloadFileLocation{baseDir: workDir, relativePath: "dxvk.conf"},
	)
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("DXVK_CONFIG_FILE", dxvkConfig)
	}

	return nil
}

func configurePOSIXUserDirectories(
	environment *environment,
	runtimeStateDir string,
	configureXDG bool,
) error {
	home := environment.value("HOME")
	if home == "" {
		home = filepath.Join(runtimeStateDir, "home")
		if err := ensurePrivateRuntimeDirectory(home); err != nil {
			return fmt.Errorf("prepare fallback home directory: %w", err)
		}
		environment.set("HOME", home)
	}
	if configureXDG {
		environment.setDefault("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		environment.setDefault("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		environment.setDefault("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	}
	return nil
}

func ensurePrivateRuntimeDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", directory)
	}
	return os.Chmod(directory, 0o700)
}

// GeneralsX @bugfix moloch 30/07/2026 Preserve the Linux launcher's LLVMpipe crash workaround.
func configureLinuxVulkanICDs(environment *environment, directories []string) {
	if environment.value("VK_DRIVER_FILES") != "" || environment.value("VK_ICD_FILENAMES") != "" {
		return
	}

	seen := make(map[string]struct{})
	var hardwareICDs []string
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			lowerName := strings.ToLower(name)
			if filepath.Ext(lowerName) != ".json" ||
				strings.Contains(lowerName, "lvp") ||
				strings.Contains(lowerName, "lavapipe") ||
				strings.Contains(lowerName, "softpipe") ||
				strings.Contains(lowerName, "llvmpipe") {
				continue
			}
			candidate := filepath.Join(directory, name)
			info, err := os.Stat(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			candidate = filepath.Clean(candidate)
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			seen[candidate] = struct{}{}
			hardwareICDs = append(hardwareICDs, candidate)
		}
	}
	if len(hardwareICDs) != 0 {
		environment.set("VK_DRIVER_FILES", strings.Join(hardwareICDs, ":"))
	}
}

// GeneralsX @bugfix Codex 02/08/2026 Pin Windows DXVK configuration to the verified payload.
func configureWindows(environment *environment, root, workDir, runtimeStateDir string) error {
	configureDXVKWritableState(environment, runtimeStateDir)
	dxvkConfig, present, err := firstOptionalPayloadFile(root,
		payloadFileLocation{baseDir: root, relativePath: "dxvk.conf"},
		payloadFileLocation{baseDir: workDir, relativePath: "dxvk.conf"},
	)
	if err != nil {
		return err
	}
	if present {
		environment.setDefault("DXVK_CONFIG_FILE", dxvkConfig)
	}
	return configureRuntimeSearchPath(environment, root, workDir, "PATH", ";")
}

// GeneralsX @bugfix moloch 30/07/2026 Keep DXVK caches and logs outside immutable extracted payloads.
func configureDXVKWritableState(environment *environment, runtimeStateDir string) {
	environment.set("DXVK_STATE_CACHE_PATH", runtimeStateDir)
	environment.set("DXVK_LOG_PATH", runtimeStateDir)
}

type payloadFileLocation struct {
	baseDir      string
	relativePath string
}

func firstOptionalPayloadFile(root string, locations ...payloadFileLocation) (string, bool, error) {
	seen := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		candidate := filepath.Clean(filepath.Join(location.baseDir, filepath.FromSlash(location.relativePath)))
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}

		resolved, present, err := optionalPayloadFile(root, location.baseDir, location.relativePath)
		if err != nil {
			return "", false, err
		}
		if present {
			return resolved, true, nil
		}
	}

	return "", false, nil
}

func optionalPayloadFile(root, workDir, relativePath string) (string, bool, error) {
	candidate := filepath.Join(workDir, filepath.FromSlash(relativePath))
	if !isWithin(root, candidate) {
		return "", false, fmt.Errorf("optional payload file %q escapes payload root", relativePath)
	}

	_, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect optional payload file %q: %w", relativePath, err)
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false, fmt.Errorf("resolve optional payload file %q: %w", relativePath, err)
	}
	if !isWithin(root, resolved) {
		return "", false, fmt.Errorf("optional payload file %q resolves outside payload root", relativePath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("stat optional payload file %q: %w", relativePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("optional payload file %q is not a regular file", relativePath)
	}

	return filepath.Clean(resolved), true, nil
}

func optionalPayloadDirectory(root, baseDir, relativePath string) (string, bool, error) {
	candidate := filepath.Join(baseDir, filepath.FromSlash(relativePath))
	if !isWithin(root, candidate) {
		return "", false, fmt.Errorf("optional payload directory %q escapes payload root", relativePath)
	}

	_, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect optional payload directory %q: %w", relativePath, err)
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false, fmt.Errorf("resolve optional payload directory %q: %w", relativePath, err)
	}
	if !isWithin(root, resolved) {
		return "", false, fmt.Errorf("optional payload directory %q resolves outside payload root", relativePath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("stat optional payload directory %q: %w", relativePath, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("optional payload directory %q is not a directory", relativePath)
	}

	return filepath.Clean(resolved), true, nil
}

type environment struct {
	caseInsensitive bool
	entries         []environmentEntry
	positions       map[string]int
}

type environmentEntry struct {
	key   string
	value string
}

func newEnvironment(entries []string, caseInsensitive bool) (*environment, error) {
	result := &environment{
		caseInsensitive: caseInsensitive,
		positions:       make(map[string]int, len(entries)),
	}

	for _, entry := range entries {
		key, value, err := splitEnvironmentEntry(entry, caseInsensitive)
		if err != nil {
			return nil, err
		}
		result.set(key, value)
	}

	return result, nil
}

func splitEnvironmentEntry(entry string, allowWindowsPseudoVariable bool) (string, string, error) {
	separator := strings.IndexByte(entry, '=')
	if separator == 0 && allowWindowsPseudoVariable {
		if next := strings.IndexByte(entry[1:], '='); next >= 0 {
			separator = next + 1
		}
	}
	if separator <= 0 {
		return "", "", fmt.Errorf("invalid environment entry %q", entry)
	}

	key := entry[:separator]
	value := entry[separator+1:]
	if strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
		return "", "", fmt.Errorf("environment entry %q contains a NUL byte", entry)
	}

	return key, value, nil
}

func (environment *environment) normalizedKey(key string) string {
	if environment.caseInsensitive {
		return strings.ToUpper(key)
	}
	return key
}

func (environment *environment) set(key, value string) {
	normalized := environment.normalizedKey(key)
	if position, exists := environment.positions[normalized]; exists {
		environment.entries[position] = environmentEntry{key: key, value: value}
		return
	}

	environment.positions[normalized] = len(environment.entries)
	environment.entries = append(environment.entries, environmentEntry{key: key, value: value})
}

func (environment *environment) setDefault(key, value string) {
	if current, exists := environment.get(key); exists && current != "" {
		return
	}
	environment.set(key, value)
}

func (environment *environment) value(key string) string {
	value, _ := environment.get(key)
	return value
}

func (environment *environment) get(key string) (string, bool) {
	position, exists := environment.positions[environment.normalizedKey(key)]
	if !exists {
		return "", false
	}
	return environment.entries[position].value, true
}

func (environment *environment) prependList(key, value, separator string) {
	current, _ := environment.get(key)
	values := []string{value}
	seen := map[string]struct{}{environment.normalizedListValue(value): {}}

	if current != "" {
		for _, existing := range strings.Split(current, separator) {
			normalized := environment.normalizedListValue(existing)
			if _, duplicate := seen[normalized]; duplicate {
				continue
			}
			seen[normalized] = struct{}{}
			values = append(values, existing)
		}
	}

	environment.set(key, strings.Join(values, separator))
}

func (environment *environment) normalizedListValue(value string) string {
	if environment.caseInsensitive {
		return strings.ToUpper(value)
	}
	return value
}

func (environment *environment) entriesCopy() []string {
	entries := make([]string, 0, len(environment.entries))
	for _, entry := range environment.entries {
		entries = append(entries, entry.key+"="+entry.value)
	}
	return entries
}
