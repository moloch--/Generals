//go:build darwin

// GeneralsX @feature Codex 01/08/2026 Verify app-bundle helper discovery and process reaping.
package progress

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundledHelperPathRequiresRealAppHelper(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "GeneralsXZH.app", "Contents", "MacOS", "GeneralsXZH")
	helper := filepath.Join(root, "GeneralsXZH.app", "Contents", "Helpers", helperBasename)
	writeTestExecutable(t, executable, "launcher")
	writeTestExecutable(t, helper, "helper")

	got, err := bundledHelperPath(executable)
	if err != nil {
		t.Fatalf("bundledHelperPath: %v", err)
	}
	resolvedHelper, err := filepath.EvalSymlinks(helper)
	if err != nil {
		t.Fatal(err)
	}
	if got != resolvedHelper {
		t.Fatalf("helper path = %q, want %q", got, resolvedHelper)
	}

	outside := filepath.Join(root, "GeneralsXZH")
	writeTestExecutable(t, outside, "launcher")
	if _, err := bundledHelperPath(outside); err == nil {
		t.Fatal("bundledHelperPath accepted a launcher outside an app bundle")
	}

	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/true", helper); err != nil {
		t.Fatal(err)
	}
	if _, err := bundledHelperPath(executable); err == nil {
		t.Fatal("bundledHelperPath accepted a symlink helper")
	}
}

func TestOpenForExecutableUsesExpectedHelper(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "GeneralsXZH.app", "Contents", "MacOS", "GeneralsXZH")
	helper := filepath.Join(root, "GeneralsXZH.app", "Contents", "Helpers", helperBasename)
	writeTestExecutable(t, executable, "launcher")
	writeTestExecutable(t, helper, "helper")

	sink := &recordingWriteCloser{}
	started := ""
	stops := 0
	reporter := openForExecutable(executable, func(path string) (io.WriteCloser, func(), error) {
		started = path
		return sink, func() { stops++ }, nil
	})
	reporter.Indeterminate("Preparing")
	reporter.Complete()
	reporter.Close()

	resolvedHelper, err := filepath.EvalSymlinks(helper)
	if err != nil {
		t.Fatal(err)
	}
	if started != resolvedHelper {
		t.Fatalf("started helper = %q, want %q", started, resolvedHelper)
	}
	if stops != 1 {
		t.Fatalf("helper stop count = %d, want 1", stops)
	}
}

func TestStartedHelperReceivesEOF(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "GeneralsXZH.app", "Contents", "MacOS", "GeneralsXZH")
	helper := filepath.Join(root, "GeneralsXZH.app", "Contents", "Helpers", helperBasename)
	output := filepath.Join(root, "progress.ndjson")
	writeTestExecutable(t, executable, "launcher")
	writeTestExecutable(t, helper, "#!/bin/sh\nexec /bin/cat > \"$GX_PROGRESS_TEST_OUTPUT\"\n")
	t.Setenv("GX_PROGRESS_TEST_OUTPUT", output)

	reporter := openForExecutable(executable, startHelper)
	reporter.Indeterminate("Preparing")
	reporter.Update("Extracting", 1, 2)
	reporter.Complete()
	reporter.Close()

	var contents []byte
	var err error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		contents, err = os.ReadFile(output)
		if err == nil && bytes.Count(contents, []byte{'\n'}) == 3 {
			break
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read helper output: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if bytes.Count(contents, []byte{'\n'}) != 3 {
		t.Fatalf("helper output after EOF = %q, want 3 events", contents)
	}
	events := decodeEvents(t, contents)
	if len(events) != 3 || !events[0].Indeterminate || events[1].Completed != 1 || !events[2].Done {
		t.Fatalf("helper events = %#v", events)
	}
}

func writeTestExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
