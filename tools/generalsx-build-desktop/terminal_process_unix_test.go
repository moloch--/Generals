//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminalCancellationStopsDescendantProcessGroup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	startedPath := filepath.Join(root, "started")
	heartbeatPath := filepath.Join(root, "heartbeat")
	childPIDPath := filepath.Join(root, "child.pid")
	spec := terminalTestSpec(t, map[string]string{
		"GX_TERMINAL_TEST_HELPER":    "spawn-child",
		"GX_TERMINAL_TEST_STARTED":   startedPath,
		"GX_TERMINAL_TEST_HEARTBEAT": heartbeatPath,
		"GX_TERMINAL_TEST_CHILD_PID": childPIDPath,
	})
	spec.CancelPath = filepath.Join(root, "cancel")
	t.Cleanup(func() {
		contents, err := os.ReadFile(childPIDPath)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	resultChannel := make(chan terminalJobResult, 1)
	go func() { resultChannel <- executeTerminalJob(spec) }()
	waitForFile(t, startedPath)
	if err := writeCancellationSentinel(spec.CancelPath); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultChannel:
		if result.ExitCode != 130 {
			t.Fatalf("cancel result = %#v", result)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("terminal process tree did not stop after cancellation")
	}

	before, err := os.Stat(heartbeatPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	after, err := os.Stat(heartbeatPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("descendant continued after cancellation: heartbeat grew from %d to %d bytes", before.Size(), after.Size())
	}
}
