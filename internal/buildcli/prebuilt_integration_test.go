package buildcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

// Synthetic PE/BIG inputs exercise assembly without proprietary game assets.
func prebuiltTestPE(t *testing.T) []byte {
	t.Helper()
	dos := make([]byte, 128)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[60:], 128)
	buffer := bytes.NewBuffer(dos)
	buffer.WriteString("PE\x00\x00")
	header := pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_I386, NumberOfSections: 1, SizeOfOptionalHeader: 224, Characteristics: 2}
	optional := pe.OptionalHeader32{Magic: 0x10b, ImageBase: 0x400000, SectionAlignment: 4096, FileAlignment: 512, SizeOfImage: 8192, SizeOfHeaders: 512, NumberOfRvaAndSizes: 16}
	section := pe.SectionHeader32{VirtualAddress: 4096, VirtualSize: 512, SizeOfRawData: 512, PointerToRawData: 512}
	copy(section.Name[:], ".text")
	for _, value := range []any{header, optional, section} {
		if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	buffer.Write(make([]byte, 1024-buffer.Len()))
	return buffer.Bytes()
}

func prebuiltFixture(t *testing.T) (runtimebundle.Descriptor, []byte, []byte) {
	t.Helper()
	files := map[string][]byte{
		"runtime/generalszh.exe":                                 prebuiltTestPE(t),
		"kit/LICENSE.md":                                         []byte("fixture notice"),
		"kit/scripts/tooling/sfx/go.mod":                         []byte("module fixture\n"),
		"kit/scripts/tooling/sfx/go.sum":                         []byte("fixture\n"),
		"kit/scripts/tooling/sfx/cmd/generalsx-sfx/main.go":      []byte("package main\n"),
		"kit/scripts/tooling/sfx/cmd/generalsx-sfx-pack/main.go": []byte("package main\n"),
		"kit/scripts/tooling/sfx/profiles/windows-zh.exclude":    []byte("discard.txt\n"),
	}
	d := runtimebundle.Descriptor{SchemaVersion: 1, Version: "runtime-v1.2.3", Repository: "owner/repo", SourceCommit: strings.Repeat("a", 40), Target: "windows-x86", EngineArch: "x86", LauncherArch: "amd64", OnlineCompatibility: 2, OnlineEndpoint: defaultOnlineEndpoint}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	zip := gzip.NewWriter(&output)
	archive := tar.NewWriter(zip)
	for _, name := range names {
		data := files[name]
		digest := sha256.Sum256(data)
		d.Files = append(d.Files, runtimebundle.File{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: 0o644})
		if err := archive.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output.Bytes())
	d.Archive = runtimebundle.Archive{Name: runtimeAssetStem(d.Version, targetWindows) + ".tar.gz", Size: int64(output.Len()), SHA256: hex.EncodeToString(digest[:])}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return d, metadata, output.Bytes()
}

func TestRuntimeDownloadCacheAndLocalAssembly(t *testing.T) {
	t.Parallel()
	d, metadata, archive := prebuiltFixture(t)
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app := application{cfg: config{buildMode: "prebuilt", runtimeRepository: d.Repository, runtimeVersion: d.Version, cacheDir: cache, target: targetWindows, onlineEndpoint: defaultOnlineEndpoint}, hostOS: "windows", hostArch: "amd64", runner: runner{stdout: &logs, stderr: &logs}}
	downloads := 0
	prefix := "https://github.com/owner/repo/releases/download/" + d.Version + "/"
	release := map[string]any{"tag_name": d.Version, "assets": []map[string]any{
		{"name": runtimeAssetStem(d.Version, targetWindows) + ".json", "browser_download_url": prefix + runtimeAssetStem(d.Version, targetWindows) + ".json", "size": len(metadata)},
		{"name": d.Archive.Name, "browser_download_url": prefix + d.Archive.Name, "size": d.Archive.Size},
	}}
	releaseJSON, _ := json.Marshal(release)
	app.http = &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://api.github.com/repos/owner/repo/releases/tags/" + d.Version:
			return runtimeTestResponse(string(releaseJSON)), nil
		case prefix + runtimeAssetStem(d.Version, targetWindows) + ".json":
			return runtimeTestResponse(string(metadata)), nil
		case prefix + d.Archive.Name:
			downloads++
			return runtimeTestResponse(string(archive)), nil
		default:
			t.Errorf("unexpected request %s", request.URL)
			return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})}
	resolved, archivePath, err := app.resolveRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SourceCommit != d.SourceCommit || downloads != 1 {
		t.Fatal("wrong resolved identity or download count")
	}
	if _, _, err := app.resolveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatal("verified cache redownloaded")
	}
	app.cfg.runtimeOffline = true
	app.http.Transport = runtimeRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("offline contacted GitHub"); return nil, nil })
	if _, _, err := app.resolveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(root, "package")
	if err := d.Extract(context.Background(), archivePath, packageRoot); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "owned")
	writeCompleteAssetTree(t, assets)
	for _, name := range []string{"binkw32.dll", "mss32.dll"} {
		if err := os.WriteFile(filepath.Join(assets, name), prebuiltTestPE(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(assets, "GENERALSZH.EXE"), []byte("old engine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "discard.txt"), []byte("exclude me"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := fileSHA256(filepath.Join(assets, "INIZH.big"))
	stage := filepath.Join(root, "assembled")
	profile := filepath.Join(packageRoot, "kit/scripts/tooling/sfx/profiles/windows-zh.exclude")
	if err := assembleRuntime(context.Background(), assets, packageRoot, stage, profile, d); err != nil {
		t.Fatal(err)
	}
	game, _ := os.ReadFile(filepath.Join(stage, "generalszh.exe"))
	if !bytes.Equal(game, prebuiltTestPE(t)) {
		t.Fatal("retail engine replaced prebuilt engine")
	}
	if _, err := os.Stat(filepath.Join(stage, "discard.txt")); !os.IsNotExist(err) {
		t.Fatal("retail exclusion not applied")
	}
	after, _ := fileSHA256(filepath.Join(assets, "INIZH.big"))
	if before != after {
		t.Fatal("owned assets changed")
	}
	if err := os.Mkdir(filepath.Join(assets, "online-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := assembleRuntime(context.Background(), assets, packageRoot, filepath.Join(root, "rejected"), profile, d); err == nil {
		t.Fatal("reserved retail directory accepted")
	}
	if err := os.WriteFile(archivePath, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.resolveRuntime(context.Background()); err == nil {
		t.Fatal("offline accepted corrupt cache")
	}
}

// GeneralsX @build Codex 10/10/2026 Exercise each native CI runtime through the complete local assembly path.
// This opt-in check uses an actual produced runtime and the released packaging
// kit, but only synthetic retail data. It never launches the native game.
func TestPrebuiltProducedRuntimeEndToEnd(t *testing.T) {
	metadataPath := os.Getenv("GX_TEST_RUNTIME_DESCRIPTOR")
	archivePath := os.Getenv("GX_TEST_RUNTIME_ARCHIVE")
	if metadataPath == "" || archivePath == "" {
		t.Skip("set GX_TEST_RUNTIME_DESCRIPTOR and GX_TEST_RUNTIME_ARCHIVE to exercise a produced native runtime")
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := runtimebundle.ReadDescriptor(bytes.NewReader(metadata))
	if err != nil {
		t.Fatal(err)
	}
	var buildTarget target
	switch d.Target {
	case "windows-x86":
		buildTarget = targetWindows
	case "macos-arm64":
		buildTarget = targetMacOS
	case "linux-amd64":
		buildTarget = targetLinux
	default:
		t.Fatalf("unsupported runtime target %s", d.Target)
	}
	root := t.TempDir()
	assets := filepath.Join(root, "owned")
	writeCompleteAssetTree(t, assets)
	if buildTarget == targetWindows {
		for _, name := range []string{"binkw32.dll", "mss32.dll"} {
			if err := os.WriteFile(filepath.Join(assets, name), prebuiltTestPE(t), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cache := filepath.Join(root, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	output := filepath.Join(root, "output", "game-sfx")
	if buildTarget == targetWindows {
		output += ".exe"
	}
	app := application{cfg: config{buildMode: "prebuilt", runtimeOffline: true, runtimeRepository: d.Repository, runtimeVersion: d.Version, target: buildTarget, assetsDir: assets, cacheDir: cache, output: output, onlineEndpoint: defaultOnlineEndpoint, skipAssets: true}, hostOS: runtime.GOOS, hostArch: runtime.GOARCH, runner: runner{stdout: &logs, stderr: &logs}}
	if buildTarget == targetMacOS {
		app.cfg.appOutput = filepath.Join(root, "output", "GeneralsXZH.app")
	}
	if err := app.validateHostTarget(); err != nil {
		t.Fatal(err)
	}
	cachePath, err := app.runtimeCache(d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, "release.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyAssemblyFile(context.Background(), archivePath, filepath.Join(cachePath, d.Archive.SHA256+".tar.gz"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.run(context.Background()); err != nil {
		t.Fatalf("end-to-end packaging failed: %v\n%s", err, logs.String())
	}
	if info, err := os.Stat(app.cfg.output); err != nil || info.Size() == 0 {
		t.Fatalf("missing final SFX: %v", err)
	}
	if buildTarget == targetMacOS {
		launcher := filepath.Join(app.cfg.appOutput, "Contents", "MacOS", "GeneralsXZH")
		if err := app.runner.run(context.Background(), command{name: launcher, args: []string{"--sfx-verify"}}); err != nil {
			t.Fatalf("verify published app: %v\n%s", err, logs.String())
		}
	}
	for _, forbidden := range []string{"git clone", "cmake --", "vcpkg install", "dumpbin"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("prebuilt path invoked %s", forbidden)
		}
	}
	t.Log(logs.String())
}
