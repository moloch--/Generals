// GeneralsX @build Codex 10/10/2026 Cover retail/runtime casing and directory collisions on every host filesystem.
package buildcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

func caseAssemblyFixture(t *testing.T) (runtimebundle.Descriptor, string, string) {
	t.Helper()
	var header elf.Header64
	copy(header.Ident[:], []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	header.Type = uint16(elf.ET_EXEC)
	header.Machine = uint16(elf.EM_X86_64)
	header.Version = 1
	header.Ehsize = 64
	var engine bytes.Buffer
	if err := binary.Write(&engine, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"runtime/GeneralsXZH":                                    engine.Bytes(),
		"runtime/Window/Menus/ExtrasMenu.wnd":                    []byte("runtime overlay"),
		"kit/LICENSE.md":                                         []byte("fixture license"),
		"kit/scripts/tooling/sfx/go.mod":                         []byte("module fixture\n"),
		"kit/scripts/tooling/sfx/go.sum":                         []byte("fixture\n"),
		"kit/scripts/tooling/sfx/cmd/generalsx-sfx/main.go":      []byte("package main\n"),
		"kit/scripts/tooling/sfx/cmd/generalsx-sfx-pack/main.go": []byte("package main\n"),
		"kit/scripts/tooling/sfx/profiles/linux-zh.exclude":      []byte("# fixture profile\n"),
	}
	d := runtimebundle.Descriptor{SchemaVersion: 1, Version: "runtime-v1.2.3", Repository: "owner/repo", SourceCommit: strings.Repeat("a", 40), Target: "linux-amd64", EngineArch: "amd64", LauncherArch: "amd64", OnlineCompatibility: 2, OnlineEndpoint: defaultOnlineEndpoint, Archive: runtimebundle.Archive{Name: "generalsxzh-runtime-runtime-v1.2.3-linux-amd64.tar.gz", Size: 1, SHA256: strings.Repeat("a", 64)}}
	root := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		mode := uint32(0644)
		if name == "runtime/GeneralsXZH" {
			mode = 0755
		}
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, contents, os.FileMode(mode)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		d.Files = append(d.Files, runtimebundle.File{Path: name, Size: int64(len(contents)), SHA256: hex.EncodeToString(digest[:]), Mode: mode})
	}
	return d, root, filepath.Join(root, "kit/scripts/tooling/sfx/profiles/linux-zh.exclude")
}

func TestAssemblyCanonicalizesRuntimeAncestors(t *testing.T) {
	t.Parallel()
	d, packageRoot, profile := caseAssemblyFixture(t)
	assets := t.TempDir()
	for name, contents := range map[string]string{
		"window/MENUS/extrasmenu.wnd": "old retail overlay",
		"window/MENUS/OtherMenu.wnd":  "retail sibling",
		"window/Custom/keep.wnd":      "unrelated child",
	} {
		full := filepath.Join(assets, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	stage := filepath.Join(t.TempDir(), "stage")
	if err := assembleRuntime(context.Background(), assets, packageRoot, stage, profile, d); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Window/Menus/ExtrasMenu.wnd": "runtime overlay", "Window/Menus/OtherMenu.wnd": "retail sibling", "Window/Custom/keep.wnd": "unrelated child"} {
		got, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	// Checking directory entry spellings catches the regression on Windows
	// too: existence checks alone would hide a wrongly cased staged directory.
	for directory, want := range map[string][]string{"": {"GeneralsXZH", "Window"}, "Window": {"Custom", "Menus"}, "Window/Menus": {"ExtrasMenu.wnd", "OtherMenu.wnd"}} {
		entries, err := os.ReadDir(filepath.Join(stage, filepath.FromSlash(directory)))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != len(want) {
			t.Fatalf("%s has entries %v; want %v", directory, entries, want)
		}
		for index, entry := range entries {
			if entry.Name() != want[index] {
				t.Fatalf("%s entry %q; want %q", directory, entry.Name(), want[index])
			}
		}
	}
	original, err := os.ReadFile(filepath.Join(assets, "window/MENUS/extrasmenu.wnd"))
	if err != nil || string(original) != "old retail overlay" {
		t.Fatal("assembly changed retail source")
	}
}

func TestAssemblyRejectsRuntimeFileDirectoryShadows(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		directory bool
		message   string
	}{
		{"window", false, "retail file shadows runtime directory"},
		{"window/menus", false, "retail file shadows runtime directory"},
		{"window/menus/extrasmenu.wnd", true, "directory shadows runtime file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, packageRoot, profile := caseAssemblyFixture(t)
			assets := t.TempDir()
			full := filepath.Join(assets, filepath.FromSlash(test.name))
			if test.directory {
				if err := os.MkdirAll(full, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("shadow"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := assembleRuntime(context.Background(), assets, packageRoot, filepath.Join(t.TempDir(), "stage"), profile, d)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("shadow error = %v; want %s", err, test.message)
			}
		})
	}
}
