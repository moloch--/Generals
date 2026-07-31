//go:build !windows

package launch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/signalctx"
)

func TestExitCodeForSignaledChild(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		signal   syscall.Signal
		exitCode int
	}{
		{name: "interrupt", signal: syscall.SIGINT, exitCode: 130},
		{name: "terminate", signal: syscall.SIGTERM, exitCode: 143},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestExitCodeSignalHelperProcess$")
			command.Env = append(
				os.Environ(),
				"GENERALSX_SIGNAL_HELPER=1",
				"GENERALSX_SIGNAL_HELPER_NUMBER="+strconv.Itoa(int(testCase.signal)),
			)
			err := command.Run()
			if err == nil {
				t.Fatal("signaled helper unexpectedly succeeded")
			}
			if code := ExitCode(err); code != testCase.exitCode {
				t.Fatalf("ExitCode(signaled helper) = %d, want %d", code, testCase.exitCode)
			}
		})
	}
}

func TestExitCodeSignalHelperProcess(t *testing.T) {
	if os.Getenv("GENERALSX_SIGNAL_HELPER") != "1" {
		return
	}
	number, err := strconv.Atoi(os.Getenv("GENERALSX_SIGNAL_HELPER_NUMBER"))
	if err != nil {
		os.Exit(127)
	}
	if err := syscall.Kill(os.Getpid(), syscall.Signal(number)); err != nil {
		os.Exit(126)
	}
	time.Sleep(time.Second)
	os.Exit(125)
}

func TestPrepareSeparatesProcessGroupAndForwardsSignalOnce(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		signal   syscall.Signal
		exitCode int
	}{
		{name: "interrupt", signal: syscall.SIGINT, exitCode: 130},
		{name: "terminate", signal: syscall.SIGTERM, exitCode: 143},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, stop := signalctx.NotifyContext(context.Background())
			defer stop()

			signalReport := filepath.Join(t.TempDir(), "signals")
			command := prepareForwardedSignalHelper(t, ctx, signalReport)
			command.Stdout = nil
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}

			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "ready" {
				_ = command.Process.Kill()
				t.Fatalf("helper readiness = %q, error = %v", scanner.Text(), scanner.Err())
			}

			childProcessGroup, err := syscall.Getpgid(command.Process.Pid)
			if err != nil {
				_ = command.Process.Kill()
				t.Fatal(err)
			}
			parentProcessGroup := syscall.Getpgrp()
			if childProcessGroup != command.Process.Pid {
				_ = command.Process.Kill()
				t.Fatalf("child process group = %d, want child pid %d", childProcessGroup, command.Process.Pid)
			}
			if childProcessGroup == parentProcessGroup {
				_ = command.Process.Kill()
				t.Fatalf("child process group = parent process group %d", parentProcessGroup)
			}

			if err := syscall.Kill(os.Getpid(), testCase.signal); err != nil {
				_ = command.Process.Kill()
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				_ = command.Process.Kill()
				t.Fatal("signal context was not canceled")
			}

			if err := command.Wait(); ExitCode(err) != testCase.exitCode {
				t.Fatalf(
					"forwarded %v helper result = %v, exit code = %d, want %d",
					testCase.signal,
					err,
					ExitCode(err),
					testCase.exitCode,
				)
			}
			assertForwardedSignalReport(t, signalReport, testCase.signal)
		})
	}
}

func TestForwardedSignalHelperProcess(t *testing.T) {
	if os.Getenv("GENERALSX_FORWARD_SIGNAL_HELPER") != "1" {
		return
	}
	if os.Getenv("GENERALSX_FORWARD_SIGNAL_ROLE") == "member" {
		runForwardedSignalGroupMember()
		return
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	member := exec.Command(os.Args[0], "-test.run=^TestForwardedSignalHelperProcess$")
	member.Env = append(
		os.Environ(),
		"GENERALSX_FORWARD_SIGNAL_HELPER=1",
		"GENERALSX_FORWARD_SIGNAL_ROLE=member",
	)
	memberStdout, err := member.StdoutPipe()
	if err != nil {
		os.Exit(94)
	}
	member.Stderr = os.Stderr
	if err := member.Start(); err != nil {
		os.Exit(95)
	}
	memberScanner := bufio.NewScanner(memberStdout)
	if !memberScanner.Scan() || memberScanner.Text() != "member-ready" {
		_ = member.Process.Kill()
		os.Exit(96)
	}

	fmt.Fprintln(os.Stdout, "ready")

	received := <-signals
	if err := appendForwardedSignalMarker("leader", received); err != nil {
		_ = member.Process.Kill()
		os.Exit(97)
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-signals:
		_ = member.Process.Kill()
		os.Exit(90)
	case <-timer.C:
	}

	memberDone := make(chan error, 1)
	go func() {
		memberDone <- member.Wait()
	}()
	select {
	case err := <-memberDone:
		if err != nil {
			os.Exit(98)
		}
	case <-time.After(time.Second):
		_ = member.Process.Kill()
		os.Exit(99)
	}

	nativeSignal, ok := received.(syscall.Signal)
	if !ok {
		os.Exit(91)
	}
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)
	if err := syscall.Kill(os.Getpid(), nativeSignal); err != nil {
		os.Exit(92)
	}
	time.Sleep(time.Second)
	os.Exit(93)
}

func runForwardedSignalGroupMember() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	fmt.Fprintln(os.Stdout, "member-ready")
	if err := appendForwardedSignalMarker("member", <-signals); err != nil {
		os.Exit(100)
	}
}

func appendForwardedSignalMarker(role string, received os.Signal) error {
	nativeSignal, ok := received.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unexpected signal type %T", received)
	}
	report, err := os.OpenFile(
		os.Getenv("GENERALSX_FORWARD_SIGNAL_REPORT"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(report, "%s:%d\n", role, nativeSignal); err != nil {
		_ = report.Close()
		return err
	}
	return report.Close()
}

func TestTerminateProcessMapsCompletedChildToProcessDone(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestCompletedProcessGroupHelper$")
	configureProcessGroup(command)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if err := terminateProcess(command.Process, context.Background()); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("terminateProcess(completed process group) error = %v, want os.ErrProcessDone", err)
	}
}

func TestSignalProcessGroupMapsESRCHToProcessDone(t *testing.T) {
	const impossibleProcessGroup = 1 << 30
	if err := signalProcessGroup(impossibleProcessGroup, syscall.SIGTERM); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signalProcessGroup(missing process group) error = %v, want os.ErrProcessDone", err)
	}
}

func TestCompletedProcessGroupHelper(t *testing.T) {}

func prepareForwardedSignalHelper(t *testing.T, ctx context.Context, signalReport string) *exec.Cmd {
	t.Helper()

	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(currentExecutable)
	if err != nil {
		t.Fatal(err)
	}

	root, _, executable := makePayload(t, "forwarded-signal-helper")
	if err := os.WriteFile(executable, contents, 0o700); err != nil {
		t.Fatal(err)
	}

	command, err := Prepare(Config{
		Root:       root,
		TargetOS:   runtime.GOOS,
		Entrypoint: "runtime/forwarded-signal-helper",
		WorkDir:    "runtime",
		Args:       []string{"-test.run=^TestForwardedSignalHelperProcess$"},
		Env: append(
			os.Environ(),
			"GENERALSX_FORWARD_SIGNAL_HELPER=1",
			"GENERALSX_FORWARD_SIGNAL_REPORT="+signalReport,
		),
		Context: ctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func assertForwardedSignalReport(t *testing.T, reportPath string, expected syscall.Signal) {
	t.Helper()

	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, marker := range strings.Fields(string(contents)) {
		counts[marker]++
	}
	for _, role := range []string{"leader", "member"} {
		marker := fmt.Sprintf("%s:%d", role, expected)
		if counts[marker] != 1 {
			t.Fatalf("forwarded signal markers = %q, want exactly one %q", contents, marker)
		}
		delete(counts, marker)
	}
	if len(counts) != 0 {
		t.Fatalf("forwarded signal markers contain unexpected entries: %q", contents)
	}
}
