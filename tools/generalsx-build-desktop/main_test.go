package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunDispatchesEveryArgumentToExistingCLI(t *testing.T) {
	t.Parallel()
	runCLI := func(arguments ...string) (int, string, string, bool) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		desktopCalled := false
		status := run(arguments, strings.NewReader(""), &stdout, &stderr, func() error {
			desktopCalled = true
			return nil
		})
		return status, stdout.String(), stderr.String(), desktopCalled
	}

	wantStatus, wantStdout, wantStderr, desktopCalled := runCLI("--help")
	if desktopCalled {
		t.Fatal("--help started the desktop")
	}
	gotStatus, gotStdout, gotStderr, desktopCalled := runCLI("--headless", "--help")
	if desktopCalled {
		t.Fatal("--headless --help started the desktop")
	}
	if gotStatus != wantStatus || gotStdout != wantStdout || gotStderr != wantStderr {
		t.Fatalf("headless dispatch differs from CLI help:\nstatus %d != %d\nstdout %q != %q\nstderr %q != %q",
			gotStatus, wantStatus, gotStdout, wantStdout, gotStderr, wantStderr)
	}
}

func TestRunDefaultsToDesktopWithoutArguments(t *testing.T) {
	t.Parallel()
	called := false
	status := run(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, func() error {
		called = true
		return nil
	})
	if status != 0 || !called {
		t.Fatalf("run() = %d, desktop called = %v", status, called)
	}
}

func TestWriteDesktopBuildInfo(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	writeDesktopBuildInfo(&output, "1.2.3", "0123456789abcdef")
	if got, want := output.String(), "version=1.2.3\ncommit=0123456789abcdef\n"; got != want {
		t.Fatalf("build info = %q, want %q", got, want)
	}
}

func TestRunReportsDesktopStartupFailure(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	status := run(nil, strings.NewReader(""), &bytes.Buffer{}, &stderr, func() error {
		return errors.New("fixture failure")
	})
	if status != 1 || !strings.Contains(stderr.String(), "fixture failure") {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
}
