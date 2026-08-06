package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type recordedEvent struct {
	name    string
	payload interface{}
	ctxErr  error
}

type eventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
	wake   chan struct{}
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{wake: make(chan struct{}, 32)}
}

func (recorder *eventRecorder) emit(ctx context.Context, name string, payload interface{}) {
	recorder.mu.Lock()
	recorder.events = append(recorder.events, recordedEvent{name: name, payload: payload, ctxErr: ctx.Err()})
	recorder.mu.Unlock()
	select {
	case recorder.wake <- struct{}{}:
	default:
	}
}

func (recorder *eventRecorder) waitForProgress(t *testing.T, status string) ProgressEvent {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		recorder.mu.Lock()
		for _, event := range recorder.events {
			if progress, ok := event.payload.(ProgressEvent); ok && event.name == progressEventName && progress.Status == status {
				recorder.mu.Unlock()
				return progress
			}
		}
		recorder.mu.Unlock()
		select {
		case <-recorder.wake:
		case <-deadline.C:
			t.Fatalf("timed out waiting for progress status %q", status)
		}
	}
}

func (recorder *eventRecorder) snapshot() []recordedEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]recordedEvent(nil), recorder.events...)
}

func testApp(t *testing.T, recorder *eventRecorder) (*App, BuildRequest, *appDependencies) {
	t.Helper()
	dependencies := defaultAppDependencies()
	dependencies.emit = recorder.emit
	dependencies.stdin = strings.NewReader("")
	dependencies.stdout = io.Discard
	dependencies.stderr = io.Discard
	dependencies.newJobID = func() string { return "job-fixture" }
	dependencies.verifyArtifact = func(context.Context, string, string) error { return nil }
	dependencies.shutdownTimeout = time.Second
	cleanupState := t.TempDir()
	dependencies.cleanupLedgerPath = func() (string, error) {
		return filepath.Join(cleanupState, "private", "cleanup-ownership-v1.json"), nil
	}
	app := newApp(dependencies)
	app.startup(context.Background())
	defaults, err := app.GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	request := defaults.Request
	workspace := t.TempDir()
	request.Target = fileArtifactTestTarget()
	request.RepoRoot = filepath.Join(workspace, "source")
	request.CacheDir = filepath.Join(workspace, "cache")
	request.SteamCMDDir = filepath.Join(request.CacheDir, "steamcmd")
	request.AssetsDir = filepath.Join(workspace, "assets")
	outputDirectory := filepath.Join(workspace, "output")
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	request.AppOutput = ""
	request.SkipAssets = true
	outputName := "GeneralsXZH-sfx"
	if request.Target == "windows" {
		outputName += ".exe"
	}
	request.Output = filepath.Join(outputDirectory, outputName)
	return app, request, &dependencies
}

func fileArtifactTestTarget() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	return "linux"
}

func TestStartBuildStreamsStructuredProgressAndLogs(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	var capturedArguments []string
	var capturedOptions buildcli.RunOptions
	dependencies.builder = func(_ context.Context, arguments []string, _ io.Reader, stdout, stderr io.Writer, options buildcli.RunOptions) int {
		capturedArguments = append([]string(nil), arguments...)
		capturedOptions = options
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseSource, Message: "Preparing source"})
		fmt.Fprintln(stdout, "source output")
		fmt.Fprintln(stderr, "source warning")
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseComplete, Message: "Complete"})
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "job-fixture" {
		t.Fatalf("job ID = %q", jobID)
	}
	final := recorder.waitForProgress(t, "success")
	if final.Phase != "complete" || final.Percent != 100 || final.ExitCode != 0 {
		t.Fatalf("final progress = %#v", final)
	}
	if !slices.Equal(capturedArguments, buildRequestArguments(request)) {
		t.Fatalf("arguments = %q", capturedArguments)
	}
	if !capturedOptions.HideBackgroundWindows {
		t.Fatal("desktop builder did not request hidden background Windows commands")
	}
	streams := map[string]bool{}
	sawVerification := false
	for _, event := range recorder.snapshot() {
		if logEvent, ok := event.payload.(LogEvent); ok {
			streams[logEvent.Stream] = true
		}
		if progressEvent, ok := event.payload.(ProgressEvent); ok && progressEvent.Status == "running" {
			if progressEvent.Phase == "complete" || progressEvent.Percent == 100 {
				t.Fatalf("builder exposed terminal progress before verification: %#v", progressEvent)
			}
			if progressEvent.Phase == "verify" && progressEvent.Percent == 95 {
				sawVerification = true
			}
		}
	}
	if !streams["stdout"] || !streams["stderr"] {
		t.Fatalf("log streams = %#v", streams)
	}
	if !sawVerification {
		t.Fatal("build did not expose the post-package verification phase")
	}
}

// GeneralsX @test Codex 05/08/2026 Recover exact completion even when streamed terminal delivery is blocked or missed.
func TestWaitForBuildReturnsDurableTerminalResultBeforeAndAfterCompletion(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	started := make(chan struct{})
	releaseBuilder := make(chan struct{})
	terminalEmissionStarted := make(chan struct{})
	releaseTerminalEmission := make(chan struct{})
	terminalEmissionFinished := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseTerminalEmission:
		default:
			close(releaseTerminalEmission)
		}
	})
	dependencies.emit = func(ctx context.Context, name string, payload interface{}) {
		if progress, ok := payload.(ProgressEvent); ok && isTerminalStatus(progress.Status) {
			close(terminalEmissionStarted)
			<-releaseTerminalEmission
			close(terminalEmissionFinished)
			return
		}
		recorder.emit(ctx, name, payload)
	}
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, options buildcli.RunOptions) int {
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseComplete, Message: "Complete"})
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		close(started)
		<-releaseBuilder
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	type waitResult struct {
		progress ProgressEvent
		err      error
	}
	waited := make(chan waitResult, 1)
	go func() {
		progress, waitErr := app.WaitForBuild(jobID)
		waited <- waitResult{progress: progress, err: waitErr}
	}()
	select {
	case result := <-waited:
		t.Fatalf("WaitForBuild returned before completion: %#v, %v", result.progress, result.err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseBuilder)
	select {
	case <-terminalEmissionStarted:
	case <-time.After(time.Second):
		t.Fatal("terminal event emission did not start")
	}
	var result waitResult
	select {
	case result = <-waited:
	case <-time.After(time.Second):
		t.Fatal("WaitForBuild remained blocked behind terminal event emission")
	}
	if result.err != nil || result.progress.Status != "success" || result.progress.Phase != "complete" || result.progress.Percent != 100 {
		t.Fatalf("WaitForBuild() = %#v, %v", result.progress, result.err)
	}
	replayed, err := app.WaitForBuild(jobID)
	if err != nil || replayed != result.progress {
		t.Fatalf("replayed WaitForBuild() = %#v, %v, want %#v", replayed, err, result.progress)
	}
	for _, event := range recorder.snapshot() {
		if progress, ok := event.payload.(ProgressEvent); ok && isTerminalStatus(progress.Status) {
			t.Fatalf("terminal event was not dropped by the fixture: %#v", progress)
		}
	}
	if _, err := app.WaitForBuild("unknown-job"); err == nil {
		t.Fatal("WaitForBuild accepted an unknown job ID")
	}
	close(releaseTerminalEmission)
	select {
	case <-terminalEmissionFinished:
	case <-time.After(time.Second):
		t.Fatal("terminal event emission did not finish")
	}
}

func TestStartBuildAllowsOnlyOneJobAndCancellationUsesStableEventContext(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	started := make(chan struct{})
	dependencies.builder = func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer, options buildcli.RunOptions) int {
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseBuild, Message: "Building"})
		close(started)
		<-ctx.Done()
		return 1
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := app.StartBuild(request); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second StartBuild() error = %v", err)
	}
	if !app.CancelBuild() {
		t.Fatal("CancelBuild() = false, want true")
	}
	if app.CancelBuild() {
		t.Fatal("second CancelBuild() = true, want false")
	}
	final := recorder.waitForProgress(t, "cancelled")
	if final.ExitCode != 1 {
		t.Fatalf("cancelled progress = %#v", final)
	}
	durable, err := app.WaitForBuild(jobID)
	if err != nil || durable != final {
		t.Fatalf("durable cancelled progress = %#v, %v, want %#v", durable, err, final)
	}
	for _, event := range recorder.snapshot() {
		if event.name == progressEventName && event.ctxErr != nil {
			t.Fatalf("progress event used cancelled context: %v", event.ctxErr)
		}
	}
}

func TestCancelBuildCancelsBeforeEmissionAndNeverEmitsRunningAfterTerminal(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	started := make(chan struct{})
	releaseBuilder := make(chan struct{})
	cancelEventEntered := make(chan struct{})
	releaseCancelEvent := make(chan struct{})
	var buildContext context.Context
	dependencies.builder = func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		buildContext = ctx
		close(started)
		<-releaseBuilder
		return 0
	}
	dependencies.emit = func(ctx context.Context, name string, payload interface{}) {
		if progress, ok := payload.(ProgressEvent); ok && progress.Message == "Cancellation requested" {
			select {
			case <-buildContext.Done():
			default:
				t.Error("cancellation event emitted before the build context was cancelled")
			}
			close(cancelEventEntered)
			<-releaseCancelEvent
		}
		recorder.emit(ctx, name, payload)
	}
	app.dependencies = *dependencies

	if _, err := app.StartBuild(request); err != nil {
		t.Fatal(err)
	}
	<-started
	cancelResult := make(chan bool, 1)
	go func() { cancelResult <- app.CancelBuild() }()
	<-cancelEventEntered
	close(releaseBuilder)

	// The cancellation callback owns the serialized progress stream, so the
	// completed builder cannot publish a terminal event ahead of it.
	for _, event := range recorder.snapshot() {
		if progress, ok := event.payload.(ProgressEvent); ok && isTerminalStatus(progress.Status) {
			t.Fatalf("terminal progress overtook cancellation request: %#v", progress)
		}
	}
	close(releaseCancelEvent)
	if !<-cancelResult {
		t.Fatal("CancelBuild() = false, want true")
	}
	final := recorder.waitForProgress(t, "cancelled")
	if final.ExitCode != 0 {
		t.Fatalf("cancelled progress = %#v", final)
	}

	terminalSeen := false
	for _, event := range recorder.snapshot() {
		progress, ok := event.payload.(ProgressEvent)
		if !ok {
			continue
		}
		if terminalSeen && progress.Status == "running" {
			t.Fatalf("running progress followed terminal progress: %#v", progress)
		}
		terminalSeen = terminalSeen || isTerminalStatus(progress.Status)
	}
}

func isTerminalStatus(status string) bool {
	return status == "success" || status == "error" || status == "cancelled"
}

func TestProductionDesktopDependenciesUseNonFailingDetachedStreams(t *testing.T) {
	t.Parallel()
	dependencies := defaultAppDependencies()
	if dependencies.stdout != io.Discard || dependencies.stderr != io.Discard {
		t.Fatalf("desktop host streams = (%T, %T), want io.Discard", dependencies.stdout, dependencies.stderr)
	}
	if _, err := dependencies.stdout.Write([]byte("discarded")); err != nil {
		t.Fatalf("write detached stdout sink: %v", err)
	}
	buffer := make([]byte, 1)
	if count, err := dependencies.stdin.Read(buffer); count != 0 || err != io.EOF {
		t.Fatalf("detached stdin Read() = %d, %v, want 0, EOF", count, err)
	}
}

func TestStartBuildHandsSteamCommandToNativeTerminalRunner(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	request.SkipAssets = false
	var gotCommand buildcli.InteractiveCommand
	dependencies.interactiveRunner = buildcli.InteractiveCommandRunnerFunc(func(_ context.Context, command buildcli.InteractiveCommand) error {
		gotCommand = command
		return nil
	})
	dependencies.builder = func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer, options buildcli.RunOptions) int {
		err := options.InteractiveRunner.RunInteractive(ctx, buildcli.InteractiveCommand{
			Purpose:    buildcli.InteractiveSteamAuthentication,
			Executable: "/private/steamcmd",
			Arguments:  []string{"+login", "commander"},
		})
		if err != nil {
			return 1
		}
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	if _, err := app.StartBuild(request); err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if gotCommand.Executable != "/private/steamcmd" || !slices.Equal(gotCommand.Arguments, []string{"+login", "commander"}) {
		t.Fatalf("terminal command = %#v", gotCommand)
	}
	foundHandoff := slices.ContainsFunc(recorder.snapshot(), func(event recordedEvent) bool {
		progress, ok := event.payload.(ProgressEvent)
		return ok && strings.Contains(progress.Message, "opened in a terminal")
	})
	if !foundHandoff {
		t.Fatal("terminal handoff progress event missing")
	}
}

func TestChooseDirectoryUsesFieldKindsAndOutputDirectorySemantics(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, _, dependencies := testApp(t, recorder)
	dependencies.chooseDirectory = func(_ context.Context, options wailsruntime.OpenDialogOptions) (string, error) {
		if options.DefaultDirectory != "/tmp/output" {
			t.Fatalf("default directory = %q", options.DefaultDirectory)
		}
		if !strings.Contains(options.Title, "SFX output") {
			t.Fatalf("title = %q", options.Title)
		}
		return "/chosen", nil
	}
	app.dependencies = *dependencies
	selected, err := app.ChooseDirectory("output", "/tmp/output/game-sfx")
	if err != nil || selected != "/chosen" {
		t.Fatalf("ChooseDirectory() = %q, %v", selected, err)
	}
	if _, err := app.ChooseDirectory("unknown", ""); err == nil {
		t.Fatal("unknown directory kind was accepted")
	}
}

func TestCopyBuildArtifactToDesktopCopiesOnlyTheMatchingSuccessfulBuild(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	sourceDirectory := t.TempDir()
	request.Output = filepath.Join(sourceDirectory, "GeneralsXZH-sfx")
	payload := []byte("verified SFX fixture")
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, payload, 0o751); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	if _, err := app.CopyBuildArtifactToDesktop("job-fixture"); err == nil {
		t.Fatal("copy before a successful build was accepted")
	}
	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if _, err := app.CopyBuildArtifactToDesktop("another-job"); err == nil {
		t.Fatal("copy with the wrong job ID was accepted")
	}
	existing := filepath.Join(desktop, filepath.Base(request.Output))
	if err := os.WriteFile(existing, []byte("keep existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination, err := app.CopyBuildArtifactToDesktop(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(desktop, "GeneralsXZH-sfx (1)"); destination != want {
		t.Fatalf("destination = %q, want %q", destination, want)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(payload) {
		t.Fatalf("copied contents = %q", contents)
	}
	kept, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "keep existing" {
		t.Fatalf("existing Desktop artifact was overwritten: %q", kept)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(destination)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o751); got != want {
			t.Fatalf("destination permissions = %o, want %o", got, want)
		}
	}
}

func TestCopyBuildArtifactToDesktopStreamsScopedProgressBeforeAuthoritativeResult(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	payload := []byte("verified SFX copy progress fixture")
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, payload, 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if _, err := app.CopyBuildArtifactToDesktopWithProgress(jobID, ""); err == nil {
		t.Fatal("empty Desktop copy operation ID was accepted")
	}
	destination, err := app.CopyBuildArtifactToDesktopWithProgress(jobID, "copy-operation-1")
	if err != nil {
		t.Fatal(err)
	}

	var copyEvents []CopyProgressEvent
	for _, event := range recorder.snapshot() {
		if event.name != copyProgressEventName {
			continue
		}
		progress, ok := event.payload.(CopyProgressEvent)
		if !ok {
			t.Fatalf("copy progress payload = %T", event.payload)
		}
		if progress.JobID != jobID || progress.OperationID != "copy-operation-1" {
			t.Fatalf("copy progress scope = %#v", progress)
		}
		copyEvents = append(copyEvents, progress)
	}
	if len(copyEvents) < 5 {
		t.Fatalf("copy progress events = %#v", copyEvents)
	}
	previousBytes := int64(0)
	terminalCount := 0
	for _, event := range copyEvents {
		if event.BytesCopied < previousBytes {
			t.Fatalf("copy event bytes regressed from %d to %d", previousBytes, event.BytesCopied)
		}
		previousBytes = event.BytesCopied
		if event.Status != "running" {
			terminalCount++
		}
	}
	last := copyEvents[len(copyEvents)-1]
	if terminalCount != 1 || last.Status != "success" || last.Phase != "complete" ||
		last.Percent != 100 || last.BytesCopied != int64(len(payload)) || last.TotalBytes != int64(len(payload)) ||
		last.Message != destination {
		t.Fatalf("terminal copy progress = %#v (terminal count %d)", last, terminalCount)
	}
}

func TestMacOSBuildPublishesVerifiedApplicationBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS application builds require a Darwin host")
	}
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	workspace := t.TempDir()
	desktop := filepath.Join(workspace, "Desktop")
	if err := os.Mkdir(desktop, 0o700); err != nil {
		t.Fatal(err)
	}
	request.Target = "macos"
	request.RepoRoot = filepath.Join(workspace, "source")
	request.CacheDir = filepath.Join(workspace, "cache")
	request.SteamCMDDir = filepath.Join(request.CacheDir, "steamcmd")
	request.AssetsDir = filepath.Join(workspace, "owned", "assets")
	request.Output = filepath.Join(request.RepoRoot, "build", "sfx", "GeneralsXZH-macos-arm64-sfx")
	request.AppOutput = filepath.Join(request.RepoRoot, "build", "sfx", "GeneralsXZH.app")
	cleanupCreateSimulatedRepo(t, request.RepoRoot)
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.MkdirAll(filepath.Dir(request.Output), 0o755); err != nil {
			t.Errorf("create SFX output directory: %v", err)
			return 1
		}
		if err := os.WriteFile(request.Output, []byte("secondary raw SFX"), 0o751); err != nil {
			t.Errorf("write raw SFX: %v", err)
			return 1
		}
		if err := createArtifactBundleFixture(request.AppOutput); err != nil {
			t.Errorf("write application bundle: %v", err)
			return 1
		}
		return 0
	}
	var verifiedPaths []string
	dependencies.verifyArtifact = func(_ context.Context, path, target string) error {
		if target != "macos" {
			t.Errorf("verified target = %q, want macos", target)
		}
		verifiedPaths = append(verifiedPaths, path)
		return nil
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	app.mu.Lock()
	recordedPath := app.completedArtifact.sourcePath
	app.mu.Unlock()
	if recordedPath != request.AppOutput {
		t.Fatalf("recorded artifact = %q, want %q", recordedPath, request.AppOutput)
	}
	destination, err := app.CopyBuildArtifactToDesktop(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(desktop, "GeneralsXZH.app"); destination != want {
		t.Fatalf("Desktop artifact = %q, want %q", destination, want)
	}
	if len(verifiedPaths) != 2 || verifiedPaths[0] != request.AppOutput {
		t.Fatalf("verified paths = %q", verifiedPaths)
	}
	if filepath.Dir(verifiedPaths[1]) != desktop ||
		!strings.HasPrefix(filepath.Base(verifiedPaths[1]), ".generalsx-copy-") ||
		filepath.Ext(verifiedPaths[1]) != ".app" {
		t.Fatalf("Desktop verifier did not receive the private app sibling: %q", verifiedPaths[1])
	}
	if _, err := os.Stat(filepath.Join(destination, "Contents", "Resources", "GeneralsXZH.icns")); err != nil {
		t.Fatalf("Desktop application icon missing: %v", err)
	}
	plan, err := app.GetBuildCleanupPlan(jobID)
	if err != nil {
		t.Fatal(err)
	}
	canonicalOutput, err := cleanupCanonicalFuturePath(request.Output)
	if err != nil {
		t.Fatal(err)
	}
	canonicalApp, err := cleanupCanonicalFuturePath(request.AppOutput)
	if err != nil {
		t.Fatal(err)
	}
	wantCleanup := map[string]bool{canonicalOutput: false, canonicalApp: false}
	for _, entry := range plan.Entries {
		if _, tracked := wantCleanup[entry.Path]; tracked {
			wantCleanup[entry.Path] = true
		}
	}
	for path, found := range wantCleanup {
		if !found {
			t.Fatalf("cleanup plan omitted generated artifact %q: %#v", path, plan.Entries)
		}
	}
	if _, err := app.CleanupBuild(jobID, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	for path := range wantCleanup {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated artifact remains after cleanup at %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "Contents", "Resources", "GeneralsXZH.icns")); err != nil {
		t.Fatalf("cleanup removed the Desktop application: %v", err)
	}
}

func TestCopyBuildArtifactToDesktopRejectsDryRunAndFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		dryRun   bool
		exitCode int
		status   string
	}{
		{name: "dry run", dryRun: true, exitCode: 0, status: "success"},
		{name: "failed build", exitCode: 1, status: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newEventRecorder()
			app, request, dependencies := testApp(t, recorder)
			request.DryRun = test.dryRun
			request.Output = filepath.Join(t.TempDir(), "GeneralsXZH-sfx")
			dependencies.desktopDirectory = func() (string, error) { return t.TempDir(), nil }
			dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
				return test.exitCode
			}
			app.dependencies = *dependencies

			jobID, err := app.StartBuild(request)
			if err != nil {
				t.Fatal(err)
			}
			recorder.waitForProgress(t, test.status)
			if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil {
				t.Fatalf("copy after %s was accepted", test.name)
			}
		})
	}
}

func TestSuccessfulBuilderWithoutArtifactReportsFailure(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	progress := recorder.waitForProgress(t, "error")
	if !strings.Contains(progress.Message, "could not be verified") {
		t.Fatalf("terminal progress = %#v", progress)
	}
	durable, err := app.WaitForBuild(jobID)
	if err != nil || durable != progress {
		t.Fatalf("durable error progress = %#v, %v, want %#v", durable, err, progress)
	}
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil {
		t.Fatal("missing artifact became eligible for Desktop copying")
	}
}

// GeneralsX @test Codex 05/08/2026 Do not trust an artifact verifier that mutates bytes while preserving visible metadata.
func TestSuccessfulBuildRejectsArtifactChangedByVerifier(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, []byte("original SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	dependencies.verifyArtifact = func(_ context.Context, path, _ string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("tampered SFX"), info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(path, info.ModTime(), info.ModTime())
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	progress := recorder.waitForProgress(t, "error")
	if !strings.Contains(progress.Message, "changed during verification") {
		t.Fatalf("terminal progress = %#v", progress)
	}
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil {
		t.Fatal("artifact changed by verifier became eligible for Desktop copying")
	}
}

func TestCopyBuildArtifactToDesktopRejectsChangedArtifact(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, []byte("original SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	if err := os.Remove(request.Output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Output, []byte("replacement SFX"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed artifact copy error = %v", err)
	}
}

// GeneralsX @test Codex 05/08/2026 A failed verifier must never publish or duplicate a Desktop artifact.
func TestCopyBuildArtifactVerifierFailureLeavesDesktopUnchanged(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	var verifierMu sync.Mutex
	verificationCount := 0
	var rejectedPaths []string
	dependencies.verifyArtifact = func(_ context.Context, path, _ string) error {
		verifierMu.Lock()
		defer verifierMu.Unlock()
		verificationCount++
		if verificationCount == 1 {
			return nil
		}
		rejectedPaths = append(rejectedPaths, path)
		return errors.New("fixture Desktop verification failure")
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil || !strings.Contains(err.Error(), "fixture Desktop verification failure") {
			t.Fatalf("copy attempt %d error = %v", attempt+1, err)
		}
		entries, err := os.ReadDir(desktop)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("Desktop entries after rejected copy %d = %v, want none", attempt+1, entries)
		}
	}
	verifierMu.Lock()
	defer verifierMu.Unlock()
	if len(rejectedPaths) != 2 {
		t.Fatalf("rejected verifier paths = %q, want two", rejectedPaths)
	}
	for _, path := range rejectedPaths {
		if !strings.HasPrefix(filepath.Base(path), ".generalsx-copy-") {
			t.Fatalf("verifier received published path %q instead of a private sibling", path)
		}
	}
}

func TestStartBuildRejectsWhileDesktopCopyIsInProgress(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	resolverEntered := make(chan struct{})
	releaseResolver := make(chan struct{})
	dependencies.desktopDirectory = func() (string, error) {
		close(resolverEntered)
		<-releaseResolver
		return desktop, nil
	}
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	copyResult := make(chan error, 1)
	go func() {
		_, copyErr := app.CopyBuildArtifactToDesktop(jobID)
		copyResult <- copyErr
	}()
	<-resolverEntered
	if _, err := app.StartBuild(request); err == nil || !strings.Contains(err.Error(), "copied to Desktop") {
		t.Fatalf("StartBuild() during copy error = %v", err)
	}
	close(releaseResolver)
	if err := <-copyResult; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCancelsAndWaitsForActiveBuild(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	started := make(chan struct{})
	stopped := make(chan struct{})
	dependencies.builder = func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		close(started)
		<-ctx.Done()
		close(stopped)
		return 1
	}
	app.dependencies = *dependencies
	if _, err := app.StartBuild(request); err != nil {
		t.Fatal(err)
	}
	<-started
	if preventClose := app.beforeClose(context.Background()); preventClose {
		t.Fatal("beforeClose() prevented close")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the builder")
	}
	if _, err := app.StartBuild(request); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("StartBuild() after shutdown error = %v", err)
	}
}

func TestShutdownCancelsAndWaitsForDesktopCopy(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	desktop := t.TempDir()
	copyStarted := make(chan struct{})
	copyStopped := make(chan struct{})
	dependencies.desktopDirectory = func() (string, error) { return desktop, nil }
	dependencies.builder = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer, _ buildcli.RunOptions) int {
		if err := os.WriteFile(request.Output, []byte("verified SFX"), 0o700); err != nil {
			t.Errorf("write source artifact: %v", err)
			return 1
		}
		return 0
	}
	dependencies.copyArtifact = func(ctx context.Context, _ *completedArtifact, _ string, _ verifyArtifactFunc) (string, error) {
		close(copyStarted)
		<-ctx.Done()
		close(copyStopped)
		return "", ctx.Err()
	}
	app.dependencies = *dependencies

	jobID, err := app.StartBuild(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder.waitForProgress(t, "success")
	copyResult := make(chan error, 1)
	go func() {
		_, copyErr := app.CopyBuildArtifactToDesktopWithProgress(jobID, "copy-shutdown")
		copyResult <- copyErr
	}()
	<-copyStarted
	if preventClose := app.beforeClose(context.Background()); preventClose {
		t.Fatal("beforeClose() prevented close")
	}
	select {
	case <-copyStopped:
	default:
		t.Fatal("shutdown returned before the Desktop copy stopped")
	}
	select {
	case copyErr := <-copyResult:
		if !errors.Is(copyErr, context.Canceled) {
			t.Fatalf("Desktop copy error = %v, want context cancellation", copyErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Desktop copy did not return after shutdown")
	}
	var terminal *CopyProgressEvent
	for _, event := range recorder.snapshot() {
		progress, ok := event.payload.(CopyProgressEvent)
		if ok && event.name == copyProgressEventName && progress.OperationID == "copy-shutdown" && progress.Status != "running" {
			copy := progress
			terminal = &copy
		}
	}
	if terminal == nil || terminal.Status != "cancelled" {
		t.Fatalf("cancelled Desktop copy terminal event = %#v", terminal)
	}
	if _, err := app.CopyBuildArtifactToDesktop(jobID); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("CopyBuildArtifactToDesktop() after shutdown error = %v", err)
	}
}
