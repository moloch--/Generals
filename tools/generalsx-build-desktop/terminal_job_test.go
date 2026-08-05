package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moloch--/Generals/internal/buildcli"
)

func TestTerminalInteractiveRunnerUsesPrivateSpecAndPreservesArguments(t *testing.T) {
	t.Parallel()
	var observedSpecPath string
	command := buildcli.InteractiveCommand{
		Purpose:          buildcli.InteractiveSteamAuthentication,
		Executable:       "/private/steam cmd",
		Arguments:        []string{"+login", "name; $(not-a-shell)", `quote"value`},
		WorkingDirectory: "/private/work tree",
		Environment:      map[string]string{"FIXTURE": "value with spaces"},
	}
	runner := &terminalInteractiveRunner{
		executablePath: func() (string, error) { return "/Applications/GeneralsX Build Tool", nil },
		launcher: func(_ context.Context, executable, specPath string) error {
			observedSpecPath = specPath
			if executable != "/Applications/GeneralsX Build Tool" {
				t.Fatalf("desktop executable = %q", executable)
			}
			info, err := os.Stat(specPath)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("spec mode = %#o", info.Mode().Perm())
			}
			spec, err := readTerminalJobSpec(specPath)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Command.Executable != command.Executable || !slices.Equal(spec.Command.Arguments, command.Arguments) {
				t.Fatalf("command changed through JSON: %#v", spec.Command)
			}
			return writeTerminalResult(spec.ResultPath, terminalJobResult{ExitCode: 0})
		},
		pollInterval:     time.Millisecond,
		cancellationWait: time.Second,
	}
	if err := runner.RunInteractive(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(observedSpecPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal job directory was not removed: %v", err)
	}
}

func TestExecuteTerminalJobSuccessIsNotClassifiedAsCancellation(t *testing.T) {
	t.Parallel()
	spec := terminalTestSpec(t, map[string]string{"GX_TERMINAL_TEST_HELPER": "success"})
	result := executeTerminalJob(spec)
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteTerminalJobCancellationStopsCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	startedPath := filepath.Join(root, "started")
	spec := terminalTestSpec(t, map[string]string{
		"GX_TERMINAL_TEST_HELPER":  "block",
		"GX_TERMINAL_TEST_STARTED": startedPath,
	})
	spec.CancelPath = filepath.Join(root, "cancel")
	resultChannel := make(chan terminalJobResult, 1)
	go func() { resultChannel <- executeTerminalJob(spec) }()
	waitForFile(t, startedPath)
	if err := writeCancellationSentinel(spec.CancelPath); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultChannel:
		if result.ExitCode != 130 || result.Error == "" {
			t.Fatalf("cancel result = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal command did not stop after cancellation")
	}
}

func TestTerminalInteractiveRunnerSignalsCancellation(t *testing.T) {
	t.Parallel()
	launched := make(chan struct{})
	runner := &terminalInteractiveRunner{
		executablePath: func() (string, error) { return "/desktop", nil },
		launcher: func(_ context.Context, _, specPath string) error {
			spec, err := readTerminalJobSpec(specPath)
			if err != nil {
				return err
			}
			close(launched)
			go func() {
				for {
					if _, err := os.Stat(spec.CancelPath); err == nil {
						_ = writeTerminalResult(spec.ResultPath, terminalJobResult{ExitCode: 130, Error: "cancelled"})
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
			return nil
		},
		pollInterval:     time.Millisecond,
		cancellationWait: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.RunInteractive(ctx, buildcli.InteractiveCommand{
			Purpose: buildcli.InteractiveSteamAuthentication, Executable: "/steamcmd",
		})
	}()
	<-launched
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunInteractive() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunInteractive did not return after cancellation")
	}
}

func TestTerminalInteractiveRunnerDetectsClosedHelperFromStaleHeartbeat(t *testing.T) {
	t.Parallel()
	runner := &terminalInteractiveRunner{
		executablePath: func() (string, error) { return "/desktop", nil },
		launcher: func(_ context.Context, _, specPath string) error {
			spec, err := readTerminalJobSpec(specPath)
			if err != nil {
				return err
			}
			return writeTerminalHeartbeat(spec.HeartbeatPath)
		},
		pollInterval:     5 * time.Millisecond,
		cancellationWait: time.Second,
		startupTimeout:   time.Second,
		heartbeatTimeout: 25 * time.Millisecond,
	}
	started := time.Now()
	err := runner.RunInteractive(context.Background(), buildcli.InteractiveCommand{
		Purpose: buildcli.InteractiveDependencyInstallation, Executable: "/installer",
	})
	if err == nil || !strings.Contains(err.Error(), "closed before reporting a result") {
		t.Fatalf("RunInteractive() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("closed terminal detection took %s", elapsed)
	}
}

func TestValidateInteractiveCommandRejectsUnknownPurpose(t *testing.T) {
	t.Parallel()
	err := validateInteractiveCommand(buildcli.InteractiveCommand{
		Purpose: "unknown-purpose", Executable: "/tool",
	})
	if err == nil || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("validateInteractiveCommand() error = %v", err)
	}
}

func terminalTestSpec(t *testing.T, environment map[string]string) terminalJobSpec {
	t.Helper()
	root := t.TempDir()
	return terminalJobSpec{
		Command: buildcli.InteractiveCommand{
			Purpose:     buildcli.InteractiveSteamAuthentication,
			Executable:  os.Args[0],
			Arguments:   []string{"-test.run=^TestTerminalHelperProcess$"},
			Environment: environment,
		},
		ResultPath: filepath.Join(root, "result.json"),
		CancelPath: filepath.Join(root, "cancel"),
	}
}

func TestTerminalHelperProcess(t *testing.T) {
	mode := os.Getenv("GX_TERMINAL_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "spawn-child" {
		child := exec.Command(os.Args[0], "-test.run=^TestTerminalHelperProcess$")
		child.Env = mergeTerminalEnvironment(os.Environ(), map[string]string{
			"GX_TERMINAL_TEST_HELPER":  "descendant",
			"GX_TERMINAL_TEST_STARTED": "",
		})
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if path := os.Getenv("GX_TERMINAL_TEST_CHILD_PID"); path != "" {
			if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		waitForFile(t, os.Getenv("GX_TERMINAL_TEST_HEARTBEAT"))
		if started := os.Getenv("GX_TERMINAL_TEST_STARTED"); started != "" {
			if err := os.WriteFile(started, []byte("started"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_ = child.Wait()
		return
	}
	if started := os.Getenv("GX_TERMINAL_TEST_STARTED"); started != "" {
		if err := os.WriteFile(started, []byte("started"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "descendant" {
		heartbeat := os.Getenv("GX_TERMINAL_TEST_HEARTBEAT")
		for {
			file, err := os.OpenFile(heartbeat, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte(".")); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if mode == "block" {
		for {
			time.Sleep(time.Second)
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
