package buildcli

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestValidateAssetsTargetAware(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)

	if err := validateAssets(root, targetLinux); err != nil {
		t.Fatalf("Linux assets rejected: %v", err)
	}
	if err := validateAssets(root, targetWindows); err == nil {
		t.Fatal("Windows assets without retail DLLs succeeded")
	}
	writeFixtureFile(t, root, "BINKW32.DLL")
	writeFixtureFile(t, root, "mss32.dll")
	if err := validateAssets(root, targetWindows); err != nil {
		t.Fatalf("Windows assets rejected: %v", err)
	}
}

func TestValidateRetailAssetsExportedWrapper(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	if err := ValidateRetailAssets(root, "linux"); err != nil {
		t.Fatalf("ValidateRetailAssets() = %v", err)
	}
	if err := ValidateRetailAssets(root, "unsupported"); err == nil {
		t.Fatal("unsupported retail target passed validation")
	}
}

func TestAcquireAssetsUsesInteractiveRunner(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	assetsDirectory := filepath.Join(root, "assets")
	steamCMDDirectory := filepath.Join(root, "steamcmd")
	if err := os.MkdirAll(steamCMDDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	steamCMD := filepath.Join(steamCMDDirectory, "steamcmd.sh")
	if err := os.WriteFile(steamCMD, []byte("placeholder"), 0o700); err != nil {
		t.Fatal(err)
	}

	var commands []InteractiveCommand
	app := application{
		cfg: config{
			target:      targetLinux,
			assetsDir:   assetsDirectory,
			steamUser:   "builder-account",
			steamCMDDir: steamCMDDirectory,
			cacheDir:    filepath.Join(root, "cache"),
		},
		hostOS:   "linux",
		hostArch: "amd64",
		runner: runner{
			stdout: io.Discard,
			stderr: io.Discard,
		},
		interactiveRunner: InteractiveCommandRunnerFunc(func(_ context.Context, command InteractiveCommand) error {
			commands = append(commands, command)
			writeCompleteAssetTree(t, assetsDirectory)
			return nil
		}),
	}
	if err := app.acquireAssets(context.Background()); err != nil {
		t.Fatalf("acquireAssets() = %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("interactive commands = %d, want 1", len(commands))
	}
	command := commands[0]
	if command.Purpose != InteractiveSteamAuthentication {
		t.Fatalf("interactive purpose = %q, want %q", command.Purpose, InteractiveSteamAuthentication)
	}
	if command.Executable != steamCMD || command.WorkingDirectory != steamCMDDirectory {
		t.Fatalf("interactive command = %#v", command)
	}
	wantArguments := []string{
		"+@sSteamCmdForcePlatformType", "windows",
		"+force_install_dir", assetsDirectory,
		"+login", "builder-account",
		"+app_update", zeroHourSteamAppID, "validate",
		"+quit",
	}
	if !slices.Equal(command.Arguments, wantArguments) {
		t.Fatalf("interactive arguments = %q, want %q", command.Arguments, wantArguments)
	}
}

func TestValidateAssetsRejectsEmptyOrSentinelOnlyTrees(t *testing.T) {
	t.Parallel()
	t.Run("empty required archive", func(t *testing.T) {
		root := t.TempDir()
		writeCompleteAssetTree(t, root)
		if err := os.WriteFile(filepath.Join(root, "INIZH.big"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := validateAssets(root, targetLinux); err == nil {
			t.Fatal("zero-byte required archive was accepted")
		}
	})
	t.Run("old sentinel set", func(t *testing.T) {
		root := t.TempDir()
		for _, relative := range []string{
			"INIZH.big",
			"W3DZH.big",
			"MapsZH.big",
			filepath.Join("ZH_Generals", "INI.big"),
			filepath.Join("ZH_Generals", "W3D.big"),
		} {
			writeFixtureFile(t, root, relative)
		}
		if err := validateAssets(root, targetLinux); err == nil {
			t.Fatal("sentinel-only retail tree was accepted")
		}
	})
}

func TestValidateAssetsRequiresCoherentLocalizedPack(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	if err := os.Remove(filepath.Join(root, "SpeechEnglishZH.big")); err != nil {
		t.Fatal(err)
	}
	err := validateAssets(root, targetLinux)
	if err == nil || !strings.Contains(err.Error(), "english retail language pack is incomplete") {
		t.Fatalf("validateAssets() error = %v", err)
	}
}

func TestValidateAssetsAcceptsEverySteamLanguageFamily(t *testing.T) {
	t.Parallel()
	for _, family := range supportedRetailLanguages {
		family := family
		t.Run(family.zeroHourValue, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeCoreAssetTree(t, root)
			writeLanguageFamily(t, root, family)
			if err := validateAssets(root, targetLinux); err != nil {
				t.Fatalf("validateAssets() rejected %s family: %v", family.zeroHourValue, err)
			}
		})
	}
}

func TestValidateAssetsRejectsIncompleteHigherPrecedenceLanguage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	writeFixtureFile(t, root, "BrazilianZH.big")
	err := validateAssets(root, targetLinux)
	if err == nil || !strings.Contains(err.Error(), "brazilian retail language pack is incomplete") {
		t.Fatalf("validateAssets() error = %v", err)
	}
}

func TestValidateAssetsRejectsInvalidArchiveSignature(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	if err := os.WriteFile(filepath.Join(root, "MapsZH.big"), []byte("not-a-big-file!!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateAssets(root, targetLinux)
	if err == nil || !strings.Contains(err.Error(), "MapsZH.big has an invalid signature") {
		t.Fatalf("validateAssets() error = %v", err)
	}
}

func TestValidateAssetsRejectsMalformedBIGArchiveStructure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		want   string
	}{
		{
			name: "truncated header",
			mutate: func(_ []byte) []byte {
				return []byte("BIGF")
			},
			want: "truncated BIGF header",
		},
		{
			name: "unsupported BIG4 signature",
			mutate: func(contents []byte) []byte {
				copy(contents[:4], "BIG4")
				return contents
			},
			want: "invalid signature",
		},
		{
			name: "wrong declared size",
			mutate: func(contents []byte) []byte {
				binary.LittleEndian.PutUint32(contents[4:8], uint32(len(contents)-1))
				return contents
			},
			want: "BIGF size field",
		},
		{
			name: "empty directory",
			mutate: func(contents []byte) []byte {
				binary.BigEndian.PutUint32(contents[8:12], 0)
				return contents
			},
			want: "BIGF directory is empty",
		},
		{
			name: "entry count above safety limit",
			mutate: func(contents []byte) []byte {
				binary.BigEndian.PutUint32(contents[8:12], maxBIGArchiveEntries+1)
				return contents
			},
			want: "BIGF entry count 250001 exceeds limit 250000",
		},
		{
			name: "directory past end",
			mutate: func(contents []byte) []byte {
				binary.BigEndian.PutUint32(contents[12:16], uint32(len(contents)+1))
				return contents
			},
			want: "invalid BIGF directory boundary",
		},
		{
			name: "unterminated entry name",
			mutate: func(contents []byte) []byte {
				boundary := binary.BigEndian.Uint32(contents[12:16])
				binary.BigEndian.PutUint32(contents[12:16], boundary-1)
				return contents
			},
			want: "unterminated BIGF entry 0 name",
		},
		{
			name: "entry name above safety limit",
			mutate: func(_ []byte) []byte {
				return bigFixtureWithNameSize(maxBIGArchiveEntryNameSize + 1)
			},
			want: "BIGF entry 0 name exceeds limit 4096",
		},
		{
			name: "payload past end",
			mutate: func(contents []byte) []byte {
				binary.BigEndian.PutUint32(contents[16:20], uint32(len(contents)))
				return contents
			},
			want: "out-of-bounds BIGF entry 0 payload",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeCompleteAssetTree(t, root)
			path := filepath.Join(root, "MapsZH.big")
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.mutate(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			err = validateAssets(root, targetLinux)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateAssets() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestNeedsSteamCMDHonorsPreparedAndSkippedAssets(t *testing.T) {
	t.Parallel()
	app := application{cfg: config{assetsDir: filepath.Join(t.TempDir(), "missing"), target: targetLinux, skipAssets: true}}
	if app.needsSteamCMD() {
		t.Fatal("skip-assets requested SteamCMD")
	}
	app.cfg.skipAssets = false
	if !app.needsSteamCMD() {
		t.Fatal("incomplete assets did not request SteamCMD")
	}
	root := t.TempDir()
	writeCompleteAssetTree(t, root)
	app.cfg.assetsDir = root
	if app.needsSteamCMD() {
		t.Fatal("complete assets requested SteamCMD")
	}
}

func TestAcquireAssetsRejectsSymlinkBeforeSteamCMD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	root := t.TempDir()
	target := filepath.Join(root, "shared-assets")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "assets-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	app := application{
		cfg: config{
			assetsDir: link,
			target:    targetLinux,
			steamUser: "fixture-user",
		},
		runner: runner{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}},
	}
	if err := app.acquireAssets(context.Background()); err == nil {
		t.Fatal("symlinked Steam install directory was accepted")
	}
}

func TestSystemSteamCMDCreatesPrivateWorkingDirectory(t *testing.T) {
	toolRoot := t.TempDir()
	toolName := "steamcmd"
	hostOS := "linux"
	if runtime.GOOS == "windows" {
		toolName = "steamcmd.exe"
		hostOS = "windows"
	}
	toolPath := filepath.Join(toolRoot, toolName)
	if err := os.WriteFile(toolPath, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolRoot)
	workDir := filepath.Join(t.TempDir(), "private", "steamcmd")
	var stdout bytes.Buffer
	app := application{
		cfg: config{
			steamCMDDir: workDir,
			cacheDir:    filepath.Dir(workDir),
			installDeps: false,
		},
		hostOS: hostOS,
		runner: runner{stdout: &stdout},
	}
	resolved, err := app.ensureSteamCMD(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != toolPath {
		t.Fatalf("SteamCMD path = %q, want %q", resolved, toolPath)
	}
	info, err := os.Lstat(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("SteamCMD working path mode = %s", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("SteamCMD working directory is not private: %#o", info.Mode().Perm())
	}
}

func TestFindFileCaseInsensitiveRejectsCollision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixtureFile(t, root, "MapsZH.big")
	writeFixtureFile(t, root, "mapszh.BIG")
	first, err := os.Stat(filepath.Join(root, "MapsZH.big"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(filepath.Join(root, "mapszh.BIG"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Skip("filesystem is case-insensitive")
	}
	if _, err := findFileCaseInsensitive(root, "mapszh.big"); err == nil {
		t.Fatal("case-colliding asset lookup succeeded")
	}
}

func writeFixtureFile(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte("fixture")
	if strings.EqualFold(filepath.Ext(relative), ".big") {
		contents = validBIGFixture()
	} else if strings.EqualFold(filepath.Ext(relative), ".dll") {
		contents = []byte("MZfixture")
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCompleteAssetTree(t *testing.T, root string) {
	t.Helper()
	writeCoreAssetTree(t, root)
	writeLanguageFamily(t, root, retailLanguageFamily{
		zeroHourStem:      "English",
		baseGeneralsStem:  "English",
		zeroHourValue:     "english",
		baseGeneralsValue: "english",
	})
}

func writeCoreAssetTree(t *testing.T, root string) {
	t.Helper()
	for _, relative := range append(append([]string(nil), requiredZeroHourArchives...), requiredBaseGeneralsArchives...) {
		writeFixtureFile(t, root, relative)
	}
}

func writeLanguageFamily(t *testing.T, root string, family retailLanguageFamily) {
	t.Helper()
	for _, relative := range []string{
		family.zeroHourStem + "ZH.big",
		"Audio" + family.zeroHourStem + "ZH.big",
		"Speech" + family.zeroHourStem + "ZH.big",
		"W3D" + family.zeroHourStem + "ZH.big",
		filepath.Join("ZH_Generals", family.baseGeneralsStem+".big"),
		filepath.Join("ZH_Generals", "Audio"+family.baseGeneralsStem+".big"),
		filepath.Join("ZH_Generals", "Speech"+family.baseGeneralsStem+".big"),
	} {
		writeFixtureFile(t, root, relative)
	}
}

func validBIGFixture() []byte {
	return bigFixtureWithNameSize(len("fixture.dat"))
}

func bigFixtureWithNameSize(nameSize int) []byte {
	directoryBoundary := 16 + 8 + nameSize + 1
	totalSize := directoryBoundary + 1
	contents := make([]byte, totalSize)
	copy(contents[:4], "BIGF")
	binary.LittleEndian.PutUint32(contents[4:8], uint32(totalSize))
	binary.BigEndian.PutUint32(contents[8:12], 1)
	binary.BigEndian.PutUint32(contents[12:16], uint32(directoryBoundary))
	binary.BigEndian.PutUint32(contents[16:20], uint32(directoryBoundary))
	binary.BigEndian.PutUint32(contents[20:24], 1)
	for index := 24; index < 24+nameSize; index++ {
		contents[index] = 'a'
	}
	contents[directoryBoundary] = 1
	return contents
}
