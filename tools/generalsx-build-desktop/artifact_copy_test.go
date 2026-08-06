package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancelAfterChecksContext struct {
	mu          sync.Mutex
	done        chan struct{}
	checks      int
	cancelAfter int
	err         error
}

func newCancelAfterChecksContext(cancelAfter int) *cancelAfterChecksContext {
	return &cancelAfterChecksContext{done: make(chan struct{}), cancelAfter: cancelAfter}
}

func (ctx *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterChecksContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *cancelAfterChecksContext) Value(any) any               { return nil }

func (ctx *cancelAfterChecksContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.err != nil {
		return ctx.err
	}
	ctx.checks++
	if ctx.checks > ctx.cancelAfter {
		ctx.err = context.Canceled
		close(ctx.done)
	}
	return ctx.err
}

func TestCopyArtifactToDirectoryRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	sourceDirectory := t.TempDir()
	empty := filepath.Join(sourceDirectory, "empty-sfx")
	if err := os.WriteFile(empty, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := copyArtifactToDirectory(empty, desktop); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty source error = %v", err)
	}
	if _, err := copyArtifactToDirectory(sourceDirectory, desktop); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory source error = %v", err)
	}

	target := filepath.Join(sourceDirectory, "real-sfx")
	if err := os.WriteFile(target, []byte("sfx"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(sourceDirectory, "linked-sfx")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, err := copyArtifactToDirectory(symlink, desktop); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink source error = %v", err)
	}
}

func TestCopyArtifactToDirectoryRequiresExistingDesktopDirectory(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH-sfx")
	if err := os.WriteFile(source, []byte("sfx"), 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := copyArtifactToDirectory(source, missing); err == nil || !strings.Contains(err.Error(), "inspect Desktop directory") {
		t.Fatalf("missing Desktop error = %v", err)
	}
	notDirectory := filepath.Join(t.TempDir(), "desktop-file")
	if err := os.WriteFile(notDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyArtifactToDirectory(source, notDirectory); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Desktop file error = %v", err)
	}
}

func TestCopyArtifactToDirectoryPublishesWithoutTemporaryFiles(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	source := filepath.Join(t.TempDir(), "GeneralsXZH-sfx.exe")
	if err := os.WriteFile(source, []byte("verified SFX"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GeneralsXZH-sfx.exe", "GeneralsXZH-sfx (1).exe"} {
		if err := os.WriteFile(filepath.Join(desktop, name), []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	destination, err := copyArtifactToDirectory(source, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(desktop, "GeneralsXZH-sfx (2).exe"); destination != want {
		t.Fatalf("destination = %q, want %q", destination, want)
	}
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".generalsx-copy-") {
			t.Fatalf("temporary Desktop copy remained after publish: %q", entry.Name())
		}
	}
}

func TestCopyArtifactToDirectoryCleansTemporaryFileWhenNamesAreExhausted(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	baseName := "GeneralsXZH-sfx"
	source := filepath.Join(t.TempDir(), baseName)
	if err := os.WriteFile(source, []byte("verified SFX"), 0o700); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < desktopCopyNameAttempts; attempt++ {
		if err := os.WriteFile(desktopCopyPath(desktop, baseName, attempt), []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := copyArtifactToDirectory(source, desktop); err == nil || !strings.Contains(err.Error(), "unused filename") {
		t.Fatalf("exhausted filename copy error = %v", err)
	}
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".generalsx-copy-") {
			t.Fatalf("temporary Desktop copy remained after failure: %q", entry.Name())
		}
	}
}

func TestCopyCompletedArtifactCancellationRemovesPartialCopy(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	baseName := "GeneralsXZH-sfx"
	source := filepath.Join(t.TempDir(), baseName)
	if err := os.WriteFile(source, bytes.Repeat([]byte("verified SFX"), 16*1024), 0o700); err != nil {
		t.Fatal(err)
	}
	completed, err := inspectCompletedArtifact("job-fixture", source)
	if err != nil {
		t.Fatal(err)
	}

	// The first two checks happen before the temporary file is created; the
	// third precedes the first read, and the fourth cancels immediately after it.
	ctx := newCancelAfterChecksContext(3)
	if _, err := copyCompletedArtifactToDirectory(ctx, completed, desktop, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Desktop entries after cancelled copy = %v, want none", entries)
	}
}

func TestCopyCompletedArtifactReportsMonotonicByteProgress(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	source := filepath.Join(t.TempDir(), "GeneralsXZH-sfx")
	payload := bytes.Repeat([]byte("verified SFX progress fixture"), 64*1024)
	if err := os.WriteFile(source, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	completed, err := inspectCompletedArtifact("job-progress", source)
	if err != nil {
		t.Fatal(err)
	}
	var progress []artifactCopyProgress
	ctx := withArtifactCopyProgress(context.Background(), func(event artifactCopyProgress) {
		progress = append(progress, event)
	})

	if _, err := copyCompletedArtifactToDirectory(ctx, completed, desktop, nil); err != nil {
		t.Fatal(err)
	}
	if len(progress) < 3 {
		t.Fatalf("copy progress events = %#v, want copying and post-copy stages", progress)
	}
	previousBytes := int64(0)
	previousPercent := 0
	seenVerifying := false
	seenPublishing := false
	for _, event := range progress {
		if event.bytesCopied < previousBytes || event.percent < previousPercent {
			t.Fatalf("copy progress regressed from %d/%d to %d/%d", previousBytes, previousPercent, event.bytesCopied, event.percent)
		}
		if event.totalBytes != int64(len(payload)) {
			t.Fatalf("copy total bytes = %d, want %d", event.totalBytes, len(payload))
		}
		previousBytes, previousPercent = event.bytesCopied, event.percent
		seenVerifying = seenVerifying || event.phase == "verifying"
		seenPublishing = seenPublishing || event.phase == "publishing"
	}
	last := progress[len(progress)-1]
	if last.bytesCopied != int64(len(payload)) || last.percent != 100 {
		t.Fatalf("final byte progress = %#v", last)
	}
	if !seenVerifying || !seenPublishing {
		t.Fatalf("post-copy progress stages = verifying:%t publishing:%t", seenVerifying, seenPublishing)
	}
}

func TestRevalidateCompletedArtifactRejectsInPlaceContentChangeWithRestoredMetadata(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "GeneralsXZH-sfx")
	if err := os.WriteFile(source, []byte("verified SFX alpha"), 0o751); err != nil {
		t.Fatal(err)
	}
	completed, err := inspectCompletedArtifact("job-fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	tamperArtifactPreservingMetadata(t, completed, []byte("verified SFX omega"))

	if err := revalidateCompletedArtifact(context.Background(), completed); err == nil ||
		!strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("revalidate tampered artifact error = %v", err)
	}
}

func TestCopyCompletedArtifactRejectsInPlaceContentChangeWithRestoredMetadata(t *testing.T) {
	t.Parallel()
	desktop := t.TempDir()
	source := filepath.Join(t.TempDir(), "GeneralsXZH-sfx")
	if err := os.WriteFile(source, []byte("verified SFX alpha"), 0o751); err != nil {
		t.Fatal(err)
	}
	completed, err := inspectCompletedArtifact("job-fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	tamperArtifactPreservingMetadata(t, completed, []byte("verified SFX omega"))

	if _, err := copyCompletedArtifactToDirectory(context.Background(), completed, desktop, nil); err == nil ||
		!strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("copy tampered artifact error = %v", err)
	}
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Desktop entries after rejected tampered copy = %v, want none", entries)
	}
}

func tamperArtifactPreservingMetadata(t *testing.T, completed *completedArtifact, replacement []byte) {
	t.Helper()
	if int64(len(replacement)) != completed.sourceInfo.Size() {
		t.Fatalf("replacement size = %d, want %d", len(replacement), completed.sourceInfo.Size())
	}
	if err := os.WriteFile(completed.sourcePath, replacement, completed.sourceInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(completed.sourcePath, completed.sourceInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(
		completed.sourcePath,
		completed.sourceInfo.ModTime(),
		completed.sourceInfo.ModTime(),
	); err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(completed.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !artifactInfoMatches(completed.sourceInfo, current) {
		t.Fatalf("tamper fixture did not restore artifact metadata: before=%v after=%v", completed.sourceInfo, current)
	}
}
