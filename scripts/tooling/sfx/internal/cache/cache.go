// Package cache publishes extracted self-extracting payloads into a
// content-addressed, per-user cache.
//
// GeneralsX @feature OpenAI 30/07/2026 Add atomic, concurrency-safe runtime extraction caching.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// CompletionFile is the marker written after extraction and validation have
	// completed. Callers must treat it as cache-manager metadata.
	CompletionFile = ".complete.json"

	defaultLockTimeout  = 5 * time.Minute
	defaultPollInterval = 25 * time.Millisecond
	maxDigestLength     = 4096
	maxMarkerSize       = 64 * 1024
	markerVersion       = 1
)

var (
	// ErrLockTimeout indicates that another process held the cache-entry guard
	// for the configured maximum wait.
	ErrLockTimeout = errors.New("cache lock wait timed out")
)

// Options configures a Manager. A zero timeout selects a conservative default.
type Options struct {
	// Root overrides the cache root. When empty, New uses
	// os.UserCacheDir()/GeneralsX/sfx.
	Root string

	// LockTimeout bounds how long Ensure or PurgeCurrent waits for another
	// process operating on the same cache key.
	LockTimeout time.Duration
}

// ExtractFunc extracts a payload into stagingDir. stagingDir is always a new,
// private directory next to the final cache entry. The callback must return
// only after all writes have completed.
type ExtractFunc func(ctx context.Context, stagingDir string) error

// ValidateFunc validates an extracted runtime. It runs before publication and
// on every cache hit. It must not modify runtimeDir.
type ValidateFunc func(runtimeDir string) error

// Request identifies one content-addressed runtime and supplies its extraction
// behavior.
type Request struct {
	Product        string
	PayloadDigest  string
	ManifestDigest string
	Extract        ExtractFunc
	Validate       ValidateFunc
}

// Manager owns a single cache root.
type Manager struct {
	root         string
	lockTimeout  time.Duration
	pollInterval time.Duration
}

// Runtime is a validated cache entry protected by a shared runtime lease.
//
// Path remains protected from PurgeCurrent until Close returns. Callers must
// keep the Runtime open for the complete period in which native code can read
// files below Path.
type Runtime struct {
	path     string
	lease    *fileLease
	close    sync.Once
	closeErr error
}

type completionMarker struct {
	Version        int       `json:"version"`
	Product        string    `json:"product"`
	PayloadDigest  string    `json:"payload_digest"`
	ManifestDigest string    `json:"manifest_digest"`
	CompletedAt    time.Time `json:"completed_at"`
}

type entryPaths struct {
	productDir string
	guardPath  string
	leasePath  string
	finalDir   string
	key        string
}

type guardMode uint8

const (
	guardShared guardMode = iota
	guardExclusive
)

type fileLease struct {
	file     *os.File
	state    platformLockState
	close    sync.Once
	closeErr error
}

type processKeyGate struct {
	token chan struct{}
	refs  int
}

var processKeyGates = struct {
	sync.Mutex
	entries map[string]*processKeyGate
}{
	entries: make(map[string]*processKeyGate),
}

// New creates a cache manager. It creates only the manager's exact cache root
// and applies private directory permissions where the host supports them.
func New(options Options) (*Manager, error) {
	if options.LockTimeout < 0 {
		return nil, errors.New("cache lock timeout cannot be negative")
	}

	root := options.Root
	if root == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user cache directory: %w", err)
		}
		root = filepath.Join(userCacheDir, "GeneralsX", "sfx")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve cache root %q: %w", root, err)
	}

	lockTimeout := options.LockTimeout
	if lockTimeout == 0 {
		lockTimeout = defaultLockTimeout
	}
	manager := &Manager{
		root:         filepath.Clean(absoluteRoot),
		lockTimeout:  lockTimeout,
		pollInterval: defaultPollInterval,
	}
	if err := preparePrivateCacheRoot(manager.root); err != nil {
		return nil, fmt.Errorf("prepare cache root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(manager.root)
	if err != nil {
		return nil, fmt.Errorf("resolve cache root after creation: %w", err)
	}
	manager.root = filepath.Clean(resolvedRoot)
	if err := requirePrivateCacheRoot(manager.root); err != nil {
		return nil, fmt.Errorf("validate resolved cache root: %w", err)
	}
	return manager, nil
}

// Root returns the absolute cache root managed by this Manager.
func (manager *Manager) Root() string {
	return manager.root
}

// Path returns the protected runtime directory. It is valid only until Close.
func (leased *Runtime) Path() string {
	if leased == nil {
		return ""
	}
	return leased.path
}

// Close releases the runtime's shared cache lease. It is safe to call more
// than once.
func (leased *Runtime) Close() error {
	if leased == nil {
		return nil
	}
	leased.close.Do(func() {
		if leased.lease != nil {
			leased.closeErr = leased.lease.Close()
		}
	})
	return leased.closeErr
}

// RuntimeStateDirectory returns a private, product-stable directory for
// writable native runtime state. Unlike extracted payload directories, this
// path deliberately does not include the content digest, so legacy relative
// files such as GameSpy identity data survive SFX upgrades.
func (manager *Manager) RuntimeStateDirectory(product string) (string, error) {
	if err := validateProduct(product); err != nil {
		return "", err
	}
	if err := requirePrivateCacheRoot(manager.root); err != nil {
		return "", fmt.Errorf("prepare runtime state root: %w", err)
	}

	productDir := filepath.Join(manager.root, product)
	if err := ensurePrivateDirectory(productDir); err != nil {
		return "", fmt.Errorf("prepare product cache directory: %w", err)
	}
	stateDir := filepath.Join(productDir, ".runtime-state")
	if err := ensurePrivateDirectory(stateDir); err != nil {
		return "", fmt.Errorf("prepare product runtime state directory: %w", err)
	}
	return stateDir, nil
}

// Ensure returns a validated runtime protected by a shared runtime lease. A
// cache miss is extracted under an exclusive acquisition guard and runtime
// lease into a private staging directory, marked complete, and atomically
// renamed into place.
func (manager *Manager) Ensure(ctx context.Context, request Request) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("cache Ensure requires a non-nil context")
	}
	if request.Extract == nil {
		return nil, errors.New("cache request requires an extraction callback")
	}

	paths, err := manager.prepareEntry(
		request.Product,
		request.PayloadDigest,
		request.ManifestDigest,
	)
	if err != nil {
		return nil, err
	}

	releaseProcessGate, err := acquireProcessKeyGate(
		ctx,
		manager.root+"\x00"+paths.key,
		time.Now().Add(manager.lockTimeout),
		paths.key,
	)
	if err != nil {
		return nil, err
	}
	processGateHeld := true
	defer func() {
		if processGateHeld {
			releaseProcessGate()
		}
	}()

	guard, err := manager.acquireFileLease(
		ctx,
		paths.guardPath,
		paths.key,
		guardExclusive,
		time.Now().Add(manager.lockTimeout),
	)
	if err != nil {
		return nil, err
	}
	guardHeld := true
	defer func() {
		if guardHeld {
			_ = guard.Close()
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("ensure cache entry: %w", err)
		}

		shared, err := manager.acquireFileLease(
			ctx,
			paths.leasePath,
			paths.key,
			guardShared,
			time.Now().Add(manager.lockTimeout),
		)
		if err != nil {
			return nil, err
		}
		hit, inspectErr := cacheHit(paths.finalDir, request)
		if inspectErr != nil {
			return nil, errors.Join(
				fmt.Errorf("inspect cache entry: %w", inspectErr),
				shared.Close(),
			)
		}
		if hit {
			if err := guard.Close(); err != nil {
				return nil, errors.Join(
					fmt.Errorf("release cache acquisition guard: %w", err),
					shared.Close(),
				)
			}
			guardHeld = false
			releaseProcessGate()
			processGateHeld = false
			return &Runtime{
				path:  paths.finalDir,
				lease: shared,
			}, nil
		}
		if err := shared.Close(); err != nil {
			return nil, fmt.Errorf("release shared cache lease: %w", err)
		}

		exclusive, err := manager.acquireFileLease(
			ctx,
			paths.leasePath,
			paths.key,
			guardExclusive,
			time.Now().Add(manager.lockTimeout),
		)
		if err != nil {
			return nil, err
		}

		// Existing Runtime readers may have kept the lease shared while this
		// caller waited. Revalidate after obtaining exclusivity.
		hit, inspectErr = cacheHit(paths.finalDir, request)
		if inspectErr != nil {
			return nil, errors.Join(
				fmt.Errorf("reinspect cache entry: %w", inspectErr),
				exclusive.Close(),
			)
		}
		if !hit {
			if err := manager.populateCacheEntry(ctx, paths, request); err != nil {
				return nil, errors.Join(err, exclusive.Close())
			}
		}
		if err := exclusive.Close(); err != nil {
			return nil, fmt.Errorf("release exclusive cache lease: %w", err)
		}

		// Reacquire and validate the shared runtime lease before releasing the
		// acquisition guard. No purge or new reader can win this handoff.
	}
}

func (manager *Manager) populateCacheEntry(
	ctx context.Context,
	paths entryPaths,
	request Request,
) error {
	if err := removeExactChild(paths.productDir, paths.finalDir); err != nil {
		return fmt.Errorf("remove incomplete cache entry: %w", err)
	}
	if err := cleanupCurrentStaging(paths); err != nil {
		return fmt.Errorf("clean abandoned staging directory: %w", err)
	}

	stagingDir, err := os.MkdirTemp(paths.productDir, "."+paths.key+".staging-")
	if err != nil {
		return fmt.Errorf("create extraction staging directory: %w", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = removeExactChild(paths.productDir, stagingDir)
		return fmt.Errorf("make extraction staging directory private: %w", err)
	}

	published := false
	defer func() {
		if !published {
			_ = removeExactChild(paths.productDir, stagingDir)
		}
	}()

	if err := request.Extract(ctx, stagingDir); err != nil {
		return fmt.Errorf("extract cache payload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("extract cache payload: %w", err)
	}
	if err := requireRealDirectory(stagingDir); err != nil {
		return fmt.Errorf("validate extraction staging directory: %w", err)
	}

	// The completion marker belongs exclusively to the cache manager. Remove a
	// same-named payload entry before validation so our marker is written last.
	if err := removeCompletionPath(stagingDir); err != nil {
		return err
	}
	if request.Validate != nil {
		if err := request.Validate(stagingDir); err != nil {
			return fmt.Errorf("validate extracted cache payload: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate extracted cache payload: %w", err)
	}

	marker := completionMarker{
		Version:        markerVersion,
		Product:        request.Product,
		PayloadDigest:  request.PayloadDigest,
		ManifestDigest: request.ManifestDigest,
		CompletedAt:    time.Now().UTC(),
	}
	if err := writeCompletionMarker(stagingDir, marker); err != nil {
		return err
	}

	if _, err := os.Lstat(paths.finalDir); err == nil {
		return errors.New("a cache entry appeared while the exclusive guard was held")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect final cache path before publication: %w", err)
	}
	if err := os.Rename(stagingDir, paths.finalDir); err != nil {
		return fmt.Errorf("publish extracted cache entry: %w", err)
	}
	published = true
	bestEffortSyncDirectory(paths.productDir)
	return nil
}

// PurgeCurrent removes only the entry and abandoned staging directories for
// the exact product and digest pair supplied. Its exclusive runtime lease
// waits for every shared Runtime lease on that entry to close.
func (manager *Manager) PurgeCurrent(
	ctx context.Context,
	product string,
	payloadDigest string,
	manifestDigest string,
) (resultErr error) {
	if ctx == nil {
		return errors.New("cache PurgeCurrent requires a non-nil context")
	}

	paths, err := manager.prepareEntry(product, payloadDigest, manifestDigest)
	if err != nil {
		return err
	}
	releaseProcessGate, err := acquireProcessKeyGate(
		ctx,
		manager.root+"\x00"+paths.key,
		time.Now().Add(manager.lockTimeout),
		paths.key,
	)
	if err != nil {
		return err
	}
	defer releaseProcessGate()

	guard, err := manager.acquireFileLease(
		ctx,
		paths.guardPath,
		paths.key,
		guardExclusive,
		time.Now().Add(manager.lockTimeout),
	)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, guard.Close())
	}()

	exclusive, err := manager.acquireFileLease(
		ctx,
		paths.leasePath,
		paths.key,
		guardExclusive,
		time.Now().Add(manager.lockTimeout),
	)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, exclusive.Close())
	}()

	if err := removeExactChild(paths.productDir, paths.finalDir); err != nil {
		return fmt.Errorf("purge current cache entry: %w", err)
	}
	if err := cleanupCurrentStaging(paths); err != nil {
		return fmt.Errorf("purge current cache staging directories: %w", err)
	}
	bestEffortSyncDirectory(paths.productDir)
	return nil
}

func (manager *Manager) prepareEntry(
	product string,
	payloadDigest string,
	manifestDigest string,
) (entryPaths, error) {
	if err := validateProduct(product); err != nil {
		return entryPaths{}, err
	}
	if err := validateDigest("payload", payloadDigest); err != nil {
		return entryPaths{}, err
	}
	if err := validateDigest("manifest", manifestDigest); err != nil {
		return entryPaths{}, err
	}
	if err := requirePrivateCacheRoot(manager.root); err != nil {
		return entryPaths{}, fmt.Errorf("prepare cache root: %w", err)
	}

	productDir := filepath.Join(manager.root, product)
	if err := ensurePrivateDirectory(productDir); err != nil {
		return entryPaths{}, fmt.Errorf("prepare product cache directory: %w", err)
	}
	lockParent := filepath.Join(productDir, ".locks")
	if err := ensurePrivateDirectory(lockParent); err != nil {
		return entryPaths{}, fmt.Errorf("prepare cache lock directory: %w", err)
	}

	key := contentKey(payloadDigest, manifestDigest)
	return entryPaths{
		productDir: productDir,
		guardPath:  filepath.Join(lockParent, key+".guard"),
		leasePath:  filepath.Join(lockParent, key+".lease"),
		finalDir:   filepath.Join(productDir, key),
		key:        key,
	}, nil
}

func (manager *Manager) acquireFileLease(
	ctx context.Context,
	path string,
	key string,
	mode guardMode,
	waitDeadline time.Time,
) (*fileLease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("wait for cache lock: %w", err)
		}

		lease, retry, err := tryAcquireFileLease(path, mode)
		if err != nil {
			return nil, fmt.Errorf("acquire cache lock %q: %w", path, err)
		}
		if lease != nil {
			return lease, nil
		}
		if !retry {
			return nil, fmt.Errorf("acquire cache lock %q: unavailable", path)
		}

		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w for %s", ErrLockTimeout, key)
		}
		wait := manager.pollInterval
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("wait for cache lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func tryAcquireFileLease(path string, mode guardMode) (*fileLease, bool, error) {
	// Reject an existing symlink or other special object before opening. The
	// post-lock identity check below closes the race between this check and
	// OpenFile.
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("cache lock is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	lease := &fileLease{file: file}
	locked, err := tryPlatformLock(file, mode, &lease.state)
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !locked {
		_ = file.Close()
		return nil, true, nil
	}

	fileInfo, fileErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if fileErr != nil || pathErr != nil {
		closeErr := lease.Close()
		if fileErr != nil {
			return nil, errors.Is(pathErr, os.ErrNotExist), errors.Join(fileErr, closeErr)
		}
		if errors.Is(pathErr, os.ErrNotExist) {
			return nil, true, closeErr
		}
		return nil, false, errors.Join(pathErr, closeErr)
	}
	if !fileInfo.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.Join(
			errors.New("cache lock is not a regular file"),
			lease.Close(),
		)
	}
	if !os.SameFile(fileInfo, pathInfo) {
		return nil, true, lease.Close()
	}
	return lease, false, nil
}

// Close releases a guard and closes the underlying descriptor. Close is
// idempotent; closing the descriptor remains the final fail-safe release even
// if the explicit platform unlock reports an error.
func (lease *fileLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.close.Do(func() {
		unlockErr := unlockPlatformLock(lease.file, &lease.state)
		closeErr := lease.file.Close()
		lease.closeErr = errors.Join(unlockErr, closeErr)
	})
	return lease.closeErr
}

func acquireProcessKeyGate(
	ctx context.Context,
	key string,
	deadline time.Time,
	contentKey string,
) (func(), error) {
	processKeyGates.Lock()
	gate := processKeyGates.entries[key]
	if gate == nil {
		gate = &processKeyGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		processKeyGates.entries[key] = gate
	}
	gate.refs++
	processKeyGates.Unlock()

	dropReference := func() {
		processKeyGates.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(processKeyGates.entries, key)
		}
		processKeyGates.Unlock()
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		dropReference()
		return nil, fmt.Errorf("%w for %s", ErrLockTimeout, contentKey)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		dropReference()
		return nil, fmt.Errorf("wait for in-process cache lock: %w", ctx.Err())
	case <-timer.C:
		dropReference()
		return nil, fmt.Errorf("%w for %s", ErrLockTimeout, contentKey)
	case <-gate.token:
	}

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			gate.token <- struct{}{}
			dropReference()
		})
	}, nil
}

func contentKey(payloadDigest, manifestDigest string) string {
	digest := sha256.New()
	_, _ = io.WriteString(digest, "generalsx-sfx-cache-v2\x00")
	_, _ = io.WriteString(digest, payloadDigest)
	_, _ = io.WriteString(digest, "\x00")
	_, _ = io.WriteString(digest, manifestDigest)
	return hex.EncodeToString(digest.Sum(nil))
}

func validateProduct(product string) error {
	if product == "" {
		return errors.New("cache product cannot be empty")
	}
	if len(product) > 128 {
		return errors.New("cache product is too long")
	}
	if product == "." || product == ".." || strings.HasPrefix(product, ".") {
		return fmt.Errorf("cache product %q is not a safe path component", product)
	}
	for _, character := range product {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("cache product %q is not a safe path component", product)
	}
	return nil
}

func validateDigest(kind, digest string) error {
	if digest == "" {
		return fmt.Errorf("cache %s digest cannot be empty", kind)
	}
	if len(digest) > maxDigestLength {
		return fmt.Errorf("cache %s digest is too long", kind)
	}
	if !utf8.ValidString(digest) {
		return fmt.Errorf("cache %s digest is not valid UTF-8", kind)
	}
	for _, character := range digest {
		if unicode.IsControl(character) {
			return fmt.Errorf("cache %s digest contains a control character", kind)
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", path)
	}
	return os.Chmod(path, 0o700)
}

// GeneralsX @bugfix moloch 30/07/2026 Never chmod an existing user-selected cache root.
func preparePrivateCacheRoot(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(path, 0o700); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", path)
	}
	return nil
}

func requirePrivateCacheRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"%q has permissions %04o; use a dedicated cache directory accessible only by its owner",
			path,
			info.Mode().Perm(),
		)
	}
	return validateCacheRootPlatform(path, info)
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", path)
	}
	return nil
}

func cacheHit(finalDir string, request Request) (bool, error) {
	if err := requireRealDirectory(finalDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		// A non-directory or symlink is an incomplete entry that can be
		// replaced after taking the exclusive cache guard.
		info, statErr := os.Lstat(finalDir)
		if statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return false, nil
		}
		return false, err
	}

	marker, ok, err := readCompletionMarker(filepath.Join(finalDir, CompletionFile))
	if err != nil {
		return false, err
	}
	if !ok ||
		marker.Version != markerVersion ||
		marker.Product != request.Product ||
		marker.PayloadDigest != request.PayloadDigest ||
		marker.ManifestDigest != request.ManifestDigest {
		return false, nil
	}
	if request.Validate != nil {
		if err := request.Validate(finalDir); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return false, err
			}
			return false, nil
		}
	}
	return true, nil
}

func readCompletionMarker(path string) (completionMarker, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return completionMarker{}, false, nil
	}
	if err != nil {
		return completionMarker{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return completionMarker{}, false, nil
	}
	if info.Size() > maxMarkerSize {
		return completionMarker{}, false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return completionMarker{}, false, err
	}
	defer file.Close()

	limited := io.LimitReader(file, maxMarkerSize+1)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var marker completionMarker
	if err := decoder.Decode(&marker); err != nil {
		return completionMarker{}, false, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return completionMarker{}, false, nil
	}
	return marker, true, nil
}

func removeCompletionPath(stagingDir string) error {
	markerPath := filepath.Join(stagingDir, CompletionFile)
	if err := removeExactChild(stagingDir, markerPath); err != nil {
		return fmt.Errorf("remove payload-provided completion marker: %w", err)
	}
	return nil
}

func writeCompletionMarker(stagingDir string, marker completionMarker) error {
	markerPath := filepath.Join(stagingDir, CompletionFile)
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create cache completion marker: %w", err)
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(marker); err != nil {
		_ = file.Close()
		return fmt.Errorf("write cache completion marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync cache completion marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close cache completion marker: %w", err)
	}
	bestEffortSyncDirectory(stagingDir)
	return nil
}

func cleanupCurrentStaging(paths entryPaths) error {
	entries, err := os.ReadDir(paths.productDir)
	if err != nil {
		return err
	}
	prefix := "." + paths.key + ".staging-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(paths.productDir, entry.Name())
		if err := removeExactChild(paths.productDir, path); err != nil {
			return err
		}
	}
	return nil
}

func removeExactChild(parent, child string) error {
	cleanParent := filepath.Clean(parent)
	cleanChild := filepath.Clean(child)
	if filepath.Dir(cleanChild) != cleanParent {
		return fmt.Errorf("refuse to remove path outside exact cache scope: %q", child)
	}
	err := os.RemoveAll(cleanChild)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func bestEffortSyncDirectory(path string) {
	directory, err := os.Open(path)
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}
