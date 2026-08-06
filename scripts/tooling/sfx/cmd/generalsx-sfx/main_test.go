package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/bundle"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/cache"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/launch"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/payload"
	"github.com/ulikunitz/xz"
)

func TestWindowsLaunchersEmbedGeneralsIcon(t *testing.T) {
	iconContents, err := os.ReadFile("generalsxzh.ico")
	if err != nil {
		t.Fatal(err)
	}
	canonicalIconContents, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "..", "..", "tools", "generalsx-build-desktop", "build", "windows", "icon.ico",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(iconContents, canonicalIconContents) {
		t.Fatal("Windows SFX icon does not match the canonical Automated Build Tool icon")
	}
	const wantIconSHA256 = "1f034656dcfcf24899abe765eaf497c1faceb91066cd10b6114dd5bc3e613b6e"
	iconSHA256 := sha256.Sum256(iconContents)
	if got := hex.EncodeToString(iconSHA256[:]); got != wantIconSHA256 {
		t.Fatalf("Generals icon SHA-256 = %s, want %s", got, wantIconSHA256)
	}
	iconImages := windowsIconImages(t, iconContents)
	resourceDefinition, err := os.ReadFile("windows_resources.rc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(resourceDefinition, []byte(`1 ICON "generalsxzh.ico"`)) {
		t.Fatal("Windows resource definition does not expose the Generals icon as group ID 1")
	}

	tests := []struct {
		arch    string
		machine uint16
	}{
		{arch: "amd64", machine: pe.IMAGE_FILE_MACHINE_AMD64},
		{arch: "386", machine: pe.IMAGE_FILE_MACHINE_I386},
	}
	for _, test := range tests {
		t.Run(test.arch, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "GeneralsXZH-sfx.exe")
			command := exec.Command(
				"go", "build", "-buildvcs=false", "-trimpath", "-o", output, ".",
			)
			command.Env = windowsLauncherTestEnvironment(test.arch)
			if buildOutput, err := command.CombinedOutput(); err != nil {
				t.Fatalf("cross-build Windows/%s SFX launcher: %v\n%s", test.arch, err, buildOutput)
			}

			executable, err := pe.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer executable.Close()
			if executable.FileHeader.Machine != test.machine {
				t.Fatalf("PE machine = %#x, want %#x", executable.FileHeader.Machine, test.machine)
			}
			resource := executable.Section(".rsrc")
			if resource == nil || resource.Size == 0 {
				t.Fatal("Windows SFX launcher has no icon resource section")
			}
			resourceContents, err := resource.Data()
			if err != nil {
				t.Fatal(err)
			}
			for index, iconImage := range iconImages {
				if !bytes.Contains(resourceContents, iconImage) {
					t.Fatalf("Windows SFX resource section does not contain canonical icon image %d", index)
				}
			}
			if !hasPEResource(resourceContents, 14, 1) {
				t.Fatal("Windows SFX resource section does not expose RT_GROUP_ICON ID 1")
			}
		})
	}
}

func windowsIconImages(t *testing.T, contents []byte) [][]byte {
	t.Helper()
	if len(contents) < 6 || binary.LittleEndian.Uint16(contents[0:2]) != 0 ||
		binary.LittleEndian.Uint16(contents[2:4]) != 1 {
		t.Fatal("Generals Windows icon has an invalid ICO header")
	}
	count := int(binary.LittleEndian.Uint16(contents[4:6]))
	if count != 6 || len(contents) < 6+count*16 {
		t.Fatalf("Generals Windows icon contains %d images, want 6", count)
	}
	wantDimensions := map[int]bool{16: true, 32: true, 48: true, 64: true, 128: true, 256: true}
	images := make([][]byte, 0, count)
	for index := 0; index < count; index++ {
		entry := contents[6+index*16 : 6+(index+1)*16]
		width := int(entry[0])
		height := int(entry[1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		if width != height || !wantDimensions[width] {
			t.Fatalf("Generals Windows icon image %d is %dx%d", index, width, height)
		}
		imageSize := int(binary.LittleEndian.Uint32(entry[8:12]))
		imageOffset := int(binary.LittleEndian.Uint32(entry[12:16]))
		if imageSize <= 0 || imageOffset < 0 || imageOffset > len(contents)-imageSize {
			t.Fatalf("Generals Windows icon image %d has an invalid payload range", index)
		}
		images = append(images, contents[imageOffset:imageOffset+imageSize])
		delete(wantDimensions, width)
	}
	if len(wantDimensions) != 0 {
		t.Fatalf("Generals Windows icon is missing dimensions: %v", wantDimensions)
	}
	return images
}

func hasPEResource(contents []byte, resourceType, resourceID uint16) bool {
	typeDirectory, found := peResourceDirectoryEntry(contents, 0, resourceType)
	if !found || typeDirectory&0x80000000 == 0 {
		return false
	}
	_, found = peResourceDirectoryEntry(contents, typeDirectory&0x7fffffff, resourceID)
	return found
}

func peResourceDirectoryEntry(contents []byte, offset uint32, identifier uint16) (uint32, bool) {
	start := int(offset)
	if start < 0 || start+16 > len(contents) {
		return 0, false
	}
	named := int(binary.LittleEndian.Uint16(contents[start+12 : start+14]))
	numeric := int(binary.LittleEndian.Uint16(contents[start+14 : start+16]))
	entryStart := start + 16
	entryEnd := entryStart + (named+numeric)*8
	if entryEnd < entryStart || entryEnd > len(contents) {
		return 0, false
	}
	for cursor := entryStart; cursor < entryEnd; cursor += 8 {
		name := binary.LittleEndian.Uint32(contents[cursor : cursor+4])
		if name&0x80000000 == 0 && name == uint32(identifier) {
			return binary.LittleEndian.Uint32(contents[cursor+4 : cursor+8]), true
		}
	}
	return 0, false
}

func windowsLauncherTestEnvironment(arch string) []string {
	overrides := map[string]string{
		"CGO_ENABLED":  "0",
		"GOARCH":       arch,
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFLAGS":      "",
		"GOOS":         "windows",
		"GOTOOLCHAIN":  "local",
		"GOWORK":       "off",
	}
	if arch == "amd64" {
		overrides["GOAMD64"] = "v1"
	} else {
		overrides["GO386"] = "sse2"
	}

	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, overridden := overrides[key]; !overridden {
			environment = append(environment, entry)
		}
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func TestParseActionForwardsGameArgumentsWithoutInterpretation(t *testing.T) {
	// GeneralsX @feature Codex 01/08/2026 Keep the Online endpoint override intact through standalone launchers.
	arguments := []string{
		"-win", "-onlineServer", "tls://online.example.org:30000",
		"argument with spaces", "$(not-a-shell)", "semi;colon",
	}
	got, err := parseAction(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != actionLaunch {
		t.Fatalf("kind = %v, want launch", got.kind)
	}
	if !reflect.DeepEqual(got.gameArgs, arguments) {
		t.Fatalf("game args = %#v, want %#v", got.gameArgs, arguments)
	}

	got, err = parseAction(append([]string{"--"}, arguments...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.gameArgs, arguments) {
		t.Fatalf("escaped game args = %#v, want %#v", got.gameArgs, arguments)
	}
}

func TestParseActionRejectsInvalidExtract(t *testing.T) {
	if _, err := parseAction([]string{"--sfx-extract"}); err == nil {
		t.Fatal("parseAction accepted --sfx-extract without a destination")
	}
	if _, err := parseAction([]string{"--sfx-info", "extra"}); err == nil {
		t.Fatal("parseAction accepted an extra --sfx-info argument")
	}
}

// GeneralsX @feature Codex 04/08/2026 Preserve Online server CLI arguments exactly while allowing one explicit separator.
func TestParseActionForwardsOnlineServerArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      []string
	}{
		{name: "safe defaults requested", arguments: []string{"--sfx-server"}},
		{
			name:      "direct arguments",
			arguments: []string{"--sfx-server", "--control-listen", "0.0.0.0:29900", "literal;value"},
			want:      []string{"--control-listen", "0.0.0.0:29900", "literal;value"},
		},
		{
			name:      "separator",
			arguments: []string{"--sfx-server", "--", "--help"},
			want:      []string{"--help"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAction(test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			if got.kind != actionServer {
				t.Fatalf("kind = %v, want Online server", got.kind)
			}
			if !reflect.DeepEqual(got.serverArgs, test.want) {
				t.Fatalf("server args = %#v, want %#v", got.serverArgs, test.want)
			}
		})
	}
}

func TestOnlineServerHelpAndInfo(t *testing.T) {
	var help bytes.Buffer
	writeHelp(&help)
	for _, expected := range []string{
		"--sfx-server [--] [SERVER_ARGUMENT...]",
		"listeners bind to loopback",
		"Other server arguments do not disable",
	} {
		if !strings.Contains(help.String(), expected) {
			t.Errorf("help is missing %q:\n%s", expected, help.String())
		}
	}

	withoutServer := makeTestBundle(t)
	var info bytes.Buffer
	writeInfo(&info, withoutServer, "/cache")
	if !strings.Contains(info.String(), "Online server:       <not included>") {
		t.Fatalf("legacy info does not report an absent Online server:\n%s", info.String())
	}

	withServer := makeTestBundleWithOnlineServer(t)
	info.Reset()
	writeInfo(&info, withServer, "/cache")
	if !strings.Contains(
		info.String(),
		"Online server:       "+withServer.manifest.OnlineServerEntrypoint,
	) {
		t.Fatalf("Online server info is missing its entrypoint:\n%s", info.String())
	}
}

func TestPrepareOnlineServerCommandUsesSafeDefaultsAndPrivateState(t *testing.T) {
	embedded := makeTestBundleWithOnlineServer(t)
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	if err := extractEmbeddedPayload(context.Background(), embedded, runtimeRoot); err != nil {
		t.Fatal(err)
	}
	manager, err := cache.New(cache.Options{Root: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}

	command, err := prepareOnlineServerCommand(
		context.Background(),
		manager,
		runtimeRoot,
		embedded.manifest,
		nil,
	)
	if err != nil {
		t.Fatalf("prepareOnlineServerCommand() error = %v", err)
	}
	stateDir := filepath.Join(
		manager.Root(),
		embedded.manifest.Product,
		".runtime-state",
		"online-server",
	)
	resolvedStateDir, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if command.Dir != resolvedStateDir {
		t.Fatalf("command.Dir = %q, want %q", command.Dir, resolvedStateDir)
	}
	wantArguments := []string{
		"--control-listen", "127.0.0.1:29900",
		"--relay-listen", "127.0.0.1:27901",
		"--health-listen", "127.0.0.1:8080",
		"--public-host", "127.0.0.1",
		"--data-file", "profiles.db",
	}
	if !reflect.DeepEqual(command.Args[1:], wantArguments) {
		t.Fatalf("safe default arguments = %#v, want %#v", command.Args[1:], wantArguments)
	}
	environment := environmentValues(command.Env)
	if environment["PWD"] != resolvedStateDir {
		t.Fatalf("PWD = %q, want %q", environment["PWD"], resolvedStateDir)
	}
	if strings.HasPrefix(resolvedStateDir, runtimeRoot+string(filepath.Separator)) {
		t.Fatalf("Online server state %q is inside payload %q", resolvedStateDir, runtimeRoot)
	}

	customArguments := []string{
		"--control-listen", "0.0.0.0:29900",
		"--data-file", "custom.db",
		"literal;value",
	}
	custom, err := prepareOnlineServerCommand(
		context.Background(),
		manager,
		runtimeRoot,
		embedded.manifest,
		customArguments,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCustomArguments := []string{
		"--relay-listen", "127.0.0.1:27901",
		"--health-listen", "127.0.0.1:8080",
		"--public-host", "127.0.0.1",
		"--control-listen", "0.0.0.0:29900",
		"--data-file", "custom.db",
		"literal;value",
	}
	if !reflect.DeepEqual(custom.Args[1:], wantCustomArguments) {
		t.Fatalf("custom server arguments = %#v, want %#v", custom.Args[1:], wantCustomArguments)
	}
}

// GeneralsX @bugfix Codex 04/08/2026 Keep every unspecified embedded-server safety default when custom flags are present.
func TestOnlineServerArgumentsMergesSafetyDefaultsPerFlag(t *testing.T) {
	allDefaults := []string{
		"--control-listen", "127.0.0.1:29900",
		"--relay-listen", "127.0.0.1:27901",
		"--health-listen", "127.0.0.1:8080",
		"--public-host", "127.0.0.1",
		"--data-file", "profiles.db",
	}
	tests := []struct {
		name      string
		requested []string
		want      []string
	}{
		{
			name:      "unrelated custom argument retains every default",
			requested: []string{"--max-online-players", "16"},
			want:      append(append([]string(nil), allDefaults...), "--max-online-players", "16"),
		},
		{
			name: "split overrides replace only their defaults",
			requested: []string{
				"--control-listen", "0.0.0.0:30000",
				"--data-file", "custom.db",
			},
			want: []string{
				"--relay-listen", "127.0.0.1:27901",
				"--health-listen", "127.0.0.1:8080",
				"--public-host", "127.0.0.1",
				"--control-listen", "0.0.0.0:30000",
				"--data-file", "custom.db",
			},
		},
		{
			name: "equals overrides replace only their defaults",
			requested: []string{
				"--relay-listen=0.0.0.0:27901",
				"--health-listen=0.0.0.0:8080",
				"--public-host=online.example.test",
			},
			want: []string{
				"--control-listen", "127.0.0.1:29900",
				"--data-file", "profiles.db",
				"--relay-listen=0.0.0.0:27901",
				"--health-listen=0.0.0.0:8080",
				"--public-host=online.example.test",
			},
		},
		{
			name:      "separator prevents a positional token from disabling a default",
			requested: []string{"--", "--control-listen", "0.0.0.0:30000"},
			want:      append(append([]string(nil), allDefaults...), "--", "--control-listen", "0.0.0.0:30000"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestedBefore := append([]string(nil), test.requested...)
			got := onlineServerArguments(test.requested)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("onlineServerArguments() = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(test.requested, requestedBefore) {
				t.Fatalf("onlineServerArguments() mutated input: got %#v, want %#v", test.requested, requestedBefore)
			}
		})
	}
}

func TestPrepareOnlineServerCommandRequiresManifestEntrypoint(t *testing.T) {
	manager, err := cache.New(cache.Options{Root: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareOnlineServerCommand(
		context.Background(),
		manager,
		t.TempDir(),
		bundle.Manifest{Product: "fixture"},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("missing Online server entrypoint error = %v", err)
	}
}

func TestExtractEmbeddedPayloadAndValidateRuntime(t *testing.T) {
	embedded := makeTestBundle(t)
	destination := filepath.Join(t.TempDir(), "runtime")

	if err := extractEmbeddedPayload(context.Background(), embedded, destination); err != nil {
		t.Fatalf("extractEmbeddedPayload() error = %v", err)
	}
	if err := validateExtractedRuntime(context.Background(), destination, embedded.manifest); err != nil {
		t.Fatalf("validateExtractedRuntime() error = %v", err)
	}

	entrypoint := filepath.Join(destination, "bin", "game")
	if err := os.WriteFile(entrypoint, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractedRuntime(context.Background(), destination, embedded.manifest); err == nil {
		t.Fatal("validateExtractedRuntime accepted a tampered entrypoint")
	}
}

// GeneralsX @feature Codex 01/08/2026 Keep the macOS progress window scoped to real cache-miss extraction work.
func TestEnsureCachedRuntimeReportsOnlyColdExtraction(t *testing.T) {
	embedded := makeTestBundle(t)
	manager, err := cache.New(cache.Options{Root: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}

	var reporters []*recordingExtractionReporter
	openProgress := func() extractionProgressReporter {
		reporter := &recordingExtractionReporter{}
		reporters = append(reporters, reporter)
		return reporter
	}
	var stderr bytes.Buffer

	first, err := ensureCachedRuntime(
		context.Background(),
		manager,
		embedded,
		&stderr,
		openProgress,
	)
	if err != nil {
		t.Fatalf("cold ensureCachedRuntime() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 1 {
		t.Fatalf("cold progress reporter count = %d, want 1", len(reporters))
	}
	reporter := reporters[0]
	if !reporter.closed {
		t.Fatal("cold progress reporter was not closed")
	}
	if !reporter.completed {
		t.Fatal("successful cold extraction was not marked complete")
	}
	if len(reporter.indeterminate) < 2 ||
		reporter.indeterminate[0] != "Checking game package..." ||
		!containsString(reporter.indeterminate, "Verifying game files...") {
		t.Fatalf("cold indeterminate phases = %#v", reporter.indeterminate)
	}
	if len(reporter.updates) == 0 {
		t.Fatal("cold extraction emitted no progress updates")
	}
	last := reporter.updates[len(reporter.updates)-1]
	if last.label != "Extracting game files..." ||
		last.completed != embedded.manifest.TotalSize ||
		last.total != embedded.manifest.TotalSize {
		t.Fatalf("last cold extraction update = %#v", last)
	}

	second, err := ensureCachedRuntime(
		context.Background(),
		manager,
		embedded,
		io.Discard,
		openProgress,
	)
	if err != nil {
		t.Fatalf("warm ensureCachedRuntime() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 1 {
		t.Fatalf("warm cache opened a progress reporter; count = %d", len(reporters))
	}
	if !strings.Contains(stderr.String(), "first launch; extracting") {
		t.Fatalf("cold extraction diagnostic = %q", stderr.String())
	}
}

// GeneralsX @feature Codex 01/08/2026 Dismiss progress when authenticated extraction fails.
func TestEnsureCachedRuntimeClosesProgressAfterExtractionFailure(t *testing.T) {
	embedded := makeTestBundle(t)
	embedded.manifest.PayloadSHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	manager, err := cache.New(cache.Options{Root: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingExtractionReporter{}

	leased, err := ensureCachedRuntime(
		context.Background(),
		manager,
		embedded,
		io.Discard,
		func() extractionProgressReporter { return reporter },
	)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("ensureCachedRuntime() error = %v", err)
	}
	if leased != nil {
		t.Fatal("failed extraction returned a leased runtime")
	}
	if !reporter.closed {
		t.Fatal("failed extraction left progress reporter open")
	}
	if reporter.completed {
		t.Fatal("failed extraction was marked complete")
	}
}

func TestExtractEmbeddedPayloadRejectsCompressedDigestMismatch(t *testing.T) {
	embedded := makeTestBundle(t)
	embedded.manifest.PayloadSHA256 = hex.EncodeToString(make([]byte, sha256.Size))

	destination := filepath.Join(t.TempDir(), "runtime")
	err := extractEmbeddedPayload(context.Background(), embedded, destination)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("extractEmbeddedPayload digest error = %v", err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("digest rejection created extraction destination: %v", statErr)
	}
}

func TestExtractEmbeddedPayloadRejectsTrailingBytesBeforeExtraction(t *testing.T) {
	embedded := makeTestBundle(t)
	archive := embeddedPayloadBytes(t, embedded)
	archive = append(archive, []byte("unauthenticated trailing data")...)
	embedded.files = fstest.MapFS{
		payload.ManifestPath: &fstest.MapFile{Data: embedded.manifestBytes, Mode: 0o444},
		payload.ArchivePath:  &fstest.MapFile{Data: archive, Mode: 0o444},
	}

	destination := filepath.Join(t.TempDir(), "runtime")
	err := extractEmbeddedPayload(context.Background(), embedded, destination)
	if err == nil || !strings.Contains(err.Error(), "payload size") {
		t.Fatalf("extractEmbeddedPayload trailing-data error = %v", err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("size rejection created extraction destination: %v", statErr)
	}
}

func TestExtractEmbeddedPayloadRejectsAuthenticatedConcatenatedXZ(t *testing.T) {
	embedded := makeTestBundle(t)
	stream := embeddedPayloadBytes(t, embedded)
	concatenated := append(append([]byte(nil), stream...), stream...)
	embedded = replaceEmbeddedPayload(t, embedded, concatenated)

	err := extractEmbeddedPayload(
		context.Background(),
		embedded,
		filepath.Join(t.TempDir(), "runtime"),
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected data after stream") {
		t.Fatalf("extractEmbeddedPayload concatenated-XZ error = %v", err)
	}
}

func TestExtractEmbeddedPayloadBoundsDecompressedOutput(t *testing.T) {
	embedded := makeTestBundle(t)
	decoder, err := xz.NewReader(bytes.NewReader(embeddedPayloadBytes(t, embedded)))
	if err != nil {
		t.Fatal(err)
	}
	tarPayload, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := maximumDecompressedPayloadSize(embedded.manifest)
	if err != nil {
		t.Fatal(err)
	}
	excess := limit + 1 - int64(len(tarPayload))
	if excess <= 0 {
		t.Fatalf("test tar is already %d bytes; limit is %d", len(tarPayload), limit)
	}
	tarPayload = append(tarPayload, make([]byte, int(excess))...)
	embedded = replaceEmbeddedPayload(t, embedded, compressTestXZ(t, tarPayload))

	err = extractEmbeddedPayload(
		context.Background(),
		embedded,
		filepath.Join(t.TempDir(), "runtime"),
	)
	if err == nil || !strings.Contains(err.Error(), "manifest-derived limit") {
		t.Fatalf("extractEmbeddedPayload decompression-limit error = %v", err)
	}
}

func TestValidateExtractedRuntimeRejectsUnexpectedPath(t *testing.T) {
	embedded := makeTestBundle(t)
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := extractEmbeddedPayload(context.Background(), embedded, destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "unexpected.dylib"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractedRuntime(context.Background(), destination, embedded.manifest); err == nil {
		t.Fatal("validateExtractedRuntime accepted an unexpected path")
	}
}

func TestValidateExtractedRuntimeRejectsSameSizeAssetTampering(t *testing.T) {
	embedded := makeTestBundle(t)
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := extractEmbeddedPayload(context.Background(), embedded, destination); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(destination, "asset.big")
	if err := os.WriteFile(asset, []byte("tampered data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractedRuntime(context.Background(), destination, embedded.manifest); err == nil {
		t.Fatal("validateExtractedRuntime accepted same-size asset tampering")
	}
}

func TestRuntimeWritesDoNotInvalidateExtractedRuntime(t *testing.T) {
	embedded := makeTestBundle(t)
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := extractEmbeddedPayload(context.Background(), embedded, destination); err != nil {
		t.Fatal(err)
	}

	command, err := launch.Prepare(launch.Config{
		Root:       destination,
		TargetOS:   embedded.manifest.TargetOS,
		Entrypoint: embedded.manifest.Entrypoint,
		WorkDir:    embedded.manifest.WorkDir,
		Env:        []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment := environmentValues(command.Env)
	stateDir := environment["DXVK_STATE_CACHE_PATH"]
	if stateDir == "" || environment["DXVK_LOG_PATH"] != stateDir {
		t.Fatalf("DXVK writable state environment = %#v", environment)
	}
	if command.Dir != stateDir {
		t.Fatalf("native working directory = %q, want runtime state %q", command.Dir, stateDir)
	}
	if relative, err := filepath.Rel(destination, stateDir); err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("DXVK state directory %q is inside runtime %q", stateDir, destination)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "GeneralsXZH.dxvk-cache"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "GeneralsXZH_d3d9.log"), []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, gameSpyState := range []string{"gp.info", "id.bin", "gstats.dat"} {
		if err := os.WriteFile(filepath.Join(command.Dir, gameSpyState), []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateExtractedRuntime(context.Background(), destination, embedded.manifest); err != nil {
		t.Fatalf("DXVK state outside the payload invalidated runtime reuse: %v", err)
	}
}

func TestLoadEmbeddedBundleRejectsDevelopmentFilesystem(t *testing.T) {
	_, err := loadEmbeddedBundle(fstest.MapFS{})
	if err == nil {
		t.Fatal("loadEmbeddedBundle accepted a filesystem without a payload")
	}
}

func TestSelectedCacheRootRejectsWindowsOverride(t *testing.T) {
	if _, err := selectedCacheRoot("windows", `C:\shared-cache`); err == nil {
		t.Fatal("selectedCacheRoot accepted a custom Windows cache root")
	}
	if got, err := selectedCacheRoot("windows", ""); err != nil || got != "" {
		t.Fatalf("selectedCacheRoot windows default = %q, %v", got, err)
	}
	if got, err := selectedCacheRoot("linux", "/private/cache"); err != nil || got != "/private/cache" {
		t.Fatalf("selectedCacheRoot Linux override = %q, %v", got, err)
	}
}

func TestDisplayCacheRootDoesNotCreateDirectory(t *testing.T) {
	requested := filepath.Join(t.TempDir(), "not-created")
	got, err := displayCacheRoot("linux", requested)
	if err != nil {
		t.Fatalf("displayCacheRoot: %v", err)
	}
	if got != requested {
		t.Fatalf("displayCacheRoot = %q, want %q", got, requested)
	}
	if _, err := os.Lstat(requested); !os.IsNotExist(err) {
		t.Fatalf("displayCacheRoot mutated %q: %v", requested, err)
	}
	if _, err := displayCacheRoot("windows", `C:\shared-cache`); err == nil {
		t.Fatal("displayCacheRoot accepted a custom Windows cache root")
	}
}

func TestValidateExtractedRuntimeHonorsCanceledContext(t *testing.T) {
	embedded := makeTestBundle(t)
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := extractEmbeddedPayload(context.Background(), embedded, destination); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateExtractedRuntime(ctx, destination, embedded.manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("validateExtractedRuntime error = %v, want context.Canceled", err)
	}
}

func environmentValues(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, present := strings.Cut(entry, "=")
		if present {
			values[key] = value
		}
	}
	return values
}

type extractionProgressUpdate struct {
	label     string
	completed int64
	total     int64
}

type recordingExtractionReporter struct {
	indeterminate []string
	updates       []extractionProgressUpdate
	completed     bool
	closed        bool
}

func (reporter *recordingExtractionReporter) Indeterminate(label string) {
	reporter.indeterminate = append(reporter.indeterminate, label)
}

func (reporter *recordingExtractionReporter) Update(label string, completed, total int64) {
	reporter.updates = append(reporter.updates, extractionProgressUpdate{
		label:     label,
		completed: completed,
		total:     total,
	})
}

func (reporter *recordingExtractionReporter) Complete() {
	reporter.completed = true
}

func (reporter *recordingExtractionReporter) Close() {
	reporter.closed = true
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func makeTestBundle(t *testing.T) embeddedBundle {
	return makeTestBundleConfigured(t, false)
}

func makeTestBundleWithOnlineServer(t *testing.T) embeddedBundle {
	return makeTestBundleConfigured(t, true)
}

func makeTestBundleConfigured(t *testing.T, includeOnlineServer bool) embeddedBundle {
	t.Helper()

	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "game"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "asset.big"), []byte("fixture asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	onlineServerEntrypoint := ""
	if includeOnlineServer {
		onlineServerEntrypoint = "online-server/generals-server"
		if runtime.GOOS == "windows" {
			onlineServerEntrypoint += ".exe"
		}
		serverPath := filepath.Join(source, filepath.FromSlash(onlineServerEntrypoint))
		if err := os.MkdirAll(filepath.Dir(serverPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(serverPath, []byte("native server placeholder"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var compressed bytes.Buffer
	xzWriter, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := bundle.WriteTar(source, xzWriter, bundle.PackOptions{
		Product:                "GeneralsXZH-test",
		Version:                "test",
		TargetOS:               runtime.GOOS,
		TargetArch:             runtime.GOARCH,
		Entrypoint:             "bin/game",
		WorkDir:                "bin",
		OnlineServerEntrypoint: onlineServerEntrypoint,
		Epoch:                  time.Unix(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatal(err)
	}

	digest := sha256.Sum256(compressed.Bytes())
	manifest.Compression = "xz"
	manifest.PayloadSHA256 = hex.EncodeToString(digest[:])
	manifest.PayloadSize = int64(compressed.Len())
	manifestBytes, err := bundle.MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)

	files := fstest.MapFS{
		payload.ManifestPath: &fstest.MapFile{Data: manifestBytes, Mode: 0o444},
		payload.ArchivePath:  &fstest.MapFile{Data: compressed.Bytes(), Mode: 0o444},
	}
	return embeddedBundle{
		files:          files,
		manifest:       manifest,
		manifestBytes:  manifestBytes,
		manifestDigest: hex.EncodeToString(manifestDigest[:]),
	}
}

func embeddedPayloadBytes(t *testing.T, embedded embeddedBundle) []byte {
	t.Helper()
	data, err := fs.ReadFile(embedded.files, payload.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func replaceEmbeddedPayload(
	t *testing.T,
	embedded embeddedBundle,
	compressed []byte,
) embeddedBundle {
	t.Helper()
	digest := sha256.Sum256(compressed)
	embedded.manifest.PayloadSHA256 = hex.EncodeToString(digest[:])
	embedded.manifest.PayloadSize = int64(len(compressed))
	manifestBytes, err := bundle.MarshalManifest(embedded.manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	embedded.manifestBytes = manifestBytes
	embedded.manifestDigest = hex.EncodeToString(manifestDigest[:])
	embedded.files = fstest.MapFS{
		payload.ManifestPath: &fstest.MapFile{Data: manifestBytes, Mode: 0o444},
		payload.ArchivePath:  &fstest.MapFile{Data: compressed, Mode: 0o444},
	}
	return embedded
}

func compressTestXZ(t *testing.T, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
