package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPrepareDarwin(t *testing.T) {
	root, workDir, executable := makePayload(t, "GeneralsXZH")
	libraryDir := makePayloadDirectory(t, root, "lib")
	baseGeneralsDir := makePayloadDirectory(t, root, "ZH_Generals")
	writePayloadFile(t, libraryDir, "libsage_patch.dylib")
	writePayloadFile(t, root, "MoltenVK_icd.json")
	writePayloadFile(t, root, "dxvk.conf")
	writePayloadFile(t, root, "fontconfig/fonts.conf")
	writePayloadFile(t, workDir, "libsage_patch.dylib")
	writePayloadFile(t, workDir, "MoltenVK_icd.json")
	writePayloadFile(t, workDir, "dxvk.conf")
	writePayloadFile(t, workDir, "fontconfig/fonts.conf")

	args := []string{"-win", "argument with spaces", "$(not-a-shell)"}
	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetDarwin,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Args:       args,
		Env: []string{
			"DYLD_LIBRARY_PATH=/usr/local/lib",
			"DYLD_INSERT_LIBRARIES=/opt/instrumentation.dylib",
			"DXVK_HUD=fps",
			"DXVK_STATE_CACHE_PATH=.",
			"DXVK_LOG_PATH=.",
			"VK_DRIVER_FILES=/custom/vulkan.json",
			"DUPLICATE=old",
			"DUPLICATE=new",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	stateDir := expectedRuntimeStateDirectory(t, root)
	assertCommandBasics(t, command, executable, stateDir, args)

	environment := environmentMap(t, command.Env, false)
	assertEnvironmentValue(t, environment, "DYLD_LIBRARY_PATH", libraryDir+":"+workDir+":/usr/local/lib")
	assertEnvironmentValue(t, environment, "DYLD_INSERT_LIBRARIES", filepath.Join(libraryDir, "libsage_patch.dylib")+":/opt/instrumentation.dylib")
	assertDXVKStateEnvironment(t, environment, root)
	assertEnvironmentValue(t, environment, "GENERALSX_SFX_RUNTIME_STATE", stateDir)
	assertEnvironmentValue(t, environment, "DXVK_WSI_DRIVER", "SDL3")
	assertEnvironmentValue(t, environment, "DXVK_HUD", "fps")
	assertEnvironmentValue(t, environment, "VK_ICD_FILENAMES", filepath.Join(root, "MoltenVK_icd.json"))
	assertEnvironmentValue(t, environment, "VK_DRIVER_FILES", "/custom/vulkan.json")
	assertEnvironmentValue(t, environment, "DXVK_CONFIG_FILE", filepath.Join(root, "dxvk.conf"))
	assertEnvironmentValue(t, environment, "FONTCONFIG_FILE", filepath.Join(root, "fontconfig/fonts.conf"))
	assertEnvironmentValue(t, environment, "FONTCONFIG_PATH", filepath.Join(root, "fontconfig"))
	assertEnvironmentValue(t, environment, "PWD", stateDir)
	assertEnvironmentValue(t, environment, "HOME", filepath.Join(stateDir, "home"))
	assertPrimaryAssetEnvironment(t, environment, root)
	assertBaseAssetEnvironment(t, environment, baseGeneralsDir)
	assertEnvironmentValue(t, environment, "DUPLICATE", "new")
}

func TestPrepareDarwinDefaultsAndDisabledSagePatch(t *testing.T) {
	root, _, _ := makePayload(t, "GeneralsXZH")
	writePayloadFile(t, filepath.Join(root, "runtime"), "libsage_patch.dylib")

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetDarwin,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env: []string{
			"DXVK_HUD=",
			"SAGE_PATCH_DISABLED=1",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, false)
	stateDir := expectedRuntimeStateDirectory(t, root)
	assertEnvironmentValue(t, environment, "DYLD_LIBRARY_PATH", filepath.Join(root, "runtime"))
	assertEnvironmentValue(t, environment, "DXVK_HUD", "0")
	assertPrimaryAssetEnvironment(t, environment, root)
	assertEnvironmentValue(t, environment, "PWD", stateDir)
	assertEnvironmentValue(t, environment, "HOME", filepath.Join(stateDir, "home"))
	if _, exists := environment["DYLD_INSERT_LIBRARIES"]; exists {
		t.Fatal("DYLD_INSERT_LIBRARIES was set while SagePatch was disabled")
	}
	if _, exists := environment["DXVK_CONFIG_FILE"]; exists {
		t.Fatal("DXVK_CONFIG_FILE was set without dxvk.conf")
	}
	if _, exists := environment["VK_ICD_FILENAMES"]; exists {
		t.Fatal("VK_ICD_FILENAMES was set without MoltenVK_icd.json")
	}
	if _, exists := environment["FONTCONFIG_FILE"]; exists {
		t.Fatal("FONTCONFIG_FILE was set without bundled Fontconfig")
	}
	assertNoBaseAssetEnvironment(t, environment)
}

func TestPrepareDarwinOptionalFilesFallBackToWorkDir(t *testing.T) {
	root, workDir, _ := makePayload(t, "GeneralsXZH")
	writePayloadFile(t, workDir, "libsage_patch.dylib")
	writePayloadFile(t, workDir, "MoltenVK_icd.json")
	writePayloadFile(t, workDir, "dxvk.conf")
	writePayloadFile(t, workDir, "fontconfig/fonts.conf")

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetDarwin,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env:        []string{},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, false)
	stateDir := expectedRuntimeStateDirectory(t, root)
	assertEnvironmentValue(t, environment, "DYLD_INSERT_LIBRARIES", filepath.Join(workDir, "libsage_patch.dylib"))
	assertEnvironmentValue(t, environment, "VK_ICD_FILENAMES", filepath.Join(workDir, "MoltenVK_icd.json"))
	assertEnvironmentValue(t, environment, "VK_DRIVER_FILES", filepath.Join(workDir, "MoltenVK_icd.json"))
	assertEnvironmentValue(t, environment, "DXVK_CONFIG_FILE", filepath.Join(workDir, "dxvk.conf"))
	assertEnvironmentValue(t, environment, "FONTCONFIG_FILE", filepath.Join(workDir, "fontconfig/fonts.conf"))
	assertEnvironmentValue(t, environment, "FONTCONFIG_PATH", filepath.Join(workDir, "fontconfig"))
	assertEnvironmentValue(t, environment, "PWD", stateDir)
}

func TestPrepareLinux(t *testing.T) {
	root, workDir, _ := makePayload(t, "GeneralsXZH")
	libraryDir := makePayloadDirectory(t, root, "lib")
	baseGeneralsDir := makePayloadDirectory(t, root, "ZH_Generals")
	writePayloadFile(t, libraryDir, "libsage_patch.so")
	writePayloadFile(t, root, "dxvk.conf")

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetLinux,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env: []string{
			"LD_LIBRARY_PATH=/usr/lib",
			"LD_PRELOAD=/opt/instrumentation.so",
			"ALSOFT_DISABLE_CPU_EXTS=sse2",
			"ALSOFT_DRIVERS=alsa",
			"DXVK_HUD=full",
			"DXVK_LOG_LEVEL=warn",
			"VK_DRIVER_FILES=/custom/hardware.json",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, false)
	stateDir := expectedRuntimeStateDirectory(t, root)
	if command.Dir != stateDir {
		t.Fatalf("command.Dir = %q, want %q", command.Dir, stateDir)
	}
	assertEnvironmentValue(t, environment, "LD_LIBRARY_PATH", libraryDir+":"+workDir+":/usr/lib")
	assertEnvironmentValue(t, environment, "LD_PRELOAD", filepath.Join(libraryDir, "libsage_patch.so")+":/opt/instrumentation.so")
	assertDXVKStateEnvironment(t, environment, root)
	assertEnvironmentValue(t, environment, "GENERALSX_SFX_RUNTIME_STATE", stateDir)
	assertEnvironmentValue(t, environment, "DXVK_WSI_DRIVER", "SDL3")
	assertEnvironmentValue(t, environment, "DXVK_HUD", "full")
	assertEnvironmentValue(t, environment, "DXVK_LOG_LEVEL", "warn")
	assertEnvironmentValue(t, environment, "VK_DRIVER_FILES", "/custom/hardware.json")
	assertEnvironmentValue(t, environment, "PWD", stateDir)
	assertEnvironmentValue(t, environment, "HOME", filepath.Join(stateDir, "home"))
	assertEnvironmentValue(t, environment, "XDG_CACHE_HOME", filepath.Join(stateDir, "home", ".cache"))
	assertEnvironmentValue(t, environment, "XDG_CONFIG_HOME", filepath.Join(stateDir, "home", ".config"))
	assertEnvironmentValue(t, environment, "XDG_DATA_HOME", filepath.Join(stateDir, "home", ".local", "share"))
	assertEnvironmentValue(t, environment, "DXVK_CONFIG_FILE", filepath.Join(root, "dxvk.conf"))
	assertEnvironmentValue(t, environment, "ALSOFT_DISABLE_CPU_EXTS", "sse2")
	assertEnvironmentValue(t, environment, "ALSOFT_DRIVERS", "alsa")
	assertPrimaryAssetEnvironment(t, environment, root)
	assertBaseAssetEnvironment(t, environment, baseGeneralsDir)
}

func TestPrepareLinuxOpenALDefaults(t *testing.T) {
	root, _, _ := makePayload(t, "GeneralsXZH")

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetLinux,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env: []string{
			"ALSOFT_DISABLE_CPU_EXTS=",
			"ALSOFT_DRIVERS=",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, false)
	stateDir := expectedRuntimeStateDirectory(t, root)
	assertEnvironmentValue(t, environment, "ALSOFT_DISABLE_CPU_EXTS", "all")
	assertEnvironmentValue(t, environment, "ALSOFT_DRIVERS", "pulse,alsa,oss,jack,null,wave")
	assertEnvironmentValue(t, environment, "DXVK_HUD", "0")
	assertEnvironmentValue(t, environment, "DXVK_LOG_LEVEL", "info")
	assertEnvironmentValue(t, environment, "PWD", stateDir)
	assertPrimaryAssetEnvironment(t, environment, root)
	assertNoBaseAssetEnvironment(t, environment)
}

func TestConfigureLinuxVulkanICDsFiltersSoftwareDrivers(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, name := range []string{
		"intel_icd.x86_64.json",
		"lvp_icd.x86_64.json",
		"lavapipe_icd.json",
		"not-an-icd.txt",
	} {
		writePayloadFile(t, first, name)
	}
	writePayloadFile(t, second, "radeon_icd.x86_64.json")
	writePayloadFile(t, second, "llvmpipe_icd.json")

	environment, err := newEnvironment([]string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	configureLinuxVulkanICDs(environment, []string{first, second})

	want := filepath.Join(first, "intel_icd.x86_64.json") + ":" +
		filepath.Join(second, "radeon_icd.x86_64.json")
	if got := environment.value("VK_DRIVER_FILES"); got != want {
		t.Fatalf("VK_DRIVER_FILES = %q, want %q", got, want)
	}
}

func TestConfigureLinuxVulkanICDsHonorsExplicitSelection(t *testing.T) {
	for _, variable := range []string{"VK_DRIVER_FILES", "VK_ICD_FILENAMES"} {
		t.Run(variable, func(t *testing.T) {
			directory := t.TempDir()
			writePayloadFile(t, directory, "intel_icd.json")
			environment, err := newEnvironment([]string{variable + "=/explicit/icd.json"}, false)
			if err != nil {
				t.Fatal(err)
			}
			configureLinuxVulkanICDs(environment, []string{directory})
			assertEnvironmentValue(
				t,
				environmentMap(t, environment.entriesCopy(), false),
				variable,
				"/explicit/icd.json",
			)
			if variable == "VK_ICD_FILENAMES" && environment.value("VK_DRIVER_FILES") != "" {
				t.Fatal("VK_DRIVER_FILES was set despite explicit VK_ICD_FILENAMES")
			}
		})
	}
}

func TestPrepareWindows(t *testing.T) {
	root, workDir, executable := makePayload(t, "generalszh.exe")
	libraryDir := makePayloadDirectory(t, root, "lib")
	dxvkConfig := writePayloadFile(t, root, "dxvk.conf")
	args := []string{"-win", "literal & metacharacters"}

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetWindows,
		Entrypoint: "runtime/generalszh.exe",
		WorkDir:    "runtime",
		Args:       args,
		Env: []string{
			"Path=C:\\Windows\\System32",
			"PATH=C:\\Tools",
			"=C:=C:\\work",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	assertCommandBasics(t, command, executable, workDir, args)

	environment := environmentMap(t, command.Env, true)
	stateDir := expectedRuntimeStateDirectory(t, root)
	assertEnvironmentValue(t, environment, "PATH", libraryDir+";"+workDir+";C:\\Tools")
	assertDXVKStateEnvironment(t, environment, root)
	assertEnvironmentValue(t, environment, "GENERALSX_SFX_RUNTIME_STATE", stateDir)
	assertEnvironmentValue(t, environment, "DXVK_CONFIG_FILE", dxvkConfig)
	assertEnvironmentValue(t, environment, "=C:", "C:\\work")
	assertPrimaryAssetEnvironment(t, environment, root)
	assertNoBaseAssetEnvironment(t, environment)
}

// GeneralsX @bugfix Codex 02/08/2026 Inherit the real host environment under its native variable semantics.
func TestPrepareInheritsEnvironmentWhenEnvIsNil(t *testing.T) {
	targetOS := runtime.GOOS
	if targetOS != TargetDarwin && targetOS != TargetLinux && targetOS != TargetWindows {
		t.Skipf("unsupported host target %q", targetOS)
	}
	t.Setenv("GENERALSX_LAUNCH_TEST_ENV", "preserved")
	root, _, _ := makePayload(t, "GeneralsXZH")

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   targetOS,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, targetOS == TargetWindows)
	assertEnvironmentValue(t, environment, "GENERALSX_LAUNCH_TEST_ENV", "preserved")
}

func TestPreparePreservesExplicitEnvironmentPrecedence(t *testing.T) {
	root, _, _ := makePayload(t, "GeneralsXZH")
	makePayloadDirectory(t, root, "lib")
	makePayloadDirectory(t, root, "ZH_Generals")
	writePayloadFile(t, root, "MoltenVK_icd.json")
	writePayloadFile(t, root, "dxvk.conf")
	writePayloadFile(t, root, "fontconfig/fonts.conf")

	explicit := map[string]string{
		"CNC_GENERALS_ZH_PATH":          "/custom/zero-hour",
		"GENERALSX_ASSET_PATH":          "/custom/compat-zero-hour",
		"CNC_ZH_INSTALLPATH":            "/custom/legacy-zero-hour",
		"CNC_GENERALS_PATH":             "/custom/generals",
		"GENERALSX_GENERALS_ASSET_PATH": "/custom/compat-generals",
		"CNC_GENERALS_INSTALLPATH":      "/custom/legacy-generals",
		"DXVK_WSI_DRIVER":               "CUSTOM",
		"DXVK_HUD":                      "fps",
		"VK_ICD_FILENAMES":              "/custom/icd.json",
		"VK_DRIVER_FILES":               "/custom/driver.json",
		"DXVK_CONFIG_FILE":              "/custom/dxvk.conf",
		"FONTCONFIG_FILE":               "/custom/fonts.conf",
		"FONTCONFIG_PATH":               "/custom/fontconfig",
	}
	baseEnvironment := make([]string, 0, len(explicit))
	for key, value := range explicit {
		baseEnvironment = append(baseEnvironment, key+"="+value)
	}

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetDarwin,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env:        baseEnvironment,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	environment := environmentMap(t, command.Env, false)
	for key, want := range explicit {
		assertEnvironmentValue(t, environment, key, want)
	}
}

func TestPrepareDefaultsWorkDirToRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "GeneralsXZH")
	if err := os.WriteFile(executable, []byte("native game"), 0o700); err != nil {
		t.Fatal(err)
	}

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetLinux,
		Entrypoint: "GeneralsXZH",
		Env:        []string{},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	assertCommandBasics(t, command, executable, expectedRuntimeStateDirectory(t, root), nil)
}

func TestPrepareRejectsUnsafeOrInvalidPaths(t *testing.T) {
	t.Run("empty root", func(t *testing.T) {
		_, err := Prepare(Config{
			TargetOS:   TargetLinux,
			Entrypoint: "runtime/GeneralsXZH",
			WorkDir:    "runtime",
			Env:        []string{},
		})
		assertErrorContains(t, err, "payload root is empty")
	})

	t.Run("unsupported target", func(t *testing.T) {
		root, _, _ := makePayload(t, "GeneralsXZH")
		_, err := Prepare(Config{
			Root:       root,
			TargetOS:   "plan9",
			Entrypoint: "runtime/GeneralsXZH",
			WorkDir:    "runtime",
			Env:        []string{},
		})
		assertErrorContains(t, err, "unsupported target OS")
	})

	for _, test := range []struct {
		name       string
		entrypoint string
		workDir    string
		want       string
	}{
		{
			name:       "absolute entrypoint",
			entrypoint: "/tmp/GeneralsXZH",
			workDir:    "runtime",
			want:       "must be relative",
		},
		{
			name:       "entrypoint traversal",
			entrypoint: "../GeneralsXZH",
			workDir:    "runtime",
			want:       "escapes payload root",
		},
		{
			name:       "work directory traversal",
			entrypoint: "runtime/GeneralsXZH",
			workDir:    "../runtime",
			want:       "escapes payload root",
		},
		{
			name:       "backslash entrypoint",
			entrypoint: `runtime\GeneralsXZH`,
			workDir:    "runtime",
			want:       "must use slash separators",
		},
		{
			name:       "volume entrypoint",
			entrypoint: "C:/runtime/generalszh.exe",
			workDir:    "runtime",
			want:       "volume or alternate stream separator",
		},
		{
			name:       "dot entrypoint",
			entrypoint: ".",
			workDir:    "runtime",
			want:       "does not name a file",
		},
		{
			name:       "missing entrypoint",
			entrypoint: "runtime/missing",
			workDir:    "runtime",
			want:       "resolve entrypoint",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _, _ := makePayload(t, "GeneralsXZH")
			_, err := Prepare(Config{
				Root:       root,
				TargetOS:   TargetLinux,
				Entrypoint: test.entrypoint,
				WorkDir:    test.workDir,
				Env:        []string{},
			})
			assertErrorContains(t, err, test.want)
		})
	}

	t.Run("entrypoint is directory", func(t *testing.T) {
		root, _, _ := makePayload(t, "GeneralsXZH")
		_, err := Prepare(Config{
			Root:       root,
			TargetOS:   TargetLinux,
			Entrypoint: "runtime",
			WorkDir:    "runtime",
			Env:        []string{},
		})
		assertErrorContains(t, err, "is not a regular file")
	})

	t.Run("work directory is file", func(t *testing.T) {
		root, _, _ := makePayload(t, "GeneralsXZH")
		_, err := Prepare(Config{
			Root:       root,
			TargetOS:   TargetLinux,
			Entrypoint: "runtime/GeneralsXZH",
			WorkDir:    "runtime/GeneralsXZH",
			Env:        []string{},
		})
		assertErrorContains(t, err, "is not a directory")
	})

	t.Run("entrypoint symlink escapes root", func(t *testing.T) {
		root, workDir, _ := makePayload(t, "GeneralsXZH")
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(workDir, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}

		_, err := Prepare(Config{
			Root:       root,
			TargetOS:   TargetLinux,
			Entrypoint: "runtime/escape",
			WorkDir:    "runtime",
			Env:        []string{},
		})
		assertErrorContains(t, err, "resolves outside payload root")
	})

	t.Run("work directory symlink escapes root", func(t *testing.T) {
		root, _, _ := makePayload(t, "GeneralsXZH")
		outside := t.TempDir()
		link := filepath.Join(root, "escaped-runtime")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}

		_, err := Prepare(Config{
			Root:       root,
			TargetOS:   TargetLinux,
			Entrypoint: "runtime/GeneralsXZH",
			WorkDir:    "escaped-runtime",
			Env:        []string{},
		})
		assertErrorContains(t, err, "resolves outside payload root")
	})
}

func TestPrepareRejectsEscapingOptionalFile(t *testing.T) {
	root, workDir, _ := makePayload(t, "GeneralsXZH")
	outside := filepath.Join(t.TempDir(), "dxvk.conf")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "dxvk.conf")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetLinux,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env:        []string{},
	})
	assertErrorContains(t, err, "optional payload file \"dxvk.conf\" resolves outside payload root")
}

func TestPrepareRejectsEscapingStagedDirectories(t *testing.T) {
	for _, directory := range []string{"lib", "ZH_Generals"} {
		t.Run(directory, func(t *testing.T) {
			root, _, _ := makePayload(t, "GeneralsXZH")
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(root, directory)); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}

			_, err := Prepare(Config{
				Root:       root,
				TargetOS:   TargetLinux,
				Entrypoint: "runtime/GeneralsXZH",
				WorkDir:    "runtime",
				Env:        []string{},
			})
			assertErrorContains(t, err, "resolves outside payload root")
		})
	}
}

func TestPrepareRejectsRuntimeStateInsidePayload(t *testing.T) {
	root, _, _ := makePayload(t, "GeneralsXZH")
	stateDir := makePayloadDirectory(t, root, "mutable-state")

	_, err := Prepare(Config{
		Root:            root,
		RuntimeStateDir: stateDir,
		TargetOS:        TargetLinux,
		Entrypoint:      "runtime/GeneralsXZH",
		WorkDir:         "runtime",
		Env:             []string{},
	})
	assertErrorContains(t, err, "inside the immutable payload root")
}

func TestPrepareRejectsInvalidEnvironment(t *testing.T) {
	root, _, _ := makePayload(t, "GeneralsXZH")
	_, err := Prepare(Config{
		Root:       root,
		TargetOS:   TargetLinux,
		Entrypoint: "runtime/GeneralsXZH",
		WorkDir:    "runtime",
		Env:        []string{"NOT_AN_ENVIRONMENT_ENTRY"},
	})
	assertErrorContains(t, err, "invalid environment entry")
}

// GeneralsX @bugfix Codex 02/08/2026 Preserve Windows drive state without weakening POSIX or NUL validation.
func TestSplitEnvironmentEntryWindowsPseudoVariable(t *testing.T) {
	for _, test := range []struct {
		entry string
		key   string
		value string
	}{
		{entry: "=C:=C:\\Users\\player", key: "=C:", value: "C:\\Users\\player"},
		{entry: "=d:=", key: "=d:", value: ""},
		{entry: "KEY=value=with=equals", key: "KEY", value: "value=with=equals"},
	} {
		key, value, err := splitEnvironmentEntry(test.entry, true)
		if err != nil {
			t.Fatalf("splitEnvironmentEntry(%q) error = %v", test.entry, err)
		}
		if key != test.key || value != test.value {
			t.Fatalf("splitEnvironmentEntry(%q) = %q, %q", test.entry, key, value)
		}
	}

	for _, malformed := range []string{"=C:", "=C=x", "==x", "=CC:=x", "=1:=x"} {
		if _, _, err := splitEnvironmentEntry(malformed, true); err == nil ||
			!strings.Contains(err.Error(), "invalid environment entry") {
			t.Fatalf("splitEnvironmentEntry(%q) error = %v", malformed, err)
		}
	}

	if _, _, err := splitEnvironmentEntry("=C:=C:\\Users\\player", false); err == nil ||
		!strings.Contains(err.Error(), "invalid environment entry") {
		t.Fatalf("POSIX pseudo-variable error = %v", err)
	}
	if _, _, err := splitEnvironmentEntry("=C:=C:\\Users\\player\x00escape", true); err == nil ||
		!strings.Contains(err.Error(), "NUL byte") {
		t.Fatalf("NUL pseudo-variable error = %v", err)
	}

	environment, err := newEnvironment([]string{"=c:=C:\\old", "=C:=C:\\new"}, true)
	if err != nil {
		t.Fatalf("newEnvironment() error = %v", err)
	}
	entries := environment.entriesCopy()
	if len(entries) != 1 {
		t.Fatalf("deduplicated pseudo-variable entries = %#v", entries)
	}
	values := environmentMap(t, entries, true)
	assertEnvironmentValue(t, values, "=C:", "C:\\new")
}

// GeneralsX @bugfix Codex 02/08/2026 Avoid interpreting Windows drive letters as synthetic Linux list separators.
func TestEnvironmentListPrependRemovesDuplicates(t *testing.T) {
	environment, err := newEnvironment([]string{
		"LD_LIBRARY_PATH=/payload/runtime:/payload/lib:/usr/lib:/payload/runtime",
	}, false)
	if err != nil {
		t.Fatalf("newEnvironment() error = %v", err)
	}
	environment.prependList("LD_LIBRARY_PATH", "/payload/runtime", ":")
	environment.prependList("LD_LIBRARY_PATH", "/payload/lib", ":")

	values := environmentMap(t, environment.entriesCopy(), false)
	assertEnvironmentValue(t, values, "LD_LIBRARY_PATH", "/payload/lib:/payload/runtime:/usr/lib")
}

func TestExitCode(t *testing.T) {
	if code := ExitCode(nil); code != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", code)
	}
	if code := ExitCode(errors.New("wrapper failure")); code != 1 {
		t.Fatalf("ExitCode(wrapper failure) = %d, want 1", code)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestExitCodeHelperProcess$")
	command.Env = append(os.Environ(),
		"GENERALSX_LAUNCH_HELPER=1",
		"GENERALSX_LAUNCH_HELPER_EXIT=23",
	)
	err := command.Run()
	if err == nil {
		t.Fatal("helper process unexpectedly succeeded")
	}
	if code := ExitCode(err); code != 23 {
		t.Fatalf("ExitCode(helper error) = %d, want 23", code)
	}
}

func TestExitCodeHelperProcess(t *testing.T) {
	if os.Getenv("GENERALSX_LAUNCH_HELPER") != "1" {
		return
	}

	code, err := strconv.Atoi(os.Getenv("GENERALSX_LAUNCH_HELPER_EXIT"))
	if err != nil {
		os.Exit(127)
	}
	os.Exit(code)
}

func TestPrepareContextCancellationTerminatesChild(t *testing.T) {
	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(currentExecutable)
	if err != nil {
		t.Fatal(err)
	}

	executableName := "context-helper"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	root, workDir, executable := makePayload(t, executableName)
	if err := os.WriteFile(executable, contents, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   runtime.GOOS,
		Entrypoint: "runtime/" + executableName,
		WorkDir:    "runtime",
		Args:       []string{"-test.run=^TestPrepareContextHelperProcess$"},
		Env: append(os.Environ(),
			"GENERALSX_CONTEXT_HELPER=1",
		),
		Context: ctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedWorkDir := workDir
	if runtime.GOOS != "windows" {
		expectedWorkDir = expectedRuntimeStateDirectory(t, root)
	}
	if command.Dir != expectedWorkDir {
		t.Fatalf("command.Dir = %q, want %q", command.Dir, expectedWorkDir)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled child unexpectedly exited successfully")
		}
	case <-time.After(7 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("canceled child was not terminated")
	}
}

func TestPrepareContextHelperProcess(t *testing.T) {
	if os.Getenv("GENERALSX_CONTEXT_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func makePayload(t *testing.T, executableName string) (string, string, string) {
	t.Helper()

	root := t.TempDir()
	workDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}

	executable := filepath.Join(workDir, executableName)
	if err := os.WriteFile(executable, []byte("native game"), 0o700); err != nil {
		t.Fatal(err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorkDir := filepath.Join(resolvedRoot, "runtime")

	return resolvedRoot, resolvedWorkDir, filepath.Join(resolvedWorkDir, executableName)
}

func writePayloadFile(t *testing.T, workDir, relativePath string) string {
	t.Helper()

	fullPath := filepath.Join(workDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(relativePath), 0o600); err != nil {
		t.Fatal(err)
	}

	return fullPath
}

func makePayloadDirectory(t *testing.T, root, relativePath string) string {
	t.Helper()

	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(fullPath, 0o700); err != nil {
		t.Fatal(err)
	}

	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertCommandBasics(t *testing.T, command *exec.Cmd, executable, workDir string, args []string) {
	t.Helper()

	if command.Path != executable {
		t.Errorf("command.Path = %q, want %q", command.Path, executable)
	}
	if command.Dir != workDir {
		t.Errorf("command.Dir = %q, want %q", command.Dir, workDir)
	}

	wantArguments := append([]string{executable}, args...)
	if !reflect.DeepEqual(command.Args, wantArguments) {
		t.Errorf("command.Args = %#v, want %#v", command.Args, wantArguments)
	}
	if command.Stdin != os.Stdin {
		t.Error("command.Stdin does not inherit os.Stdin")
	}
	if command.Stdout != os.Stdout {
		t.Error("command.Stdout does not inherit os.Stdout")
	}
	if command.Stderr != os.Stderr {
		t.Error("command.Stderr does not inherit os.Stderr")
	}
}

func assertPrimaryAssetEnvironment(t *testing.T, environment map[string]string, root string) {
	t.Helper()

	assertEnvironmentValue(t, environment, "CNC_GENERALS_ZH_PATH", root)
	assertEnvironmentValue(t, environment, "GENERALSX_ASSET_PATH", root)
	assertEnvironmentValue(t, environment, "CNC_ZH_INSTALLPATH", root)
}

func assertDXVKStateEnvironment(t *testing.T, environment map[string]string, root string) {
	t.Helper()

	resolved := expectedRuntimeStateDirectory(t, root)
	assertEnvironmentValue(t, environment, "DXVK_STATE_CACHE_PATH", resolved)
	assertEnvironmentValue(t, environment, "DXVK_LOG_PATH", resolved)
	if isWithin(root, resolved) {
		t.Fatalf("DXVK runtime state directory %q is inside payload root %q", resolved, root)
	}
}

func expectedRuntimeStateDirectory(t *testing.T, root string) string {
	t.Helper()

	stateDir := filepath.Join(filepath.Dir(root), ".runtime-state", filepath.Base(root))
	resolved, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatalf("resolve generated runtime state directory: %v", err)
	}
	return resolved
}

func assertBaseAssetEnvironment(t *testing.T, environment map[string]string, baseGeneralsDir string) {
	t.Helper()

	assertEnvironmentValue(t, environment, "CNC_GENERALS_PATH", baseGeneralsDir)
	assertEnvironmentValue(t, environment, "GENERALSX_GENERALS_ASSET_PATH", baseGeneralsDir)
	assertEnvironmentValue(t, environment, "CNC_GENERALS_INSTALLPATH", baseGeneralsDir)
}

func assertNoBaseAssetEnvironment(t *testing.T, environment map[string]string) {
	t.Helper()

	for _, key := range []string{
		"CNC_GENERALS_PATH",
		"GENERALSX_GENERALS_ASSET_PATH",
		"CNC_GENERALS_INSTALLPATH",
	} {
		if _, exists := environment[key]; exists {
			t.Errorf("environment variable %q was set without ZH_Generals", key)
		}
	}
}

func environmentMap(t *testing.T, entries []string, caseInsensitive bool) map[string]string {
	t.Helper()

	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, err := splitEnvironmentEntry(entry, caseInsensitive)
		if err != nil {
			t.Fatalf("invalid prepared environment entry %q: %v", entry, err)
		}

		normalized := key
		if caseInsensitive {
			normalized = strings.ToUpper(key)
		}
		if _, duplicate := result[normalized]; duplicate {
			t.Fatalf("prepared environment contains duplicate key %q", key)
		}
		result[normalized] = value
	}

	return result
}

func assertEnvironmentValue(t *testing.T, environment map[string]string, key, want string) {
	t.Helper()

	lookupKey := key
	if _, exists := environment[lookupKey]; !exists {
		lookupKey = strings.ToUpper(key)
	}

	got, exists := environment[lookupKey]
	if !exists {
		t.Fatalf("environment variable %q is missing", key)
	}
	if got != want {
		t.Errorf("environment variable %q = %q, want %q", key, got, want)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}
