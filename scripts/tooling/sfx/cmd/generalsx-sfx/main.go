// GeneralsX @feature moloch 30/07/2026 Run an embedded native game from a verified extraction cache.
package main

//go:generate ../../generate-windows-icon-resources.sh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/bundle"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/cache"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/launch"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/notices"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/payload"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/progress"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/signalctx"
	"github.com/ulikunitz/xz"
)

const cacheEnvironmentVariable = "GX_SFX_CACHE"

const (
	// A schema-v1 tar member can require a PAX header containing both a path
	// and a link target, followed by its normal header and block padding.
	// Three maximum-length paths plus eight tar blocks leaves conservative
	// slack without permitting an authenticated compressed-data bomb to be
	// drained without bound after the tar end marker.
	tarBlockSize           = int64(512)
	tarOverheadBlocks      = int64(8)
	tarPathFieldsPerEntry  = int64(3)
	tarEndMarkerBlockCount = int64(2)
)

type actionKind uint8

const (
	actionLaunch actionKind = iota
	actionServer
	actionHelp
	actionInfo
	actionVerify
	actionExtract
	actionPurge
	actionNotices
)

type action struct {
	kind       actionKind
	gameArgs   []string
	serverArgs []string
	path       string
}

type embeddedBundle struct {
	files          fs.FS
	manifest       bundle.Manifest
	manifestBytes  []byte
	manifestDigest string
}

// GeneralsX @feature Codex 01/08/2026 Keep first-launch progress presentation optional and testable.
type extractionProgressReporter interface {
	Indeterminate(label string)
	Update(label string, completed, total int64)
	Complete()
	Close()
}

type extractionProgressFactory func() extractionProgressReporter

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	request, err := parseAction(arguments)
	if err != nil {
		fmt.Fprintf(stderr, "GeneralsX SFX: %v\n\n", err)
		writeHelp(stderr)
		return 2
	}

	switch request.kind {
	case actionHelp:
		writeHelp(stdout)
		return 0
	case actionNotices:
		fmt.Fprint(stdout, notices.Text)
		if !strings.HasSuffix(notices.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		return 0
	}

	embedded, err := loadEmbeddedBundle(payload.Files)
	if err != nil {
		fmt.Fprintf(stderr, "GeneralsX SFX: %v\n", err)
		return 1
	}
	if err := validateHost(embedded.manifest); err != nil {
		fmt.Fprintf(stderr, "GeneralsX SFX: %v\n", err)
		return 1
	}
	if request.kind == actionServer && embedded.manifest.OnlineServerEntrypoint == "" {
		fmt.Fprintln(stderr, "GeneralsX SFX: this bundle does not include an Online server")
		return 1
	}

	ctx, stop := signalctx.NotifyContext(context.Background())
	defer stop()

	switch request.kind {
	case actionInfo:
		cacheRoot, err := displayCacheRoot(
			runtime.GOOS,
			os.Getenv(cacheEnvironmentVariable),
		)
		if err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: inspect extraction cache: %v\n", err)
			return 1
		}
		writeInfo(stdout, embedded, cacheRoot)
		return 0
	case actionPurge:
		manager, err := newCacheManager(runtime.GOOS, os.Getenv(cacheEnvironmentVariable))
		if err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: initialize extraction cache: %v\n", err)
			return 1
		}
		if err := manager.PurgeCurrent(
			ctx,
			embedded.manifest.Product,
			embedded.manifest.PayloadSHA256,
			embedded.manifestDigest,
		); err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: purge cached runtime: %v\n", err)
			return contextFailureExitCode(ctx, 1)
		}
		fmt.Fprintln(stdout, "GeneralsX SFX: cached runtime removed.")
		return 0
	case actionVerify:
		if err := verifyEmbeddedPayload(ctx, embedded); err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: verification failed: %v\n", err)
			return contextFailureExitCode(ctx, 1)
		}
		fmt.Fprintln(stdout, "GeneralsX SFX: embedded payload and every extracted file verified.")
		return 0
	case actionExtract:
		if err := extractToRequestedPath(ctx, embedded, request.path); err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: extract payload: %v\n", err)
			return contextFailureExitCode(ctx, 1)
		}
		absolute, _ := filepath.Abs(request.path)
		fmt.Fprintf(stdout, "GeneralsX SFX: payload extracted to %s\n", absolute)
		return 0
	}

	manager, err := newCacheManager(runtime.GOOS, os.Getenv(cacheEnvironmentVariable))
	if err != nil {
		fmt.Fprintf(stderr, "GeneralsX SFX: initialize extraction cache: %v\n", err)
		return 1
	}
	leasedRuntime, err := ensureCachedRuntime(
		ctx,
		manager,
		embedded,
		stderr,
		func() extractionProgressReporter { return progress.Open() },
	)
	if err != nil {
		fmt.Fprintf(stderr, "GeneralsX SFX: prepare native runtime: %v\n", err)
		return contextFailureExitCode(ctx, 1)
	}
	defer leasedRuntime.Close()
	runtimeRoot := leasedRuntime.Path()

	childLabel := "game"
	var command *exec.Cmd
	if request.kind == actionServer {
		childLabel = "Online server"
		command, err = prepareOnlineServerCommand(
			ctx,
			manager,
			runtimeRoot,
			embedded.manifest,
			request.serverArgs,
		)
		if err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: prepare Online server process: %v\n", err)
			return 1
		}
	} else {
		runtimeStateDir, stateErr := manager.RuntimeStateDirectory(embedded.manifest.Product)
		if stateErr != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: prepare writable runtime state: %v\n", stateErr)
			return 1
		}
		command, err = launch.Prepare(launch.Config{
			Root:            runtimeRoot,
			RuntimeStateDir: runtimeStateDir,
			TargetOS:        embedded.manifest.TargetOS,
			Entrypoint:      embedded.manifest.Entrypoint,
			WorkDir:         embedded.manifest.WorkDir,
			Args:            request.gameArgs,
			Context:         ctx,
		})
		if err != nil {
			fmt.Fprintf(stderr, "GeneralsX SFX: prepare game process: %v\n", err)
			return 1
		}
	}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return contextFailureExitCode(ctx, 1)
		}
		if errors.Is(err, os.ErrPermission) {
			fmt.Fprintf(stderr,
				"GeneralsX SFX: the cache filesystem may prohibit executable files; set %s to a local executable filesystem\n",
				cacheEnvironmentVariable,
			)
		}
		var childExit *exec.ExitError
		if errors.As(err, &childExit) {
			return launch.ExitCode(err)
		}
		fmt.Fprintf(stderr, "GeneralsX SFX: start %s: %v\n", childLabel, err)
		return 1
	}
	return 0
}

// GeneralsX @feature Codex 04/08/2026 Launch the optional Online backend as a separate verified process with private persistent state.
func prepareOnlineServerCommand(
	ctx context.Context,
	manager *cache.Manager,
	runtimeRoot string,
	manifest bundle.Manifest,
	requestedArguments []string,
) (*exec.Cmd, error) {
	if manifest.OnlineServerEntrypoint == "" {
		return nil, errors.New("bundle does not declare an Online server entrypoint")
	}
	stateDir, err := manager.OnlineServerStateDirectory(manifest.Product)
	if err != nil {
		return nil, err
	}
	arguments := onlineServerArguments(requestedArguments)
	return launch.PrepareSidecar(launch.SidecarConfig{
		Root:            runtimeRoot,
		RuntimeStateDir: stateDir,
		TargetOS:        manifest.TargetOS,
		Entrypoint:      manifest.OnlineServerEntrypoint,
		Args:            arguments,
		Context:         ctx,
	})
}

// GeneralsX @bugfix Codex 04/08/2026 Preserve private defaults independently when operators customize the embedded Online server.
func onlineServerArguments(requested []string) []string {
	defaults := []struct {
		name  string
		value string
	}{
		{name: "--control-listen", value: "127.0.0.1:29900"},
		{name: "--relay-listen", value: "127.0.0.1:27901"},
		{name: "--health-listen", value: "127.0.0.1:8080"},
		{name: "--public-host", value: "127.0.0.1"},
		{name: "--data-file", value: "profiles.db"},
	}

	arguments := make([]string, 0, len(requested)+(2*len(defaults)))
	for _, defaultArgument := range defaults {
		if !hasOnlineServerFlag(requested, defaultArgument.name) {
			arguments = append(arguments, defaultArgument.name, defaultArgument.value)
		}
	}
	return append(arguments, requested...)
}

func hasOnlineServerFlag(arguments []string, name string) bool {
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return false
}

// GeneralsX @feature Codex 01/08/2026 Show native progress only for the process that owns a cache-miss extraction.
func ensureCachedRuntime(
	ctx context.Context,
	manager *cache.Manager,
	embedded embeddedBundle,
	stderr io.Writer,
	openProgress extractionProgressFactory,
) (*cache.Runtime, error) {
	var reporter extractionProgressReporter
	leasedRuntime, err := manager.Ensure(ctx, cache.Request{
		Product:        embedded.manifest.Product,
		PayloadDigest:  embedded.manifest.PayloadSHA256,
		ManifestDigest: embedded.manifestDigest,
		Extract: func(ctx context.Context, destination string) error {
			if openProgress != nil {
				reporter = openProgress()
			}
			if reporter != nil {
				reporter.Indeterminate("Checking game package...")
			}
			fmt.Fprintf(stderr,
				"GeneralsX SFX: first launch; extracting %.2f GiB to the local cache...\n",
				float64(embedded.manifest.TotalSize)/(1<<30),
			)
			return extractEmbeddedPayloadWithProgress(
				ctx,
				embedded,
				destination,
				func(completed, total int64) {
					if reporter != nil {
						reporter.Update("Extracting game files...", completed, total)
					}
				},
			)
		},
		Validate: func(runtimeRoot string) error {
			if reporter != nil {
				reporter.Indeterminate("Verifying game files...")
			}
			return validateExtractedRuntime(ctx, runtimeRoot, embedded.manifest)
		},
	})
	if reporter != nil {
		if err == nil {
			reporter.Complete()
		}
		reporter.Close()
	}
	return leasedRuntime, err
}

func contextFailureExitCode(ctx context.Context, fallback int) int {
	if code, ok := signalctx.ExitCode(ctx); ok {
		return code
	}
	return fallback
}

func parseAction(arguments []string) (action, error) {
	if len(arguments) == 0 {
		return action{kind: actionLaunch}, nil
	}
	if arguments[0] == "--" {
		return action{kind: actionLaunch, gameArgs: append([]string(nil), arguments[1:]...)}, nil
	}

	switch arguments[0] {
	case "--sfx-help", "-h", "--help":
		if len(arguments) != 1 {
			return action{}, fmt.Errorf("%s does not accept arguments", arguments[0])
		}
		return action{kind: actionHelp}, nil
	case "--sfx-info":
		if len(arguments) != 1 {
			return action{}, errors.New("--sfx-info does not accept arguments")
		}
		return action{kind: actionInfo}, nil
	case "--sfx-verify":
		if len(arguments) != 1 {
			return action{}, errors.New("--sfx-verify does not accept arguments")
		}
		return action{kind: actionVerify}, nil
	case "--sfx-purge-cache":
		if len(arguments) != 1 {
			return action{}, errors.New("--sfx-purge-cache does not accept arguments")
		}
		return action{kind: actionPurge}, nil
	case "--sfx-notices":
		if len(arguments) != 1 {
			return action{}, errors.New("--sfx-notices does not accept arguments")
		}
		return action{kind: actionNotices}, nil
	case "--sfx-server":
		serverArgs := arguments[1:]
		if len(serverArgs) != 0 && serverArgs[0] == "--" {
			serverArgs = serverArgs[1:]
		}
		return action{
			kind:       actionServer,
			serverArgs: append([]string(nil), serverArgs...),
		}, nil
	case "--sfx-extract":
		if len(arguments) != 2 || arguments[1] == "" {
			return action{}, errors.New("--sfx-extract requires exactly one destination directory")
		}
		return action{kind: actionExtract, path: arguments[1]}, nil
	default:
		return action{kind: actionLaunch, gameArgs: append([]string(nil), arguments...)}, nil
	}
}

func writeHelp(writer io.Writer) {
	fmt.Fprintln(writer, `GeneralsX self-extracting launcher

Usage:
  generalsx-sfx [GAME_ARGUMENT...]
  generalsx-sfx -- [GAME_ARGUMENT...]
  generalsx-sfx --sfx-info
  generalsx-sfx --sfx-verify
  generalsx-sfx --sfx-extract DIRECTORY
  generalsx-sfx --sfx-purge-cache
  generalsx-sfx --sfx-notices
  generalsx-sfx --sfx-server [--] [SERVER_ARGUMENT...]

The first normal launch verifies and extracts the embedded native game into a
content-addressed per-user cache, then executes it directly. Later launches
reuse that cache. On macOS and Linux, set GX_SFX_CACHE to choose a dedicated
owner-private cache filesystem. Windows always uses its per-user cache.

Use "--" when a game argument has the same name as an SFX option. When the
bundle includes an Online server, --sfx-server runs it from dedicated private
writable state. Control, relay, and health listeners bind to loopback, the
advertised host is loopback, and data uses private persistent state unless that
specific setting is supplied explicitly. Other server arguments do not disable
these safety defaults.`)
}

func loadEmbeddedBundle(files fs.FS) (embeddedBundle, error) {
	manifestBytes, err := fs.ReadFile(files, payload.ManifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return embeddedBundle{}, errors.New(
				"this development launcher has no payload; build one with generalsx-sfx-pack",
			)
		}
		return embeddedBundle{}, fmt.Errorf("read embedded manifest: %w", err)
	}
	manifest, err := bundle.ParseManifest(manifestBytes)
	if err != nil {
		return embeddedBundle{}, fmt.Errorf("parse embedded manifest: %w", err)
	}
	digest := sha256.Sum256(manifestBytes)
	return embeddedBundle{
		files:          files,
		manifest:       manifest,
		manifestBytes:  manifestBytes,
		manifestDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func selectedCacheRoot(targetOS, requested string) (string, error) {
	if targetOS == "windows" && requested != "" {
		return "", fmt.Errorf(
			"%s overrides are disabled on Windows because the Go standard library cannot verify an owner-only DACL",
			cacheEnvironmentVariable,
		)
	}
	return requested, nil
}

func newCacheManager(targetOS, requested string) (*cache.Manager, error) {
	cacheRoot, err := selectedCacheRoot(targetOS, requested)
	if err != nil {
		return nil, err
	}
	return cache.New(cache.Options{Root: cacheRoot})
}

// displayCacheRoot resolves the path shown by --sfx-info without creating or
// permission-checking it. Inspection must not mutate the extraction cache.
func displayCacheRoot(targetOS, requested string) (string, error) {
	cacheRoot, err := selectedCacheRoot(targetOS, requested)
	if err != nil {
		return "", err
	}
	if cacheRoot == "" {
		userCacheDir, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			return "", fmt.Errorf("resolve user cache directory: %w", cacheErr)
		}
		cacheRoot = filepath.Join(userCacheDir, "GeneralsX", "sfx")
	}
	absolute, absoluteErr := filepath.Abs(cacheRoot)
	if absoluteErr != nil {
		return "", fmt.Errorf("resolve cache root %q: %w", cacheRoot, absoluteErr)
	}
	return filepath.Clean(absolute), nil
}

func validateHost(manifest bundle.Manifest) error {
	if manifest.TargetOS != runtime.GOOS || manifest.TargetArch != runtime.GOARCH {
		return fmt.Errorf(
			"payload targets %s/%s but this launcher is running on %s/%s",
			manifest.TargetOS,
			manifest.TargetArch,
			runtime.GOOS,
			runtime.GOARCH,
		)
	}
	return nil
}

func writeInfo(writer io.Writer, embedded embeddedBundle, cacheRoot string) {
	manifest := embedded.manifest
	fmt.Fprintf(writer, "Product:             %s\n", manifest.Product)
	fmt.Fprintf(writer, "Version:             %s\n", manifest.Version)
	fmt.Fprintf(writer, "Target:              %s/%s\n", manifest.TargetOS, manifest.TargetArch)
	fmt.Fprintf(writer, "Entrypoint:          %s\n", manifest.Entrypoint)
	if manifest.OnlineServerEntrypoint == "" {
		fmt.Fprintln(writer, "Online server:       <not included>")
	} else {
		fmt.Fprintf(writer, "Online server:       %s\n", manifest.OnlineServerEntrypoint)
	}
	if manifest.WorkDir == "" {
		fmt.Fprintln(writer, "Working directory:   <payload root>")
	} else {
		fmt.Fprintf(writer, "Working directory:   %s\n", manifest.WorkDir)
	}
	fmt.Fprintf(writer, "Manifest entries:    %d\n", len(manifest.Entries))
	fmt.Fprintf(writer, "Extracted size:      %d bytes\n", manifest.TotalSize)
	fmt.Fprintf(writer, "Embedded payload:    %d bytes (%s)\n", manifest.PayloadSize, manifest.Compression)
	fmt.Fprintf(writer, "Payload SHA-256:     %s\n", manifest.PayloadSHA256)
	fmt.Fprintf(writer, "Manifest SHA-256:    %s\n", embedded.manifestDigest)
	fmt.Fprintf(writer, "Cache root:          %s\n", cacheRoot)
}

func verifyEmbeddedPayload(ctx context.Context, embedded embeddedBundle) error {
	parent, err := os.MkdirTemp("", "generalsx-sfx-verify-")
	if err != nil {
		return fmt.Errorf("create verification directory: %w", err)
	}
	defer os.RemoveAll(parent)

	destination := filepath.Join(parent, "runtime")
	if err := extractEmbeddedPayload(ctx, embedded, destination); err != nil {
		return err
	}
	return validateExtractedRuntime(ctx, destination, embedded.manifest)
}

func extractToRequestedPath(ctx context.Context, embedded embeddedBundle, requested string) error {
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return fmt.Errorf("destination %q already exists", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination %q: %w", absolute, err)
	}

	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(absolute)+".extracting-")
	if err != nil {
		return fmt.Errorf("create extraction staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := extractEmbeddedPayload(ctx, embedded, staging); err != nil {
		return err
	}
	if err := validateExtractedRuntime(ctx, staging, embedded.manifest); err != nil {
		return err
	}
	if err := os.Rename(staging, absolute); err != nil {
		return fmt.Errorf("publish extracted payload: %w", err)
	}
	published = true
	return nil
}

func extractEmbeddedPayload(ctx context.Context, embedded embeddedBundle, destination string) error {
	return extractEmbeddedPayloadWithProgress(ctx, embedded, destination, nil)
}

// GeneralsX @feature Codex 01/08/2026 Forward verified regular-file byte counts to first-launch UI.
func extractEmbeddedPayloadWithProgress(
	ctx context.Context,
	embedded embeddedBundle,
	destination string,
	report func(completed, total int64),
) error {
	if ctx == nil {
		return errors.New("extract embedded payload requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authenticateEmbeddedPayload(ctx, embedded); err != nil {
		return err
	}

	maxDecompressedSize, err := maximumDecompressedPayloadSize(embedded.manifest)
	if err != nil {
		return err
	}

	archive, err := embedded.files.Open(payload.ArchivePath)
	if err != nil {
		return fmt.Errorf("open embedded payload: %w", err)
	}
	defer archive.Close()

	compressed := &io.LimitedReader{
		R: &contextReader{ctx: ctx, reader: archive},
		N: embedded.manifest.PayloadSize,
	}
	decompressed, closeDecoder, err := decompressor(embedded.manifest.Compression, compressed)
	if err != nil {
		return err
	}
	if closeDecoder != nil {
		defer closeDecoder()
	}

	boundedDecompressed := &io.LimitedReader{R: decompressed, N: maxDecompressedSize + 1}
	countedDecompressed := &countingReader{reader: boundedDecompressed}
	checkedDecompressed := &contextReader{ctx: ctx, reader: countedDecompressed}
	if err := bundle.ExtractTarWithProgress(
		checkedDecompressed,
		destination,
		embedded.manifest,
		report,
	); err != nil {
		return fmt.Errorf("extract embedded payload: %w", err)
	}
	if _, err := io.Copy(io.Discard, checkedDecompressed); err != nil {
		return fmt.Errorf("finish decompression: %w", err)
	}
	if countedDecompressed.count > maxDecompressedSize {
		return fmt.Errorf(
			"decompressed payload exceeds the manifest-derived limit of %d bytes",
			maxDecompressedSize,
		)
	}
	if compressed.N != 0 {
		return fmt.Errorf(
			"compressed payload decoder left %d trailing bytes",
			compressed.N,
		)
	}
	return nil
}

// authenticateEmbeddedPayload makes compressed size and digest failures happen
// before a decoder is initialized or any payload content is written.
// payload.Files is a go:embed filesystem and therefore immutable between this
// pass and the subsequent open in extractEmbeddedPayload.
func authenticateEmbeddedPayload(ctx context.Context, embedded embeddedBundle) error {
	archive, err := embedded.files.Open(payload.ArchivePath)
	if err != nil {
		return fmt.Errorf("open embedded payload for authentication: %w", err)
	}
	defer archive.Close()

	info, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("stat embedded payload: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("embedded payload is not a regular file")
	}
	if info.Size() != embedded.manifest.PayloadSize {
		return fmt.Errorf(
			"embedded payload size is %d; manifest declares %d",
			info.Size(),
			embedded.manifest.PayloadSize,
		)
	}

	digest := sha256.New()
	limited := &io.LimitedReader{
		R: &contextReader{ctx: ctx, reader: archive},
		N: embedded.manifest.PayloadSize + 1,
	}
	count, err := io.Copy(digest, limited)
	if err != nil {
		return fmt.Errorf("authenticate embedded payload: %w", err)
	}
	if count != embedded.manifest.PayloadSize {
		return fmt.Errorf(
			"embedded payload size is %d; manifest declares %d",
			count,
			embedded.manifest.PayloadSize,
		)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != embedded.manifest.PayloadSHA256 {
		return errors.New("embedded payload SHA-256 mismatch")
	}
	return nil
}

func maximumDecompressedPayloadSize(manifest bundle.Manifest) (int64, error) {
	limits := bundle.DefaultLimits()
	perEntryOverhead :=
		tarPathFieldsPerEntry*int64(limits.MaxPathBytes) +
			tarOverheadBlocks*tarBlockSize
	entryCount := int64(len(manifest.Entries))
	if entryCount > (int64(^uint64(0)>>1)-manifest.TotalSize-
		tarEndMarkerBlockCount*tarBlockSize)/perEntryOverhead {
		return 0, errors.New("decompressed payload limit overflows int64")
	}
	return manifest.TotalSize +
		entryCount*perEntryOverhead +
		tarEndMarkerBlockCount*tarBlockSize, nil
}

func decompressor(compression string, reader io.Reader) (io.Reader, func() error, error) {
	switch compression {
	case "xz":
		decoder, err := (xz.ReaderConfig{
			SingleStream: true,
		}).NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize xz decoder: %w", err)
		}
		return decoder, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported embedded payload compression %q", compression)
	}
}

func validateExtractedRuntime(ctx context.Context, root string, manifest bundle.Manifest) error {
	if ctx == nil {
		return errors.New("validate cached runtime requires a non-nil context")
	}
	expected := make(map[string]bundle.Entry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		expected[entry.Path] = entry
	}

	seen := make(map[string]struct{}, len(manifest.Entries))
	err := filepath.WalkDir(root, func(name string, directoryEntry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == cache.CompletionFile {
			return nil
		}

		entry, ok := expected[relative]
		if !ok {
			return fmt.Errorf("cached runtime contains unexpected path %q", relative)
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if err := validateRuntimeEntry(name, info, entry, manifest.TargetOS); err != nil {
			return err
		}
		if entry.Type == bundle.EntryFile {
			digest, err := hashFile(ctx, name)
			if err != nil {
				return fmt.Errorf("hash %q: %w", entry.Path, err)
			}
			if digest != entry.SHA256 {
				return fmt.Errorf("%q SHA-256 mismatch", entry.Path)
			}
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate cached runtime: %w", err)
	}
	if len(seen) != len(expected) {
		var missing []string
		for name := range expected {
			if _, ok := seen[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("cached runtime is missing %q", missing[0])
	}

	return nil
}

func validateRuntimeEntry(name string, info fs.FileInfo, expected bundle.Entry, targetOS string) error {
	switch expected.Type {
	case bundle.EntryDirectory:
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", expected.Path)
		}
	case bundle.EntryFile:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", expected.Path)
		}
		if info.Size() != expected.Size {
			return fmt.Errorf("%q has size %d; expected %d", expected.Path, info.Size(), expected.Size)
		}
	case bundle.EntrySymlink:
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%q is not a symbolic link", expected.Path)
		}
		target, err := os.Readlink(name)
		if err != nil {
			return fmt.Errorf("read symbolic link %q: %w", expected.Path, err)
		}
		if filepath.ToSlash(target) != expected.LinkTarget {
			return fmt.Errorf("%q has an unexpected symbolic-link target", expected.Path)
		}
	default:
		return fmt.Errorf("%q has unsupported type %q", expected.Path, expected.Type)
	}

	// Symlink permission bits are not portable: macOS reports the mode of a
	// newly created link differently from the normalized tar metadata.
	if targetOS != "windows" &&
		expected.Type != bundle.EntrySymlink &&
		uint32(info.Mode().Perm()) != expected.Mode {
		return fmt.Errorf(
			"%q has mode %#o; expected %#o",
			expected.Path,
			info.Mode().Perm(),
			expected.Mode,
		)
	}
	return nil
}

func hashFile(ctx context.Context, name string) (string, error) {
	if ctx == nil {
		return "", errors.New("hash file requires a non-nil context")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, &contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.count += int64(count)
	return count, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
