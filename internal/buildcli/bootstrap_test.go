package buildcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxDockerScriptsKeepOnlineEnvironmentArrayBash32Safe(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"docker-configure-linux.sh",
		"docker-build-linux-generals.sh",
		"docker-build-linux-zh.sh",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "scripts", "build", "linux", name)
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(contents)
			if !strings.Contains(text, "DOCKER_ONLINE_ENV=(-e GX_ONLINE_SERVER_DEFAULT)") {
				t.Fatal("Docker Online environment forwarding is not initialized with a Bash 3.2-safe non-empty array")
			}
			if strings.Contains(text, "DOCKER_ONLINE_ENV=()") {
				t.Fatal("empty arrays abort under set -u on macOS Bash 3.2")
			}
			if !strings.Contains(text, `if [[ \${GX_ONLINE_SERVER_DEFAULT+x} == x ]]; then`) {
				t.Fatal("container shell does not distinguish an unset endpoint without unsafe outer-shell quoting")
			}
			if strings.Contains(text, `if [ "\${GX_ONLINE_SERVER_DEFAULT+x}" = x ]; then`) {
				t.Fatal("inner test quotes are consumed by the outer bash -c string and fail when the endpoint is unset")
			}
			if !strings.Contains(text, `CMAKE_CONFIGURE_ARGS+=("-DSAGE_ONLINE_SERVER_DEFAULT=\${GX_ONLINE_SERVER_DEFAULT}")`) {
				t.Fatal("set endpoint is not forwarded to CMake, including the explicit-empty cache-clearing value")
			}
		})
	}
}

func TestLinuxPackageManagerTranslations(t *testing.T) {
	t.Parallel()
	wantDocker := map[string]string{
		"apt-get": "docker.io",
		"dnf":     "moby-engine",
		"pacman":  "",
	}
	managers := supportedLinuxPackageManagers()
	if len(managers) != len(wantDocker) {
		t.Fatalf("supportedLinuxPackageManagers() returned %d managers, want %d", len(managers), len(wantDocker))
	}
	for _, manager := range managers {
		want, ok := wantDocker[manager.name]
		if !ok {
			t.Errorf("unexpected Linux package manager %q", manager.name)
			continue
		}
		if got := manager.translate["docker"]; got != want {
			t.Errorf("%s Docker package = %q, want %q", manager.name, got, want)
		}
	}
}

func TestPacmanRepositoryEnabledRequiresActiveRepositoryAndSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contents string
		want     bool
	}{
		{name: "enabled include", contents: "[multilib]\nInclude = /etc/pacman.d/mirrorlist\n", want: true},
		{name: "enabled server", contents: "[multilib]\nServer = https://mirror.invalid/$repo/os/$arch\n", want: true},
		{name: "commented", contents: "#[multilib]\n#Include = /etc/pacman.d/mirrorlist\n"},
		{name: "missing source", contents: "[multilib]\n#Include = /etc/pacman.d/mirrorlist\n"},
		{name: "different repository", contents: "[core]\nInclude = /etc/pacman.d/mirrorlist\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "pacman.conf")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := pacmanRepositoryEnabled(path, "multilib")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("pacmanRepositoryEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWindowsSDKAvailableRequiresMatchingHeadersAndResourceCompiler(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sdkRoot := filepath.Join(root, "Windows Kits", "10")
	version := "10.0.26100.0"
	header := filepath.Join(sdkRoot, "Include", version, "um", "Windows.h")
	if err := os.MkdirAll(filepath.Dir(header), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(header, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if windowsSDKAvailable(root) {
		t.Fatal("SDK accepted without rc.exe")
	}
	resourceCompiler := filepath.Join(sdkRoot, "bin", version, "x86", "rc.exe")
	if err := os.MkdirAll(filepath.Dir(resourceCompiler), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourceCompiler, []byte("fixture\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !windowsSDKAvailable(root) {
		t.Fatal("complete SDK was not detected")
	}
}

func TestValidVCPKGRequiresBootstrappedPlatformTool(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmakeToolchain := filepath.Join(root, "scripts", "buildsystems", "vcpkg.cmake")
	if err := os.MkdirAll(filepath.Dir(cmakeToolchain), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmakeToolchain, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if validVCPKG(root, "") || validVCPKG(root, ".bat") {
		t.Fatal("source-only vcpkg checkout was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "vcpkg"), []byte("fixture\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !validVCPKG(root, "") {
		t.Fatal("bootstrapped Unix vcpkg checkout was rejected")
	}
	if validVCPKG(root, ".bat") {
		t.Fatal("Unix vcpkg tool satisfied the Windows check")
	}
	if err := os.WriteFile(filepath.Join(root, "vcpkg.exe"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !validVCPKG(root, ".bat") {
		t.Fatal("bootstrapped Windows vcpkg checkout was rejected")
	}
}

func TestWindowsBuildToolsAreFoundInFreshInstallDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	programFiles := filepath.Join(root, "Program Files")
	localAppData := filepath.Join(root, "Local")
	visualStudio := filepath.Join(root, "Visual Studio")
	directories := windowsBuildToolDirectories(programFiles, localAppData, visualStudio)
	wantDirectories := []string{
		filepath.Join(programFiles, "CMake", "bin"),
		filepath.Join(localAppData, "Microsoft", "WinGet", "Links"),
		filepath.Join(visualStudio, "Common7", "IDE", "CommonExtensions", "Microsoft", "CMake", "CMake", "bin"),
		filepath.Join(visualStudio, "Common7", "IDE", "CommonExtensions", "Microsoft", "CMake", "Ninja"),
	}
	if len(directories) != len(wantDirectories) {
		t.Fatalf("windowsBuildToolDirectories() returned %d entries, want %d", len(directories), len(wantDirectories))
	}
	for index, want := range wantDirectories {
		if directories[index] != want {
			t.Errorf("directory %d = %q, want %q", index, directories[index], want)
		}
	}

	cmake := filepath.Join(wantDirectories[0], "cmake.exe")
	ninja := filepath.Join(wantDirectories[1], "ninja.exe")
	for _, path := range []string{cmake, ninja} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pathMiss := func(string) (string, error) { return "", os.ErrNotExist }
	if got := locateWindowsBuildToolWithLookup("cmake.exe", directories, pathMiss); got != cmake {
		t.Fatalf("fresh CMake location = %q, want %q", got, cmake)
	}
	if got := locateWindowsBuildToolWithLookup("ninja.exe", directories, pathMiss); got != ninja {
		t.Fatalf("fresh Ninja location = %q, want %q", got, ninja)
	}
	if missing := missingWindowsBuildToolsWithLookup(directories, pathMiss); len(missing) != 0 {
		t.Fatalf("freshly installed build tools reported missing: %#v", missing)
	}
	if missing := missingWindowsBuildToolsWithLookup(nil, pathMiss); len(missing) != 2 || missing[0] != "cmake.exe" || missing[1] != "ninja.exe" {
		t.Fatalf("missing build tools = %#v, want cmake.exe and ninja.exe", missing)
	}

	pathEntries := append([]string{}, directories...)
	pathEntries = append(pathEntries, "inherited-path")
	gotPath := joinPathEntries(pathEntries...)
	if entries := strings.Split(gotPath, string(os.PathListSeparator)); len(entries) != 5 || entries[0] != directories[0] || entries[3] != directories[3] || entries[4] != "inherited-path" {
		t.Fatalf("refreshed PATH entries = %#v", entries)
	}
}

func TestWindowsBuildToolDirectoriesIgnoreEmptyEnvironmentRoots(t *testing.T) {
	t.Parallel()
	if directories := windowsBuildToolDirectories("", "", ""); len(directories) != 0 {
		t.Fatalf("empty Windows environment produced search directories: %#v", directories)
	}
}
