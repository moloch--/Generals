package buildcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

func TestPrebuiltArgumentsKeepSourceDefaultAndRejectConflicts(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil || cfg.buildMode != "source" {
		t.Fatalf("source default: %+v, %v", cfg, err)
	}
	base := []string{"--build-mode", "prebuilt", "--target", "windows", "--output", filepath.Join(t.TempDir(), "game.exe")}
	cfg, err = parseConfig(base, io.Discard)
	if err != nil || cfg.runtimeVersion != "stable" {
		t.Fatalf("prebuilt defaults: %+v, %v", cfg, err)
	}
	for _, args := range [][]string{
		{"--source-ref", "main"}, {"--repo", t.TempDir()}, {"--source-repo", "https://github.com/other/repo"},
		{"--with-online-server"}, {"--skip-game-build"}, {"--keep-windows-stage"},
		{"--online-endpoint", "tls://custom.example"}, {"--runtime-offline"},
		{"--runtime-repo", "../repo"}, {"--runtime-repo", "https://github.com/owner/repo"}, {"--runtime-version", "main"},
	} {
		if _, err := parseConfig(append(append([]string{}, base...), args...), io.Discard); err == nil {
			t.Errorf("accepted incompatible arguments %q", args)
		}
	}
	if _, err := parseConfig(append(base, "--runtime-offline", "--runtime-version", "runtime-v1.2.3"), io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestPrebuiltDryRunDoesNotPrepareSourceOrEngineTools(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var output bytes.Buffer
	app := application{cfg: config{buildMode: "prebuilt", runtimeRepository: "owner/repo", runtimeVersion: "stable", target: targetWindows, cacheDir: filepath.Join(root, "cache"), assetsDir: filepath.Join(root, "assets"), output: filepath.Join(root, "output", "game.exe"), dryRun: true}, hostOS: "windows", hostArch: "amd64", runner: runner{dryRun: true, stdout: &output, stderr: &output}}
	if err := app.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"git clone", "submodule", "cmake", "vcpkg", "docker", "cl.exe", "go build"} {
		if strings.Contains(output.String(), forbidden) {
			t.Errorf("dry run unexpectedly prepares %q: %s", forbidden, output.String())
		}
	}
	if !strings.Contains(output.String(), "--sfx-verify") {
		t.Fatal("missing output verification")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry run mutated workspace: %v, %v", entries, err)
	}
	app.hostOS = "darwin"
	app.hostArch = "arm64"
	app.cfg.target = targetLinux
	if err := app.validateHostTarget(); err == nil {
		t.Fatal("prebuilt cross-host verification accepted")
	}
}

type runtimeRoundTripper func(*http.Request) (*http.Response, error)

func (transport runtimeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func runtimeTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header)}
}

func TestRuntimeSelectionIgnoresBuilderAndPreviewReleases(t *testing.T) {
	t.Parallel()
	body := `[{"tag_name":"v99.0.0","published_at":"2026-10-10T00:00:00Z"},{"tag_name":"runtime-v2.0.0","prerelease":true,"published_at":"2026-10-10T00:00:00Z"},{"tag_name":"runtime-v1.1.0","draft":true,"published_at":"2026-10-10T00:00:00Z"},{"tag_name":"runtime-v1.0.0","published_at":"2026-10-09T00:00:00Z"}]`
	app := application{cfg: config{runtimeVersion: "stable", runtimeRepository: "owner/repo"}, http: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/repos/owner/repo/releases?per_page=100&page=1" {
			t.Errorf("unexpected URL %s", request.URL)
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("public runtime request included credentials")
		}
		return runtimeTestResponse(body), nil
	})}}
	release, err := app.resolveRuntimeRelease(context.Background())
	if err != nil || release.Tag != "runtime-v1.0.0" {
		t.Fatalf("release = %+v, %v", release, err)
	}
	app.cfg.runtimeVersion = "runtime-v1.0.0"
	app.http.Transport = runtimeRoundTripper(func(*http.Request) (*http.Response, error) {
		return runtimeTestResponse(`{"tag_name":"runtime-v9.0.0"}`), nil
	})
	if _, err := app.resolveRuntimeRelease(context.Background()); err == nil {
		t.Fatal("accepted different exact version")
	}
}

func TestRuntimeDownloadsAreBoundedAndDoNotReplaceOnFailure(t *testing.T) {
	t.Parallel()
	payload := "verified runtime archive"
	digest := sha256.Sum256([]byte(payload))
	descriptor := runtimebundle.Descriptor{Archive: runtimebundle.Archive{Name: "runtime.tar.gz", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}
	destination := filepath.Join(t.TempDir(), "runtime.tar.gz")
	if err := os.WriteFile(destination, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"wrong", payload + "trailing", strings.Repeat("x", len(payload))} {
		app := application{http: &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) {
			response := runtimeTestResponse(body)
			response.ContentLength = -1
			return response, nil
		})}}
		if err := app.downloadRuntime(context.Background(), "https://github.com/owner/repo/releases/download/runtime-v1.0.0/runtime.tar.gz", destination, descriptor); err == nil {
			t.Fatalf("accepted invalid payload %q", body)
		}
		contents, err := os.ReadFile(destination)
		if err != nil || string(contents) != "previous" {
			t.Fatalf("failed download changed existing archive: %q, %v", contents, err)
		}
	}
	app := application{http: &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) { return runtimeTestResponse(payload), nil })}}
	var wait sync.WaitGroup
	for i := 0; i < 3; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := app.downloadRuntime(context.Background(), "https://github.com/owner/repo/releases/download/runtime-v1.0.0/runtime.tar.gz", destination, descriptor); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if err := verifyRuntimeArchive(destination, descriptor); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(destination))
	if len(entries) != 1 {
		t.Fatalf("download left temporary files: %v", entries)
	}
}

func TestRuntimeMetadataLimitsAndRedirects(t *testing.T) {
	t.Parallel()
	app := application{http: &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) { return runtimeTestResponse("oversized"), nil })}}
	if _, err := app.runtimeGET(context.Background(), "https://api.github.com/repos/owner/repo/releases", 3); err == nil {
		t.Fatal("accepted oversized metadata")
	}
	request, _ := http.NewRequest(http.MethodGet, "http://example.invalid/archive", nil)
	if err := app.runtimeHTTP().CheckRedirect(request, nil); err == nil {
		t.Fatal("accepted HTTPS downgrade")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.http.Transport = runtimeRoundTripper(func(request *http.Request) (*http.Response, error) { return nil, request.Context().Err() })
	if _, err := app.runtimeGET(ctx, "https://api.github.com/repos/owner/repo/releases", 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestAssetCopyCancellationLeavesSourceUnchanged(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "asset.big")
	if err := os.WriteFile(source, []byte("owned asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyAssemblyFile(ctx, source, filepath.Join(t.TempDir(), "copy.big"), 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation=%v", err)
	}
	data, _ := os.ReadFile(source)
	if string(data) != "owned asset" {
		t.Fatal("copy changed original")
	}
}
