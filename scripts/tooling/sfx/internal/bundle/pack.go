// GeneralsX @feature OpenAI 30/07/2026 Streams deterministic source trees into verified tar payloads.
package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

// Limits bounds memory, disk, and iteration costs for untrusted bundles.
type Limits struct {
	MaxEntries     int
	MaxFileSize    int64
	MaxTotalSize   int64
	MaxPayloadSize int64
	MaxPathBytes   int
}

var defaultLimits = Limits{
	MaxEntries:     250_000,
	MaxFileSize:    8 << 30,
	MaxTotalSize:   16 << 30,
	MaxPayloadSize: 16 << 30,
	MaxPathBytes:   4_096,
}

// DefaultLimits returns a copy of the bundle safety limits.
func DefaultLimits() Limits {
	return defaultLimits
}

// SymlinkMode controls how WriteTar treats source-tree symbolic links.
type SymlinkMode uint8

const (
	// RejectSymlinks is the secure default and fails on the first symlink.
	RejectSymlinks SymlinkMode = iota
	// PreserveSymlinks records safe, relative links without dereferencing them.
	PreserveSymlinks
)

// ExcludeFunc may omit a source path. Returning true for a directory omits its
// complete subtree. The path is a normalized, slash-separated archive path.
type ExcludeFunc func(path string, entry fs.DirEntry) (bool, error)

// PackOptions defines immutable bundle metadata and source-tree policy.
type PackOptions struct {
	Context                context.Context
	Product                string
	Version                string
	TargetOS               string
	TargetArch             string
	Entrypoint             string
	WorkDir                string
	OnlineServerEntrypoint string
	Epoch                  time.Time
	Exclude                ExcludeFunc
	SymlinkMode            SymlinkMode
	Limits                 Limits
}

type sourceItem struct {
	entry Entry
	full  string
	info  fs.FileInfo
}

// WriteTar writes a deterministic, uncompressed tar stream and returns its
// uncompressed manifest. The caller must compress the stream, then set
// Compression, PayloadSHA256, and PayloadSize before publishing the manifest.
func WriteTar(root string, w io.Writer, opts PackOptions) (Manifest, error) {
	if w == nil {
		return Manifest{}, fmt.Errorf("bundle output writer is nil")
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	limits, err := normalizeLimits(opts.Limits)
	if err != nil {
		return Manifest{}, err
	}
	items, manifest, err := collectSource(root, opts, limits)
	if err != nil {
		return Manifest{}, err
	}

	tw := tar.NewWriter(w)
	for i := range items {
		if err := ctx.Err(); err != nil {
			_ = tw.Close()
			return Manifest{}, err
		}
		item := &items[i]
		header := normalizedHeader(item.entry, time.Unix(manifest.Epoch, 0).UTC())
		if err := tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			return Manifest{}, fmt.Errorf("write tar header %q: %w", item.entry.Path, err)
		}
		if item.entry.Type != EntryFile {
			continue
		}
		digest, err := streamFile(ctx, tw, *item)
		if err != nil {
			_ = tw.Close()
			return Manifest{}, err
		}
		item.entry.SHA256 = digest
		manifest.Entries[i].SHA256 = digest
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("finish bundle tar: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate generated manifest: %w", err)
	}
	return manifest, nil
}

func collectSource(root string, opts PackOptions, limits Limits) ([]sourceItem, Manifest, error) {
	if root == "" {
		return nil, Manifest{}, fmt.Errorf("bundle source root is empty")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("resolve bundle source root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("resolve bundle source root symlinks: %w", err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("stat bundle source root: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, Manifest{}, fmt.Errorf("bundle source root %q is not a directory", root)
	}
	if opts.SymlinkMode != RejectSymlinks && opts.SymlinkMode != PreserveSymlinks {
		return nil, Manifest{}, fmt.Errorf("unsupported symlink mode %d", opts.SymlinkMode)
	}

	epoch := opts.Epoch
	if epoch.IsZero() {
		epoch = time.Unix(0, 0)
	}
	epoch = epoch.UTC().Truncate(time.Second)
	if epoch.Unix() < 0 || epoch.Unix() > maxEpoch {
		return nil, Manifest{}, fmt.Errorf("bundle epoch is outside the supported range")
	}
	entrypoint := opts.Entrypoint
	workDir := opts.WorkDir
	if workDir == "." {
		workDir = ""
	}
	schemaVersion := SchemaVersion
	if opts.OnlineServerEntrypoint != "" {
		// GeneralsX @feature Codex 04/08/2026 Version server-bearing manifests without breaking schema-v1 readers' contract.
		schemaVersion = OnlineServerSchemaVersion
	}

	manifest := Manifest{
		SchemaVersion:          schemaVersion,
		Product:                opts.Product,
		Version:                opts.Version,
		TargetOS:               opts.TargetOS,
		TargetArch:             opts.TargetArch,
		Entrypoint:             entrypoint,
		WorkDir:                workDir,
		OnlineServerEntrypoint: opts.OnlineServerEntrypoint,
		Epoch:                  epoch.Unix(),
	}
	var items []sourceItem
	err = filepath.WalkDir(resolvedRoot, func(full string, dirEntry fs.DirEntry, walkErr error) error {
		if opts.Context != nil {
			if err := opts.Context.Err(); err != nil {
				return err
			}
		}
		if walkErr != nil {
			return walkErr
		}
		if full == resolvedRoot {
			return nil
		}
		relative, err := filepath.Rel(resolvedRoot, full)
		if err != nil {
			return fmt.Errorf("make source path relative: %w", err)
		}
		name := filepath.ToSlash(relative)
		if err := validateArchivePath(name, opts.TargetOS); err != nil {
			return err
		}
		if len(name) > limits.MaxPathBytes {
			return fmt.Errorf("archive path %q exceeds configured limit of %d bytes", name, limits.MaxPathBytes)
		}
		if opts.Exclude != nil {
			excluded, err := opts.Exclude(name, dirEntry)
			if err != nil {
				return fmt.Errorf("evaluate exclusion for %q: %w", name, err)
			}
			if excluded {
				if dirEntry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if len(items) >= limits.MaxEntries {
			return fmt.Errorf("source tree exceeds %d entries", limits.MaxEntries)
		}

		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("lstat source path %q: %w", name, err)
		}
		item := sourceItem{full: full, info: info}
		item.entry.Path = name
		item.entry.Mode = uint32(info.Mode().Perm())
		switch {
		case info.Mode().IsDir():
			item.entry.Type = EntryDirectory
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > limits.MaxFileSize {
				return fmt.Errorf("source file %q has invalid size %d", name, info.Size())
			}
			if info.Size() > limits.MaxTotalSize-manifest.TotalSize {
				return fmt.Errorf("source tree exceeds %d bytes", limits.MaxTotalSize)
			}
			item.entry.Type = EntryFile
			item.entry.Size = info.Size()
			item.entry.SHA256 = zeroSHA256
			manifest.TotalSize += info.Size()
		case info.Mode()&os.ModeSymlink != 0:
			if opts.SymlinkMode != PreserveSymlinks {
				return fmt.Errorf("source path %q is a symlink but symlinks are disabled", name)
			}
			target, err := os.Readlink(full)
			if err != nil {
				return fmt.Errorf("read source symlink %q: %w", name, err)
			}
			item.entry.Type = EntrySymlink
			item.entry.Mode = 0o777
			item.entry.LinkTarget = filepath.ToSlash(target)
			if err := validateLinkTarget(name, item.entry.LinkTarget); err != nil {
				return err
			}
			if len(item.entry.LinkTarget) > limits.MaxPathBytes {
				return fmt.Errorf("symlink %q target exceeds configured limit of %d bytes", name, limits.MaxPathBytes)
			}
		default:
			return fmt.Errorf("source path %q is a special filesystem node", name)
		}
		normalizeSourceEntryMode(
			&item.entry,
			runtime.GOOS,
			opts.TargetOS,
			entrypoint,
			opts.OnlineServerEntrypoint,
		)
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("collect bundle source: %w", err)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].entry.Path < items[j].entry.Path
	})
	manifest.Entries = make([]Entry, len(items))
	for i := range items {
		manifest.Entries[i] = items[i].entry
	}
	if err := manifest.Validate(); err != nil {
		return nil, Manifest{}, fmt.Errorf("validate source manifest: %w", err)
	}
	return items, manifest, nil
}

// GeneralsX @bugfix Codex 04/08/2026 Synthesize POSIX executable bits during native Windows cross-packaging.
// normalizeSourceEntryMode supplies deterministic archive permissions where
// Windows cannot represent POSIX executable bits during cross-packaging.
func normalizeSourceEntryMode(
	entry *Entry,
	hostOS string,
	targetOS string,
	executablePaths ...string,
) {
	if entry == nil || entry.Type == EntrySymlink {
		return
	}
	if targetOS == "windows" {
		if entry.Type == EntryDirectory {
			entry.Mode = 0o755
		} else {
			entry.Mode = 0o644
		}
		return
	}
	if hostOS != "windows" {
		return
	}
	if entry.Type == EntryDirectory {
		entry.Mode = 0o755
		return
	}
	entry.Mode = 0o644
	for _, executablePath := range executablePaths {
		if executablePath != "" && entry.Path == executablePath {
			entry.Mode = 0o755
			return
		}
	}
}

func normalizedHeader(entry Entry, epoch time.Time) *tar.Header {
	header := &tar.Header{
		Name:       entry.Path,
		Mode:       int64(entry.Mode),
		Uid:        0,
		Gid:        0,
		Size:       entry.Size,
		ModTime:    epoch,
		AccessTime: time.Time{},
		ChangeTime: time.Time{},
		Format:     tar.FormatPAX,
	}
	switch entry.Type {
	case EntryDirectory:
		header.Typeflag = tar.TypeDir
	case EntryFile:
		header.Typeflag = tar.TypeReg
	case EntrySymlink:
		header.Typeflag = tar.TypeSymlink
		header.Linkname = entry.LinkTarget
		header.Size = 0
	}
	return header
}

func streamFile(ctx context.Context, w io.Writer, item sourceItem) (string, error) {
	expected := item.entry
	file, err := os.Open(item.full)
	if err != nil {
		return "", fmt.Errorf("open source file %q: %w", expected.Path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat opened source file %q: %w", expected.Path, err)
	}
	if !os.SameFile(item.info, info) || !info.Mode().IsRegular() || info.Size() != expected.Size ||
		info.Mode().Perm() != item.info.Mode().Perm() ||
		!info.ModTime().Equal(item.info.ModTime()) {
		return "", fmt.Errorf("source file %q changed while packing", expected.Path)
	}

	hash := sha256.New()
	written, err := io.CopyN(
		io.MultiWriter(w, hash),
		&contextReader{ctx: ctx, reader: file},
		expected.Size,
	)
	if err != nil {
		return "", fmt.Errorf("stream source file %q after %d bytes: %w", expected.Path, written, err)
	}
	var extra [1]byte
	if n, readErr := file.Read(extra[:]); n != 0 || readErr != io.EOF {
		if readErr == nil {
			readErr = fmt.Errorf("unexpected data")
		}
		return "", fmt.Errorf("source file %q grew while packing: %w", expected.Path, readErr)
	}
	streamDigest := hash.Sum(nil)

	// GeneralsX @build moloch 30/07/2026 Require a quiescent source snapshot.
	// A second pass catches same-size in-place writes that can evade size and
	// coarse timestamp checks while the first pass is feeding the tar stream.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind source file %q for stability check: %w", expected.Path, err)
	}
	verifyHash := sha256.New()
	verified, err := io.CopyN(
		verifyHash,
		&contextReader{ctx: ctx, reader: file},
		expected.Size,
	)
	if err != nil {
		return "", fmt.Errorf("re-read source file %q after %d bytes: %w", expected.Path, verified, err)
	}
	if n, readErr := file.Read(extra[:]); n != 0 || readErr != io.EOF {
		if readErr == nil {
			readErr = fmt.Errorf("unexpected data")
		}
		return "", fmt.Errorf("source file %q changed while packing: %w", expected.Path, readErr)
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat packed source file %q: %w", expected.Path, err)
	}
	if !os.SameFile(info, finalInfo) || !finalInfo.Mode().IsRegular() ||
		finalInfo.Size() != expected.Size ||
		finalInfo.Mode().Perm() != info.Mode().Perm() ||
		!finalInfo.ModTime().Equal(info.ModTime()) ||
		!bytes.Equal(streamDigest, verifyHash.Sum(nil)) {
		return "", fmt.Errorf("source file %q changed while packing", expected.Path)
	}
	return fmt.Sprintf("%x", streamDigest), nil
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

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaultLimits.MaxEntries
	}
	if limits.MaxFileSize == 0 {
		limits.MaxFileSize = defaultLimits.MaxFileSize
	}
	if limits.MaxTotalSize == 0 {
		limits.MaxTotalSize = defaultLimits.MaxTotalSize
	}
	if limits.MaxPayloadSize == 0 {
		limits.MaxPayloadSize = defaultLimits.MaxPayloadSize
	}
	if limits.MaxPathBytes == 0 {
		limits.MaxPathBytes = defaultLimits.MaxPathBytes
	}
	if limits.MaxEntries < 0 || limits.MaxEntries > defaultLimits.MaxEntries ||
		limits.MaxFileSize < 0 || limits.MaxFileSize > defaultLimits.MaxFileSize ||
		limits.MaxTotalSize < 0 || limits.MaxTotalSize > defaultLimits.MaxTotalSize ||
		limits.MaxPayloadSize < 0 || limits.MaxPayloadSize > defaultLimits.MaxPayloadSize ||
		limits.MaxPathBytes < 0 || limits.MaxPathBytes > defaultLimits.MaxPathBytes {
		return Limits{}, fmt.Errorf("bundle limits must be positive and no greater than schema-v1 defaults")
	}
	if limits.MaxFileSize > limits.MaxTotalSize {
		return Limits{}, fmt.Errorf("maximum file size exceeds maximum total size")
	}
	return limits, nil
}

const zeroSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
