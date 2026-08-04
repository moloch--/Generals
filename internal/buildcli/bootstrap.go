package buildcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	vcpkgCommit         = "ffc071e0c08432c60c9b64f00334c0227667931b"
	vcpkgRepository     = "https://github.com/microsoft/vcpkg.git"
	vulkanSDKVersion    = "1.4.341.1"
	vulkanSDKURL        = "https://sdk.lunarg.com/sdk/download/1.4.341.1/mac/vulkansdk-macos-1.4.341.1.zip"
	vulkanSDKArchiveSHA = "632cbe96c8ed6ed00c6ce25e3a7738c466134f76586e1c51f1419410d7f9042e"
	homebrewInstallURL  = "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh"
)

// GeneralsX @build Codex 04/08/2026 Bootstrap target toolchains through native package managers and pinned user-local SDKs.
func (app application) bootstrap(ctx context.Context, gitPath string) (map[string]string, error) {
	environment := make(map[string]string)
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"submodule", "sync", "--recursive"},
		dir:  app.cfg.repoRoot,
	}); err != nil {
		return nil, err
	}
	if err := app.runner.run(ctx, command{
		name: gitPath,
		args: []string{"submodule", "update", "--init", "--recursive", "--jobs", "6"},
		dir:  app.cfg.repoRoot,
	}); err != nil {
		return nil, err
	}

	switch app.cfg.target {
	case targetMacOS:
		macEnvironment, err := app.bootstrapMacOS(ctx, gitPath)
		if err != nil {
			return nil, err
		}
		mergeStringMap(environment, macEnvironment)
	case targetLinux:
		linuxEnvironment, err := app.bootstrapLinux(ctx)
		if err != nil {
			return nil, err
		}
		mergeStringMap(environment, linuxEnvironment)
	case targetWindows:
		windowsEnvironment, err := app.bootstrapWindows(ctx, gitPath)
		if err != nil {
			return nil, err
		}
		mergeStringMap(environment, windowsEnvironment)
	}
	return environment, nil
}

func (app application) ensureGit(ctx context.Context) (string, error) {
	gitPath, gitErr := exec.LookPath("git")
	if app.hostOS == "darwin" {
		_, toolsErr := app.runner.output(ctx, command{name: "xcode-select", args: []string{"-p"}})
		if gitErr == nil && toolsErr == nil {
			return gitPath, nil
		}
		if !app.cfg.installDeps {
			return "", errors.New("Git and the Xcode Command Line Tools are required; rerun with --install-deps")
		}
		if app.cfg.nonInteractive {
			return "", errors.New("Xcode Command Line Tools installation requires interaction; install it before using --non-interactive")
		}
		if err := app.runner.run(ctx, command{name: "xcode-select", args: []string{"--install"}}); err != nil {
			return "", err
		}
		if app.cfg.dryRun {
			if gitErr == nil {
				return gitPath, nil
			}
			return "git", nil
		}
		return "", errors.New("Xcode Command Line Tools installation was requested; complete it and rerun")
	}
	if gitErr == nil {
		return gitPath, nil
	}
	if !app.cfg.installDeps {
		return "", errors.New("git is required; rerun with --install-deps")
	}
	if app.hostOS == "windows" {
		if err := app.installWingetPackage(ctx, "Git.Git", nil); err != nil {
			return "", err
		}
		if path := firstExistingFile(
			`C:\Program Files\Git\cmd\git.exe`,
			`C:\Program Files\Git\bin\git.exe`,
		); path != "" {
			return path, nil
		}
		if app.cfg.dryRun {
			return "git.exe", nil
		}
		return "", errors.New("Git was installed but is not visible; start a new terminal and rerun")
	}
	if err := app.installLinuxPackages(ctx, []string{"git", "ca-certificates"}); err != nil {
		return "", err
	}
	if app.cfg.dryRun {
		return "git", nil
	}
	path, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("git installation completed but the executable is not visible; rerun in a new shell")
	}
	return path, nil
}

func (app application) bootstrapMacOS(ctx context.Context, gitPath string) (map[string]string, error) {
	if app.hostOS != "darwin" || app.hostArch != "arm64" {
		return nil, errors.New("macOS bootstrap requires Apple Silicon macOS")
	}
	if !app.cfg.dryRun {
		if _, err := app.runner.output(ctx, command{name: "xcode-select", args: []string{"-p"}}); err != nil {
			if !app.cfg.installDeps {
				return nil, errors.New("Xcode Command Line Tools are required")
			}
			if err := app.runner.run(ctx, command{name: "xcode-select", args: []string{"--install"}}); err != nil {
				return nil, err
			}
			return nil, errors.New("Xcode Command Line Tools installation was requested; complete it and rerun")
		}
	} else {
		fmt.Fprintln(app.runner.stdout, "> xcode-select -p")
	}
	brew, err := app.ensureHomebrew(ctx)
	if err != nil {
		return nil, err
	}
	packages := []string{
		"cmake", "ninja", "meson", "python3", "pkgconf",
		"autoconf", "autoconf-archive", "automake", "libtool",
		"ffmpeg", "libbluray", "gnutls", "librist", "srt", "libssh", "zeromq",
		"libvpx", "webp", "jpeg-xl", "dav1d", "opencore-amr", "snappy", "aom",
		"libvmaf", "lame", "xz", "aribb24", "libpng", "freetype", "fontconfig",
		"glslang", "sdl3",
	}
	if app.cfg.installDeps {
		arguments := append([]string{"install"}, packages...)
		if err := app.runner.run(ctx, command{name: brew, args: arguments}); err != nil {
			return nil, err
		}
	} else {
		for _, tool := range []string{"cmake", "ninja", "meson", "python3", "xz"} {
			if _, err := exec.LookPath(tool); err != nil {
				return nil, fmt.Errorf("required tool %s is missing; rerun with --install-deps", tool)
			}
		}
	}
	vcpkgRoot, err := app.ensureVCPKG(ctx, gitPath, "")
	if err != nil {
		return nil, err
	}
	vulkanRoot, err := app.ensureVulkanSDK(ctx)
	if err != nil {
		return nil, err
	}
	brewPrefix := filepath.Dir(filepath.Dir(brew))
	pathValue := strings.Join([]string{
		filepath.Join(vulkanRoot, "bin"),
		filepath.Join(brewPrefix, "bin"),
		os.Getenv("PATH"),
	}, string(os.PathListSeparator))
	return map[string]string{
		"VCPKG_ROOT":            vcpkgRoot,
		"VCPKG_DEFAULT_TRIPLET": "arm64-osx",
		"VULKAN_SDK":            vulkanRoot,
		"PATH":                  pathValue,
		"PKG_CONFIG_PATH": joinPathEntries(
			filepath.Join(brewPrefix, "lib", "pkgconfig"),
			os.Getenv("PKG_CONFIG_PATH"),
		),
	}, nil
}

func (app application) ensureHomebrew(ctx context.Context) (string, error) {
	if path, err := exec.LookPath("brew"); err == nil {
		return path, nil
	}
	if path := firstExistingFile("/opt/homebrew/bin/brew", "/usr/local/bin/brew"); path != "" {
		return path, nil
	}
	if !app.cfg.installDeps {
		return "", errors.New("Homebrew is required to install macOS build dependencies")
	}
	if app.cfg.nonInteractive {
		return "", errors.New("the Homebrew bootstrap may require sudo interaction; install Homebrew before using --non-interactive")
	}
	installer := filepath.Join(app.cfg.cacheDir, "downloads", "homebrew-install.sh")
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] download %s -> %s\n", homebrewInstallURL, installer)
	} else if err := downloadFile(ctx, app.http, homebrewInstallURL, installer, ""); err != nil {
		return "", err
	}
	if err := app.runner.run(ctx, command{
		name: "/bin/bash",
		args: []string{installer},
	}); err != nil {
		return "", err
	}
	if app.cfg.dryRun {
		return "/opt/homebrew/bin/brew", nil
	}
	if path := firstExistingFile("/opt/homebrew/bin/brew", "/usr/local/bin/brew"); path != "" {
		return path, nil
	}
	return "", errors.New("Homebrew installer completed but brew was not found")
}

func (app application) ensureVulkanSDK(ctx context.Context) (string, error) {
	if root := discoverVulkanSDK(); root != "" {
		return root, nil
	}
	if !app.cfg.installDeps {
		return "", errors.New("LunarG Vulkan SDK is missing; rerun with --install-deps --accept-sdk-licenses")
	}
	if !app.cfg.acceptSDKLicenses {
		return "", errors.New("installing the LunarG Vulkan SDK requires --accept-sdk-licenses")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	installRoot := filepath.Join(home, "VulkanSDK", vulkanSDKVersion)
	platformRoot := filepath.Join(installRoot, "macOS")
	archivePath := filepath.Join(app.cfg.cacheDir, "downloads", "vulkansdk-macos-"+vulkanSDKVersion+".zip")
	extractRoot := filepath.Join(app.cfg.cacheDir, "vulkansdk-installer-"+vulkanSDKVersion)
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] download and SHA-256 verify %s -> %s\n", vulkanSDKURL, archivePath)
		fmt.Fprintf(app.runner.stdout, "[dry-run] extract %s -> %s\n", archivePath, extractRoot)
		installer := filepath.Join(extractRoot, "InstallVulkan.app", "Contents", "MacOS", "vulkansdk-macOS-"+vulkanSDKVersion)
		if err := app.runner.run(ctx, command{name: installer, args: vulkanInstallerArguments(installRoot)}); err != nil {
			return "", err
		}
		return platformRoot, nil
	}
	if err := downloadFile(ctx, app.http, vulkanSDKURL, archivePath, vulkanSDKArchiveSHA); err != nil {
		return "", err
	}
	if _, err := os.Stat(extractRoot); errors.Is(err, os.ErrNotExist) {
		if err := extractZip(archivePath, extractRoot); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	installer, err := findVulkanInstaller(extractRoot)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(installer, 0o700); err != nil {
		return "", err
	}
	if err := app.runner.run(ctx, command{name: installer, args: vulkanInstallerArguments(installRoot)}); err != nil {
		return "", err
	}
	if !validVulkanSDK(platformRoot) {
		return "", fmt.Errorf("Vulkan SDK installer did not create a complete SDK at %s", platformRoot)
	}
	return platformRoot, nil
}

func vulkanInstallerArguments(root string) []string {
	return []string{"--root", root, "--accept-licenses", "--default-answer", "--confirm-command", "install"}
}

func discoverVulkanSDK() string {
	for _, value := range []string{os.Getenv("VULKAN_SDK"), os.Getenv("VULKAN_SDK_ROOT")} {
		if validVulkanSDK(value) {
			return value
		}
		if validVulkanSDK(filepath.Join(value, "macOS")) {
			return filepath.Join(value, "macOS")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates, _ := filepath.Glob(filepath.Join(home, "VulkanSDK", "*", "macOS"))
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	for _, candidate := range candidates {
		if validVulkanSDK(candidate) {
			return candidate
		}
	}
	return ""
}

func validVulkanSDK(root string) bool {
	if root == "" {
		return false
	}
	for _, relative := range []string{filepath.Join("lib", "libvulkan.dylib"), filepath.Join("lib", "libMoltenVK.dylib")} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func findVulkanInstaller(root string) (string, error) {
	var result string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "vulkansdk-macOS-") {
			if result != "" {
				return fmt.Errorf("multiple Vulkan SDK installers found under %s", root)
			}
			result = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if result == "" {
		return "", fmt.Errorf("Vulkan SDK installer not found under %s", root)
	}
	return result, nil
}

func (app application) bootstrapLinux(ctx context.Context) (map[string]string, error) {
	steamRuntimeNeeded := app.needsSteamCMD()
	steamRuntimePlanned := false
	if _, err := exec.LookPath("docker"); err != nil {
		if !app.cfg.installDeps {
			return nil, errors.New("Docker is required for the containerized Linux build")
		}
		if app.hostOS == "darwin" {
			if app.cfg.nonInteractive {
				return nil, errors.New("Docker Desktop installation requires interaction; install and start it before using --non-interactive")
			}
			brew, err := app.ensureHomebrew(ctx)
			if err != nil {
				return nil, err
			}
			if err := app.runner.run(ctx, command{name: brew, args: []string{"install", "--cask", "docker"}}); err != nil {
				return nil, err
			}
			if err := app.runner.run(ctx, command{name: "open", args: []string{"-a", "Docker"}}); err != nil {
				return nil, err
			}
			if !app.cfg.dryRun {
				return nil, errors.New("Docker Desktop was installed and opened; finish its first-run setup, then rerun")
			}
		} else {
			packages := []string{"docker", "xz", "git", "ca-certificates"}
			if app.cfg.withOnlineServer {
				packages = append(packages, "file")
			}
			if steamRuntimeNeeded {
				packages = append(packages, "steamcmd-runtime")
			}
			if err := app.installLinuxPackages(ctx, packages); err != nil {
				return nil, err
			}
			steamRuntimePlanned = steamRuntimeNeeded
			if err := app.runner.run(ctx, command{name: "sudo", args: []string{"systemctl", "enable", "--now", "docker"}}); err != nil {
				return nil, err
			}
			user := os.Getenv("USER")
			if user != "" {
				if err := app.runner.run(ctx, command{name: "sudo", args: []string{"usermod", "-aG", "docker", user}}); err != nil {
					return nil, err
				}
			}
			if !app.cfg.dryRun {
				return nil, errors.New("Docker was installed; log out and back in for docker-group access, then rerun")
			}
		}
	}
	if app.hostOS == "linux" {
		if app.cfg.withOnlineServer {
			if _, err := exec.LookPath("file"); err != nil {
				if !app.cfg.installDeps {
					return nil, errors.New("file is required to validate the bundled online server; rerun with --install-deps")
				}
				if err := app.installLinuxPackages(ctx, []string{"file"}); err != nil {
					return nil, err
				}
			}
		}
		if _, err := exec.LookPath("xz"); err != nil {
			if !app.cfg.installDeps {
				return nil, errors.New("xz is required for Linux SFX packaging; rerun with --install-deps")
			}
			if err := app.installLinuxPackages(ctx, []string{"xz"}); err != nil {
				return nil, err
			}
		}
		if steamRuntimeNeeded && !hasLinux32Runtime() && !steamRuntimePlanned {
			if !app.cfg.installDeps {
				return nil, errors.New("SteamCMD requires a 32-bit Linux runtime; rerun with --install-deps")
			}
			if err := app.installLinuxPackages(ctx, []string{"steamcmd-runtime"}); err != nil {
				return nil, err
			}
		}
		if steamRuntimeNeeded && !app.cfg.dryRun && !hasLinux32Runtime() {
			return nil, errors.New("the 32-bit SteamCMD runtime was installed but /lib/ld-linux.so.2 is still unavailable")
		}
	} else if app.hostOS == "darwin" {
		if _, err := exec.LookPath("xz"); err != nil {
			if !app.cfg.installDeps {
				return nil, errors.New("xz is required for Linux SFX packaging; rerun with --install-deps")
			}
			brew, brewErr := app.ensureHomebrew(ctx)
			if brewErr != nil {
				return nil, brewErr
			}
			if err := app.runner.run(ctx, command{name: brew, args: []string{"install", "xz"}}); err != nil {
				return nil, err
			}
		}
	}
	if !app.cfg.dryRun {
		if _, err := app.runner.output(ctx, command{name: "docker", args: []string{"info"}}); err != nil {
			return nil, fmt.Errorf("Docker is installed but unavailable; start the daemon and verify current-user access: %w", err)
		}
	} else {
		fmt.Fprintln(app.runner.stdout, "> docker info")
	}
	vcpkgDirectory := filepath.Join(app.cfg.cacheDir, "vcpkg-linux")
	if !app.cfg.dryRun {
		if err := ensurePrivateDirectory(vcpkgDirectory); err != nil {
			return nil, fmt.Errorf("prepare Linux vcpkg cache: %w", err)
		}
	}
	return map[string]string{"VCPKG_DIR": vcpkgDirectory}, nil
}

func hasLinux32Runtime() bool {
	return firstExistingFile(
		"/lib/ld-linux.so.2",
		"/lib32/ld-linux.so.2",
		"/lib/i386-linux-gnu/ld-linux.so.2",
		"/usr/lib/ld-linux.so.2",
		"/usr/lib32/ld-linux.so.2",
	) != ""
}

type linuxPackageManager struct {
	name      string
	prefix    []string
	translate map[string]string
}

func supportedLinuxPackageManagers() []linuxPackageManager {
	return []linuxPackageManager{
		{name: "apt-get", prefix: []string{"install", "-y", "--no-install-recommends"}, translate: map[string]string{"docker": "docker.io", "xz": "xz-utils", "steamcmd-runtime": "lib32gcc-s1"}},
		{name: "dnf", prefix: []string{"install", "-y"}, translate: map[string]string{"docker": "moby-engine", "steamcmd-runtime": "glibc.i686 libgcc.i686 libstdc++.i686"}},
		{name: "pacman", prefix: []string{"-S", "--needed", "--noconfirm"}, translate: map[string]string{"steamcmd-runtime": "lib32-gcc-libs"}},
	}
}

func (app application) installLinuxPackages(ctx context.Context, generic []string) error {
	if app.cfg.nonInteractive {
		return errors.New("Linux package installation may require sudo interaction; install dependencies before using --non-interactive")
	}
	managers := supportedLinuxPackageManagers()
	selected := -1
	for index, candidate := range managers {
		if _, err := exec.LookPath(candidate.name); err == nil {
			selected = index
			break
		}
	}
	if selected < 0 && app.cfg.dryRun {
		selected = 0
	}
	if selected >= 0 {
		candidate := managers[selected]
		if candidate.name == "pacman" && containsString(generic, "steamcmd-runtime") && !app.cfg.dryRun {
			enabled, err := pacmanRepositoryEnabled("/etc/pacman.conf", "multilib")
			if err != nil {
				return fmt.Errorf("inspect pacman multilib configuration: %w", err)
			}
			if !enabled {
				return errors.New("SteamCMD needs Arch Linux's multilib repository; uncomment [multilib] and its Include line in /etc/pacman.conf, run sudo pacman -Syu, then rerun")
			}
		}
		if candidate.name == "apt-get" {
			if err := app.runner.run(ctx, command{name: "sudo", args: []string{"apt-get", "update"}}); err != nil {
				return err
			}
		}
		packages := make([]string, 0, len(generic))
		for _, name := range generic {
			if translated := candidate.translate[name]; translated != "" {
				packages = append(packages, strings.Fields(translated)...)
			} else {
				packages = append(packages, name)
			}
		}
		args := append([]string{candidate.name}, candidate.prefix...)
		args = append(args, packages...)
		return app.runner.run(ctx, command{name: "sudo", args: args})
	}
	return errors.New("no supported Linux package manager found (apt-get, dnf, or pacman)")
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func pacmanRepositoryEnabled(configurationPath, repository string) (bool, error) {
	contents, err := os.ReadFile(configurationPath)
	if err != nil {
		return false, err
	}
	inRepository := false
	for _, rawLine := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inRepository = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), repository)
			continue
		}
		if inRepository && (strings.HasPrefix(strings.ToLower(line), "include") || strings.HasPrefix(strings.ToLower(line), "server")) {
			return true, nil
		}
	}
	return false, nil
}

func (app application) bootstrapWindows(ctx context.Context, gitPath string) (map[string]string, error) {
	if app.hostOS != "windows" || app.hostArch != "amd64" {
		return nil, errors.New("Windows bootstrap requires native 64-bit Windows")
	}
	vswhere := locateVSWhere()
	cppInstallation := ""
	if vswhere != "" {
		cppInstallation, _ = queryVisualStudioInstallation(ctx, vswhere, true)
	}
	hasWindowsSDK := windowsSDKAvailable(os.Getenv("ProgramFiles(x86)"))
	if cppInstallation == "" || !hasWindowsSDK {
		if !app.cfg.installDeps {
			return nil, errors.New("Visual Studio 2022 C++ tools and a Windows SDK are required")
		}
		if app.cfg.nonInteractive {
			return nil, errors.New("Visual Studio C++ tools installation may require UAC interaction; install it before using --non-interactive")
		}
		existingInstallation := ""
		if vswhere != "" {
			existingInstallation, _ = queryVisualStudioInstallation(ctx, vswhere, false)
		}
		if existingInstallation != "" {
			setup := firstExistingFile(filepath.Join(filepath.Dir(vswhere), "setup.exe"))
			if setup == "" {
				return nil, errors.New("Visual Studio Installer setup.exe is required to add the C++ workload")
			}
			if err := app.runner.run(ctx, command{
				name: setup,
				args: []string{
					"modify",
					"--installPath", existingInstallation,
					"--quiet", "--norestart", "--nocache",
					"--add", "Microsoft.VisualStudio.Workload.VCTools",
					"--includeRecommended",
				},
			}); err != nil {
				return nil, fmt.Errorf("add Visual Studio C++ components: %w", err)
			}
		} else {
			override := []string{
				"--override",
				"--quiet --wait --norestart --nocache --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended",
			}
			if err := app.installWingetPackage(ctx, "Microsoft.VisualStudio.2022.BuildTools", override); err != nil {
				return nil, err
			}
		}
		vswhere = locateVSWhere()
		if app.cfg.dryRun {
			if vswhere == "" {
				vswhere = `C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe`
			}
			cppInstallation = "<planned>"
		} else if vswhere != "" {
			cppInstallation, _ = queryVisualStudioInstallation(ctx, vswhere, true)
			hasWindowsSDK = windowsSDKAvailable(os.Getenv("ProgramFiles(x86)"))
		}
	}
	if vswhere == "" || cppInstallation == "" || (!app.cfg.dryRun && !hasWindowsSDK) {
		return nil, errors.New("Visual Studio setup completed but the C++ x86/x64 tools and Windows SDK (Windows.h and x86 rc.exe) were not both found; finish any installer/UAC prompt and rerun")
	}
	buildToolDirectories := windowsBuildToolDirectories(
		os.Getenv("ProgramFiles"),
		os.Getenv("LOCALAPPDATA"),
		cppInstallation,
	)
	missingBuildTools := missingWindowsBuildTools(buildToolDirectories)
	if len(missingBuildTools) != 0 && !app.cfg.installDeps {
		return nil, fmt.Errorf("required Windows build tools are missing: %s; rerun with --install-deps", strings.Join(missingBuildTools, ", "))
	}
	packageForTool := map[string]string{
		"cmake.exe": "Kitware.CMake",
		"ninja.exe": "Ninja-build.Ninja",
	}
	for _, tool := range missingBuildTools {
		if err := app.installWingetPackage(ctx, packageForTool[tool], nil); err != nil {
			return nil, err
		}
	}
	if !app.cfg.dryRun {
		missingBuildTools = missingWindowsBuildTools(buildToolDirectories)
		if len(missingBuildTools) != 0 {
			return nil, fmt.Errorf("WinGet installation completed but required Windows build tools were not found in PATH or known install locations: %s", strings.Join(missingBuildTools, ", "))
		}
	}
	vcpkgRoot, err := app.ensureVCPKG(ctx, gitPath, ".bat")
	if err != nil {
		return nil, err
	}
	pathEntries := append([]string{}, buildToolDirectories...)
	pathEntries = append(pathEntries, os.Getenv("PATH"))
	return map[string]string{
		"VCPKG_ROOT":            vcpkgRoot,
		"VCPKG_DEFAULT_TRIPLET": "x86-windows",
		"GX_VSWHERE":            vswhere,
		"PATH":                  joinPathEntries(pathEntries...),
	}, nil
}

func windowsBuildToolDirectories(programFiles, localAppData, cppInstallation string) []string {
	directories := make([]string, 0, 4)
	if programFiles != "" {
		directories = append(directories, filepath.Join(programFiles, "CMake", "bin"))
	}
	if localAppData != "" {
		directories = append(directories, filepath.Join(localAppData, "Microsoft", "WinGet", "Links"))
	}
	if cppInstallation != "" {
		directories = append(directories,
			filepath.Join(cppInstallation, "Common7", "IDE", "CommonExtensions", "Microsoft", "CMake", "CMake", "bin"),
			filepath.Join(cppInstallation, "Common7", "IDE", "CommonExtensions", "Microsoft", "CMake", "Ninja"),
		)
	}
	return directories
}

func missingWindowsBuildTools(searchDirectories []string) []string {
	return missingWindowsBuildToolsWithLookup(searchDirectories, exec.LookPath)
}

func missingWindowsBuildToolsWithLookup(searchDirectories []string, lookup func(string) (string, error)) []string {
	missing := make([]string, 0, 2)
	for _, name := range []string{"cmake.exe", "ninja.exe"} {
		if locateWindowsBuildToolWithLookup(name, searchDirectories, lookup) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func locateWindowsBuildTool(name string, searchDirectories []string) string {
	return locateWindowsBuildToolWithLookup(name, searchDirectories, exec.LookPath)
}

func locateWindowsBuildToolWithLookup(name string, searchDirectories []string, lookup func(string) (string, error)) string {
	if path, err := lookup(name); err == nil {
		return path
	}
	for _, directory := range searchDirectories {
		if path := firstExistingFile(filepath.Join(directory, name)); path != "" {
			return path
		}
	}
	return ""
}

func windowsSDKAvailable(programFilesX86 string) bool {
	if programFilesX86 == "" {
		return false
	}
	sdkRoot := filepath.Join(programFilesX86, "Windows Kits", "10")
	headers, err := filepath.Glob(filepath.Join(sdkRoot, "Include", "*", "um", "Windows.h"))
	if err != nil {
		return false
	}
	for _, header := range headers {
		version := filepath.Base(filepath.Dir(filepath.Dir(header)))
		if firstExistingFile(filepath.Join(sdkRoot, "bin", version, "x86", "rc.exe")) != "" {
			return true
		}
	}
	return false
}

func queryVisualStudioInstallation(ctx context.Context, vswhere string, requireCpp bool) (string, error) {
	arguments := []string{"-latest", "-products", "*"}
	if requireCpp {
		arguments = append(arguments,
			"-requires",
			"Microsoft.VisualStudio.Component.VC.Tools.x86.x64",
		)
	}
	arguments = append(arguments, "-property", "installationPath")
	command := exec.CommandContext(ctx, vswhere, arguments...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	installation := strings.TrimSpace(string(output))
	if installation == "" {
		return "", errors.New("matching Visual Studio installation not found")
	}
	info, err := os.Stat(installation)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("Visual Studio installation path %q is not a directory", installation)
	}
	return installation, nil
}

func (app application) installWingetPackage(ctx context.Context, packageID string, extra []string) error {
	if app.cfg.nonInteractive {
		return errors.New("winget installation may require interaction; install Windows dependencies before using --non-interactive")
	}
	winget := locateWinget()
	if winget == "" {
		powershell := firstExistingFile(
			filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		)
		if powershell == "" {
			powershell, _ = exec.LookPath("powershell.exe")
		}
		if powershell == "" && !app.cfg.dryRun {
			return errors.New("WinGet is unavailable and Windows PowerShell was not found to repair its App Installer registration")
		}
		if powershell == "" {
			powershell = "powershell.exe"
		}
		if err := app.runner.run(ctx, command{
			name: powershell,
			args: []string{
				"-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
				"Add-AppxPackage -RegisterByFamilyName -MainPackage Microsoft.DesktopAppInstaller_8wekyb3d8bbwe",
			},
		}); err != nil {
			return fmt.Errorf("repair WinGet App Installer registration; install or update Microsoft's App Installer if it is absent: %w", err)
		}
		winget = locateWinget()
		if winget == "" && app.cfg.dryRun {
			winget = "winget.exe"
		}
		if winget == "" {
			return errors.New("WinGet is still unavailable after App Installer registration; install or update Microsoft's App Installer, then rerun")
		}
	}
	args := []string{"install", "--id", packageID, "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements"}
	args = append(args, extra...)
	return app.runner.run(ctx, command{name: winget, args: args})
}

func locateWinget() string {
	if path, err := exec.LookPath("winget.exe"); err == nil {
		return path
	}
	return firstExistingFile(filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps", "winget.exe"))
}

func (app application) ensureVCPKG(ctx context.Context, gitPath, bootstrapSuffix string) (string, error) {
	if explicit := os.Getenv("VCPKG_ROOT"); validVCPKG(explicit, bootstrapSuffix) {
		return explicit, nil
	}
	managed := filepath.Join(app.cfg.cacheDir, "vcpkg")
	if validVCPKG(managed, bootstrapSuffix) {
		return managed, nil
	}
	if !app.cfg.installDeps {
		return "", errors.New("vcpkg is missing; set VCPKG_ROOT or rerun with --install-deps")
	}
	if info, err := os.Lstat(managed); err == nil && !app.cfg.dryRun {
		return "", fmt.Errorf("managed vcpkg path %q exists but is incomplete (mode %s); move it aside and rerun", managed, info.Mode())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := os.Lstat(managed); errors.Is(err, os.ErrNotExist) || app.cfg.dryRun {
		if err := app.runner.run(ctx, command{name: gitPath, args: []string{"clone", vcpkgRepository, managed}}); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := app.runner.run(ctx, command{name: gitPath, args: []string{"checkout", "--detach", vcpkgCommit}, dir: managed}); err != nil {
		return "", err
	}
	bootstrap := filepath.Join(managed, "bootstrap-vcpkg.sh")
	bootstrapCommand := command{name: bootstrap, args: []string{"-disableMetrics"}, dir: managed}
	if bootstrapSuffix == ".bat" {
		bootstrap = filepath.Join(managed, "bootstrap-vcpkg.bat")
		powerShell, err := findPowerShell()
		if err != nil {
			if !app.cfg.dryRun {
				return "", err
			}
			powerShell = "powershell.exe"
		}
		invocation := "& " + powerShellLiteral(bootstrap) + " '-disableMetrics'; exit $LASTEXITCODE"
		bootstrapCommand = command{
			name: powerShell,
			args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", invocation},
			dir:  managed,
		}
	}
	if err := app.runner.run(ctx, bootstrapCommand); err != nil {
		return "", err
	}
	return managed, nil
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func joinPathEntries(values ...string) string {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return strings.Join(nonEmpty, string(os.PathListSeparator))
}

func validVCPKG(root, bootstrapSuffix string) bool {
	if root == "" {
		return false
	}
	toolName := "vcpkg"
	if bootstrapSuffix == ".bat" {
		toolName = "vcpkg.exe"
	}
	toolInfo, toolErr := os.Stat(filepath.Join(root, toolName))
	cmakeInfo, cmakeErr := os.Stat(filepath.Join(root, "scripts", "buildsystems", "vcpkg.cmake"))
	if toolErr != nil || cmakeErr != nil || !toolInfo.Mode().IsRegular() || !cmakeInfo.Mode().IsRegular() {
		return false
	}
	// Windows does not preserve Unix executable permission bits, including on
	// test fixtures representing a Unix vcpkg checkout. The native Windows path
	// still requires vcpkg.exe above; only Unix hosts can validate +x here.
	return bootstrapSuffix == ".bat" || runtime.GOOS == "windows" || toolInfo.Mode().Perm()&0o111 != 0
}

func locateVSWhere() string {
	if path := os.Getenv("GX_VSWHERE"); path != "" {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return firstExistingFile(
		`C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe`,
		`C:\Program Files\Microsoft Visual Studio\Installer\vswhere.exe`,
	)
}

func firstExistingFile(candidates ...string) string {
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func mergeStringMap(destination, source map[string]string) {
	for key, value := range source {
		destination[key] = value
	}
}
