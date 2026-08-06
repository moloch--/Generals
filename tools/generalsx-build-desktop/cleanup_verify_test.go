package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVerifySFXArtifactRunsNativeTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "verify-sfx")
	contents := []byte("#!/bin/sh\n[ \"$1\" = \"--sfx-verify\" ]\n")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifySFXArtifact(context.Background(), path, targetForHost(runtime.GOOS), runtime.GOOS); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySFXArtifactRejectsUnsupportedHostTargetPair(t *testing.T) {
	t.Parallel()
	err := verifySFXArtifact(context.Background(), "unused", "windows", "darwin")
	if err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("verify mismatch error = %v", err)
	}
}

func TestVerifySFXArtifactPropagatesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "verify-sfx")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := verifySFXArtifact(ctx, path, targetForHost(runtime.GOOS), runtime.GOOS)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled verifier error = %v", err)
	}
}

func TestLinuxSFXDockerVerifyArgumentsAreHardened(t *testing.T) {
	t.Parallel()
	arguments := linuxSFXDockerVerifyArguments("/Desktop/game", "/tmp/verify", "/tmp/cid", "builder:test", "501:20")
	joined := strings.Join(arguments, " ")
	for _, required := range []string{
		"--pull=never", "--platform linux/amd64", "--network none", "--read-only",
		"--cap-drop ALL", "--security-opt no-new-privileges", "--user 501:20",
		"/Desktop/game:/sfx:ro", "/tmp/verify:/tmp:rw", "--entrypoint /sfx", "builder:test --sfx-verify",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("Docker verifier arguments %q omit %q", joined, required)
		}
	}
}
