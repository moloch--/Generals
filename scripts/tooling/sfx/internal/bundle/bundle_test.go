// GeneralsX @feature OpenAI 30/07/2026 Covers deterministic bundle archives and hostile extraction inputs.
package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteTarDeterministicRoundTrip(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, "bin"), 0o750)
	mustMkdir(t, filepath.Join(source, "data"), 0o755)
	mustWrite(t, filepath.Join(source, "bin", "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	mustWrite(t, filepath.Join(source, "data", "config.ini"), []byte("answer=42\n"), 0o640)
	mustWrite(t, filepath.Join(source, "empty"), nil, 0o600)

	options := PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test-1",
		TargetOS:   "darwin",
		TargetArch: "arm64",
		Entrypoint: "bin/run.sh",
		WorkDir:    "bin",
		Epoch:      time.Unix(1_700_000_000, 0),
	}
	var first, second bytes.Buffer
	firstManifest, err := WriteTar(source, &first, options)
	if err != nil {
		t.Fatalf("WriteTar(first): %v", err)
	}
	secondManifest, err := WriteTar(source, &second, options)
	if err != nil {
		t.Fatalf("WriteTar(second): %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("identical source trees produced different tar bytes")
	}
	if !reflect.DeepEqual(firstManifest, secondManifest) {
		t.Fatal("identical source trees produced different manifests")
	}
	if got, want := firstManifest.TotalSize, int64(27); got != want {
		t.Fatalf("TotalSize = %d, want %d", got, want)
	}
	for i := 1; i < len(firstManifest.Entries); i++ {
		if firstManifest.Entries[i-1].Path >= firstManifest.Entries[i].Path {
			t.Fatalf("entries are not sorted: %#v", firstManifest.Entries)
		}
	}

	finalized := finalizeManifest(firstManifest, first.Bytes(), "xz")
	encoded, err := MarshalManifest(finalized)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	decoded, err := ParseManifest(encoded)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !reflect.DeepEqual(finalized, decoded) {
		t.Fatalf("manifest JSON round trip changed value:\n got %#v\nwant %#v", decoded, finalized)
	}

	destination := filepath.Join(t.TempDir(), "extracted")
	if err := ExtractTar(bytes.NewReader(first.Bytes()), destination, decoded); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	assertFile(t, filepath.Join(destination, "bin", "run.sh"), "#!/bin/sh\nexit 0\n", 0o755)
	assertFile(t, filepath.Join(destination, "data", "config.ini"), "answer=42\n", 0o640)
	assertFile(t, filepath.Join(destination, "empty"), "", 0o600)
}

// GeneralsX @test Codex 05/08/2026 Keep XZ-facing source progress exact, monotonic, and output-neutral.
func TestWriteTarReportsPostExclusionSourceBytes(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, "data"), 0o755)
	large := bytes.Repeat([]byte("GeneralsX-XZ-progress-"), 16*1024)
	small := []byte("small payload\n")
	launcher := []byte("#!/bin/sh\n")
	mustWrite(t, filepath.Join(source, "data", "large.big"), large, 0o644)
	mustWrite(t, filepath.Join(source, "data", "small.ini"), small, 0o644)
	mustWrite(t, filepath.Join(source, "data", "excluded.big"), bytes.Repeat([]byte("excluded"), 4096), 0o644)
	mustWrite(t, filepath.Join(source, "run"), launcher, 0o755)

	var updates []PackProgress
	options := PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "linux",
		TargetArch: "amd64",
		Entrypoint: "run",
		Epoch:      time.Unix(1234, 0),
		Exclude: func(name string, _ fs.DirEntry) (bool, error) {
			return name == "data/excluded.big", nil
		},
		Progress: func(progress PackProgress) {
			updates = append(updates, progress)
		},
	}
	var withProgress bytes.Buffer
	manifest, err := WriteTar(source, &withProgress, options)
	if err != nil {
		t.Fatalf("WriteTar with progress: %v", err)
	}
	wantTotal := int64(len(large) + len(small) + len(launcher))
	if manifest.TotalSize != wantTotal {
		t.Fatalf("manifest total = %d, want %d", manifest.TotalSize, wantTotal)
	}
	if len(updates) < 4 {
		t.Fatalf("progress updates = %#v, want initial, intermediate, and completed events", updates)
	}
	if got := updates[0]; got != (PackProgress{TotalBytes: wantTotal}) {
		t.Fatalf("initial progress = %#v, want 0/%d incomplete", got, wantTotal)
	}
	previous := int64(-1)
	sawIntermediate := false
	for index, got := range updates {
		if got.TotalBytes != wantTotal {
			t.Fatalf("update %d total = %d, want %d", index, got.TotalBytes, wantTotal)
		}
		if got.CompletedBytes < previous || got.CompletedBytes > got.TotalBytes {
			t.Fatalf("update %d is not bounded and monotonic: previous=%d update=%#v", index, previous, got)
		}
		if got.CompletedBytes > 0 && got.CompletedBytes < got.TotalBytes {
			sawIntermediate = true
		}
		if got.Complete && index != len(updates)-1 {
			t.Fatalf("completion appeared before the final update: %#v", updates)
		}
		previous = got.CompletedBytes
	}
	if !sawIntermediate {
		t.Fatalf("progress never advanced inside a source file: %#v", updates)
	}
	if got := updates[len(updates)-1]; got != (PackProgress{
		CompletedBytes: wantTotal,
		TotalBytes:     wantTotal,
		Complete:       true,
	}) {
		t.Fatalf("final progress = %#v, want completed %d/%d", got, wantTotal, wantTotal)
	}

	options.Progress = nil
	var withoutProgress bytes.Buffer
	secondManifest, err := WriteTar(source, &withoutProgress, options)
	if err != nil {
		t.Fatalf("WriteTar without progress: %v", err)
	}
	if !bytes.Equal(withProgress.Bytes(), withoutProgress.Bytes()) {
		t.Fatal("enabling progress changed deterministic tar bytes")
	}
	if !reflect.DeepEqual(manifest, secondManifest) {
		t.Fatal("enabling progress changed the generated manifest")
	}
}

// GeneralsX @bugfix Codex 04/08/2026 Cover executable-mode synthesis when Windows hosts cross-package POSIX launchers.
func TestNormalizeSourceEntryModeForWindowsCrossPackaging(t *testing.T) {
	tests := []struct {
		name        string
		entry       Entry
		hostOS      string
		targetOS    string
		executables []string
		want        uint32
	}{
		{
			name:   "POSIX entrypoint from Windows",
			entry:  Entry{Path: "bin/game", Type: EntryFile, Mode: 0o666},
			hostOS: "windows", targetOS: "linux", executables: []string{"bin/game"}, want: 0o755,
		},
		{
			name:   "POSIX sidecar from Windows",
			entry:  Entry{Path: "online-server/generals-server", Type: EntryFile, Mode: 0o666},
			hostOS: "windows", targetOS: "darwin",
			executables: []string{"bin/game", "online-server/generals-server"}, want: 0o755,
		},
		{
			name:   "POSIX asset from Windows",
			entry:  Entry{Path: "asset.big", Type: EntryFile, Mode: 0o666},
			hostOS: "windows", targetOS: "linux", executables: []string{"bin/game"}, want: 0o644,
		},
		{
			name:   "POSIX directory from Windows",
			entry:  Entry{Path: "bin", Type: EntryDirectory, Mode: 0o666},
			hostOS: "windows", targetOS: "linux", executables: []string{"bin/game"}, want: 0o755,
		},
		{
			name:   "Windows target",
			entry:  Entry{Path: "game.exe", Type: EntryFile, Mode: 0o755},
			hostOS: "darwin", targetOS: "windows", executables: []string{"game.exe"}, want: 0o644,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := test.entry
			normalizeSourceEntryMode(&entry, test.hostOS, test.targetOS, test.executables...)
			if entry.Mode != test.want {
				t.Fatalf("normalized mode = %#o, want %#o", entry.Mode, test.want)
			}
		})
	}
}

// GeneralsX @feature Codex 04/08/2026 Keep schema-v1 payloads compatible while authenticating an optional Online server executable.
func TestManifestOptionalOnlineServerEntrypointRoundTrip(t *testing.T) {
	legacy := finalizeManifest(validTestManifest(), []byte("payload"), CompressionXZ)
	encodedLegacy, err := MarshalManifest(legacy)
	if err != nil {
		t.Fatalf("MarshalManifest legacy: %v", err)
	}
	if bytes.Contains(encodedLegacy, []byte("online_server_entrypoint")) {
		t.Fatalf("legacy manifest unexpectedly contains Online server metadata: %s", encodedLegacy)
	}
	decodedLegacy, err := ParseManifest(encodedLegacy)
	if err != nil {
		t.Fatalf("ParseManifest legacy: %v", err)
	}
	if decodedLegacy.OnlineServerEntrypoint != "" {
		t.Fatalf("legacy Online server entrypoint = %q", decodedLegacy.OnlineServerEntrypoint)
	}

	withServer := validTestManifestWithOnlineServer()
	withServer = finalizeManifest(withServer, []byte("payload"), CompressionXZ)
	encodedServer, err := MarshalManifest(withServer)
	if err != nil {
		t.Fatalf("MarshalManifest with server: %v", err)
	}
	decodedServer, err := ParseManifest(encodedServer)
	if err != nil {
		t.Fatalf("ParseManifest with server: %v", err)
	}
	if !reflect.DeepEqual(decodedServer, withServer) {
		t.Fatalf("Online server manifest round trip changed value:\n got %#v\nwant %#v", decodedServer, withServer)
	}
	if decodedServer.SchemaVersion != OnlineServerSchemaVersion {
		t.Fatalf(
			"Online server schema = %d, want %d",
			decodedServer.SchemaVersion,
			OnlineServerSchemaVersion,
		)
	}
}

func TestManifestRejectsInvalidOnlineServerEntrypoint(t *testing.T) {
	tests := map[string]func(*Manifest){
		"unsafe path": func(manifest *Manifest) {
			manifest.OnlineServerEntrypoint = "../generals-server"
		},
		"missing": func(manifest *Manifest) {
			manifest.OnlineServerEntrypoint = "online-server/missing"
		},
		"directory": func(manifest *Manifest) {
			manifest.OnlineServerEntrypoint = "online-server"
		},
		"not executable": func(manifest *Manifest) {
			for index := range manifest.Entries {
				if manifest.Entries[index].Path == manifest.OnlineServerEntrypoint {
					manifest.Entries[index].Mode = 0o644
				}
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := validTestManifestWithOnlineServer()
			manifest.Entries = append([]Entry(nil), manifest.Entries...)
			mutate(&manifest)
			if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "online server") {
				t.Fatalf("Online server validation error = %v", err)
			}
		})
	}
}

func TestManifestSchemaOneRejectsOnlineServerField(t *testing.T) {
	manifest := validTestManifestWithOnlineServer()
	manifest.SchemaVersion = SchemaVersion
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "requires bundle manifest schema") {
		t.Fatalf("schema-v1 Online server error = %v", err)
	}
}

// GeneralsX @feature Codex 01/08/2026 Cover byte-accurate extraction progress without changing ExtractTar callers.
func TestExtractTarWithProgressReportsRegularFileBytes(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, "data"), 0o755)
	large := bytes.Repeat([]byte("GeneralsX-progress-"), 16*1024)
	mustWrite(t, filepath.Join(source, "data", "large.big"), large, 0o644)
	mustWrite(t, filepath.Join(source, "run"), []byte("#!/bin/sh\n"), 0o755)

	var archive bytes.Buffer
	manifest, err := WriteTar(source, &archive, PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "darwin",
		TargetArch: "arm64",
		Entrypoint: "run",
		Epoch:      time.Unix(1234, 0),
	})
	if err != nil {
		t.Fatalf("WriteTar: %v", err)
	}

	type update struct {
		completed int64
		total     int64
	}
	var updates []update
	destination := filepath.Join(t.TempDir(), "extracted")
	err = ExtractTarWithProgress(
		bytes.NewReader(archive.Bytes()),
		destination,
		manifest,
		func(completed, total int64) {
			updates = append(updates, update{completed: completed, total: total})
		},
	)
	if err != nil {
		t.Fatalf("ExtractTarWithProgress: %v", err)
	}
	if len(updates) < 3 {
		t.Fatalf("progress updates = %#v, want start, intermediate, and completion", updates)
	}
	if got := updates[0]; got != (update{completed: 0, total: manifest.TotalSize}) {
		t.Fatalf("initial progress = %#v, want 0/%d", got, manifest.TotalSize)
	}
	previous := int64(-1)
	for index, got := range updates {
		if got.total != manifest.TotalSize {
			t.Fatalf("update %d total = %d, want %d", index, got.total, manifest.TotalSize)
		}
		if got.completed < previous || got.completed > got.total {
			t.Fatalf("update %d is not bounded and monotonic: previous=%d update=%#v", index, previous, got)
		}
		previous = got.completed
	}
	if got := updates[len(updates)-1]; got.completed != manifest.TotalSize {
		t.Fatalf("final progress = %#v, want %d/%d", got, manifest.TotalSize, manifest.TotalSize)
	}
	assertFile(t, filepath.Join(destination, "data", "large.big"), string(large), 0o644)
}

func TestWriteTarPreservesSafeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, "bin"), 0o755)
	mustMkdir(t, filepath.Join(source, "data"), 0o755)
	mustMkdir(t, filepath.Join(source, "Versions", "A"), 0o755)
	mustWrite(t, filepath.Join(source, "bin", "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	mustWrite(t, filepath.Join(source, "data", "settings.ini"), []byte("safe=true\n"), 0o644)
	mustWrite(t, filepath.Join(source, "Versions", "A", "Framework"), []byte("framework\n"), 0o755)
	if err := os.Symlink("../data/settings.ini", filepath.Join(source, "bin", "settings.ini")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("A", filepath.Join(source, "Versions", "Current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Versions/Current/Framework", filepath.Join(source, "Framework")); err != nil {
		t.Fatal(err)
	}

	var payload bytes.Buffer
	manifest, err := WriteTar(source, &payload, PackOptions{
		Product:     "GeneralsXZH",
		Version:     "test",
		TargetOS:    "linux",
		TargetArch:  "amd64",
		Entrypoint:  "bin/run.sh",
		Epoch:       time.Unix(1234, 0),
		SymlinkMode: PreserveSymlinks,
	})
	if err != nil {
		t.Fatalf("WriteTar: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "extracted")
	if err := ExtractTar(bytes.NewReader(payload.Bytes()), destination, manifest); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	target, err := os.Readlink(filepath.Join(destination, "bin", "settings.ini"))
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != "../data/settings.ini" {
		t.Fatalf("symlink target = %q", target)
	}
	assertFile(t, filepath.Join(destination, "Framework"), "framework\n", 0o755)
}

func TestWriteTarRejectsEscapingAndDisabledSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "run"), []byte("run"), 0o755)
	if err := os.Symlink("../../outside", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	options := PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "linux",
		TargetArch: "amd64",
		Entrypoint: "run",
	}

	if _, err := WriteTar(source, &bytes.Buffer{}, options); err == nil ||
		!strings.Contains(err.Error(), "symlinks are disabled") {
		t.Fatalf("disabled symlink error = %v", err)
	}
	options.SymlinkMode = PreserveSymlinks
	if _, err := WriteTar(source, &bytes.Buffer{}, options); err == nil ||
		!strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func TestWriteTarExclusionAndLimits(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "run"), []byte("run"), 0o755)
	mustMkdir(t, filepath.Join(source, "excluded"), 0o755)
	mustWrite(t, filepath.Join(source, "excluded", "large"), []byte("ignored"), 0o644)
	options := PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "linux",
		TargetArch: "amd64",
		Entrypoint: "run",
		Exclude: func(name string, _ fs.DirEntry) (bool, error) {
			return name == "excluded", nil
		},
	}
	manifest, err := WriteTar(source, &bytes.Buffer{}, options)
	if err != nil {
		t.Fatalf("WriteTar with exclusion: %v", err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != "run" {
		t.Fatalf("excluded subtree appears in manifest: %#v", manifest.Entries)
	}

	options.Exclude = nil
	options.Limits = Limits{MaxEntries: 1}
	if _, err := WriteTar(source, &bytes.Buffer{}, options); err == nil ||
		!strings.Contains(err.Error(), "exceeds 1 entries") {
		t.Fatalf("entry limit error = %v", err)
	}
	options.Limits = Limits{MaxTotalSize: 2, MaxFileSize: 2}
	if _, err := WriteTar(source, &bytes.Buffer{}, options); err == nil ||
		!strings.Contains(err.Error(), "invalid size") {
		t.Fatalf("file limit error = %v", err)
	}
}

func TestWriteTarReservesCacheMarkerBeforeExclusion(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "run"), []byte("run"), 0o755)
	mustWrite(t, filepath.Join(source, ".complete.json"), []byte("collision"), 0o644)
	_, err := WriteTar(source, &bytes.Buffer{}, PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "linux",
		TargetArch: "amd64",
		Entrypoint: "run",
		Exclude: func(name string, _ fs.DirEntry) (bool, error) {
			return name == ".complete.json", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved cache marker error = %v", err)
	}
}

func TestWriteTarNormalizesWindowsModes(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, "Data"), 0o700)
	mustWrite(t, filepath.Join(source, "GeneralsXZH.exe"), []byte("exe"), 0o755)
	mustWrite(t, filepath.Join(source, "Data", "config.ini"), []byte("config"), 0o400)
	var payload bytes.Buffer
	manifest, err := WriteTar(source, &payload, PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "windows",
		TargetArch: "386",
		Entrypoint: "GeneralsXZH.exe",
	})
	if err != nil {
		t.Fatalf("WriteTar: %v", err)
	}
	for _, entry := range manifest.Entries {
		expected := uint32(0o644)
		if entry.Type == EntryDirectory {
			expected = 0o755
		}
		if entry.Mode != expected {
			t.Fatalf("%s mode = %#o, want %#o", entry.Path, entry.Mode, expected)
		}
	}
	if err := ExtractTar(
		bytes.NewReader(payload.Bytes()),
		filepath.Join(t.TempDir(), "extracted"),
		manifest,
	); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
}

func TestWindowsExtendedReservedNames(t *testing.T) {
	for _, name := range []string{
		"CONIN$",
		"conout$.log",
		"CoM¹",
		"com².txt",
		"COM³.backup.tar",
		"CON .txt",
		"conin$  .cfg",
		"lPt¹",
		"LPT².ini",
		"lpt³.data",
		"LPT1 .log",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateWindowsComponent(name); err == nil ||
				!strings.Contains(err.Error(), "reserved") {
				t.Fatalf("validateWindowsComponent(%q) error = %v", name, err)
			}
		})
	}

	for _, name := range []string{
		"CONIN",
		"CONOUT",
		"COM0",
		"COM⁴",
		"LPT0",
		"LPT⁴.txt",
		"XCOM¹",
	} {
		t.Run("allowed_"+name, func(t *testing.T) {
			if err := validateWindowsComponent(name); err != nil {
				t.Fatalf("validateWindowsComponent(%q) error = %v", name, err)
			}
		})
	}
}

func TestManifestRejectsUnsafeInputs(t *testing.T) {
	base := validTestManifest()
	tests := map[string]func(*Manifest){
		"traversal": func(m *Manifest) {
			m.Entries[0].Path = "../run"
		},
		"absolute path": func(m *Manifest) {
			m.Entries[0].Path = "/run"
		},
		"backslash": func(m *Manifest) {
			m.Entries[0].Path = `bad\run`
		},
		"duplicate": func(m *Manifest) {
			m.Entries = append(m.Entries, m.Entries[0])
			m.TotalSize *= 2
		},
		"case folding collision": func(m *Manifest) {
			first := m.Entries[0]
			first.Path = "Run"
			m.Entrypoint = "Run"
			m.Entries = []Entry{first, m.Entries[0]}
			m.TotalSize *= 2
		},
		"special type": func(m *Manifest) {
			m.Entries[0].Type = EntryType("device")
		},
		"missing parent": func(m *Manifest) {
			m.Entries[0].Path = "bin/run"
			m.Entrypoint = "bin/run"
		},
		"workdir mismatch": func(m *Manifest) {
			m.WorkDir = "run"
		},
		"windows reserved name": func(m *Manifest) {
			m.TargetOS = "windows"
			m.Entries[0].Path = "CON.exe"
			m.Entrypoint = "CON.exe"
		},
		"partial payload metadata": func(m *Manifest) {
			m.Compression = "xz"
		},
		"reserved cache marker": func(m *Manifest) {
			m.Entries[0].Path = ".complete.json"
			m.Entrypoint = ".complete.json"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := base
			manifest.Entries = append([]Entry(nil), base.Entries...)
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatalf("Validate accepted unsafe manifest: %#v", manifest)
			}
		})
	}
}

func TestManifestRejectsEscapingSymlinkAndCycle(t *testing.T) {
	base := validTestManifest()
	base.TargetOS = "linux"
	base.Entries = append([]Entry{
		{Path: "escape", Type: EntrySymlink, Mode: 0o777, LinkTarget: "../outside"},
	}, base.Entries...)
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping symlink validation error = %v", err)
	}

	base = validTestManifest()
	base.TargetOS = "linux"
	base.Entries = append([]Entry{
		{Path: "a", Type: EntrySymlink, Mode: 0o777, LinkTarget: "b"},
		{Path: "b", Type: EntrySymlink, Mode: 0o777, LinkTarget: "a"},
	}, base.Entries...)
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("symlink cycle validation error = %v", err)
	}

	base = validTestManifest()
	base.TargetOS = "linux"
	base.Entries = append([]Entry{
		{Path: "a", Type: EntrySymlink, Mode: 0o777, LinkTarget: "."},
		{Path: "b", Type: EntrySymlink, Mode: 0o777, LinkTarget: "a/a"},
	}, base.Entries...)
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("symlink expansion cycle validation error = %v", err)
	}
}

func TestManifestRejectsDirectoryTopologyCycles(t *testing.T) {
	t.Run("ancestor link", func(t *testing.T) {
		manifest := validTestManifest()
		manifest.TargetOS = "linux"
		manifest.Entries = []Entry{
			{Path: "Data", Type: EntryDirectory, Mode: 0o755},
			{Path: "Data/Sub", Type: EntryDirectory, Mode: 0o755},
			{Path: "Data/Sub/up", Type: EntrySymlink, Mode: 0o777, LinkTarget: ".."},
			manifest.Entries[0],
		}

		if err := manifest.Validate(); err == nil ||
			!strings.Contains(err.Error(), "directory topology cycle") {
			t.Fatalf("ancestor directory cycle validation error = %v", err)
		}
	})

	t.Run("cross-linked directories", func(t *testing.T) {
		manifest := validTestManifest()
		manifest.TargetOS = "linux"
		manifest.Entries = []Entry{
			{Path: "A", Type: EntryDirectory, Mode: 0o755},
			{Path: "A/to-B", Type: EntrySymlink, Mode: 0o777, LinkTarget: "../B"},
			{Path: "B", Type: EntryDirectory, Mode: 0o755},
			{Path: "B/to-A", Type: EntrySymlink, Mode: 0o777, LinkTarget: "../A"},
			manifest.Entries[0],
		}

		if err := manifest.Validate(); err == nil ||
			!strings.Contains(err.Error(), "directory topology cycle") {
			t.Fatalf("cross-linked directory cycle validation error = %v", err)
		}
	})
}

func TestManifestAllowsSafeSymlinkComponentChain(t *testing.T) {
	manifest := validTestManifest()
	manifest.TargetOS = "linux"
	manifest.Entries = []Entry{
		{Path: "Framework", Type: EntrySymlink, Mode: 0o777, LinkTarget: "Versions/Current/Framework"},
		{Path: "Versions", Type: EntryDirectory, Mode: 0o755},
		{Path: "Versions/A", Type: EntryDirectory, Mode: 0o755},
		{
			Path:   "Versions/A/Framework",
			Type:   EntryFile,
			Mode:   0o755,
			Size:   manifest.Entries[0].Size,
			SHA256: manifest.Entries[0].SHA256,
		},
		{Path: "Versions/Current", Type: EntrySymlink, Mode: 0o777, LinkTarget: "A"},
	}
	manifest.Entrypoint = "Versions/A/Framework"
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate rejected framework-style symlink chain: %v", err)
	}
}

func TestParseManifestIsStrictAndSupportsXZ(t *testing.T) {
	manifest := finalizeManifest(validTestManifest(), []byte("payload"), CompressionXZ)
	encoded, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	if _, err := ParseManifest(encoded); err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	withUnknown := bytes.Replace(encoded, []byte(`"product"`), []byte(`"unknown":true,"product"`), 1)
	if _, err := ParseManifest(withUnknown); err == nil {
		t.Fatal("ParseManifest accepted unknown field")
	}
	wrongCase := bytes.Replace(encoded, []byte(`"product"`), []byte(`"Product"`), 1)
	if _, err := ParseManifest(wrongCase); err == nil {
		t.Fatal("ParseManifest accepted non-canonical field case")
	}
	if _, err := ParseManifest(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("ParseManifest accepted trailing JSON")
	}
	withDuplicate := bytes.Replace(
		encoded,
		[]byte(`"product":"GeneralsXZH"`),
		[]byte(`"product":"other","product":"GeneralsXZH"`),
		1,
	)
	if _, err := ParseManifest(withDuplicate); err == nil {
		t.Fatal("ParseManifest accepted duplicate field")
	}

	for _, unsupported := range []string{"gzip", "bzip2"} {
		manifest.Compression = unsupported
		if _, err := MarshalManifest(manifest); err == nil {
			t.Fatalf("MarshalManifest accepted unsupported compression %q", unsupported)
		}
	}
}

func TestMarshalManifestRejectsOversizedEncoding(t *testing.T) {
	manifest := finalizeManifest(validTestManifest(), []byte("payload"), CompressionXZ)
	manifest.TargetOS = "linux"
	manifest.WorkDir = ""
	manifest.Entries = make([]Entry, 1_200)
	const digest = "0000000000000000000000000000000000000000000000000000000000000000"
	for index := range manifest.Entries {
		manifest.Entries[index] = Entry{
			Path: fmt.Sprintf(
				"file-%04d-%s",
				index,
				strings.Repeat("x", 4_000),
			),
			Type:   EntryFile,
			Mode:   0o755,
			SHA256: digest,
		}
	}
	manifest.Entrypoint = manifest.Entries[0].Path
	manifest.TotalSize = 0

	encoded, err := MarshalManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "manifest exceeds") {
		t.Fatalf("MarshalManifest oversized error = %v", err)
	}
	if encoded != nil {
		t.Fatalf("MarshalManifest returned %d bytes with an oversize error", len(encoded))
	}
}

func TestExtractTarRejectsTamperingAndUnexpectedEntries(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "run"), []byte("ABCDE"), 0o755)
	var payload bytes.Buffer
	manifest, err := WriteTar(source, &payload, PackOptions{
		Product:    "GeneralsXZH",
		Version:    "test",
		TargetOS:   "linux",
		TargetArch: "amd64",
		Entrypoint: "run",
		Epoch:      time.Unix(42, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("content hash mismatch", func(t *testing.T) {
		tampered := append([]byte(nil), payload.Bytes()...)
		offset := bytes.Index(tampered, []byte("ABCDE"))
		if offset < 0 {
			t.Fatal("could not locate tar file content")
		}
		copy(tampered[offset:], "abcde")
		err := ExtractTar(bytes.NewReader(tampered), filepath.Join(t.TempDir(), "out"), manifest)
		if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
			t.Fatalf("tampered extraction error = %v", err)
		}
	})

	t.Run("unsafe header path", func(t *testing.T) {
		malicious := makeSingleEntryTar(t, manifest.Entries[0], manifest.Epoch, "../run", tar.TypeReg, []byte("ABCDE"))
		err := ExtractTar(bytes.NewReader(malicious), filepath.Join(t.TempDir(), "out"), manifest)
		if err == nil || !strings.Contains(err.Error(), "not normalized") {
			t.Fatalf("traversal extraction error = %v", err)
		}
	})

	t.Run("special node", func(t *testing.T) {
		malicious := makeSingleEntryTar(t, manifest.Entries[0], manifest.Epoch, "run", tar.TypeChar, nil)
		err := ExtractTar(bytes.NewReader(malicious), filepath.Join(t.TempDir(), "out"), manifest)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("special-node extraction error = %v", err)
		}
	})

	t.Run("extra entry", func(t *testing.T) {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		header := normalizedHeader(manifest.Entries[0], time.Unix(manifest.Epoch, 0))
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("ABCDE")); err != nil {
			t.Fatal(err)
		}
		extra := &tar.Header{
			Name:     "extra",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     0,
			ModTime:  time.Unix(manifest.Epoch, 0),
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		err := ExtractTar(bytes.NewReader(archive.Bytes()), filepath.Join(t.TempDir(), "out"), manifest)
		if err == nil || !strings.Contains(err.Error(), "unexpected entry") {
			t.Fatalf("extra-entry extraction error = %v", err)
		}
	})

	t.Run("unsupported PAX metadata", func(t *testing.T) {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		header := normalizedHeader(manifest.Entries[0], time.Unix(manifest.Epoch, 0))
		header.PAXRecords = map[string]string{"comment": "not in schema"}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("ABCDE")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		err := ExtractTar(bytes.NewReader(archive.Bytes()), filepath.Join(t.TempDir(), "out"), manifest)
		if err == nil || !strings.Contains(err.Error(), "unsupported PAX metadata") {
			t.Fatalf("PAX metadata extraction error = %v", err)
		}
	})
}

func TestStreamFileRejectsSameSizeMutationDuringPacking(t *testing.T) {
	source := filepath.Join(t.TempDir(), "large.big")
	original := bytes.Repeat([]byte("A"), 4<<20)
	mutated := bytes.Repeat([]byte("B"), len(original)/2)
	mustWrite(t, source, original, 0o644)
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}

	writer := &mutateSourceWriter{
		path:    source,
		offset:  int64(len(original) / 2),
		content: mutated,
	}
	_, err = streamFile(context.Background(), writer, sourceItem{
		full: source,
		info: info,
		entry: Entry{
			Path: "large.big",
			Type: EntryFile,
			Mode: uint32(info.Mode().Perm()),
			Size: info.Size(),
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "changed while packing") {
		t.Fatalf("streamFile mutation error = %v", err)
	}
}

type mutateSourceWriter struct {
	path    string
	offset  int64
	content []byte
	mutated bool
}

func (writer *mutateSourceWriter) Write(content []byte) (int, error) {
	if !writer.mutated {
		writer.mutated = true
		file, err := os.OpenFile(writer.path, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		if _, err := file.WriteAt(writer.content, writer.offset); err != nil {
			_ = file.Close()
			return 0, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return 0, err
		}
		if err := file.Close(); err != nil {
			return 0, err
		}
	}
	return len(content), nil
}

func TestExtractTarRequiresEmptyDestination(t *testing.T) {
	destination := t.TempDir()
	mustWrite(t, filepath.Join(destination, "existing"), []byte("keep"), 0o644)
	if err := ExtractTar(bytes.NewReader(nil), destination, validTestManifest()); err == nil ||
		!strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty destination error = %v", err)
	}
}

func validTestManifest() Manifest {
	content := []byte("run")
	digest := sha256.Sum256(content)
	return Manifest{
		SchemaVersion: SchemaVersion,
		Product:       "GeneralsXZH",
		Version:       "test",
		TargetOS:      "darwin",
		TargetArch:    "arm64",
		Entrypoint:    "run",
		Epoch:         1234,
		Entries: []Entry{{
			Path:   "run",
			Type:   EntryFile,
			Mode:   0o755,
			Size:   int64(len(content)),
			SHA256: fmt.Sprintf("%x", digest),
		}},
		TotalSize: int64(len(content)),
	}
}

func validTestManifestWithOnlineServer() Manifest {
	manifest := validTestManifest()
	manifest.SchemaVersion = OnlineServerSchemaVersion
	serverContent := []byte("server")
	serverDigest := sha256.Sum256(serverContent)
	manifest.OnlineServerEntrypoint = "online-server/generals-server"
	manifest.Entries = []Entry{
		{Path: "online-server", Type: EntryDirectory, Mode: 0o755},
		{
			Path:   manifest.OnlineServerEntrypoint,
			Type:   EntryFile,
			Mode:   0o755,
			Size:   int64(len(serverContent)),
			SHA256: fmt.Sprintf("%x", serverDigest),
		},
		manifest.Entries[0],
	}
	manifest.TotalSize += int64(len(serverContent))
	return manifest
}

func finalizeManifest(manifest Manifest, payload []byte, compression string) Manifest {
	digest := sha256.Sum256(payload)
	manifest.Compression = compression
	manifest.PayloadSHA256 = fmt.Sprintf("%x", digest)
	manifest.PayloadSize = int64(len(payload))
	return manifest
}

func makeSingleEntryTar(
	t *testing.T,
	entry Entry,
	epoch int64,
	name string,
	entryType byte,
	content []byte,
) []byte {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	header := normalizedHeader(entry, time.Unix(epoch, 0))
	header.Name = name
	header.Typeflag = entryType
	header.Size = int64(len(content))
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func mustMkdir(t *testing.T, name string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(name, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, name string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, name, expected string, mode os.FileMode) {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("%s content = %q, want %q", name, content, expected)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %#o, want %#o", name, info.Mode().Perm(), mode)
	}
}
