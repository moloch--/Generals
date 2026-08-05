package buildcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	processTreeHelperMode = "GENERALSX_PROCESS_TREE_HELPER_MODE"
	processTreeHelperPID  = "GENERALSX_PROCESS_TREE_HELPER_PID"
)

func TestRunnerCancellationTerminatesDescendants(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, runner, command) error
	}{
		{
			name: "streamed command",
			run: func(ctx context.Context, runner runner, command command) error {
				return runner.run(ctx, command)
			},
		},
		{
			name: "captured command",
			run: func(ctx context.Context, runner runner, command command) error {
				_, err := runner.output(ctx, command)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "descendant.pid")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan error, 1)
			go func() {
				completed <- test.run(ctx, runner{
					stdout: io.Discard,
					stderr: io.Discard,
				}, command{
					name: os.Args[0],
					args: []string{"-test.run=^TestRunnerProcessTreeHelper$"},
					env: map[string]string{
						processTreeHelperMode: "parent",
						processTreeHelperPID:  pidFile,
					},
				})
			}()

			childPID := waitForHelperPID(t, pidFile)
			cancel()
			select {
			case err := <-completed:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("runner cancellation error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not finish after cancellation")
			}

			deadline := time.Now().Add(5 * time.Second)
			for processExists(childPID) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if processExists(childPID) {
				t.Fatalf("descendant process %d survived cancellation", childPID)
			}
		})
	}
}

func TestRunnerProcessTreeHelper(t *testing.T) {
	switch os.Getenv(processTreeHelperMode) {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestRunnerProcessTreeHelper$")
		child.Env = mergeEnvironment(os.Environ(), map[string]string{processTreeHelperMode: "child"})
		if err := child.Start(); err != nil {
			t.Fatalf("start descendant helper: %v", err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		ignoreGracefulTermination()
		pidFile := os.Getenv(processTreeHelperPID)
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write descendant PID: %v", err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
}

func waitForHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(contents))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid descendant PID %q: %v", contents, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read descendant PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("descendant helper did not write %s", path))
	return 0
}
