package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureExtractsIntoStagingAndReusesCompleteEntry(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	var extractionCount atomic.Int32
	var validationCount atomic.Int32
	request := testRequest("sha256:payload", func(_ context.Context, stagingDir string) error {
		extractionCount.Add(1)
		if !strings.Contains(filepath.Base(stagingDir), ".staging-") {
			t.Errorf("extraction path %q is not a staging directory", stagingDir)
		}
		if _, err := os.Stat(filepath.Join(stagingDir, CompletionFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("completion marker existed during extraction: %v", err)
		}
		return os.WriteFile(filepath.Join(stagingDir, "runtime.bin"), []byte("payload"), 0o700)
	})
	request.Validate = func(runtimeDir string) error {
		validationCount.Add(1)
		contents, err := os.ReadFile(filepath.Join(runtimeDir, "runtime.bin"))
		if err != nil {
			return err
		}
		if string(contents) != "payload" {
			return errors.New("unexpected runtime contents")
		}
		return nil
	}

	first, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("first Ensure failed: %v", err)
	}
	defer first.Close()
	second, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("second Ensure failed: %v", err)
	}
	defer second.Close()

	if first.Path() != second.Path() {
		t.Fatalf("cache paths differ: first %q, second %q", first.Path(), second.Path())
	}
	if got := extractionCount.Load(); got != 1 {
		t.Fatalf("extraction count = %d, want 1", got)
	}
	if got := validationCount.Load(); got != 3 {
		t.Fatalf("validation count = %d, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(first.Path(), CompletionFile)); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}
	assertPrivateDirectory(t, manager.Root())
	assertPrivateDirectory(t, filepath.Dir(first.Path()))
	assertPrivateDirectory(t, first.Path())
}

func TestRuntimeStateDirectoryIsStableAcrossPayloadVersions(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	first, err := manager.RuntimeStateDirectory("generalsx-zh")
	if err != nil {
		t.Fatalf("first RuntimeStateDirectory: %v", err)
	}
	second, err := manager.RuntimeStateDirectory("generalsx-zh")
	if err != nil {
		t.Fatalf("second RuntimeStateDirectory: %v", err)
	}
	if first != second {
		t.Fatalf("runtime state paths differ: first %q, second %q", first, second)
	}
	want := filepath.Join(manager.Root(), "generalsx-zh", ".runtime-state")
	if first != want {
		t.Fatalf("runtime state path = %q, want %q", first, want)
	}
	assertPrivateDirectory(t, first)
}

func TestEnsureConcurrentCallersPublishOnce(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{LockTimeout: 3 * time.Second})
	var extractionCount atomic.Int32
	request := testRequest("sha256:concurrent-payload", func(_ context.Context, stagingDir string) error {
		extractionCount.Add(1)
		time.Sleep(100 * time.Millisecond)
		return os.WriteFile(filepath.Join(stagingDir, "runtime.bin"), []byte("complete"), 0o600)
	})

	const callers = 16
	start := make(chan struct{})
	results := make(chan *Runtime, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := manager.Ensure(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Ensure failed: %v", err)
	}
	var expectedPath string
	for result := range results {
		if expectedPath == "" {
			expectedPath = result.Path()
		} else if result.Path() != expectedPath {
			t.Errorf("cache path = %q, want %q", result.Path(), expectedPath)
		}
		if err := result.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	}
	if got := extractionCount.Load(); got != 1 {
		t.Fatalf("extraction count = %d, want 1", got)
	}
}

func TestEnsureReplacesInterruptedEntryAndCurrentStaging(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	request := testRequest("sha256:interrupted", func(_ context.Context, stagingDir string) error {
		return os.WriteFile(filepath.Join(stagingDir, "runtime.bin"), []byte("recovered"), 0o600)
	})
	paths, err := manager.prepareEntry(request.Product, request.PayloadDigest, request.ManifestDigest)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if err := os.Mkdir(paths.finalDir, 0o700); err != nil {
		t.Fatalf("create interrupted final directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.finalDir, "partial.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write interrupted final payload: %v", err)
	}
	orphanedStaging := filepath.Join(paths.productDir, "."+paths.key+".staging-orphaned")
	if err := os.Mkdir(orphanedStaging, 0o700); err != nil {
		t.Fatalf("create orphaned staging directory: %v", err)
	}

	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	defer leased.Close()
	if _, err := os.Stat(filepath.Join(leased.Path(), "partial.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial payload survived recovery: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(leased.Path(), "runtime.bin")); err != nil {
		t.Fatalf("read recovered payload: %v", err)
	} else if string(contents) != "recovered" {
		t.Fatalf("recovered payload = %q, want recovered", contents)
	}
	if _, err := os.Stat(orphanedStaging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned staging directory survived recovery: %v", err)
	}
}

func TestEnsureDoesNotDeleteValidEntryWhenValidationIsCanceled(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	request := testRequest("sha256:canceled-validation", writeTestRuntime("payload"))
	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("populate cache entry: %v", err)
	}
	runtimePath := leased.Path()
	if err := leased.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}

	request.Validate = func(string) error {
		return context.Canceled
	}
	if _, err := manager.Ensure(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(runtimePath, "runtime.bin")); err != nil {
		t.Fatalf("canceled validation removed valid cache entry: %v", err)
	}
}

func TestEnsureCleansFailedExtractionWithoutPublishing(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	extractionFailure := errors.New("simulated interrupted extraction")
	request := testRequest("sha256:failed-extraction", func(_ context.Context, stagingDir string) error {
		if err := os.WriteFile(filepath.Join(stagingDir, "partial.bin"), []byte("partial"), 0o600); err != nil {
			return err
		}
		return extractionFailure
	})
	paths, err := manager.prepareEntry(request.Product, request.PayloadDigest, request.ManifestDigest)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}

	if _, err := manager.Ensure(context.Background(), request); !errors.Is(err, extractionFailure) {
		t.Fatalf("Ensure error = %v, want extraction failure", err)
	}
	if _, err := os.Stat(paths.finalDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed extraction published a final entry: %v", err)
	}
	assertNoCurrentStaging(t, paths)
}

func TestLockTimeoutDoesNotLimitExtractionWork(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{LockTimeout: 25 * time.Millisecond})
	request := testRequest("sha256:slow-extraction", func(_ context.Context, stagingDir string) error {
		time.Sleep(100 * time.Millisecond)
		return os.WriteFile(filepath.Join(stagingDir, "runtime.bin"), []byte("complete"), 0o600)
	})
	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("Ensure treated extraction time as lock wait: %v", err)
	}
	if err := leased.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func TestValidationFailureRebuildsEntry(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	var extractionCount atomic.Int32
	request := testRequest("sha256:validated", func(_ context.Context, stagingDir string) error {
		extractionCount.Add(1)
		return os.WriteFile(filepath.Join(stagingDir, "required.bin"), []byte("payload"), 0o600)
	})
	request.Validate = func(runtimeDir string) error {
		_, err := os.Stat(filepath.Join(runtimeDir, "required.bin"))
		return err
	}

	first, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("initial Ensure failed: %v", err)
	}
	runtimePath := first.Path()
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}
	if err := os.Remove(filepath.Join(runtimePath, "required.bin")); err != nil {
		t.Fatalf("damage cached entry: %v", err)
	}
	second, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("recovery Ensure failed: %v", err)
	}
	defer second.Close()
	if got := extractionCount.Load(); got != 2 {
		t.Fatalf("extraction count = %d, want 2", got)
	}
}

func TestRuntimeLeaseBlocksPurgeUntilClose(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{LockTimeout: 100 * time.Millisecond})
	request := testRequest("sha256:leased", writeTestRuntime("leased"))
	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	err = manager.PurgeCurrent(
		context.Background(),
		request.Product,
		request.PayloadDigest,
		request.ManifestDigest,
	)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("PurgeCurrent error = %v, want ErrLockTimeout", err)
	}
	if _, err := os.Stat(filepath.Join(leased.Path(), "runtime.bin")); err != nil {
		t.Fatalf("timed-out purge touched leased runtime: %v", err)
	}
	if err := leased.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if err := manager.PurgeCurrent(
		context.Background(),
		request.Product,
		request.PayloadDigest,
		request.ManifestDigest,
	); err != nil {
		t.Fatalf("PurgeCurrent after Close: %v", err)
	}
	if _, err := os.Stat(leased.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged entry still exists: %v", err)
	}
}

func TestGuardAndLeaseFilesRemainStableAcrossPurge(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	request := testRequest("sha256:permanent-lock-files", writeTestRuntime("payload"))
	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := leased.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	paths, err := manager.prepareEntry(request.Product, request.PayloadDigest, request.ManifestDigest)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	guardBefore := requireRegularFile(t, paths.guardPath)
	leaseBefore := requireRegularFile(t, paths.leasePath)

	if err := manager.PurgeCurrent(
		context.Background(),
		request.Product,
		request.PayloadDigest,
		request.ManifestDigest,
	); err != nil {
		t.Fatalf("PurgeCurrent: %v", err)
	}
	guardAfter := requireRegularFile(t, paths.guardPath)
	leaseAfter := requireRegularFile(t, paths.leasePath)
	if !os.SameFile(guardBefore, guardAfter) {
		t.Fatal("PurgeCurrent replaced the permanent guard file")
	}
	if !os.SameFile(leaseBefore, leaseAfter) {
		t.Fatal("PurgeCurrent replaced the permanent lease file")
	}
}

func TestEnsureRejectsNonRegularLockFiles(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	request := testRequest("sha256:bad-lock-file", writeTestRuntime("payload"))
	paths, err := manager.prepareEntry(request.Product, request.PayloadDigest, request.ManifestDigest)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if err := os.Mkdir(paths.guardPath, 0o700); err != nil {
		t.Fatalf("create directory at guard path: %v", err)
	}
	if _, err := manager.Ensure(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Ensure error = %v, want non-regular guard rejection", err)
	}
}

func TestEnsureRejectsSymlinkLockFile(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	request := testRequest("sha256:symlink-lock-file", writeTestRuntime("payload"))
	paths, err := manager.prepareEntry(request.Product, request.PayloadDigest, request.ManifestDigest)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	if err := os.Symlink(target, paths.guardPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires extra Windows privileges: %v", err)
		}
		t.Fatalf("create guard symlink: %v", err)
	}
	if _, err := manager.Ensure(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Ensure error = %v, want symlink guard rejection", err)
	}
	if contents, err := os.ReadFile(target); err != nil {
		t.Fatalf("read symlink target: %v", err)
	} else if string(contents) != "unchanged" {
		t.Fatalf("symlink target changed to %q", contents)
	}
}

func TestCacheLockRecoversAfterProcessCrash(t *testing.T) {
	if os.Getenv("GENERALSX_SFX_CACHE_CRASH_HELPER") == "1" {
		runCacheCrashHelper()
		return
	}

	root := filepath.Join(t.TempDir(), "cache")
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestCacheLockRecoversAfterProcessCrash$",
		"-test.count=1",
	)
	command.Env = append(
		os.Environ(),
		"GENERALSX_SFX_CACHE_CRASH_HELPER=1",
		"GENERALSX_SFX_CACHE_CRASH_ROOT="+root,
		"GENERALSX_SFX_CACHE_CRASH_READY="+ready,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start crash helper: %v", err)
	}
	waitForFile(t, ready, command, &output)
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill crash helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("crash helper unexpectedly exited successfully")
	}

	manager, err := New(Options{Root: root, LockTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New after crash: %v", err)
	}
	request := testRequest("sha256:crash-recovery", writeTestRuntime("parent"))
	leased, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatalf("Ensure after crash: %v\nhelper output:\n%s", err, output.String())
	}
	defer leased.Close()
	contents, err := os.ReadFile(filepath.Join(leased.Path(), "runtime.bin"))
	if err != nil {
		t.Fatalf("read recovered runtime: %v", err)
	}
	if string(contents) != "parent" {
		t.Fatalf("recovered runtime = %q, want parent", contents)
	}
}

func TestPurgeCurrentHasExactScope(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	firstRequest := testRequest("sha256:first", writeTestRuntime("first"))
	secondRequest := testRequest("sha256:second", writeTestRuntime("second"))
	first, err := manager.Ensure(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("populate first entry: %v", err)
	}
	firstPath := first.Path()
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}
	second, err := manager.Ensure(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("populate second entry: %v", err)
	}
	secondPath := second.Path()
	if err := second.Close(); err != nil {
		t.Fatalf("close second runtime: %v", err)
	}
	stateDir, err := manager.RuntimeStateDirectory(firstRequest.Product)
	if err != nil {
		t.Fatalf("prepare runtime state: %v", err)
	}
	stateFile := filepath.Join(stateDir, "id.bin")
	if err := os.WriteFile(stateFile, []byte("stable identity"), 0o600); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}

	if err := manager.PurgeCurrent(
		context.Background(),
		firstRequest.Product,
		firstRequest.PayloadDigest,
		firstRequest.ManifestDigest,
	); err != nil {
		t.Fatalf("PurgeCurrent failed: %v", err)
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged entry still exists: %v", err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("unrelated entry was removed: %v", err)
	}
	if contents, err := os.ReadFile(stateFile); err != nil {
		t.Fatalf("runtime state was removed: %v", err)
	} else if string(contents) != "stable identity" {
		t.Fatalf("runtime state contents = %q", contents)
	}
}

func TestEnsureRejectsUnsafeProduct(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{})
	for _, product := range []string{"", ".", "..", "../escape", "nested/product", ".locks", "white space"} {
		product := product
		t.Run(product, func(t *testing.T) {
			request := testRequest("sha256:payload", func(context.Context, string) error { return nil })
			request.Product = product
			if _, err := manager.Ensure(context.Background(), request); err == nil {
				t.Fatalf("Ensure accepted unsafe product %q", product)
			}
		})
	}
}

func TestNewDoesNotChangeExistingCacheRootPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix directory permissions")
	}

	root := filepath.Join(t.TempDir(), "shared-cache")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := New(Options{Root: root}); err == nil {
		t.Fatal("New accepted an existing group/world-accessible cache root")
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing cache root permissions changed to %04o", got)
	}
}

func TestNewRejectsCacheBelowUnsafeWritableAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cache overrides are rejected by the launcher")
	}

	unsafeParent := t.TempDir()
	if err := os.Chmod(unsafeParent, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(unsafeParent, 0o700)
	})

	if _, err := New(Options{Root: filepath.Join(unsafeParent, "private-cache")}); err == nil {
		t.Fatal("New accepted a cache below a non-sticky group/world-writable ancestor")
	}
}

func runCacheCrashHelper() {
	root := os.Getenv("GENERALSX_SFX_CACHE_CRASH_ROOT")
	ready := os.Getenv("GENERALSX_SFX_CACHE_CRASH_READY")
	manager, err := New(Options{Root: root, LockTimeout: 5 * time.Second})
	if err != nil {
		panic(err)
	}
	request := testRequest("sha256:crash-recovery", func(_ context.Context, stagingDir string) error {
		if err := os.WriteFile(filepath.Join(stagingDir, "partial.bin"), []byte("child"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return err
		}
		for {
			time.Sleep(time.Hour)
		}
	})
	_, err = manager.Ensure(context.Background(), request)
	if err != nil {
		panic(err)
	}
	panic("crash helper unexpectedly returned")
}

func waitForFile(t *testing.T, path string, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if command.ProcessState != nil {
			t.Fatalf("crash helper exited before ready:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	t.Fatalf("crash helper did not become ready:\n%s", output.String())
}

func testRequest(payload string, extract ExtractFunc) Request {
	return Request{
		Product:        "generalsx-zh",
		PayloadDigest:  payload,
		ManifestDigest: "sha256:manifest",
		Extract:        extract,
	}
}

func writeTestRuntime(contents string) ExtractFunc {
	return func(_ context.Context, stagingDir string) error {
		return os.WriteFile(filepath.Join(stagingDir, "runtime.bin"), []byte(contents), 0o600)
	}
}

func newTestManager(t *testing.T, options Options) *Manager {
	t.Helper()
	options.Root = filepath.Join(t.TempDir(), "cache")
	manager, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager
}

func requireRegularFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%q is not a regular file", path)
	}
	return info
}

func assertNoCurrentStaging(t *testing.T, paths entryPaths) {
	t.Helper()
	entries, err := os.ReadDir(paths.productDir)
	if err != nil {
		t.Fatalf("read product cache: %v", err)
	}
	stagingPrefix := "." + paths.key + ".staging-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			t.Fatalf("failed extraction left staging path %q", entry.Name())
		}
	}
}

func assertPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("permissions for %q = %04o, want 0700", path, got)
	}
}
