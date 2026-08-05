package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
)

const terminalJobFileLimit = 1 << 20

type terminalJobSpec struct {
	Command       buildcli.InteractiveCommand `json:"command"`
	ResultPath    string                      `json:"resultPath"`
	CancelPath    string                      `json:"cancelPath"`
	HeartbeatPath string                      `json:"heartbeatPath"`
}

type terminalJobResult struct {
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

type terminalInteractiveRunner struct {
	executablePath   func() (string, error)
	launcher         func(context.Context, string, string) error
	pollInterval     time.Duration
	cancellationWait time.Duration
	startupTimeout   time.Duration
	heartbeatTimeout time.Duration
}

func newTerminalInteractiveRunner() buildcli.InteractiveCommandRunner {
	return &terminalInteractiveRunner{
		executablePath:   os.Executable,
		launcher:         launchTerminalJob,
		pollInterval:     150 * time.Millisecond,
		cancellationWait: 5 * time.Second,
		startupTimeout:   15 * time.Second,
		heartbeatTimeout: 5 * time.Second,
	}
}

// GeneralsX @feature Codex 05/08/2026 Handoff prompt-capable commands through a mode-0600 terminal job.
func (runner *terminalInteractiveRunner) RunInteractive(ctx context.Context, command buildcli.InteractiveCommand) error {
	if ctx == nil {
		return errors.New("interactive command context is nil")
	}
	if err := validateInteractiveCommand(command); err != nil {
		return err
	}
	desktopExecutable, err := runner.executablePath()
	if err != nil {
		return fmt.Errorf("locate desktop executable: %w", err)
	}
	desktopExecutable, err = filepath.Abs(desktopExecutable)
	if err != nil {
		return fmt.Errorf("resolve desktop executable: %w", err)
	}
	jobDirectory, err := os.MkdirTemp("", "generalsx-terminal-job-")
	if err != nil {
		return fmt.Errorf("create private terminal job directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(jobDirectory)
		}
	}()

	specPath := filepath.Join(jobDirectory, "job.json")
	resultPath := filepath.Join(jobDirectory, "result.json")
	cancelPath := filepath.Join(jobDirectory, "cancel")
	heartbeatPath := filepath.Join(jobDirectory, "heartbeat")
	spec := terminalJobSpec{
		Command:       cloneInteractiveCommand(command),
		ResultPath:    resultPath,
		CancelPath:    cancelPath,
		HeartbeatPath: heartbeatPath,
	}
	if err := writePrivateJSON(specPath, spec); err != nil {
		return fmt.Errorf("write terminal job: %w", err)
	}
	if err := runner.launcher(ctx, desktopExecutable, specPath); err != nil {
		return fmt.Errorf("open native terminal: %w", err)
	}

	result, err := waitForTerminalJob(ctx, spec, runner.pollInterval, runner.startupTimeout, runner.heartbeatTimeout)
	if err != nil && ctx.Err() != nil {
		if cancelErr := writeCancellationSentinel(cancelPath); cancelErr != nil {
			return fmt.Errorf("%w; signal terminal cancellation: %v", ctx.Err(), cancelErr)
		}
		waitContext, cancel := context.WithTimeout(context.Background(), runner.cancellationWait)
		_, waitErr := waitForTerminalJob(waitContext, spec, runner.pollInterval, runner.startupTimeout, runner.heartbeatTimeout)
		cancel()
		if waitErr != nil {
			cleanup = false
			return fmt.Errorf("%w; terminal command did not acknowledge cancellation", ctx.Err())
		}
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if result.Error != "" {
		return errors.New(result.Error)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("interactive command exited with code %d", result.ExitCode)
	}
	return nil
}

func cloneInteractiveCommand(command buildcli.InteractiveCommand) buildcli.InteractiveCommand {
	cloned := command
	cloned.Arguments = append([]string(nil), command.Arguments...)
	if command.Environment != nil {
		cloned.Environment = make(map[string]string, len(command.Environment))
		for key, value := range command.Environment {
			cloned.Environment[key] = value
		}
	}
	return cloned
}

func validateInteractiveCommand(command buildcli.InteractiveCommand) error {
	switch command.Purpose {
	case buildcli.InteractiveSteamAuthentication, buildcli.InteractiveDependencyInstallation:
	default:
		return fmt.Errorf("unsupported interactive command purpose %q", command.Purpose)
	}
	if command.Executable == "" {
		return errors.New("interactive command executable is empty")
	}
	values := append([]string{command.Executable, command.WorkingDirectory}, command.Arguments...)
	for _, value := range values {
		if strings.ContainsRune(value, '\x00') {
			return errors.New("interactive command contains a NUL byte")
		}
	}
	for key, value := range command.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("interactive command environment key %q is invalid", key)
		}
	}
	return nil
}

func terminalPurposeMessages(purpose buildcli.InteractivePurpose) (heading, privacy, completed, cancelled string) {
	switch purpose {
	case buildcli.InteractiveSteamAuthentication:
		return "GeneralsX opened SteamCMD in this terminal for private authentication.",
			"Passwords and Steam Guard codes are read directly by SteamCMD and are never sent to the desktop UI.",
			"SteamCMD completed. The desktop build will continue automatically.",
			"SteamCMD authentication was cancelled."
	case buildcli.InteractiveDependencyInstallation:
		return "GeneralsX opened dependency installation in this terminal.",
			"Password and confirmation prompts are read directly by the installer and are never sent to the desktop UI.",
			"Dependency installation completed. The desktop build will continue automatically.",
			"Dependency installation was cancelled."
	default:
		return "GeneralsX opened an interactive build command in this terminal.",
			"Private prompts are read directly by the command and are never sent to the desktop UI.",
			"Interactive command completed. The desktop build will continue automatically.",
			"Interactive command was cancelled."
	}
}

func writePrivateJSON(path string, value interface{}) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func writeCancellationSentinel(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func waitForTerminalJob(ctx context.Context, spec terminalJobSpec, interval, startupTimeout, heartbeatTimeout time.Duration) (terminalJobResult, error) {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	if startupTimeout <= 0 {
		startupTimeout = 15 * time.Second
	}
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = 5 * time.Second
	}
	startedWaiting := time.Now()
	heartbeatSeen := false
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, ready, err := readTerminalResult(spec.ResultPath)
		if err != nil {
			return terminalJobResult{}, err
		}
		if ready {
			return result, nil
		}
		heartbeat, heartbeatErr := os.Stat(spec.HeartbeatPath)
		switch {
		case heartbeatErr == nil:
			heartbeatSeen = true
			if time.Since(heartbeat.ModTime()) > heartbeatTimeout {
				return terminalJobResult{}, errors.New("native terminal helper closed before reporting a result")
			}
		case !errors.Is(heartbeatErr, os.ErrNotExist):
			return terminalJobResult{}, fmt.Errorf("inspect terminal helper heartbeat: %w", heartbeatErr)
		case !heartbeatSeen && time.Since(startedWaiting) > startupTimeout:
			return terminalJobResult{}, errors.New("native terminal helper did not start")
		case heartbeatSeen:
			return terminalJobResult{}, errors.New("native terminal helper closed before reporting a result")
		}
		select {
		case <-ctx.Done():
			return terminalJobResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readTerminalResult(path string) (terminalJobResult, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return terminalJobResult{}, false, nil
	}
	if err != nil {
		return terminalJobResult{}, false, fmt.Errorf("open terminal result: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, terminalJobFileLimit))
	decoder.DisallowUnknownFields()
	var result terminalJobResult
	if err := decoder.Decode(&result); err != nil {
		return terminalJobResult{}, false, fmt.Errorf("decode terminal result: %w", err)
	}
	return result, true, nil
}

// runTerminalJob is an intentionally hidden child mode reached only through a private job spec.
func runTerminalJob(specPath string) int {
	spec, err := readTerminalJobSpec(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generalsx-build-desktop: %v\n", err)
		return 2
	}
	stopHeartbeat, err := startTerminalHeartbeat(spec.HeartbeatPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generalsx-build-desktop: start terminal heartbeat: %v\n", err)
		return 2
	}
	result := executeTerminalJob(spec)
	stopHeartbeat()
	if err := writeTerminalResult(spec.ResultPath, result); err != nil {
		fmt.Fprintf(os.Stderr, "generalsx-build-desktop: write terminal result: %v\n", err)
		return 1
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", result.Error)
	}
	if result.ExitCode < 0 || result.ExitCode > 255 {
		return 1
	}
	return result.ExitCode
}

func readTerminalJobSpec(path string) (terminalJobSpec, error) {
	if !filepath.IsAbs(path) {
		return terminalJobSpec{}, errors.New("terminal job path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return terminalJobSpec{}, fmt.Errorf("inspect terminal job: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return terminalJobSpec{}, errors.New("terminal job is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return terminalJobSpec{}, errors.New("terminal job is accessible by other users")
	}
	file, err := os.Open(path)
	if err != nil {
		return terminalJobSpec{}, fmt.Errorf("open terminal job: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, terminalJobFileLimit))
	decoder.DisallowUnknownFields()
	var spec terminalJobSpec
	if err := decoder.Decode(&spec); err != nil {
		return terminalJobSpec{}, fmt.Errorf("decode terminal job: %w", err)
	}
	if err := validateInteractiveCommand(spec.Command); err != nil {
		return terminalJobSpec{}, err
	}
	jobDirectory := filepath.Clean(filepath.Dir(path))
	for label, candidate := range map[string]string{
		"result": spec.ResultPath, "cancel": spec.CancelPath, "heartbeat": spec.HeartbeatPath,
	} {
		if !filepath.IsAbs(candidate) || filepath.Clean(filepath.Dir(candidate)) != jobDirectory {
			return terminalJobSpec{}, fmt.Errorf("terminal %s path must remain in the private job directory", label)
		}
	}
	return spec, nil
}

func startTerminalHeartbeat(path string) (func(), error) {
	if err := writeTerminalHeartbeat(path); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = writeTerminalHeartbeat(path)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}, nil
}

func writeTerminalHeartbeat(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "%d\n", time.Now().UnixNano())
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func executeTerminalJob(spec terminalJobSpec) terminalJobResult {
	streams, err := openTerminalStreams()
	if err != nil {
		return terminalJobResult{ExitCode: 1, Error: fmt.Sprintf("open terminal streams: %v", err)}
	}
	defer streams.close()
	heading, privacy, completed, cancelled := terminalPurposeMessages(spec.Command.Purpose)
	fmt.Fprintln(streams.stdout, heading)
	fmt.Fprintln(streams.stdout, privacy)
	if cancellationSentinelExists(spec.CancelPath) {
		return terminalJobResult{ExitCode: 130, Error: cancelled}
	}

	command := exec.Command(spec.Command.Executable, spec.Command.Arguments...)
	command.Dir = spec.Command.WorkingDirectory
	command.Env = mergeTerminalEnvironment(os.Environ(), spec.Command.Environment)
	command.Stdin = streams.stdin
	command.Stdout = streams.stdout
	command.Stderr = streams.stderr
	process, err := startTerminalProcess(command)
	if err != nil {
		return terminalJobResult{ExitCode: 1, Error: fmt.Sprintf("start interactive command: %v", err)}
	}
	defer process.close()

	var cancellationRequested atomic.Bool
	monitorStop := make(chan struct{})
	monitorDone := make(chan struct{})
	go monitorTerminalCancellation(monitorStop, func() {
		cancellationRequested.Store(true)
		_ = process.terminate()
	}, spec.CancelPath, monitorDone)
	runErr := process.wait()
	close(monitorStop)
	<-monitorDone

	if cancellationRequested.Load() {
		return terminalJobResult{ExitCode: 130, Error: cancelled}
	}
	if runErr == nil {
		fmt.Fprintln(streams.stdout, "\n"+completed)
		return terminalJobResult{ExitCode: 0}
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		exitCode := exitError.ExitCode()
		return terminalJobResult{ExitCode: exitCode}
	}
	return terminalJobResult{ExitCode: 1, Error: fmt.Sprintf("start interactive command: %v", runErr)}
}

func cancellationSentinelExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func monitorTerminalCancellation(stop <-chan struct{}, cancel context.CancelFunc, path string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(path); err == nil {
			cancel()
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			cancel()
			return
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func mergeTerminalEnvironment(base []string, overrides map[string]string) []string {
	caseInsensitive := runtime.GOOS == "windows"
	normalize := func(value string) string {
		if caseInsensitive {
			return strings.ToUpper(value)
		}
		return value
	}
	entries := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found && key != "" {
			entries[normalize(key)] = entry
		}
	}
	for key, value := range overrides {
		entries[normalize(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, entries[key])
	}
	return result
}

func writeTerminalResult(path string, result terminalJobResult) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".result-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(result); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
