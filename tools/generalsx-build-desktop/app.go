package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	progressEventName = "builder:progress"
	logEventName      = "builder:log"
)

type ProgressEvent struct {
	JobID    string `json:"jobId"`
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Percent  int    `json:"percent"`
	ExitCode int    `json:"exitCode"`
}

type LogEvent struct {
	JobID  string `json:"jobId"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type buildMainFunc func(context.Context, []string, io.Reader, io.Writer, io.Writer, buildcli.RunOptions) int
type emitEventFunc func(context.Context, string, interface{})
type chooseDirectoryFunc func(context.Context, wailsruntime.OpenDialogOptions) (string, error)
type copyArtifactFunc func(context.Context, *completedArtifact, string) (string, error)
type verifyArtifactFunc func(context.Context, string, string) error
type cleanupBuildFunc func(context.Context, *buildCleanupReceipt) (string, error)

type appDependencies struct {
	builder           buildMainFunc
	emit              emitEventFunc
	chooseDirectory   chooseDirectoryFunc
	desktopDirectory  func() (string, error)
	copyArtifact      copyArtifactFunc
	verifyArtifact    verifyArtifactFunc
	cleanupBuild      cleanupBuildFunc
	interactiveRunner buildcli.InteractiveCommandRunner
	newJobID          func() string
	loadDefaults      func() (buildcli.ConfigurationDefaults, error)
	stdin             io.Reader
	stdout            io.Writer
	stderr            io.Writer
	hostOS            string
	hostArch          string
	shutdownTimeout   time.Duration
}

type activeBuild struct {
	id              string
	phase           string
	artifactPath    string
	dryRun          bool
	cleanupSnapshot *buildCleanupSnapshot
	cancel          context.CancelFunc
	done            chan struct{}
	cancelRequested bool
	finished        bool
	eventMu         sync.Mutex
	terminalEmitted bool
}

// App is the bound Wails backend for one automated build at a time.
type App struct {
	mu                sync.Mutex
	ctx               context.Context
	active            *activeBuild
	completedArtifact *completedArtifact
	desktopArtifact   *completedArtifact
	cleanupReceipt    *buildCleanupReceipt
	preparedCleanup   *preparedCleanupPlan
	cleanupPlanning   bool
	copyInProgress    bool
	copyCancel        context.CancelFunc
	copyDone          chan struct{}
	cleanupInProgress bool
	cleanupCancel     context.CancelFunc
	cleanupDone       chan struct{}
	shuttingDown      bool
	dependencies      appDependencies
}

// NewApp constructs the production Wails backend.
func NewApp() *App {
	return newApp(defaultAppDependencies())
}

func newApp(dependencies appDependencies) *App {
	return &App{dependencies: dependencies}
}

func defaultAppDependencies() appDependencies {
	return appDependencies{
		builder: buildcli.MainWithOptions,
		emit: func(ctx context.Context, event string, payload interface{}) {
			wailsruntime.EventsEmit(ctx, event, payload)
		},
		chooseDirectory: func(ctx context.Context, options wailsruntime.OpenDialogOptions) (string, error) {
			return wailsruntime.OpenDirectoryDialog(ctx, options)
		},
		desktopDirectory: systemDesktopDirectory,
		copyArtifact:     copyCompletedArtifactToDirectory,
		verifyArtifact: func(ctx context.Context, path, target string) error {
			return verifySFXArtifact(ctx, path, target, runtime.GOOS)
		},
		cleanupBuild:      executeBuildCleanup,
		interactiveRunner: newTerminalInteractiveRunner(),
		newJobID:          generateJobID,
		loadDefaults:      buildcli.LoadConfigurationDefaults,
		stdin:             strings.NewReader(""),
		stdout:            io.Discard,
		stderr:            io.Discard,
		hostOS:            runtime.GOOS,
		hostArch:          runtime.GOARCH,
		shutdownTimeout:   5 * time.Second,
	}
}

var fallbackJobSequence atomic.Uint64

func generateJobID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("build-%d-%d", time.Now().UnixMilli(), fallbackJobSequence.Add(1))
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.shuttingDown = false
	a.mu.Unlock()
}

// ChooseDirectory opens only known directory selectors. For output and appOutput it returns
// the selected directory; the frontend preserves or appends the requested output filename.
func (a *App) ChooseDirectory(kind, current string) (string, error) {
	titles := map[string]string{
		"repoRoot":           "Choose the GeneralsX source directory",
		"assetsDir":          "Choose the owned Zero Hour retail-data directory",
		"cacheDir":           "Choose the private builder cache directory",
		"steamCMDDir":        "Choose the SteamCMD directory",
		"onlineServerSource": "Choose the Online server source directory",
		"output":             "Choose the SFX output directory",
		"appOutput":          "Choose the macOS app output directory",
	}
	title, ok := titles[kind]
	if !ok {
		return "", fmt.Errorf("unsupported directory kind %q", kind)
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return "", errors.New("desktop runtime is not ready")
	}
	defaultDirectory := current
	if (kind == "output" || kind == "appOutput") && current != "" {
		defaultDirectory = filepath.Dir(current)
	}
	return a.dependencies.chooseDirectory(ctx, wailsruntime.OpenDialogOptions{
		DefaultDirectory:     defaultDirectory,
		Title:                title,
		CanCreateDirectories: true,
	})
}

// GeneralsX @build Codex 05/08/2026 Run the existing builder with cancellable context and structured desktop events.
func (a *App) StartBuild(request BuildRequest) (string, error) {
	if err := validationError(a.ValidateBuild(request)); err != nil {
		return "", err
	}
	artifactPath := ""
	if !request.DryRun {
		var err error
		artifactPath, err = effectiveArtifactPath(request, a.dependencies.hostOS)
		if err != nil {
			return "", fmt.Errorf("resolve SFX output path: %w", err)
		}
	}
	cleanupSnapshot := snapshotBuildCleanup(request, a.dependencies.hostOS)

	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return "", errors.New("desktop application is shutting down")
	}
	if a.ctx == nil {
		a.mu.Unlock()
		return "", errors.New("desktop runtime is not ready")
	}
	if a.copyInProgress {
		a.mu.Unlock()
		return "", errors.New("the SFX artifact is still being copied to Desktop")
	}
	if a.cleanupPlanning {
		a.mu.Unlock()
		return "", errors.New("a cleanup plan is still being prepared")
	}
	if a.cleanupInProgress {
		a.mu.Unlock()
		return "", errors.New("build files are still being cleaned up")
	}
	if a.active != nil {
		a.mu.Unlock()
		return "", errors.New("a build is already running")
	}
	jobID := a.dependencies.newJobID()
	buildContext, cancel := context.WithCancel(a.ctx)
	job := &activeBuild{
		id: jobID, phase: "preflight", artifactPath: artifactPath, dryRun: request.DryRun,
		cleanupSnapshot: cleanupSnapshot, cancel: cancel, done: make(chan struct{}),
	}
	previousCleanupReceipt := a.cleanupReceipt
	a.completedArtifact = nil
	a.desktopArtifact = nil
	a.cleanupReceipt = nil
	a.preparedCleanup = nil
	a.active = job
	a.mu.Unlock()
	discardBuildCleanupReceipt(previousCleanupReceipt)

	a.emitJobProgress(a.runtimeContext(), job, ProgressEvent{
		JobID: jobID, Phase: "preflight", Status: "running",
		Message: "Build started", Percent: 0, ExitCode: -1,
	})
	go a.runBuild(buildContext, job, buildRequestArguments(request))
	return jobID, nil
}

func (a *App) runBuild(ctx context.Context, job *activeBuild, arguments []string) {
	eventContext := a.runtimeContext()
	stdoutEvents := eventWriter{ctx: eventContext, jobID: job.id, stream: "stdout", emit: a.dependencies.emit}
	stderrEvents := eventWriter{ctx: eventContext, jobID: job.id, stream: "stderr", emit: a.dependencies.emit}
	stdout := io.MultiWriter(a.dependencies.stdout, stdoutEvents)
	stderr := io.MultiWriter(a.dependencies.stderr, stderrEvents)
	reporter := buildcli.ReporterFunc(func(event buildcli.ProgressEvent) {
		a.reportBuilderProgress(eventContext, job, event)
	})
	interactiveRunner := buildcli.InteractiveCommandRunnerFunc(func(ctx context.Context, command buildcli.InteractiveCommand) error {
		phase, message := interactiveHandoffProgress(command.Purpose)
		a.emitJobProgress(eventContext, job, ProgressEvent{
			JobID: job.id, Phase: phase, Status: "running",
			Message: message, Percent: phasePercent(phase), ExitCode: -1,
		})
		if a.dependencies.interactiveRunner == nil {
			return errors.New("no native terminal launcher is available")
		}
		return a.dependencies.interactiveRunner.RunInteractive(ctx, command)
	})
	exitCode := a.dependencies.builder(ctx, arguments, a.dependencies.stdin, stdout, stderr, buildcli.RunOptions{
		Reporter:          reporter,
		InteractiveRunner: interactiveRunner,
	})
	a.mu.Lock()
	phase := job.phase
	cancelled := job.cancelRequested || ctx.Err() != nil
	a.mu.Unlock()

	var artifactErr error
	var completedArtifact *completedArtifact
	var cleanupReceipt *buildCleanupReceipt
	if !cancelled && exitCode == 0 && !job.dryRun {
		completed, err := inspectCompletedArtifactContext(ctx, job.id, job.artifactPath)
		if err != nil {
			artifactErr = err
		} else {
			completedArtifact = completed
			cleanupReceipt = finalizeBuildCleanup(job.id, job.cleanupSnapshot)
		}
	}
	a.mu.Lock()
	cancelled = job.cancelRequested || ctx.Err() != nil
	job.finished = true
	if !cancelled && artifactErr == nil && completedArtifact != nil {
		a.completedArtifact = completedArtifact
		a.cleanupReceipt = cleanupReceipt
	}
	a.mu.Unlock()
	if cancelled && cleanupReceipt != nil {
		discardBuildCleanupReceipt(cleanupReceipt)
	}

	progress := ProgressEvent{
		JobID: job.id, Phase: phase, Percent: -1, ExitCode: exitCode,
	}
	if cancelled {
		progress.Status = "cancelled"
		progress.Message = "Build cancelled"
	} else if artifactErr != nil {
		progress.Status = "error"
		progress.Message = fmt.Sprintf("Build completed but the SFX artifact could not be verified: %v", artifactErr)
		progress.ExitCode = 1
	} else if exitCode == 0 {
		progress.Status = "success"
		progress.Message = "Build completed"
		progress.Phase = "complete"
		progress.Percent = 100
	} else {
		progress.Status = "error"
		progress.Message = fmt.Sprintf("Build failed with exit code %d", exitCode)
	}
	a.emitJobProgress(eventContext, job, progress)

	a.mu.Lock()
	if a.active == job {
		a.active = nil
	}
	close(job.done)
	a.mu.Unlock()
}

func interactiveHandoffProgress(purpose buildcli.InteractivePurpose) (string, string) {
	switch purpose {
	case buildcli.InteractiveSteamAuthentication:
		return "assets", "Steam authentication opened in a terminal"
	case buildcli.InteractiveDependencyInstallation:
		return "toolchain", "Dependency installation opened in a terminal"
	default:
		return "toolchain", "Interactive command opened in a terminal"
	}
}

func (a *App) reportBuilderProgress(ctx context.Context, job *activeBuild, event buildcli.ProgressEvent) {
	phase := string(event.Phase)
	a.mu.Lock()
	if a.active == job {
		job.phase = phase
	}
	a.mu.Unlock()
	a.emitJobProgress(ctx, job, ProgressEvent{
		JobID: job.id, Phase: phase, Status: "running",
		Message: event.Message, Percent: phasePercent(phase), ExitCode: -1,
	})
}

func phasePercent(phase string) int {
	switch phase {
	case "preflight":
		return 5
	case "source":
		return 15
	case "toolchain":
		return 30
	case "assets":
		return 45
	case "online-server":
		return 65
	case "build":
		return 75
	case "complete":
		return 100
	default:
		return -1
	}
}

// CancelBuild requests cancellation for the sole active job.
func (a *App) CancelBuild() bool {
	a.mu.Lock()
	if a.active == nil || a.active.cancelRequested || a.active.finished {
		a.mu.Unlock()
		return false
	}
	job := a.active
	job.cancelRequested = true
	job.cancel()
	phase := job.phase
	a.mu.Unlock()

	a.emitJobProgress(a.runtimeContext(), job, ProgressEvent{
		JobID: job.id, Phase: phase, Status: "running",
		Message: "Cancellation requested", Percent: -1, ExitCode: -1,
	})
	return true
}

func (a *App) runtimeContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

func (a *App) emitProgress(ctx context.Context, event ProgressEvent) {
	if ctx != nil {
		a.dependencies.emit(ctx, progressEventName, event)
	}
}

// emitJobProgress serializes a job's progress stream and makes its first
// terminal status final. This prevents delayed cancellation/progress callbacks
// from moving the UI back to a running state after completion.
func (a *App) emitJobProgress(ctx context.Context, job *activeBuild, event ProgressEvent) bool {
	job.eventMu.Lock()
	defer job.eventMu.Unlock()
	if job.terminalEmitted {
		return false
	}
	switch event.Status {
	case "success", "error", "cancelled":
		job.terminalEmitted = true
	}
	a.emitProgress(ctx, event)
	return true
}

type eventWriter struct {
	ctx    context.Context
	jobID  string
	stream string
	emit   emitEventFunc
}

func (w eventWriter) Write(contents []byte) (int, error) {
	if len(contents) != 0 {
		w.emit(w.ctx, logEventName, LogEvent{JobID: w.jobID, Stream: w.stream, Text: string(contents)})
	}
	return len(contents), nil
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.shutdown(ctx)
	return false
}

// GeneralsX @build Codex 05/08/2026 Cancel active build, copy, and cleanup work, then wait briefly before the native window exits.
func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	a.shuttingDown = true
	job := a.active
	copyCancel := a.copyCancel
	copyDone := a.copyDone
	cleanupCancel := a.cleanupCancel
	cleanupDone := a.cleanupDone
	if job != nil && !job.finished {
		job.cancelRequested = true
		job.cancel()
	}
	if copyCancel != nil {
		copyCancel()
	}
	if cleanupCancel != nil {
		cleanupCancel()
	}
	a.mu.Unlock()

	doneChannels := make([]<-chan struct{}, 0, 3)
	if job != nil {
		doneChannels = append(doneChannels, job.done)
	}
	if copyDone != nil {
		doneChannels = append(doneChannels, copyDone)
	}
	if cleanupDone != nil {
		doneChannels = append(doneChannels, cleanupDone)
	}
	if len(doneChannels) > 0 {
		timer := time.NewTimer(a.dependencies.shutdownTimeout)
		defer timer.Stop()
		for _, done := range doneChannels {
			select {
			case <-done:
			case <-timer.C:
				return
			}
		}
	}

	a.mu.Lock()
	cleanupReceipt := a.cleanupReceipt
	if a.active != nil || a.copyInProgress || a.cleanupInProgress {
		cleanupReceipt = nil
	} else {
		a.cleanupReceipt = nil
		a.preparedCleanup = nil
	}
	a.mu.Unlock()
	discardBuildCleanupReceipt(cleanupReceipt)
}
