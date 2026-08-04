package buildcli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecureArchivePathRejectsTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, value := range []string{
		"../escape",
		"/absolute",
		"//server/share",
		"C:/absolute",
		"C:relative",
		`bad\\path`,
	} {
		if _, err := secureArchivePath(root, value); err == nil {
			t.Errorf("secureArchivePath(%q) succeeded", value)
		}
	}
	if _, err := secureArchivePath(root, "linux32/steamcmd"); err != nil {
		t.Fatal(err)
	}
}

func TestExtractZipPublishesPrivateTree(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "steamcmd.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	member, err := archive.Create("steamcmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "steamcmd")
	if err := extractZip(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "steamcmd.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "fixture" {
		t.Fatalf("contents = %q", contents)
	}
	if err := extractZip(archivePath, destination); err == nil {
		t.Fatal("extractZip replaced an existing directory")
	}
}

func TestValidateArchiveSymlinkRejectsEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "nested", "link")
	if err := validateArchiveSymlink(root, path, "../file", "nested/link"); err != nil {
		t.Fatalf("safe symlink rejected: %v", err)
	}
	if err := validateArchiveSymlink(root, path, "../../outside", "nested/link"); err == nil {
		t.Fatal("escaping symlink succeeded")
	}
	for _, target := range []string{"/absolute", "C:/absolute", "C:relative", `..\\outside`} {
		if err := validateArchiveSymlink(root, path, target, "nested/link"); err == nil {
			t.Errorf("unsafe symlink target %q succeeded", target)
		}
	}
}

func TestExtractZipCreatesSafeSymlinkAfterFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	archivePath := filepath.Join(t.TempDir(), "links.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	linkHeader := &zip.FileHeader{Name: "bin/tool"}
	linkHeader.SetMode(os.ModeSymlink | 0o777)
	link, err := archive.CreateHeader(linkHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := link.Write([]byte("../lib/tool")); err != nil {
		t.Fatal(err)
	}
	regular, err := archive.Create("lib/tool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := regular.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "sdk")
	if err := extractZip(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, []byte("fixture")) {
		t.Fatalf("symlink contents = %q", contents)
	}
}
