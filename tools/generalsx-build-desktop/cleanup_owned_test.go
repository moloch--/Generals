package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildCleanupFreshRootsPreserveDesktopAndAssets(t *testing.T) {
	base := t.TempDir()
	request := cleanupTestRequest(base)
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	assetSentinel := filepath.Join(request.AssetsDir, "owned.big")
	cleanupWriteFile(t, assetSentinel, []byte("retail data"), 0o600)

	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	cleanupWriteFile(t, filepath.Join(request.RepoRoot, "source.txt"), []byte("source"), 0o600)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "dependency.zip"), []byte("cache"), 0o600)
	desktop := cleanupTestArtifact(t, "job-fresh", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("verified desktop SFX"))

	receipt := finalizeBuildCleanup("job-fresh", snapshot)
	if len(receipt.candidates) != 2 {
		t.Fatalf("owned candidates = %d, want repo and cache", len(receipt.candidates))
	}
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("plan entries = %d, want 2", len(plan.Entries))
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertNotExist(t, request.RepoRoot)
	cleanupAssertNotExist(t, request.CacheDir)
	cleanupAssertFileContents(t, desktop.sourcePath, []byte("verified desktop SFX"))
	cleanupAssertFileContents(t, assetSentinel, []byte("retail data"))
}

func TestBuildCleanupPreservesPreexistingRoots(t *testing.T) {
	base := t.TempDir()
	request := cleanupTestRequest(base)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	repoSentinel := filepath.Join(request.RepoRoot, "keep-source.txt")
	cacheSentinel := filepath.Join(request.CacheDir, "keep-cache.txt")
	cleanupWriteFile(t, repoSentinel, []byte("keep repo"), 0o600)
	cleanupWriteFile(t, cacheSentinel, []byte("keep cache"), 0o600)
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	generated := []string{
		filepath.Join(request.RepoRoot, "build", "linux64-deploy", "object.o"),
		filepath.Join(request.CacheDir, "downloads", "dependency.zip"),
		filepath.Join(request.CacheDir, "steamcmd", "steamcmd.sh"),
		filepath.Join(request.CacheDir, "vcpkg-linux", "vcpkg"),
		filepath.Join(request.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux")),
		filepath.Join(request.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux")+".stage-contents.txt"),
		filepath.Join(request.RepoRoot, cleanupPortableBundleName("linux")),
		filepath.Join(request.RepoRoot, cleanupBuildLog("linux")),
	}
	for _, path := range generated {
		cleanupWriteFile(t, path, []byte("generated"), 0o700)
	}
	desktop := cleanupTestArtifact(t, "job-existing", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-existing", snapshot)
	for _, candidate := range receipt.candidates {
		if candidate.path == request.RepoRoot || candidate.path == request.CacheDir {
			t.Fatalf("preexisting root was claimed: %s", candidate.path)
		}
	}
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, repoSentinel, []byte("keep repo"))
	cleanupAssertFileContents(t, cacheSentinel, []byte("keep cache"))
	for _, path := range generated {
		cleanupAssertNotExist(t, path)
	}
}

func TestBuildCleanupRejectsChangedOwnershipMarker(t *testing.T) {
	base := t.TempDir()
	request := cleanupIsolatedOutputRequest(t, base)
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	output := request.Output
	cleanupWriteFile(t, output, []byte("generated output"), 0o700)
	desktop := cleanupTestArtifact(t, "job-marker", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-marker", snapshot)
	if len(receipt.candidates) != 1 {
		t.Fatalf("owned candidates = %d, want 1", len(receipt.candidates))
	}
	cleanupWriteFile(t, receipt.candidates[0].markerPath, []byte("tampered"), 0o600)
	if _, _, err := prepareBuildCleanup(receipt, desktop); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("prepare error = %v, want marker rejection", err)
	}
	cleanupAssertFileContents(t, output, []byte("generated output"))
}

func TestBuildCleanupRejectsSameSizeRestoredMtimeContentTamper(t *testing.T) {
	base := t.TempDir()
	request := cleanupIsolatedOutputRequest(t, base)
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupWriteFile(t, request.Output, []byte("original"), 0o700)
	desktop := cleanupTestArtifact(t, "job-content", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-content", snapshot)
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(request.Output)
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriteFile(t, request.Output, []byte("modified"), before.Mode().Perm())
	if err := os.Chtimes(request.Output, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("execute error = %v, want content-change rejection", err)
	}
	cleanupAssertFileContents(t, request.Output, []byte("modified"))
}

func TestBuildCleanupRejectsDesktopContentTamperAfterReview(t *testing.T) {
	base := t.TempDir()
	request := cleanupIsolatedOutputRequest(t, base)
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupWriteFile(t, request.Output, []byte("generated output"), 0o700)
	desktop := cleanupTestArtifact(t, "job-desktop-content", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-desktop-content", snapshot)
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	tamperArtifactPreservingMetadata(t, desktop, []byte("changed"))
	if _, err := executeBuildCleanup(context.Background(), prepared); err == nil || !strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("execute error = %v, want Desktop content-change rejection", err)
	}
	cleanupAssertFileContents(t, request.Output, []byte("generated output"))
}

func TestBuildCleanupSymlinkDoesNotDeleteExternalSentinel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation depends on Developer Mode; junction coverage runs in native integration")
	}
	base := t.TempDir()
	request := cleanupTestRequest(base)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	external := filepath.Join(base, "external")
	sentinel := filepath.Join(external, "sentinel.txt")
	cleanupWriteFile(t, sentinel, []byte("keep"), 0o600)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "owned.txt"), []byte("delete"), 0o600)
	if err := os.Symlink(external, filepath.Join(request.CacheDir, "external-link")); err != nil {
		t.Fatal(err)
	}
	desktop := cleanupTestArtifact(t, "job-link", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-link", snapshot)
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertNotExist(t, request.CacheDir)
	cleanupAssertFileContents(t, sentinel, []byte("keep"))
}

func TestBuildCleanupDesktopInsideRootExcludesRootWithoutPoisoningReceipt(t *testing.T) {
	base := t.TempDir()
	request := cleanupTestRequest(base)
	if err := os.MkdirAll(request.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	desktop := cleanupTestArtifact(t, "job-overlap", filepath.Join(request.RepoRoot, "DesktopCopy", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-overlap", snapshot)
	plan, _, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("protected-root plan entries = %v, want none", plan.Entries)
	}
	// Preparing again proves the first preview did not delete the source marker.
	if _, _, err := prepareBuildCleanup(receipt, desktop); err != nil {
		t.Fatalf("second prepare failed after protected exclusion: %v", err)
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("protected repo was changed: %v", err)
	}
}

func TestBuildCleanupRetrySkipsAlreadyRemovedRoot(t *testing.T) {
	base := t.TempDir()
	request := cleanupIsolatedOutputRequest(t, base)
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupWriteFile(t, request.Output, []byte("generated"), 0o700)
	desktop := cleanupTestArtifact(t, "job-retry", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	receipt := finalizeBuildCleanup("job-retry", snapshot)
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	plan, retry, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatalf("prepare retry: %v", err)
	}
	if len(plan.Entries) != 0 || len(retry.candidates) != 0 {
		t.Fatalf("retry retained removed candidate: plan=%v receipt=%d", plan.Entries, len(retry.candidates))
	}
}

func TestDiscardBuildCleanupReceiptDoesNotRemoveReplacedMarker(t *testing.T) {
	base := t.TempDir()
	request := cleanupIsolatedOutputRequest(t, base)
	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	cleanupWriteFile(t, request.Output, []byte("generated"), 0o700)
	receipt := finalizeBuildCleanup("job-discard", snapshot)
	if len(receipt.candidates) != 1 {
		t.Fatalf("owned candidates = %d, want 1", len(receipt.candidates))
	}
	marker := receipt.candidates[0].markerPath
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	cleanupWriteFile(t, marker, receipt.candidates[0].markerContents, 0o600)
	replacementTime := receipt.candidates[0].markerInfo.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(marker, replacementTime, replacementTime); err != nil {
		t.Fatal(err)
	}
	discardBuildCleanupReceipt(receipt)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("replaced marker was removed: %v", err)
	}
}

func TestBuildCleanupSnapshotUsesEffectiveDefaultsAndManagedChildren(t *testing.T) {
	base := t.TempDir()
	request := cleanupTestRequest(base)
	request.Target = "macos"
	request.Output = ""
	request.AppOutput = ""
	request.SteamCMDDir = filepath.Join(base, "custom-steamcmd")
	request.WithOnlineServer = true
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	if err := os.MkdirAll(filepath.Join(request.CacheDir, "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotBuildCleanup(request, "darwin")
	want := []string{
		filepath.Join(request.RepoRoot, "build", "sfx", cleanupDefaultOutputName("macos")),
		filepath.Join(request.RepoRoot, "build", "sfx", "GeneralsXZH.app"),
		request.SteamCMDDir,
		filepath.Join(request.CacheDir, "sources", "generals-server"),
		filepath.Join(request.CacheDir, "vulkansdk-installer-1.4.341.1"),
	}
	for _, path := range want {
		if !cleanupSnapshotContains(snapshot, path) {
			t.Errorf("snapshot missing %s", path)
		}
	}
}

func cleanupTestRequest(base string) BuildRequest {
	return BuildRequest{
		RepoRoot: filepath.Join(base, "repo"), CacheDir: filepath.Join(base, "cache"),
		AssetsDir: filepath.Join(base, "assets"), Target: "linux",
	}
}

func cleanupIsolatedOutputRequest(t *testing.T, base string) BuildRequest {
	t.Helper()
	request := cleanupTestRequest(base)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	if err := os.MkdirAll(request.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Precreate every non-output candidate so the receipt isolates one file.
	for _, path := range []string{
		filepath.Join(request.RepoRoot, "build", "linux64-deploy"),
		filepath.Join(request.CacheDir, "downloads"),
		filepath.Join(request.CacheDir, "steamcmd"),
		filepath.Join(request.CacheDir, "vcpkg-linux"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(request.RepoRoot, cleanupPortableBundleName("linux")),
		filepath.Join(request.RepoRoot, cleanupBuildLog("linux")),
	} {
		cleanupWriteFile(t, path, []byte("preexisting"), 0o600)
	}
	request.Output = filepath.Join(base, "generated", "GeneralsXZH-sfx")
	// The stage manifest is separately absent; precreate it to keep the receipt isolated.
	cleanupWriteFile(t, request.Output+".stage-contents.txt", []byte("preexisting"), 0o600)
	return request
}

func cleanupCreateSimulatedRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func cleanupTestArtifact(t *testing.T, jobID, path string, contents []byte) *completedArtifact {
	t.Helper()
	cleanupWriteFile(t, path, contents, 0o700)
	artifact, err := inspectCompletedArtifact(jobID, path)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func cleanupWriteFile(t *testing.T, path string, contents []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func cleanupAssertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still exists or could not be inspected: %v", path, err)
	}
}

func cleanupAssertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(want) {
		t.Fatalf("%s contents = %q, want %q", path, contents, want)
	}
}

func cleanupSnapshotContains(snapshot *buildCleanupSnapshot, path string) bool {
	want, err := cleanupCanonicalFuturePath(path)
	if err != nil {
		return false
	}
	for _, candidate := range snapshot.candidates {
		if cleanupSamePath(candidate.path, want) {
			return true
		}
	}
	return false
}
