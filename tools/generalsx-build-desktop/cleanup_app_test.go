package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
)

func TestCleanupBuildRequiresCopyAndConsumesExactReviewedPlan(t *testing.T) {
	app, request, dependencies, recorder, assetSentinel := cleanupAppFixture(t)
	var verifiedTarget string
	dependencies.verifyArtifact = func(_ context.Context, _ string, target string) error {
		verifiedTarget = target
		return nil
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if _, err := app.GetBuildCleanupPlan(jobID); err == nil || !strings.Contains(err.Error(), "copy") {
		t.Fatalf("cleanup plan before Desktop copy error = %v", err)
	}
	desktopPath, err := app.CopyBuildArtifactToDesktop(jobID)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := app.GetBuildCleanupPlan(jobID)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := app.GetBuildCleanupPlan(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.PlanID == "" || secondPlan.PlanID == "" || firstPlan.PlanID == secondPlan.PlanID {
		t.Fatalf("cleanup plan IDs = %q and %q", firstPlan.PlanID, secondPlan.PlanID)
	}
	if len(secondPlan.Entries) != 2 {
		t.Fatalf("cleanup entries = %#v, want source and cache roots", secondPlan.Entries)
	}
	if secondPlan.DesktopCopyPath != desktopPath {
		t.Fatalf("Desktop path = %q, want %q", secondPlan.DesktopCopyPath, desktopPath)
	}
	if _, err := app.CleanupBuild(jobID, firstPlan.PlanID); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale cleanup plan error = %v", err)
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("stale plan changed source root: %v", err)
	}

	result, err := app.CleanupBuild(jobID, secondPlan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Removed 2 builder-owned paths") {
		t.Fatalf("cleanup result = %q", result)
	}
	if verifiedTarget != fileArtifactTestTarget() {
		t.Fatalf("verified target = %q, want %q", verifiedTarget, fileArtifactTestTarget())
	}
	cleanupAssertNotExist(t, request.RepoRoot)
	cleanupAssertNotExist(t, request.CacheDir)
	cleanupAssertFileContents(t, desktopPath, []byte("verified SFX fixture"))
	cleanupAssertFileContents(t, assetSentinel, []byte("owned retail fixture"))
	if _, err := app.CleanupBuild(jobID, secondPlan.PlanID); err == nil {
		t.Fatal("replayed cleanup plan was accepted")
	}
}

func TestCleanupBuildRechecksDesktopDigestAfterVerifier(t *testing.T) {
	app, request, dependencies, recorder, _ := cleanupAppFixture(t)
	verifierStarted := make(chan struct{})
	releaseVerifier := make(chan struct{})
	var cleanupCalled atomic.Bool
	var verifierCalls atomic.Int32
	dependencies.verifyArtifact = func(_ context.Context, _ string, _ string) error {
		if verifierCalls.Add(1) < 3 {
			return nil
		}
		close(verifierStarted)
		<-releaseVerifier
		return nil
	}
	dependencies.cleanupBuild = func(context.Context, *buildCleanupReceipt) (string, error) {
		cleanupCalled.Store(true)
		return "unexpected", nil
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err != nil {
		t.Fatal(err)
	}
	plan, err := app.GetBuildCleanupPlan(jobID)
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	desktopArtifact := app.desktopArtifact
	app.mu.Unlock()
	cleanupResult := make(chan error, 1)
	go func() {
		_, cleanupErr := app.CleanupBuild(jobID, plan.PlanID)
		cleanupResult <- cleanupErr
	}()
	<-verifierStarted
	tamperArtifactPreservingMetadata(t, desktopArtifact, []byte("tampered SFX fixture"))
	close(releaseVerifier)
	if err := <-cleanupResult; err == nil || !strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("Desktop tamper cleanup error = %v", err)
	}
	if cleanupCalled.Load() {
		t.Fatal("cleanup engine ran after the Desktop copy changed")
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("source root was removed after Desktop tamper: %v", err)
	}
	if _, err := os.Stat(request.CacheDir); err != nil {
		t.Fatalf("cache root was removed after Desktop tamper: %v", err)
	}
	if _, err := app.GetBuildCleanupPlan(jobID); err == nil || !strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("tampered Desktop copy became reviewable again: %v", err)
	}
}

func TestShutdownCancelsAndWaitsForBuildCleanup(t *testing.T) {
	app, request, dependencies, recorder, _ := cleanupAppFixture(t)
	cleanupStarted := make(chan struct{})
	cleanupStopped := make(chan struct{})
	dependencies.verifyArtifact = func(context.Context, string, string) error { return nil }
	dependencies.cleanupBuild = func(ctx context.Context, _ *buildCleanupReceipt) (string, error) {
		close(cleanupStarted)
		<-ctx.Done()
		close(cleanupStopped)
		return "", ctx.Err()
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err != nil {
		t.Fatal(err)
	}
	plan, err := app.GetBuildCleanupPlan(jobID)
	if err != nil {
		t.Fatal(err)
	}
	cleanupResult := make(chan error, 1)
	go func() {
		_, cleanupErr := app.CleanupBuild(jobID, plan.PlanID)
		cleanupResult <- cleanupErr
	}()
	<-cleanupStarted
	if _, err := app.StartBuild(request); err == nil || !strings.Contains(err.Error(), "cleaned up") {
		t.Fatalf("StartBuild during cleanup error = %v", err)
	}
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil || !strings.Contains(err.Error(), "cleaned up") {
		t.Fatalf("Copy during cleanup error = %v", err)
	}
	if app.beforeClose(context.Background()) {
		t.Fatal("beforeClose prevented shutdown")
	}
	select {
	case <-cleanupStopped:
	default:
		t.Fatal("shutdown returned before cleanup stopped")
	}
	select {
	case err := <-cleanupResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cleanup error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not return after shutdown")
	}
}

func TestShutdownDiscardsCleanupMarkersWithoutDeletingBuildFiles(t *testing.T) {
	app, request, dependencies, recorder, _ := cleanupAppFixture(t)
	app.dependencies = *dependencies
	if _, err := app.StartBuild(request); err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	app.mu.Lock()
	receipt := app.cleanupReceipt
	app.mu.Unlock()
	if receipt == nil || len(receipt.candidates) == 0 {
		t.Fatal("successful build did not record cleanup markers")
	}
	markerPaths := make([]string, 0, len(receipt.candidates))
	for _, candidate := range receipt.candidates {
		markerPaths = append(markerPaths, candidate.markerPath)
	}
	if app.beforeClose(context.Background()) {
		t.Fatal("beforeClose prevented shutdown")
	}
	for _, marker := range markerPaths {
		cleanupAssertNotExist(t, marker)
	}
	if _, err := os.Stat(request.RepoRoot); err != nil {
		t.Fatalf("shutdown removed source files: %v", err)
	}
	if _, err := os.Stat(request.CacheDir); err != nil {
		t.Fatalf("shutdown removed cache files: %v", err)
	}
}

func cleanupAppFixture(t *testing.T) (*App, BuildRequest, *appDependencies, *eventRecorder, string) {
	t.Helper()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	base := t.TempDir()
	request.Target = fileArtifactTestTarget()
	request.RepoRoot = filepath.Join(base, "source")
	request.CacheDir = filepath.Join(base, "cache")
	request.SteamCMDDir = filepath.Join(request.CacheDir, "steamcmd")
	request.AssetsDir = filepath.Join(base, "assets")
	request.AppOutput = ""
	request.Output = ""
	request.Output, _ = effectiveArtifactPath(request, dependencies.hostOS)
	request.SkipAssets = true
	assetSentinel := filepath.Join(request.AssetsDir, "owned.big")
	cleanupWriteFile(t, assetSentinel, []byte("owned retail fixture"), 0o600)
	desktop := filepath.Join(base, "Desktop")
	if err := os.MkdirAll(desktop, 0o700); err != nil {
		t.Fatal(err)
	}
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		cleanupCreateSimulatedRepo(t, request.RepoRoot)
		cleanupWriteFile(t, filepath.Join(request.CacheDir, "downloads", "dependency"), []byte("cache"), 0o600)
		cleanupWriteFile(t, request.Output, []byte("verified SFX fixture"), 0o700)
		return 0
	}
	return app, request, dependencies, recorder, assetSentinel
}
