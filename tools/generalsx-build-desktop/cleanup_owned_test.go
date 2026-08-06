package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
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
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "dependency.zip"), []byte("cache"), 0o600)
	desktop := cleanupTestArtifact(t, "job-fresh", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("verified desktop SFX"))

	receipt := finalizeBuildCleanup("job-fresh", snapshot)
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
	cleanupAssertNotExist(t, filepath.Join(request.RepoRoot, "build"))
	cleanupAssertNotExist(t, filepath.Join(request.RepoRoot, "logs"))
}

func TestBuildCleanupOwnershipSurvivesRestartForStillDisposablePaths(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	firstSnapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupWriteFile(t, filepath.Join(request.RepoRoot, "partial-clone"), []byte("partial source"), 0o600)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "partial"), []byte("partial"), 0o600)
	firstReceipt := finalizeBuildCleanup("failed-job", firstSnapshot)
	if err := persistBuildCleanupOwnership(firstReceipt, false); err != nil {
		t.Fatal(err)
	}
	discardBuildCleanupReceipt(firstReceipt)

	secondRequest := request
	secondRequest.RepoRoot = filepath.Join(base, "successful-source")
	secondRequest.CacheDir = filepath.Join(base, "successful-cache")
	secondRequest.SteamCMDDir = filepath.Join(secondRequest.CacheDir, "steamcmd")
	secondRequest.Output = filepath.Join(secondRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	secondSnapshot := snapshotBuildCleanupWithOwnership(secondRequest, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(secondSnapshot, request.RepoRoot) {
		t.Fatalf("ambiguous partial source was recovered: %#v", secondSnapshot.candidates)
	}
	if !cleanupSnapshotContains(secondSnapshot, request.CacheDir) {
		t.Fatalf("managed cache was not recovered: %#v", secondSnapshot.candidates)
	}
	desktop := cleanupTestArtifact(t, "successful-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	secondReceipt := finalizeBuildCleanup("successful-job", secondSnapshot)
	if err := persistBuildCleanupOwnership(secondReceipt, true); err != nil {
		t.Fatal(err)
	}
	_, prepared, err := prepareBuildCleanup(secondReceipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, filepath.Join(request.RepoRoot, "partial-clone"), []byte("partial source"))
	cleanupAssertNotExist(t, request.CacheDir)
	cleanupAssertNotExist(t, ledgerPath)
	cleanupAssertFileContents(t, desktop.sourcePath, []byte("desktop"))
}

func TestBuildCleanupPersistedSourcePreservesLaterUserChanges(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	buildObject := filepath.Join(request.RepoRoot, "build", "linux64-deploy", "object.o")
	cleanupWriteFile(t, buildObject, []byte("generated object"), 0o600)
	receipt := finalizeBuildCleanup("source-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(request.RepoRoot, "user-notes.txt")
	cleanupWriteFile(t, userFile, []byte("keep user work"), 0o600)

	nextRequest := request
	nextRequest.RepoRoot = filepath.Join(base, "next-source")
	nextRequest.CacheDir = filepath.Join(base, "next-cache")
	nextRequest.SteamCMDDir = filepath.Join(nextRequest.CacheDir, "steamcmd")
	nextRequest.Output = filepath.Join(nextRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	restarted := snapshotBuildCleanupWithOwnership(nextRequest, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.RepoRoot) {
		t.Fatalf("modified persisted source was recovered: %#v", restarted.candidates)
	}

	desktop := cleanupTestArtifact(t, "source-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.RepoRoot) {
		t.Fatalf("modified source appeared in cleanup plan: %#v", plan.Entries)
	}
	if !cleanupPlanContains(plan, filepath.Dir(buildObject)) {
		t.Fatalf("generated build fallback was absent from cleanup plan: %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, userFile, []byte("keep user work"))
	cleanupAssertNotExist(t, filepath.Dir(buildObject))
}

func TestBuildCleanupPersistedSourcePreservesIgnoredUserFile(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	receipt := finalizeBuildCleanup("ignored-source-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	ignoredUserFile := filepath.Join(request.RepoRoot, ".env")
	cleanupWriteFile(t, ignoredUserFile, []byte("keep ignored user data"), 0o600)

	nextRequest := request
	nextRequest.RepoRoot = filepath.Join(base, "next-source")
	nextRequest.CacheDir = filepath.Join(base, "next-cache")
	nextRequest.SteamCMDDir = filepath.Join(nextRequest.CacheDir, "steamcmd")
	nextRequest.Output = filepath.Join(nextRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	if restarted := snapshotBuildCleanupWithOwnership(nextRequest, runtime.GOOS, ledgerPath); cleanupSnapshotContains(restarted, request.RepoRoot) {
		t.Fatalf("source with ignored user data was recovered: %#v", restarted.candidates)
	}
	desktop := cleanupTestArtifact(t, "ignored-source-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.RepoRoot) {
		t.Fatalf("source with ignored user data appeared in cleanup plan: %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, ignoredUserFile, []byte("keep ignored user data"))
}

func TestBuildCleanupPersistedSourcePreservesDetachedCommittedChange(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	receipt := finalizeBuildCleanup("committed-source-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	cleanupWriteFile(t, filepath.Join(request.RepoRoot, "tracked.txt"), []byte("user commit"), 0o600)
	cleanupRunGit(t, request.RepoRoot, "add", "tracked.txt")
	cleanupRunGit(t, request.RepoRoot, "commit", "-m", "user change")
	status := exec.Command("git", "-C", request.RepoRoot, "status", "--porcelain=v1")
	configureDesktopBackgroundCommand(status)
	if output, err := status.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("committed fixture is not clean: %q (%v)", output, err)
	}

	nextRequest := request
	nextRequest.RepoRoot = filepath.Join(base, "next-source")
	nextRequest.CacheDir = filepath.Join(base, "next-cache")
	nextRequest.SteamCMDDir = filepath.Join(nextRequest.CacheDir, "steamcmd")
	nextRequest.Output = filepath.Join(nextRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	restarted := snapshotBuildCleanupWithOwnership(nextRequest, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.RepoRoot) {
		t.Fatalf("clean committed user change was recovered: %#v", restarted.candidates)
	}
	desktop := cleanupTestArtifact(t, "committed-source-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.RepoRoot) {
		t.Fatalf("clean committed user change appeared in cleanup plan: %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, filepath.Join(request.RepoRoot, "tracked.txt"), []byte("user commit"))
}

func TestBuildCleanupPersistedSourcePreservesLocalBranchWork(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	receipt := finalizeBuildCleanup("branch-source-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	originalHead := ""
	for _, candidate := range receipt.candidates {
		if candidate.policy == cleanupManagedSourceRoot {
			originalHead = candidate.sourceHead
			break
		}
	}
	if originalHead == "" {
		t.Fatal("source ownership did not record the builder commit")
	}
	cleanupRunGit(t, request.RepoRoot, "switch", "-c", "user-work")
	cleanupWriteFile(t, filepath.Join(request.RepoRoot, "tracked.txt"), []byte("branch-only user work"), 0o600)
	cleanupRunGit(t, request.RepoRoot, "add", "tracked.txt")
	cleanupRunGit(t, request.RepoRoot, "commit", "-m", "branch user work")
	cleanupRunGit(t, request.RepoRoot, "checkout", "--detach", originalHead)

	nextRequest := request
	nextRequest.RepoRoot = filepath.Join(base, "next-source")
	nextRequest.CacheDir = filepath.Join(base, "next-cache")
	nextRequest.SteamCMDDir = filepath.Join(nextRequest.CacheDir, "steamcmd")
	nextRequest.Output = filepath.Join(nextRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	restarted := snapshotBuildCleanupWithOwnership(nextRequest, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.RepoRoot) {
		t.Fatalf("source with branch-only user work was recovered: %#v", restarted.candidates)
	}
	desktop := cleanupTestArtifact(t, "branch-source-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.RepoRoot) {
		t.Fatalf("source with branch-only user work appeared in cleanup plan: %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", request.RepoRoot, "show", "user-work:tracked.txt")
	configureDesktopBackgroundCommand(command)
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "branch-only user work" {
		t.Fatalf("branch-only user work was not preserved: %q (%v)", output, err)
	}
}

func TestBuildCleanupSourceChangeAfterReviewFailsClosed(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	receipt := finalizeBuildCleanup("source-review-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	desktop := cleanupTestArtifact(t, "source-review-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanupPlanContains(plan, request.RepoRoot) {
		t.Fatalf("clean source was absent from cleanup plan: %#v", plan.Entries)
	}
	userFile := filepath.Join(request.RepoRoot, ".env")
	cleanupWriteFile(t, userFile, []byte("keep ignored data after review"), 0o600)
	if _, err := executeBuildCleanup(context.Background(), prepared); err == nil || !strings.Contains(err.Error(), "source checkout") {
		t.Fatalf("execute error = %v, want managed source rejection", err)
	}
	cleanupAssertFileContents(t, userFile, []byte("keep ignored data after review"))
}

func TestBuildCleanupPersistedCachePreservesUnknownSibling(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "owned"), []byte("cache"), 0o600)
	receipt := finalizeBuildCleanup("cache-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(request.CacheDir, "keep-user-cache.txt")
	cleanupWriteFile(t, unknown, []byte("keep"), 0o600)

	restarted := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.CacheDir) {
		t.Fatalf("persisted cache with unknown sibling was recovered: %#v", restarted.candidates)
	}
	desktop := cleanupTestArtifact(t, "cache-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.CacheDir) {
		t.Fatalf("cache with unknown sibling appeared in cleanup plan: %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, unknown, []byte("keep"))
	cleanupAssertNotExist(t, filepath.Join(request.CacheDir, "downloads"))
}

func TestBuildCleanupCacheChangeAfterReviewFailsClosed(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "owned"), []byte("cache"), 0o600)
	receipt := finalizeBuildCleanup("cache-review-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	desktop := cleanupTestArtifact(t, "cache-review-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanupPlanContains(plan, request.CacheDir) {
		t.Fatalf("managed cache was absent from cleanup plan: %#v", plan.Entries)
	}
	unknown := filepath.Join(request.CacheDir, "keep-user-cache.txt")
	cleanupWriteFile(t, unknown, []byte("keep after review"), 0o600)
	if _, err := executeBuildCleanup(context.Background(), prepared); err == nil || !strings.Contains(err.Error(), "unknown top-level entry") {
		t.Fatalf("execute error = %v, want unknown-cache rejection", err)
	}
	cleanupAssertFileContents(t, unknown, []byte("keep after review"))
}

func TestBuildCleanupMissingCandidateDoesNotBlockRemainingPlan(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	request.Target = "linux"
	request.SkipGameBuild = true
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	if err := os.MkdirAll(request.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request.Output = filepath.Join(base, "generated", "GeneralsXZH-sfx")
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupWriteFile(t, request.Output, []byte("raw output"), 0o700)
	manifest := request.Output + ".stage-contents.txt"
	cleanupWriteFile(t, manifest, []byte("manifest"), 0o600)
	receipt := finalizeBuildCleanup("missing-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(request.Output); err != nil {
		t.Fatal(err)
	}
	desktop := cleanupTestArtifact(t, "missing-job", filepath.Join(base, "Desktop", "GeneralsXZH-sfx"), []byte("desktop"))
	plan, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPlanContains(plan, request.Output) || !cleanupPlanContains(plan, manifest) {
		t.Fatalf("missing/remaining plan entries = %#v", plan.Entries)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertNotExist(t, manifest)
}

func TestBuildCleanupOwnershipMarkerTamperFailsClosedAcrossRestart(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "owned"), []byte("cache"), 0o600)
	receipt := finalizeBuildCleanup("first-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	if len(receipt.candidates) == 0 {
		t.Fatal("fixture produced no persisted candidates")
	}
	tampered := receipt.candidates[0]
	cleanupWriteFile(t, tampered.markerPath, []byte("tampered\n"), 0o600)

	restarted := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, tampered.path) {
		t.Fatalf("tampered ownership was accepted for %q: %#v", tampered.path, restarted.candidates)
	}
	recoveredUntampered := false
	for _, candidate := range receipt.candidates[1:] {
		if cleanupSnapshotContains(restarted, candidate.path) {
			recoveredUntampered = true
			break
		}
	}
	if !recoveredUntampered {
		t.Fatalf("tampered record wedged independent valid ownership: %#v", restarted.candidates)
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("tamper check changed source root: %v", err)
	}
}

func TestBuildCleanupMissingOwnershipRecordDoesNotWedgeIndependentPaths(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "owned"), []byte("cache"), 0o600)
	receipt := finalizeBuildCleanup("first-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(request.RepoRoot); err != nil {
		t.Fatal(err)
	}

	secondRequest := request
	secondRequest.RepoRoot = filepath.Join(base, "next-source")
	secondRequest.CacheDir = filepath.Join(base, "next-cache")
	secondRequest.SteamCMDDir = filepath.Join(secondRequest.CacheDir, "steamcmd")
	secondRequest.Output = filepath.Join(secondRequest.RepoRoot, "build", "sfx", cleanupDefaultOutputName("linux"))
	restarted := snapshotBuildCleanupWithOwnership(secondRequest, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.RepoRoot) {
		t.Fatalf("missing stale root was recovered: %#v", restarted.candidates)
	}
	if !cleanupSnapshotContains(restarted, request.CacheDir) {
		t.Fatalf("missing stale root wedged independent cache ownership: %#v", restarted.candidates)
	}
}

func TestBuildCleanupOwnershipLedgerTamperFailsClosedAcrossRestart(t *testing.T) {
	base := t.TempDir()
	ledgerPath := filepath.Join(base, "private-state", "cleanup-ownership-v1.json")
	request := cleanupTestRequest(base)
	snapshot := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "owned"), []byte("cache"), 0o600)
	receipt := finalizeBuildCleanup("first-job", snapshot)
	if err := persistBuildCleanupOwnership(receipt, true); err != nil {
		t.Fatal(err)
	}
	cleanupWriteFile(t, ledgerPath, []byte("{\"version\":1,\"records\":[]}\n"), 0o600)

	restarted := snapshotBuildCleanupWithOwnership(request, runtime.GOOS, ledgerPath)
	if cleanupSnapshotContains(restarted, request.RepoRoot) || cleanupSnapshotContains(restarted, request.CacheDir) {
		t.Fatalf("tampered private ledger was accepted: %#v", restarted.candidates)
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("ledger tamper check changed source root: %v", err)
	}
}

func TestBuildCleanupDefaultCachePreservesUnknownSibling(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	_, defaultCache, err := cleanupCanonicalDefaultWorkspacePaths()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	request := cleanupTestRequest(base)
	request.Target = "windows"
	request.SkipGameBuild = true
	request.CacheDir = defaultCache
	request.SteamCMDDir = filepath.Join(defaultCache, "steamcmd")
	request.Output = filepath.Join(base, "missing.exe")
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	if err := os.MkdirAll(request.AssetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(defaultCache, "keep-user-cache.txt")
	cleanupWriteFile(t, sentinel, []byte("keep"), 0o600)
	managed := []string{
		filepath.Join(defaultCache, "downloads", "archive.zip"),
		filepath.Join(defaultCache, "vcpkg", "vcpkg"),
		filepath.Join(defaultCache, "vcpkg-linux", "vcpkg"),
	}
	for _, path := range managed {
		cleanupWriteFile(t, path, []byte("managed"), 0o700)
	}

	snapshot := snapshotBuildCleanup(request, runtime.GOOS)
	if cleanupSnapshotContains(snapshot, defaultCache) {
		t.Fatal("default cache root with an unknown sibling was adopted")
	}
	for _, path := range []string{filepath.Dir(managed[0]), filepath.Dir(managed[1]), filepath.Dir(managed[2])} {
		if !cleanupSnapshotContains(snapshot, path) {
			t.Errorf("known builder child was not eligible: %s", path)
		}
	}
	desktop := cleanupTestArtifact(t, "cache-job", filepath.Join(base, "Desktop", "GeneralsXZH.exe"), []byte("desktop"))
	receipt := finalizeBuildCleanup("cache-job", snapshot)
	_, prepared, err := prepareBuildCleanup(receipt, desktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeBuildCleanup(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	cleanupAssertFileContents(t, sentinel, []byte("keep"))
	for _, path := range managed {
		cleanupAssertNotExist(t, path)
	}
}

func TestBuildCleanupAdoptsOnlyCleanDetachedDefaultBuilderClone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	defaultRepo, _, err := cleanupCanonicalDefaultWorkspacePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(defaultRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupRunGit(t, defaultRepo, "init", "-b", "main")
	cleanupRunGit(t, defaultRepo, "config", "user.name", "GeneralsX Test")
	cleanupRunGit(t, defaultRepo, "config", "user.email", "test@example.invalid")
	cleanupRunGit(t, defaultRepo, "config", "commit.gpgsign", "false")
	cleanupWriteFile(t, filepath.Join(defaultRepo, "tracked.txt"), []byte("tracked"), 0o600)
	cleanupRunGit(t, defaultRepo, "add", "tracked.txt")
	cleanupRunGit(t, defaultRepo, "commit", "-m", "fixture")
	cleanupRunGit(t, defaultRepo, "remote", "add", "origin", "https://github.com/moloch--/Generals.git")
	cleanupRunGit(t, defaultRepo, "checkout", "--detach", "HEAD")
	cleanupWriteFetchHead(t, defaultRepo)
	reflogPath := filepath.Join(defaultRepo, ".git", "logs", "HEAD")
	reflog, err := os.OpenFile(reflogPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(reflog, "fixture clone: from https://github.com/moloch--/Generals.git\nfixture checkout: moving from main to FETCH_HEAD\n"); err != nil {
		reflog.Close()
		t.Fatal(err)
	}
	if err := reflog.Close(); err != nil {
		t.Fatal(err)
	}
	request := cleanupTestRequest(t.TempDir())
	request.RepoRoot = defaultRepo
	request.SourceRepo = "https://github.com/moloch--/Generals.git"
	request.SkipGameBuild = true
	request.Target = "windows"
	request.Output = filepath.Join(t.TempDir(), "missing.exe")
	if !cleanupSnapshotContains(snapshotBuildCleanup(request, runtime.GOOS), defaultRepo) {
		t.Fatal("clean detached default builder clone was not adopted")
	}
	cleanupWriteFile(t, filepath.Join(defaultRepo, "untracked.txt"), []byte("user data"), 0o600)
	if cleanupSnapshotContains(snapshotBuildCleanup(request, runtime.GOOS), defaultRepo) {
		t.Fatal("dirty default clone was adopted")
	}
	if err := os.Remove(filepath.Join(defaultRepo, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	cleanupRunGit(t, defaultRepo, "switch", "main")
	if cleanupSnapshotContains(snapshotBuildCleanup(request, runtime.GOOS), defaultRepo) {
		t.Fatal("branched default clone was adopted")
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
	managedChild := filepath.Join(request.CacheDir, "downloads")
	cleanupWriteFile(t, filepath.Join(managedChild, "owned.txt"), []byte("delete"), 0o600)
	if err := os.Symlink(external, filepath.Join(managedChild, "external-link")); err != nil {
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
		SourceRepo: "https://github.com/moloch--/Generals.git",
	}
}

func cleanupIsolatedOutputRequest(t *testing.T, base string) BuildRequest {
	t.Helper()
	request := cleanupTestRequest(base)
	request.Target = "windows"
	request.SkipGameBuild = true
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
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupRunGit(t, path, "init", "-b", "main")
	cleanupRunGit(t, path, "config", "user.name", "GeneralsX Test")
	cleanupRunGit(t, path, "config", "user.email", "test@example.invalid")
	cleanupRunGit(t, path, "config", "commit.gpgsign", "false")
	cleanupWriteFile(t, filepath.Join(path, "tracked.txt"), []byte("tracked"), 0o600)
	cleanupWriteFile(t, filepath.Join(path, ".gitignore"), []byte("build/\nlogs/\n*.tar.gz\n*.zip\n"), 0o600)
	cleanupRunGit(t, path, "add", "tracked.txt", ".gitignore")
	cleanupRunGit(t, path, "commit", "-m", "fixture")
	cleanupRunGit(t, path, "remote", "add", "origin", "https://github.com/moloch--/Generals.git")
	cleanupRunGit(t, path, "checkout", "--detach", "HEAD")
	cleanupWriteFetchHead(t, path)
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

func cleanupPlanContains(plan BuildCleanupPlan, path string) bool {
	want, err := cleanupCanonicalFuturePath(path)
	if err != nil {
		return false
	}
	for _, entry := range plan.Entries {
		if cleanupSamePath(entry.Path, want) {
			return true
		}
	}
	return false
}

func cleanupRunGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	configureDesktopBackgroundCommand(command)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func cleanupWriteFetchHead(t *testing.T, directory string) {
	t.Helper()
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = directory
	configureDesktopBackgroundCommand(command)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(output))
	cleanupWriteFile(t, filepath.Join(directory, ".git", "FETCH_HEAD"), []byte(head+"\t\tbranch 'main' of https://github.com/moloch--/Generals\n"), 0o600)
}
