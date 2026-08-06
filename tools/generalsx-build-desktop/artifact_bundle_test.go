package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyCompletedMacOSAppPreservesBundleAndChoosesUnusedName(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	writeArtifactBundleFixture(t, source)
	completed, err := inspectCompletedArtifact("job-app", source)
	if err != nil {
		t.Fatal(err)
	}
	desktop := t.TempDir()
	existing := filepath.Join(desktop, "GeneralsXZH.app")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "keep"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination, err := copyCompletedArtifactToDirectory(context.Background(), completed, desktop, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(desktop, "GeneralsXZH (1).app"); destination != want {
		t.Fatalf("destination = %q, want %q", destination, want)
	}
	copied, err := inspectCompletedArtifact("job-app", destination)
	if err != nil {
		t.Fatal(err)
	}
	if copied.sourceSHA256 != completed.sourceSHA256 {
		t.Fatal("copied application fingerprint differs from source")
	}
	icon, err := os.ReadFile(filepath.Join(destination, "Contents", "Resources", "GeneralsXZH.icns"))
	if err != nil || string(icon) != "fixture icon" {
		t.Fatalf("copied icon = %q, %v", icon, err)
	}
	if runtime.GOOS != "windows" {
		executable, err := os.Stat(filepath.Join(destination, "Contents", "MacOS", "GeneralsXZH"))
		if err != nil {
			t.Fatal(err)
		}
		if executable.Mode().Perm() != 0o751 {
			t.Fatalf("copied executable mode = %o, want 751", executable.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(existing, "keep")); err != nil {
		t.Fatalf("existing application was replaced: %v", err)
	}
	assertNoBundleCopyTemporary(t, desktop)
}

func TestCopyCompletedMacOSAppReportsCumulativePayloadBytes(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	writeArtifactBundleFixture(t, source)
	completed, err := inspectCompletedArtifact("job-app-progress", source)
	if err != nil {
		t.Fatal(err)
	}
	if completed.sourceBytes <= 0 {
		t.Fatalf("application payload bytes = %d", completed.sourceBytes)
	}
	var progress []artifactCopyProgress
	ctx := withArtifactCopyProgress(context.Background(), func(event artifactCopyProgress) {
		progress = append(progress, event)
	})
	if _, err := copyCompletedArtifactToDirectory(ctx, completed, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	previousBytes := int64(0)
	for _, event := range progress {
		if event.totalBytes != completed.sourceBytes {
			t.Fatalf("application copy total = %d, want %d", event.totalBytes, completed.sourceBytes)
		}
		if event.bytesCopied < previousBytes {
			t.Fatalf("application byte progress regressed from %d to %d", previousBytes, event.bytesCopied)
		}
		previousBytes = event.bytesCopied
	}
	if len(progress) == 0 || progress[len(progress)-1].bytesCopied != completed.sourceBytes || progress[len(progress)-1].percent != 100 {
		t.Fatalf("final application progress = %#v", progress)
	}
}

func TestMacOSAppArtifactRejectsLinksAndDetectsNestedTampering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require Windows developer mode")
	}
	t.Run("symbolic link", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
		writeArtifactBundleFixture(t, source)
		icon := filepath.Join(source, "Contents", "Resources", "GeneralsXZH.icns")
		if err := os.Remove(icon); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../Info.plist", icon); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectCompletedArtifact("job-app", source); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("linked application error = %v", err)
		}
	})

	t.Run("nested contents", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
		writeArtifactBundleFixture(t, source)
		completed, err := inspectCompletedArtifact("job-app", source)
		if err != nil {
			t.Fatal(err)
		}
		iconPath := filepath.Join(source, "Contents", "Resources", "GeneralsXZH.icns")
		info, err := os.Stat(iconPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(iconPath, []byte("tampered icon"), info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(iconPath, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := revalidateCompletedArtifact(context.Background(), completed); err == nil || !strings.Contains(err.Error(), "contents changed") {
			t.Fatalf("tampered application error = %v", err)
		}
	})
}

func TestCancelledMacOSAppCopyRemovesTemporaryBundle(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	writeArtifactBundleFixture(t, source)
	completed, err := inspectCompletedArtifact("job-app", source)
	if err != nil {
		t.Fatal(err)
	}
	desktop := t.TempDir()
	ctx := newCancelAfterChecksContext(2)
	if _, err := copyCompletedArtifactToDirectory(ctx, completed, desktop, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled application copy error = %v", err)
	}
	assertNoBundleCopyTemporary(t, desktop)
}

func TestRejectedMacOSAppCopyNeverPublishesTemporaryBundle(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH.app")
	writeArtifactBundleFixture(t, source)
	completed, err := inspectCompletedArtifact("job-app", source)
	if err != nil {
		t.Fatal(err)
	}
	completed.target = "macos"
	desktop := t.TempDir()
	var verifiedPath string
	verifier := func(_ context.Context, path, target string) error {
		verifiedPath = path
		if target != "macos" {
			t.Fatalf("verification target = %q, want macos", target)
		}
		return errors.New("fixture signature failure")
	}
	if _, err := copyCompletedArtifactToDirectory(context.Background(), completed, desktop, verifier); err == nil ||
		!strings.Contains(err.Error(), "fixture signature failure") {
		t.Fatalf("rejected application copy error = %v", err)
	}
	if filepath.Dir(verifiedPath) != desktop || !strings.HasPrefix(filepath.Base(verifiedPath), ".generalsx-copy-") {
		t.Fatalf("verifier path = %q, want a private Desktop sibling", verifiedPath)
	}
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Desktop entries after rejected app copy = %v, want none", entries)
	}
}

func writeArtifactBundleFixture(t *testing.T, path string) {
	t.Helper()
	if err := createArtifactBundleFixture(path); err != nil {
		t.Fatal(err)
	}
}

func createArtifactBundleFixture(path string) error {
	for _, directory := range []string{
		filepath.Join(path, "Contents", "MacOS"),
		filepath.Join(path, "Contents", "Resources"),
		filepath.Join(path, "Contents", "Helpers"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	files := []struct {
		path     string
		contents string
		mode     os.FileMode
	}{
		{filepath.Join(path, "Contents", "Info.plist"), "fixture plist", 0o644},
		{filepath.Join(path, "Contents", "MacOS", "GeneralsXZH"), "fixture sfx", 0o751},
		{filepath.Join(path, "Contents", "Resources", "GeneralsXZH.icns"), "fixture icon", 0o644},
		{filepath.Join(path, "Contents", "Helpers", "GeneralsX-SFX-Progress"), "fixture helper", 0o751},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, []byte(file.contents), file.mode); err != nil {
			return err
		}
	}
	return nil
}

func assertNoBundleCopyTemporary(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".generalsx-copy-") {
			t.Fatalf("temporary application bundle remained after copy: %q", entry.Name())
		}
	}
}
