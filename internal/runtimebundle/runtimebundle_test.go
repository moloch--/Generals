// GeneralsX @build Codex 10/10/2026 Exercise runtime trust boundaries with synthetic binaries and hostile archives.
package runtimebundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func syntheticPE(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 128)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[60:], 128)
	buffer := bytes.NewBuffer(data)
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
	if buffer.Len() > 512 {
		t.Fatal("synthetic PE headers overflow")
	}
	buffer.Write(make([]byte, 1024-buffer.Len()))
	return buffer.Bytes()
}

func syntheticELF(t *testing.T) []byte {
	t.Helper()
	var header elf.Header64
	copy(header.Ident[:], []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	header.Type = uint16(elf.ET_EXEC)
	header.Machine = uint16(elf.EM_X86_64)
	header.Version = 1
	header.Ehsize = 64
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func fixture(t *testing.T, target string) (Descriptor, map[string][]byte) {
	t.Helper()
	d := Descriptor{SchemaVersion: 1, Version: "runtime-v1.2.3", Repository: "example/Generals", SourceCommit: strings.Repeat("a", 40), Target: target, EngineArch: "x86", LauncherArch: "amd64", OnlineEndpoint: "tls://multiplayer.generals.network", OnlineCompatibility: 2}
	contents := map[string][]byte{"runtime/generalszh.exe": syntheticPE(t), "kit/LICENSE.md": []byte("license"), "kit/scripts/tooling/sfx/go.mod": []byte("module fixture\n\ngo 1.25.0\n"), "kit/scripts/tooling/sfx/go.sum": []byte("fixture\n"), "kit/scripts/tooling/sfx/cmd/generalsx-sfx/main.go": []byte("package main\nfunc main(){}\n"), "kit/scripts/tooling/sfx/cmd/generalsx-sfx-pack/main.go": []byte("package main\nfunc main(){}\n")}
	if target == "linux-amd64" {
		d.EngineArch = "amd64"
		delete(contents, "runtime/generalszh.exe")
		contents["runtime/GeneralsXZH"] = syntheticELF(t)
	}
	for name, data := range contents {
		mode := uint32(0644)
		if name == "runtime/GeneralsXZH" {
			mode = 0755
		}
		d.Files = append(d.Files, File{Path: name, Size: int64(len(data)), SHA256: hashBytes(data), Mode: mode})
	}
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Path < d.Files[j].Path })
	d.Archive = Archive{Name: ArtifactStem(d.Version, d.Target) + ".tar.gz", Size: 1, SHA256: strings.Repeat("0", 64)}
	return d, contents
}

func fixtureArchive(t *testing.T, d *Descriptor, contents map[string][]byte, mutate func(*tar.Header)) string {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, file := range d.Files {
		header := &tar.Header{Name: file.Path, Mode: int64(file.Mode), Size: file.Size, Typeflag: tar.TypeReg}
		if file.LinkTarget != "" {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = file.LinkTarget
		}
		if mutate != nil {
			mutate(header)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if file.LinkTarget == "" {
			if _, err := writer.Write(contents[file.Path]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	d.Archive.Size = int64(buffer.Len())
	d.Archive.SHA256 = hashBytes(buffer.Bytes())
	name := filepath.Join(t.TempDir(), "runtime.tar.gz")
	if err := os.WriteFile(name, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestExtractRoundTripAndDetectModifiedDirectory(t *testing.T) {
	d, contents := fixture(t, "windows-x86")
	archive := fixtureArchive(t, &d, contents, nil)
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := d.Extract(context.Background(), archive, destination); err != nil {
		t.Fatal(err)
	}
	if err := d.VerifyDirectory(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "runtime", "generalszh.exe"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := d.VerifyDirectory(destination); err == nil {
		t.Fatal("accepted changed runtime engine")
	}
}

func TestRejectDescriptorAmbiguityAndUnsafePaths(t *testing.T) {
	d, _ := fixture(t, "windows-x86")
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"duplicate": strings.Replace(string(encoded), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), "unknown": strings.Replace(string(encoded), `"schema_version":1`, `"schema_version":1,"unexpected":true`, 1), "trailing": string(encoded) + "{}", "oversized": strings.Repeat(" ", MaxDescriptorBytes+1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadDescriptor(strings.NewReader(data)); err == nil {
				t.Fatal("accepted invalid descriptor")
			}
		})
	}
	for _, name := range []string{"../outside", "runtime/../outside", "runtime/CON.txt", "runtime/foo:bar.dll", "runtime/evil\\file.dll", "runtime/Movies.big", "runtime/binkw32.dll", "runtime/licenses/retail.big", "runtime/lib/../foo.dll"} {
		t.Run(name, func(t *testing.T) {
			bad := d
			bad.Files = append(append([]File{}, d.Files...), File{Path: name, Mode: 0644, Size: 1, SHA256: hashBytes([]byte("x"))})
			if err := bad.Validate(); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, name := range []string{"kit/scripts/tooling/sfx/GO.MOD", "kit/Scripts/new.go", "kit/scripts"} {
		bad := d
		bad.Files = append(append([]File{}, d.Files...), File{Path: name, Mode: 0644, Size: 1, SHA256: hashBytes([]byte("x"))})
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted path collision %s", name)
		}
	}
}

func TestExtractRejectsTamperAndDoesNotPublish(t *testing.T) {
	for _, kind := range []string{"digest", "path", "mode", "file-hash", "unmanifested", "cancelled", "wrong-architecture"} {
		t.Run(kind, func(t *testing.T) {
			d, contents := fixture(t, "windows-x86")
			var mutate func(*tar.Header)
			switch kind {
			case "path":
				mutate = func(h *tar.Header) {
					if h.Name == "runtime/generalszh.exe" {
						h.Name = "../escaped.exe"
					}
				}
			case "mode":
				mutate = func(h *tar.Header) { h.Mode = 04755 }
			case "unmanifested":
				mutate = func(h *tar.Header) {
					if h.Name == "runtime/generalszh.exe" {
						h.Name = "runtime/extra.dll"
					}
				}
			case "wrong-architecture":
				binary.LittleEndian.PutUint16(contents["runtime/generalszh.exe"][132:], pe.IMAGE_FILE_MACHINE_AMD64)
				for i := range d.Files {
					if d.Files[i].Path == "runtime/generalszh.exe" {
						d.Files[i].SHA256 = hashBytes(contents[d.Files[i].Path])
					}
				}
			}
			archive := fixtureArchive(t, &d, contents, mutate)
			if kind == "digest" {
				d.Archive.SHA256 = strings.Repeat("f", 64)
			}
			if kind == "file-hash" {
				d.Files[0].SHA256 = strings.Repeat("f", 64)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			parent := t.TempDir()
			destination := filepath.Join(parent, "published")
			if err := d.Extract(ctx, archive, destination); err == nil {
				t.Fatal("accepted invalid runtime")
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed extraction left files: %v", entries)
			}
		})
	}
}

func TestSymlinkContract(t *testing.T) {
	d, _ := fixture(t, "linux-amd64")
	for _, target := range []string{"../../kit/LICENSE.md", "../../runtime/GeneralsXZH", "/etc/passwd", "missing.so", "libalias.so"} {
		bad := d
		bad.Files = append(append([]File{}, d.Files...), File{Path: "runtime/lib/libalias.so", Mode: 0777, SHA256: hashBytes([]byte(target)), LinkTarget: target})
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted symlink target %s", target)
		}
	}
	windows, _ := fixture(t, "windows-x86")
	windows.Files = append(windows.Files, File{Path: "runtime/alias.dll", Mode: 0777, SHA256: hashBytes([]byte("generalszh.exe")), LinkTarget: "generalszh.exe"})
	if err := windows.Validate(); err == nil {
		t.Fatal("accepted Windows symlink")
	}
}

func TestExtractThroughAncestorAlias(t *testing.T) {
	// macOS /var is a symlink to /private/var. The chosen destination parent
	// itself remains a real directory, while an ancestor may be an OS alias.
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.MkdirAll(filepath.Join(real, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	d, contents := fixture(t, "windows-x86")
	archive := fixtureArchive(t, &d, contents, nil)
	destination := filepath.Join(alias, "nested", "package")
	if err := d.Extract(context.Background(), archive, destination); err != nil {
		t.Fatal(err)
	}
	if err := d.VerifyDirectory(destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(real, "nested", "package", "runtime", "generalszh.exe")); err != nil {
		t.Fatal(err)
	}
	// An alias used as the extraction/verification root itself is rejected.
	rootAlias := filepath.Join(parent, "root-alias")
	if err := os.Symlink(filepath.Join(real, "nested", "package"), rootAlias); err != nil {
		t.Fatal(err)
	}
	if err := d.VerifyDirectory(rootAlias); err == nil {
		t.Fatal("accepted symlink package root")
	}
}

func TestLinuxNormalizedLibrarySymlink(t *testing.T) {
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, "target"), []byte("probe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(probe, "link")); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	d, contents := fixture(t, "linux-amd64")
	contents["runtime/lib/libfixture.so.1"] = syntheticELF(t)
	d.Files = append(d.Files, File{Path: "runtime/lib/libfixture.so.1", Mode: 0755, Size: int64(len(contents["runtime/lib/libfixture.so.1"])), SHA256: hashBytes(contents["runtime/lib/libfixture.so.1"])}, File{Path: "runtime/lib/libfixture.so", Mode: 0777, SHA256: hashBytes([]byte("libfixture.so.1")), LinkTarget: "libfixture.so.1"})
	archive := fixtureArchive(t, &d, contents, nil)
	destination := filepath.Join(t.TempDir(), "package")
	if err := d.Extract(context.Background(), archive, destination); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(destination, "runtime", "lib", "libfixture.so"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(resolved) != "libfixture.so.1" {
		t.Fatalf("library resolves to %s", resolved)
	}
	if err := d.VerifyDirectory(destination); err != nil {
		t.Fatal(err)
	}
}

func TestPackagingKitResourceAllowlist(t *testing.T) {
	for _, name := range []string{
		"scripts/tooling/sfx/go.mod", "scripts/tooling/sfx/go.sum",
		"scripts/tooling/sfx/internal/payload/packed.go", "scripts/tooling/sfx/internal/payload/dev.go",
		"scripts/tooling/sfx/internal/notices/THIRD_PARTY_NOTICES.md",
		"scripts/tooling/sfx/profiles/linux-zh.exclude", "scripts/tooling/sfx/profiles/macos-zh.exclude", "scripts/tooling/sfx/profiles/windows-zh.exclude",
		"scripts/tooling/sfx/cmd/generalsx-sfx/rsrc_windows_amd64.syso",
		"scripts/build/macos/package-sfx-macos-zh-app.sh", "scripts/tooling/sfx/macos/progress.m", "assets/generalsx-zh_icon.png",
	} {
		if !kitPath(name) {
			t.Fatalf("packaging resource excluded: %s", name)
		}
	}
	for _, name := range []string{
		"scripts/tooling/sfx/internal/payload/generated/payload.tar.xz",
		"scripts/tooling/sfx/internal/payload/generated/manifest.json",
		"scripts/tooling/sfx/internal/payload/generated/private.go",
		"scripts/tooling/sfx/build/generalsx-sfx.exe",
		"scripts/tooling/sfx/cmd/generalsx-sfx/retail.big",
	} {
		if kitPath(name) {
			t.Fatalf("private/generated resource included: %s", name)
		}
	}
}

func TestPEImportsIncludeOrdinalAndDelayLibraries(t *testing.T) {
	data := syntheticPE(t)
	// Import descriptor table in .text followed by its DLL name. No symbol
	// thunk is needed: import closure includes ordinal-only DLLs too.
	optionalOffset := 128 + 24
	binary.LittleEndian.PutUint32(data[optionalOffset+96+8:], 4096)
	binary.LittleEndian.PutUint32(data[optionalOffset+96+12:], 40)
	binary.LittleEndian.PutUint32(data[512+12:], 4096+80)
	copy(data[512+80:], "example.dll\x00")
	binary.LittleEndian.PutUint32(data[optionalOffset+96+13*8:], 4096+128)
	binary.LittleEndian.PutUint32(data[optionalOffset+96+13*8+4:], 64)
	binary.LittleEndian.PutUint32(data[512+128:], 1)
	binary.LittleEndian.PutUint32(data[512+128+4:], 4096+208)
	copy(data[512+208:], "delayed.dll\x00")
	binaryFile, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	imports, err := peLibraries(binaryFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(imports, ",") != "example.dll,delayed.dll" {
		t.Fatalf("imports %v", imports)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "generalszh.exe"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntime(root, "windows-x86"); err == nil {
		t.Fatal("accepted missing imported DLLs")
	}
}

func TestRetailWindowsLibrariesRequireTheirNonSystemImports(t *testing.T) {
	root := t.TempDir()
	bink := syntheticPE(t)
	optionalOffset := 128 + 24
	binary.LittleEndian.PutUint32(bink[optionalOffset+96+8:], 4096)
	binary.LittleEndian.PutUint32(bink[optionalOffset+96+12:], 40)
	binary.LittleEndian.PutUint32(bink[512+12:], 4096+80)
	copy(bink[512+80:], "retail-helper.dll\x00")
	if err := os.WriteFile(filepath.Join(root, "binkw32.dll"), bink, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mss32.dll"), syntheticPE(t), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetailWindowsLibraries(root); err == nil || !strings.Contains(err.Error(), "retail-helper.dll") {
		t.Fatalf("missing retail import error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "retail-helper.dll"), syntheticPE(t), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetailWindowsLibraries(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "mss32.dll")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetailWindowsLibraries(root); err == nil || !strings.Contains(err.Error(), "mss32.dll") {
		t.Fatalf("missing retail Miles error = %v", err)
	}
}

func TestGenerateTrackedKitAndImmutableAssets(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git required for producer integration")
	}
	repo := t.TempDir()
	d, contents := fixture(t, "windows-x86")
	for name, data := range contents {
		if !strings.HasPrefix(name, "kit/") {
			continue
		}
		full := filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(name, "kit/")))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("add", ".")
	git("-c", "user.name=Runtime test", "-c", "user.email=runtime@example.invalid", "commit", "--quiet", "-m", "fixture")
	runtimeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtimeDir, "generalszh.exe"), contents["runtime/generalszh.exe"], 0644); err != nil {
		t.Fatal(err)
	}
	options := Options{RuntimeDir: runtimeDir, Repo: repo, OutputDir: t.TempDir(), Target: d.Target, Repository: d.Repository, Version: d.Version, Commit: git("rev-parse", "HEAD"), OnlineEndpoint: d.OnlineEndpoint}
	result, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Archive.Size == 0 || result.Archive.SHA256 == "" {
		t.Fatal("missing archive identity")
	}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("overwrote immutable runtime release")
	}
	options.OutputDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "LICENSE.md"), []byte("changed license"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("published dirty packaging kit under clean commit identity")
	}
	for _, name := range []string{"scripts/tooling/sfx/internal/payload/generated/payload.tar.xz", "scripts/tooling/sfx/internal/payload/generated/private.go", "scripts/tooling/sfx/internal/runtimebundle/produce.go", "scripts/tooling/sfx/cmd/generalsx-runtime-pack/main.go", "retail/Movies.big"} {
		if kitPath(name) {
			t.Fatalf("unsafe kit path allowed: %s", name)
		}
	}
}
