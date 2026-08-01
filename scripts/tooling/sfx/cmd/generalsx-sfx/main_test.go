package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/bundle"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/launch"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/payload"
	"github.com/ulikunitz/xz"
)

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

func makeTestBundle(t *testing.T) embeddedBundle {
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

	var compressed bytes.Buffer
	xzWriter, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := bundle.WriteTar(source, xzWriter, bundle.PackOptions{
		Product:    "GeneralsXZH-test",
		Version:    "test",
		TargetOS:   runtime.GOOS,
		TargetArch: runtime.GOARCH,
		Entrypoint: "bin/game",
		WorkDir:    "bin",
		Epoch:      time.Unix(0, 0),
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
