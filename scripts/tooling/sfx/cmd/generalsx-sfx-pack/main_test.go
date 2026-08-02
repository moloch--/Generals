package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/bundle"
	"github.com/ulikunitz/xz"
)

func TestParseExclusionProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "test.exclude")
	writeTestFile(t, profile, []byte(`
# comment

logs/
*.tmp
bin/?ame
`), 0o600)

	exclude, err := parseExclusionProfile(profile)
	if err != nil {
		t.Fatalf("parseExclusionProfile: %v", err)
	}
	testCases := []struct {
		name string
		want bool
	}{
		{"logs", true},
		{"logs/old/output.txt", true},
		{"scratch.tmp", true},
		{"nested/scratch.tmp", false},
		{"bin/game", true},
		{"bin/long-name", false},
		{"assets/data.big", false},
	}
	for _, testCase := range testCases {
		got, err := exclude(testCase.name, nil)
		if err != nil {
			t.Fatalf("exclude(%q): %v", testCase.name, err)
		}
		if got != testCase.want {
			t.Errorf("exclude(%q) = %t, want %t", testCase.name, got, testCase.want)
		}
	}
}

func TestParseTarget(t *testing.T) {
	testCases := []struct {
		value    string
		wantOS   string
		wantArch string
		wantErr  bool
	}{
		{"darwin/arm64", "darwin", "arm64", false},
		{"linux/amd64", "linux", "amd64", false},
		{"windows/386", "windows", "386", false},
		{"plan9/amd64", "", "", true},
		{"darwin", "", "", true},
		{"darwin/arm64/extra", "", "", true},
		{"linux/AMD64", "", "", true},
	}
	for _, testCase := range testCases {
		gotOS, gotArch, err := parseTarget(testCase.value)
		if (err != nil) != testCase.wantErr {
			t.Errorf("parseTarget(%q) error = %v, wantErr %t", testCase.value, err, testCase.wantErr)
			continue
		}
		if gotOS != testCase.wantOS || gotArch != testCase.wantArch {
			t.Errorf(
				"parseTarget(%q) = %q/%q, want %q/%q",
				testCase.value,
				gotOS,
				gotArch,
				testCase.wantOS,
				testCase.wantArch,
			)
		}
	}
}

func TestParseExclusionProfileRejectsMalformedPattern(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "bad.exclude")
	writeTestFile(t, profile, []byte("[unterminated\n"), 0o600)
	if _, err := parseExclusionProfile(profile); err == nil {
		t.Fatal("parseExclusionProfile accepted a malformed pattern")
	}
}

func TestRunRejectsOutputInsidePayloadWithoutMutation(t *testing.T) {
	source := createPayloadFixture(t)
	output := filepath.Join(source, "new-output-directory", "launcher")
	currentDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := findModuleRoot(currentDirectory)
	if moduleRoot == "" {
		t.Fatal("could not locate real SFX module")
	}

	err = run(
		context.Background(),
		[]string{
			"-source", source,
			"-output", output,
			"-target", runtime.GOOS + "/" + runtime.GOARCH,
			"-entry", "bin/game",
			"-product", "fixture-product",
			"-version", "test",
			"-module", moduleRoot,
		},
		io.Discard,
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "inside the payload source tree") {
		t.Fatalf("run error = %v, want output/source overlap rejection", err)
	}
	assertNotExist(t, filepath.Join(source, "new-output-directory"))
}

func TestCreatePrivatePackerWorkspaceIgnoresAmbientTempInsideSource(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	module := filepath.Join(root, "module")
	outputDirectory := filepath.Join(root, "output")
	for _, directory := range []string{source, module, outputDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", source)
	t.Setenv("TMP", source)
	t.Setenv("TEMP", source)

	workspace, err := createPrivatePackerWorkspace(
		filepath.Join(outputDirectory, "launcher"),
		source,
		module,
	)
	if err != nil {
		t.Fatalf("createPrivatePackerWorkspace: %v", err)
	}
	defer os.RemoveAll(workspace)

	if !pathWithin(outputDirectory, workspace) {
		t.Fatalf("workspace %q is outside output directory %q", workspace, outputDirectory)
	}
	for _, protectedRoot := range []string{source, module} {
		if pathWithin(protectedRoot, workspace) || pathWithin(workspace, protectedRoot) {
			t.Fatalf("workspace %q overlaps protected tree %q", workspace, protectedRoot)
		}
	}
	if entries, err := os.ReadDir(source); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("ambient temporary directory gained packer files: %v", entries)
	}
}

func TestCreatePrivatePackerWorkspaceRejectsProtectedOutputDirectory(t *testing.T) {
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(source, "output")
	if err := os.Mkdir(outputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}

	workspace, err := createPrivatePackerWorkspace(
		filepath.Join(outputDirectory, "launcher"),
		source,
	)
	if err == nil || !strings.Contains(err.Error(), "overlaps protected tree") {
		t.Fatalf("createPrivatePackerWorkspace overlap error = %v", err)
	}
	if workspace != "" {
		t.Fatalf("overlap rejection returned workspace %q", workspace)
	}
	if entries, readErr := os.ReadDir(outputDirectory); readErr != nil {
		t.Fatal(readErr)
	} else if len(entries) != 0 {
		t.Fatalf("overlap rejection created workspace files: %v", entries)
	}
}

func TestCopyModuleOmitsGeneratedPayloadAndOutput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "internal", "payload", "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0o600)
	writeTestFile(t, filepath.Join(source, "main.go"), []byte("package main\n"), 0o600)
	writeTestFile(t, filepath.Join(source, "internal", "payload", "generated", "payload.tar.xz"), []byte("stale"), 0o600)
	output := filepath.Join(source, "launcher")
	writeTestFile(t, output, []byte("old launcher"), 0o700)

	if err := copyModule(source, destination, output); err != nil {
		t.Fatalf("copyModule: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(destination, "main.go")); err != nil {
		t.Fatalf("read copied source: %v", err)
	} else if string(contents) != "package main\n" {
		t.Fatalf("copied source = %q", contents)
	}
	assertNotExist(t, filepath.Join(destination, "internal", "payload", "generated"))
	assertNotExist(t, filepath.Join(destination, "launcher"))
}

func TestCopyModuleRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require additional Windows privileges")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "real.go"), []byte("package real\n"), 0o600)
	if err := os.Symlink("real.go", filepath.Join(source, "linked.go")); err != nil {
		t.Fatalf("create module symlink: %v", err)
	}
	err := copyModule(source, filepath.Join(root, "destination"), filepath.Join(root, "launcher"))
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("copyModule error = %v, want symlink rejection", err)
	}
}

func TestWriteCompressedPayloadPureGoIsDeterministic(t *testing.T) {
	source := createPayloadFixture(t)
	options := fixturePackOptions(runtime.GOOS, runtime.GOARCH)
	firstPath := filepath.Join(t.TempDir(), "first.tar.xz")
	secondPath := filepath.Join(t.TempDir(), "second.tar.xz")

	firstManifest, firstDigest, firstSize, err := writeCompressedPayload(
		context.Background(),
		source,
		firstPath,
		options,
		1<<20,
		"",
	)
	if err != nil {
		t.Fatalf("first writeCompressedPayload: %v", err)
	}
	secondManifest, secondDigest, secondSize, err := writeCompressedPayload(
		context.Background(),
		source,
		secondPath,
		options,
		1<<20,
		"",
	)
	if err != nil {
		t.Fatalf("second writeCompressedPayload: %v", err)
	}

	if !reflect.DeepEqual(firstManifest, secondManifest) {
		t.Fatalf("manifests differ:\nfirst: %#v\nsecond: %#v", firstManifest, secondManifest)
	}
	if firstDigest != secondDigest || firstSize != secondSize {
		t.Fatalf(
			"compressed metadata differs: first %s/%d, second %s/%d",
			firstDigest,
			firstSize,
			secondDigest,
			secondSize,
		)
	}
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("pure-Go xz payload is not deterministic")
	}
	sum := sha256.Sum256(firstBytes)
	if got := hex.EncodeToString(sum[:]); got != firstDigest {
		t.Fatalf("payload digest = %s, want %s", firstDigest, got)
	}
	if int64(len(firstBytes)) != firstSize {
		t.Fatalf("payload size = %d, want %d", firstSize, len(firstBytes))
	}

	reader, err := xz.NewReader(bytes.NewReader(firstBytes))
	if err != nil {
		t.Fatalf("open generated xz stream: %v", err)
	}
	tarReader := tar.NewReader(reader)
	var names []string
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read generated tar: %v", err)
		}
		names = append(names, header.Name)
		if got := header.ModTime.Unix(); got != options.Epoch.Unix() {
			t.Errorf("tar epoch for %q = %d, want %d", header.Name, got, options.Epoch.Unix())
		}
	}
	if !reflect.DeepEqual(names, []string{"bin", "bin/game", "data.txt"}) {
		t.Fatalf("tar entries = %v", names)
	}
}

func TestWriteCompressedPayloadEnforcesEmbedLimit(t *testing.T) {
	source := createPayloadFixture(t)
	output := filepath.Join(t.TempDir(), "payload.tar.xz")
	_, _, _, err := writeCompressedPayload(
		context.Background(),
		source,
		output,
		fixturePackOptions(runtime.GOOS, runtime.GOARCH),
		1,
		"",
	)
	if !errors.Is(err, errPayloadTooLarge) {
		t.Fatalf("writeCompressedPayload error = %v, want errPayloadTooLarge", err)
	}
	assertNotExist(t, output)
}

func TestWriteCompressedPayloadPreservesSafeSymlinkForNonWindowsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require additional Windows privileges")
	}

	source := createPayloadFixture(t)
	if err := os.Symlink("game", filepath.Join(source, "bin", "game-link")); err != nil {
		t.Fatalf("create payload symlink: %v", err)
	}
	options := fixturePackOptions("linux", "amd64")
	options.SymlinkMode = bundle.PreserveSymlinks
	manifest, _, _, err := writeCompressedPayload(
		context.Background(),
		source,
		filepath.Join(t.TempDir(), "payload.tar.xz"),
		options,
		1<<20,
		"",
	)
	if err != nil {
		t.Fatalf("writeCompressedPayload: %v", err)
	}

	for _, entry := range manifest.Entries {
		if entry.Path == "bin/game-link" {
			if entry.Type != bundle.EntrySymlink || entry.LinkTarget != "game" {
				t.Fatalf("symlink entry = %#v", entry)
			}
			return
		}
	}
	t.Fatal("safe symlink is missing from generated manifest")
}

// GeneralsX @bugfix Codex 02/08/2026 Respect the Windows cache-override security contract in native launcher tests.
func TestRunBuildsRealPackedLauncherWithoutTouchingModule(t *testing.T) {
	if testing.Short() {
		t.Skip("real Go build is skipped in short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("Go compiler unavailable: %v", err)
	}

	root := t.TempDir()
	currentDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := findModuleRoot(currentDirectory)
	if moduleRoot == "" {
		t.Fatal("could not locate real SFX module")
	}
	sourceGeneratedPath := filepath.Join(moduleRoot, filepath.FromSlash(generatedPayloadDir))
	_, generatedErrBefore := os.Lstat(sourceGeneratedPath)
	sourceRoot := filepath.Join(root, "payload")
	if err := os.MkdirAll(filepath.Join(sourceRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceRoot, "bin", "game"), []byte("native game placeholder"), 0o700)
	if err := os.Mkdir(filepath.Join(sourceRoot, "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceRoot, "ignored", "secret.dat"), []byte("excluded"), 0o600)
	writeTestFile(t, filepath.Join(sourceRoot, "scratch.tmp"), []byte("excluded"), 0o600)
	symlinkCount := 0
	if runtime.GOOS != "windows" {
		if err := os.Symlink("game", filepath.Join(sourceRoot, "bin", "game-link")); err != nil {
			t.Fatalf("create payload symlink: %v", err)
		}
		symlinkCount = 1
	}
	excludePath := filepath.Join(root, "fixture.exclude")
	writeTestFile(t, excludePath, []byte("ignored/\n*.tmp\n"), 0o600)

	outputName := "launcher"
	if runtime.GOOS == "windows" {
		outputName += ".exe"
	}
	outputPath := filepath.Join(root, "output", outputName)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatal(err)
	}
	resolvedOutputParent, err := filepath.EvalSymlinks(filepath.Dir(outputPath))
	if err != nil {
		t.Fatalf("resolve expected output parent: %v", err)
	}
	canonicalOutputPath := filepath.Join(resolvedOutputParent, filepath.Base(outputPath))
	t.Setenv("SOURCE_DATE_EPOCH", "123456789")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err = run(
		context.Background(),
		[]string{
			"-source", sourceRoot,
			"-output", outputPath,
			"-target", runtime.GOOS + "/" + runtime.GOARCH,
			"-entry", "bin/game",
			"-workdir", "bin",
			"-product", "fixture-product",
			"-version", "1.2.3",
			"-exclude", excludePath,
			"-module", moduleRoot,
			"-max-embed-bytes", "1048576",
		},
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat packed launcher: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("packed launcher is empty")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("packed launcher mode %04o, want private executable mode 0700", info.Mode().Perm())
	}
	if got := strings.TrimSpace(stdout.String()); got != canonicalOutputPath {
		t.Fatalf("packer stdout = %q, want %q", got, canonicalOutputPath)
	}
	if !strings.Contains(stderr.String(), "packing ") || !strings.Contains(stderr.String(), "linking launcher") {
		t.Fatalf("packer progress output is incomplete: %q", stderr.String())
	}

	cacheRoot := filepath.Join(root, "cache")
	commandEnvironment := removeEnvironment(os.Environ(), "GX_SFX_CACHE")
	if runtime.GOOS == "windows" {
		localAppData := filepath.Join(root, "local-app-data")
		cacheRoot = filepath.Join(localAppData, "GeneralsX", "sfx")
		commandEnvironment = overrideEnvironment(commandEnvironment, map[string]string{
			"LOCALAPPDATA": localAppData,
		})
	} else {
		commandEnvironment = append(commandEnvironment, "GX_SFX_CACHE="+cacheRoot)
	}
	infoCommand := exec.Command(outputPath, "--sfx-info")
	infoCommand.Env = commandEnvironment
	infoOutput, err := infoCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("run packed launcher --sfx-info: %v\n%s", err, infoOutput)
	}
	infoText := string(infoOutput)
	for _, expected := range []string{
		"Product:             fixture-product",
		"Version:             1.2.3",
		"Target:              " + runtime.GOOS + "/" + runtime.GOARCH,
		"Entrypoint:          bin/game",
		"Working directory:   bin",
		"Cache root:          " + cacheRoot,
		fmt.Sprintf("Manifest entries:    %d", 2+symlinkCount),
	} {
		if !strings.Contains(infoText, expected) {
			t.Errorf("--sfx-info output missing %q:\n%s", expected, infoText)
		}
	}

	verifyCommand := exec.Command(outputPath, "--sfx-verify")
	verifyCommand.Env = commandEnvironment
	verifyOutput, err := verifyCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("run packed launcher --sfx-verify: %v\n%s", err, verifyOutput)
	}
	if !strings.Contains(string(verifyOutput), "embedded payload and every extracted file verified") {
		t.Fatalf("unexpected --sfx-verify output: %s", verifyOutput)
	}
	if _, err := os.Lstat(cacheRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection or verification unexpectedly created the cache root: %v", err)
	}

	_, generatedErrAfter := os.Lstat(sourceGeneratedPath)
	if !sameExistenceError(generatedErrBefore, generatedErrAfter) {
		t.Fatalf(
			"source generated directory changed: before %v, after %v",
			generatedErrBefore,
			generatedErrAfter,
		)
	}
}

func TestWriteCompressedPayloadHonorsCanceledContext(t *testing.T) {
	source := createPayloadFixture(t)
	output := filepath.Join(t.TempDir(), "payload.tar.xz")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := writeCompressedPayload(
		ctx,
		source,
		output,
		fixturePackOptions(runtime.GOOS, runtime.GOARCH),
		1<<20,
		"",
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeCompressedPayload error = %v, want context.Canceled", err)
	}
	assertNotExist(t, output)
}

func createPayloadFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "bin", "game"), []byte("#!/bin/sh\nexit 0\n"), 0o700)
	writeTestFile(t, filepath.Join(source, "data.txt"), []byte("deterministic data\n"), 0o600)
	return source
}

func fixturePackOptions(targetOS, targetArch string) bundle.PackOptions {
	return bundle.PackOptions{
		Product:     "fixture-product",
		Version:     "1.0.0",
		TargetOS:    targetOS,
		TargetArch:  targetArch,
		Entrypoint:  "bin/game",
		WorkDir:     "bin",
		Epoch:       time.Unix(123456789, 0).UTC(),
		SymlinkMode: bundle.RejectSymlinks,
	}
}

func writeTestFile(t *testing.T, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func assertNotExist(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%q exists or returned unexpected error: %v", name, err)
	}
}

func sameExistenceError(before, after error) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return errors.Is(before, os.ErrNotExist) && errors.Is(after, os.ErrNotExist)
}
