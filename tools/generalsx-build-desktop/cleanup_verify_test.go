package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeMacOSSFXAppFixture(t *testing.T, plistOverrides map[string]string) string {
	t.Helper()
	values := map[string]string{
		"CFBundleIdentifier": macOSSFXBundleIdentifier,
		"CFBundleExecutable": macOSSFXExecutableName,
		"CFBundleIconFile":   macOSSFXIconName,
	}
	for key, value := range plistOverrides {
		values[key] = value
	}

	app := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	for _, directory := range []string{
		filepath.Join(app, "Contents", "MacOS"),
		filepath.Join(app, "Contents", "Resources"),
		filepath.Join(app, "Contents", "Helpers"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>%s</string>
  <key>CFBundleExecutable</key><string>%s</string>
  <key>CFBundleIconFile</key><string>%s</string>
</dict>
</plist>
`, values["CFBundleIdentifier"], values["CFBundleExecutable"], values["CFBundleIconFile"])
	files := []struct {
		path     string
		contents []byte
		mode     os.FileMode
	}{
		{filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644},
		{
			filepath.Join(app, "Contents", "MacOS", macOSSFXExecutableName),
			[]byte("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = \"--sfx-verify\" ]\n"),
			0o755,
		},
		{filepath.Join(app, "Contents", "Resources", macOSSFXIconName), macOSSFXFixtureICNS(t), 0o644},
		{
			filepath.Join(app, "Contents", "Helpers", macOSSFXProgressHelperName),
			[]byte("#!/bin/sh\nexit 0\n"),
			0o755,
		},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.contents, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func macOSSFXFixtureICNS(t *testing.T) []byte {
	t.Helper()
	icon := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			icon.SetRGBA(x, y, color.RGBA{R: 0x12, G: 0x66, B: 0xcc, A: 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, icon); err != nil {
		t.Fatal(err)
	}
	return macOSSFXICNSWithPNG(t, "ic11", encoded.Bytes())
}

func macOSSFXICNSWithPNG(t *testing.T, elementType string, payload []byte) []byte {
	t.Helper()
	if len(elementType) != 4 || len(payload) == 0 {
		t.Fatal("invalid ICNS fixture element")
	}
	contents := make([]byte, 16+len(payload))
	copy(contents, "icns")
	binary.BigEndian.PutUint32(contents[4:], uint32(len(contents)))
	copy(contents[8:], elementType)
	binary.BigEndian.PutUint32(contents[12:], uint32(8+len(payload)))
	copy(contents[16:], payload)
	return contents
}

// GeneralsX @test Codex 05/08/2026 Exercise app layout resolution without requiring macOS or codesign.
func TestResolveMacOSSFXAppFindsDirectExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires POSIX executable permission bits")
	}
	t.Parallel()
	path := writeMacOSSFXAppFixture(t, nil)
	app, err := resolveMacOSSFXApp(path)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if app.bundlePath != resolvedPath {
		t.Fatalf("bundle path = %q, want %q", app.bundlePath, resolvedPath)
	}
	wantExecutable := filepath.Join(resolvedPath, "Contents", "MacOS", macOSSFXExecutableName)
	if app.executablePath != wantExecutable {
		t.Fatalf("executable path = %q, want %q", app.executablePath, wantExecutable)
	}
}

func TestResolveMacOSSFXAppRejectsInvalidBundles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires POSIX executable permission bits")
	}
	t.Parallel()
	tests := []struct {
		name      string
		overrides map[string]string
		mutate    func(*testing.T, string)
		want      string
	}{
		{
			name: "bundle identifier",
			overrides: map[string]string{
				"CFBundleIdentifier": "com.example.untrusted",
			},
			want: "CFBundleIdentifier",
		},
		{
			name: "bundle executable",
			overrides: map[string]string{
				"CFBundleExecutable": "UnexpectedLauncher",
			},
			want: "CFBundleExecutable",
		},
		{
			name: "bundle icon",
			overrides: map[string]string{
				"CFBundleIconFile": "Unexpected.icns",
			},
			want: "CFBundleIconFile",
		},
		{
			name: "missing plist",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.Remove(filepath.Join(app, "Contents", "Info.plist")); err != nil {
					t.Fatal(err)
				}
			},
			want: "Info.plist",
		},
		{
			name: "malformed plist",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("<not-plist/>"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "plist root",
		},
		{
			name: "required values nested outside root dictionary",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Nested</key>
  <dict>
    <key>CFBundleIdentifier</key><string>com.generalsx.generalsxzh.sfx</string>
    <key>CFBundleExecutable</key><string>GeneralsXZH</string>
    <key>CFBundleIconFile</key><string>GeneralsXZH.icns</string>
  </dict>
</dict>
</plist>
`
				if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "CFBundleIdentifier",
		},
		{
			name: "missing executable",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.Remove(filepath.Join(app, "Contents", "MacOS", macOSSFXExecutableName)); err != nil {
					t.Fatal(err)
				}
			},
			want: "GeneralsXZH",
		},
		{
			name: "empty icon",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(app, "Contents", "Resources", macOSSFXIconName), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "empty",
		},
		{
			name: "invalid icon container",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(app, "Contents", "Resources", macOSSFXIconName), []byte("not an icns"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "ICNS",
		},
		{
			name: "header-only icon container",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				contents := make([]byte, 8)
				copy(contents, "icns")
				binary.BigEndian.PutUint32(contents[4:], uint32(len(contents)))
				if err := os.WriteFile(filepath.Join(app, "Contents", "Resources", macOSSFXIconName), contents, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "no supported ICNS image",
		},
		{
			name: "truncated PNG icon representation",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				valid := macOSSFXFixtureICNS(t)
				const pngHeaderThroughIHDR = 33
				truncated := macOSSFXICNSWithPNG(t, "ic11", valid[16:16+pngHeaderThroughIHDR])
				if err := os.WriteFile(filepath.Join(app, "Contents", "Resources", macOSSFXIconName), truncated, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a complete 32x32 PNG",
		},
		{
			name: "non-executable helper",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				if err := os.Chmod(filepath.Join(app, "Contents", "Helpers", macOSSFXProgressHelperName), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "not executable",
		},
		{
			name: "symlink executable",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				executable := filepath.Join(app, "Contents", "MacOS", macOSSFXExecutableName)
				target := filepath.Join(app, "Contents", "Helpers", macOSSFXProgressHelperName)
				if err := os.Remove(executable); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, executable); err != nil {
					t.Skipf("create symlink fixture: %v", err)
				}
			},
			want: "not a real file",
		},
		{
			name: "symlink contents directory",
			mutate: func(t *testing.T, app string) {
				t.Helper()
				contents := filepath.Join(app, "Contents")
				relocated := filepath.Join(filepath.Dir(app), "relocated-contents")
				if err := os.Rename(contents, relocated); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(relocated, contents); err != nil {
					t.Skipf("create symlink fixture: %v", err)
				}
			},
			want: "not a real directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app := writeMacOSSFXAppFixture(t, test.overrides)
			if test.mutate != nil {
				test.mutate(t, app)
			}
			_, err := resolveMacOSSFXApp(app)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolve invalid app error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestResolveMacOSSFXAppRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires POSIX executable permission bits")
	}
	t.Parallel()
	target := writeMacOSSFXAppFixture(t, nil)
	link := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}
	if _, err := resolveMacOSSFXApp(link); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("resolve symlink app error = %v", err)
	}
}

func TestVerifyMacOSSFXAppChecksSignatureThenNestedPayload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	t.Parallel()
	path := writeMacOSSFXAppFixture(t, nil)
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	signatureChecks := 0
	err = verifyMacOSSFXApp(context.Background(), path, func(ctx context.Context, got string) error {
		signatureChecks++
		if err := ctx.Err(); err != nil {
			return err
		}
		if got != resolvedPath {
			return fmt.Errorf("signature path = %q, want %q", got, resolvedPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if signatureChecks != 1 {
		t.Fatalf("signature checks = %d, want 1", signatureChecks)
	}
}

// GeneralsX @test Codex 05/08/2026 Exercise production codesign and nested verification before atomic app publication.
func TestProductionMacOSSFXAppVerificationAndCopy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("production macOS code-signature verification requires Darwin")
	}
	app := writeMacOSSFXAppFixture(t, nil)
	source := filepath.Join(t.TempDir(), "main.go")
	program := `package main

import "os"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--sfx-verify" {
		return
	}
	os.Exit(2)
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(app, "Contents", "MacOS", macOSSFXExecutableName)
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", executable, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture SFX executable: %v\n%s", err, output)
	}
	executableContents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(app, "Contents", "Helpers", macOSSFXProgressHelperName)
	if err := os.WriteFile(helper, executableContents, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalIcon, err := filepath.Abs(filepath.Join("..", "..", "assets", "generalsx-zh_icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	resizedIcon := filepath.Join(t.TempDir(), "GeneralsXZH-1024.png")
	command = exec.Command("/usr/bin/sips", "-z", "1024", "1024", canonicalIcon, "--out", resizedIcon)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render canonical Generals icon: %v\n%s", err, output)
	}
	iconContents, err := os.ReadFile(resizedIcon)
	if err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(app, "Contents", "Resources", macOSSFXIconName)
	if err := os.WriteFile(icon, macOSSFXICNSWithPNG(t, "ic10", iconContents), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{executable, helper} {
		command = exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("sign fixture executable %q: %v\n%s", path, err, output)
		}
	}
	command = exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", app)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sign fixture application: %v\n%s", err, output)
	}

	ctx := context.Background()
	if err := verifySFXArtifact(ctx, app, "macos", "darwin"); err != nil {
		t.Fatalf("verify signed source application: %v", err)
	}
	completed, err := inspectCompletedArtifact("job-signed-app", app)
	if err != nil {
		t.Fatal(err)
	}
	completed.target = "macos"
	desktop := t.TempDir()
	verifier := func(ctx context.Context, path, target string) error {
		return verifySFXArtifact(ctx, path, target, "darwin")
	}
	destination, err := copyCompletedArtifactToDirectory(ctx, completed, desktop, verifier)
	if err != nil {
		t.Fatalf("copy signed application: %v", err)
	}
	if destination != filepath.Join(desktop, "GeneralsXZH.app") {
		t.Fatalf("Desktop application = %q", destination)
	}
	if err := verifySFXArtifact(ctx, destination, "macos", "darwin"); err != nil {
		t.Fatalf("verify published application: %v", err)
	}
}

func TestVerifyMacOSSFXAppStopsAfterSignatureFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires POSIX executable permission bits")
	}
	t.Parallel()
	path := writeMacOSSFXAppFixture(t, nil)
	want := errors.New("invalid signature")
	err := verifyMacOSSFXApp(context.Background(), path, func(context.Context, string) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("signature failure = %v, want %v", err, want)
	}
}

func TestMacOSSFXCodeSignatureArgumentsRequireStrictDeepVerification(t *testing.T) {
	t.Parallel()
	path := "/Applications/GeneralsXZH.app"
	got := strings.Join(macOSSFXCodeSignatureArguments(path), " ")
	want := "--verify --deep --strict --verbose=2 " + path
	if got != want {
		t.Fatalf("codesign arguments = %q, want %q", got, want)
	}
}

func TestVerifySFXArtifactRunsNativeTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "verify-sfx")
	contents := []byte("#!/bin/sh\n[ \"$1\" = \"--sfx-verify\" ]\n")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifySFXArtifact(context.Background(), path, targetForHost(runtime.GOOS), runtime.GOOS); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySFXArtifactRejectsUnsupportedHostTargetPair(t *testing.T) {
	t.Parallel()
	err := verifySFXArtifact(context.Background(), "unused", "windows", "darwin")
	if err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("verify mismatch error = %v", err)
	}
}

func TestVerifySFXArtifactPropagatesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "verify-sfx")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := verifySFXArtifact(ctx, path, targetForHost(runtime.GOOS), runtime.GOOS)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled verifier error = %v", err)
	}
}

func TestLinuxSFXDockerVerifyArgumentsAreHardened(t *testing.T) {
	t.Parallel()
	arguments := linuxSFXDockerVerifyArguments("/Desktop/game", "/tmp/verify", "/tmp/cid", "builder:test", "501:20")
	joined := strings.Join(arguments, " ")
	for _, required := range []string{
		"--pull=never", "--platform linux/amd64", "--network none", "--read-only",
		"--cap-drop ALL", "--security-opt no-new-privileges", "--user 501:20",
		"/Desktop/game:/sfx:ro", "/tmp/verify:/tmp:rw", "--entrypoint /sfx", "builder:test --sfx-verify",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("Docker verifier arguments %q omit %q", joined, required)
		}
	}
}
