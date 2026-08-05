package main

import (
	"context"
	"fmt"
	"io"
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
	dependencies.shutdownTimeout = time.Second
	app := newApp(dependencies)
	app.startup(context.Background())
	defaults, err := app.GetDefaults()
	if err != nil {
		t.Fatal(err)
	}
	request := defaults.Request
	request.SkipAssets = true
	return app, request, &dependencies
}

func TestStartBuildStreamsStructuredProgressAndLogs(t *testing.T) {
	t.Parallel()
	recorder := newEventRecorder()
	app, request, dependencies := testApp(t, recorder)
	var capturedArguments []string
	dependencies.builder = func(_ context.Context, arguments []string, _ io.Reader, stdout, stderr io.Writer, options buildcli.RunOptions) int {
		capturedArguments = append([]string(nil), arguments...)
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseSource, Message: "Preparing source"})
		fmt.Fprintln(stdout, "source output")
		fmt.Fprintln(stderr, "source warning")
		options.Reporter.Report(buildcli.ProgressEvent{Phase: buildcli.ProgressPhaseComplete, Message: "Complete"})
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
	streams := map[string]bool{}
	for _, event := range recorder.snapshot() {
		if logEvent, ok := event.payload.(LogEvent); ok {
			streams[logEvent.Stream] = true
		}
	}
	if !streams["stdout"] || !streams["stderr"] {
		t.Fatalf("log streams = %#v", streams)
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

	if _, err := app.StartBuild(request); err != nil {
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
