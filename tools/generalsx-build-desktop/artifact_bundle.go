package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type bundleFingerprintBuilder struct {
	hash hash.Hash
}

func newBundleFingerprintBuilder() *bundleFingerprintBuilder {
	return &bundleFingerprintBuilder{hash: sha256.New()}
}

func (builder *bundleFingerprintBuilder) add(relative string, info fs.FileInfo) {
	kind := byte('f')
	size := info.Size()
	if info.IsDir() {
		kind = 'd'
		size = 0
	}
	fmt.Fprintf(builder.hash, "%c\x00%d:%s\x00%d\x00%d\x00", kind, len(relative), relative, uint32(info.Mode().Perm()), size)
}

func (builder *bundleFingerprintBuilder) result() [sha256.Size]byte {
	var digest [sha256.Size]byte
	copy(digest[:], builder.hash.Sum(nil))
	return digest
}

func validateBundleArtifactRoot(path string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source application bundle must not be a symbolic link")
	}
	if !info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".app") {
		return errors.New("source artifact is not a regular file or macOS .app bundle")
	}
	return nil
}

func validateBundleEntry(relative string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("application bundle contains symbolic link %q", relative)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("application bundle contains unsupported special entry %q", relative)
	}
	return nil
}

// GeneralsX @feature Codex 05/08/2026 Fingerprint the complete app without following links outside its rooted tree.
func hashStableBundleArtifact(ctx context.Context, path string, recorded fs.FileInfo) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return empty, err
	}
	if err := validateBundleArtifactRoot(path, current); err != nil {
		return empty, err
	}
	if !artifactInfoMatches(recorded, current) {
		return empty, errors.New("application bundle metadata changed after it was recorded")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !artifactInfoMatches(recorded, opened) {
		return empty, errors.New("application bundle changed before it could be opened safely")
	}
	builder := newBundleFingerprintBuilder()
	builder.add(".", opened)
	if err := hashBundleDirectory(ctx, root, ".", opened, builder); err != nil {
		return empty, err
	}
	after, err := root.Stat(".")
	if err != nil || !artifactInfoMatches(recorded, after) {
		return empty, errors.New("application bundle changed while it was being hashed")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !artifactInfoMatches(recorded, pathAfter) {
		return empty, errors.New("application bundle path changed while it was being hashed")
	}
	return builder.result(), nil
}

func hashBundleDirectory(ctx context.Context, root *os.Root, relative string, expected fs.FileInfo, builder *bundleFingerprintBuilder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !artifactInfoMatches(expected, opened) {
		return fmt.Errorf("application bundle directory %q changed before inventory", relative)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := entry.Name()
		if relative != "." {
			child = filepath.Join(relative, child)
		}
		info, err := root.Lstat(child)
		if err != nil {
			return err
		}
		if err := validateBundleEntry(child, info); err != nil {
			return err
		}
		builder.add(child, info)
		if info.IsDir() {
			if err := hashBundleDirectory(ctx, root, child, info, builder); err != nil {
				return err
			}
			continue
		}
		file, err := root.Open(child)
		if err != nil {
			return err
		}
		openedFile, statErr := file.Stat()
		if statErr != nil || !artifactInfoMatches(info, openedFile) {
			file.Close()
			return fmt.Errorf("application bundle file %q changed before hashing", child)
		}
		_, copyErr := io.Copy(builder.hash, contextReader{ctx: ctx, reader: file})
		afterFile, afterErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if afterErr != nil || !artifactInfoMatches(info, afterFile) {
			return fmt.Errorf("application bundle file %q changed while hashing", child)
		}
		if closeErr != nil {
			return closeErr
		}
		pathInfo, err := root.Lstat(child)
		if err != nil || !artifactInfoMatches(info, pathInfo) {
			return fmt.Errorf("application bundle file %q changed after hashing", child)
		}
	}
	after, err := directory.Stat()
	if err != nil || !artifactInfoMatches(expected, after) {
		return fmt.Errorf("application bundle directory %q changed during inventory", relative)
	}
	pathInfo, err := root.Lstat(relative)
	if err != nil || !artifactInfoMatches(expected, pathInfo) {
		return fmt.Errorf("application bundle directory %q changed after inventory", relative)
	}
	return nil
}

// GeneralsX @feature Codex 05/08/2026 Copy a verified app through a private sibling and publish it without replacement.
func copyCompletedBundleToDirectory(
	ctx context.Context,
	completed *completedArtifact,
	destinationDirectory string,
	verifyArtifact verifyArtifactFunc,
) (destination string, err error) {
	destinationDirectory, err = validateDesktopDirectory(destinationDirectory)
	if err != nil {
		return "", err
	}
	current, err := os.Lstat(completed.sourcePath)
	if err != nil {
		return "", fmt.Errorf("inspect source application bundle: %w", err)
	}
	if !artifactInfoMatches(completed.sourceInfo, current) {
		return "", errors.New("source application bundle changed after the build completed")
	}
	baseName := filepath.Base(completed.sourcePath)
	exactDestination := filepath.Join(destinationDirectory, baseName)
	if destinationInfo, statErr := os.Lstat(exactDestination); statErr == nil &&
		destinationInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(current, destinationInfo) {
		if err := verifyArtifactBeforePublication(ctx, completed, exactDestination, verifyArtifact); err != nil {
			return "", err
		}
		return exactDestination, nil
	}

	temporaryPath, err := os.MkdirTemp(destinationDirectory, ".generalsx-copy-*.app")
	if err != nil {
		return "", fmt.Errorf("create temporary Desktop application bundle: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		if removeErr := os.RemoveAll(temporaryPath); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove partial Desktop application bundle %q: %w", temporaryPath, removeErr))
		}
	}()

	copiedDigest, err := copyBundleArtifact(ctx, completed, temporaryPath)
	if err != nil {
		return "", err
	}
	if copiedDigest != completed.sourceSHA256 {
		return "", errors.New("source application bundle contents changed after the build completed")
	}
	if err := verifyArtifactBeforePublication(ctx, completed, temporaryPath, verifyArtifact); err != nil {
		return "", err
	}
	for attempt := 0; attempt < desktopCopyNameAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate := desktopCopyPath(destinationDirectory, baseName, attempt)
		if publishErr := publishTemporaryArtifact(temporaryPath, candidate); publishErr == nil {
			published = true
			return candidate, nil
		} else if !errors.Is(publishErr, fs.ErrExist) {
			return "", fmt.Errorf("publish Desktop application bundle %q: %w", candidate, publishErr)
		}
	}
	return "", fmt.Errorf("could not choose an unused application name after %d attempts", desktopCopyNameAttempts)
}

func copyBundleArtifact(ctx context.Context, completed *completedArtifact, destinationPath string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	sourceRoot, err := os.OpenRoot(completed.sourcePath)
	if err != nil {
		return empty, err
	}
	defer sourceRoot.Close()
	destinationRoot, err := os.OpenRoot(destinationPath)
	if err != nil {
		return empty, err
	}
	defer destinationRoot.Close()
	sourceInfo, err := sourceRoot.Stat(".")
	if err != nil || !artifactInfoMatches(completed.sourceInfo, sourceInfo) {
		return empty, errors.New("source application bundle changed before it could be copied")
	}
	builder := newBundleFingerprintBuilder()
	builder.add(".", sourceInfo)
	if err := copyBundleDirectory(ctx, sourceRoot, destinationRoot, ".", sourceInfo, builder); err != nil {
		return empty, err
	}
	if err := destinationRoot.Chmod(".", sourceInfo.Mode().Perm()); err != nil {
		return empty, fmt.Errorf("preserve application bundle permissions: %w", err)
	}
	if err := destinationRoot.Chtimes(".", sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return empty, fmt.Errorf("preserve application bundle timestamp: %w", err)
	}
	pathInfo, err := os.Lstat(completed.sourcePath)
	if err != nil || !artifactInfoMatches(completed.sourceInfo, pathInfo) {
		return empty, errors.New("source application bundle changed while it was being copied")
	}
	return builder.result(), nil
}

func copyBundleDirectory(ctx context.Context, sourceRoot, destinationRoot *os.Root, relative string, expected fs.FileInfo, builder *bundleFingerprintBuilder) error {
	directory, err := sourceRoot.Open(relative)
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := entry.Name()
		if relative != "." {
			child = filepath.Join(relative, child)
		}
		info, err := sourceRoot.Lstat(child)
		if err != nil {
			return err
		}
		if err := validateBundleEntry(child, info); err != nil {
			return err
		}
		builder.add(child, info)
		if info.IsDir() {
			if err := destinationRoot.Mkdir(child, 0o700); err != nil {
				return err
			}
			if err := copyBundleDirectory(ctx, sourceRoot, destinationRoot, child, info, builder); err != nil {
				return err
			}
			if err := destinationRoot.Chmod(child, info.Mode().Perm()); err != nil {
				return err
			}
			if err := destinationRoot.Chtimes(child, info.ModTime(), info.ModTime()); err != nil {
				return err
			}
			continue
		}
		source, err := sourceRoot.Open(child)
		if err != nil {
			return err
		}
		opened, statErr := source.Stat()
		if statErr != nil || !artifactInfoMatches(info, opened) {
			source.Close()
			return fmt.Errorf("application bundle file %q changed before copy", child)
		}
		destination, err := destinationRoot.OpenFile(child, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			source.Close()
			return err
		}
		copied, copyErr := io.Copy(io.MultiWriter(destination, builder.hash), contextReader{ctx: ctx, reader: source})
		afterSource, sourceStatErr := source.Stat()
		sourceCloseErr := source.Close()
		if copyErr == nil && copied != info.Size() {
			copyErr = fmt.Errorf("copied %d bytes from %q, expected %d", copied, child, info.Size())
		}
		if copyErr == nil && (sourceStatErr != nil || !artifactInfoMatches(info, afterSource)) {
			copyErr = fmt.Errorf("application bundle file %q changed while copying", child)
		}
		if copyErr == nil {
			copyErr = destination.Chmod(info.Mode().Perm())
		}
		if copyErr == nil {
			copyErr = destination.Sync()
		}
		destinationCloseErr := destination.Close()
		if copyErr != nil {
			return copyErr
		}
		if sourceCloseErr != nil {
			return sourceCloseErr
		}
		if destinationCloseErr != nil {
			return destinationCloseErr
		}
		if err := destinationRoot.Chtimes(child, info.ModTime(), info.ModTime()); err != nil {
			return err
		}
		pathInfo, err := sourceRoot.Lstat(child)
		if err != nil || !artifactInfoMatches(info, pathInfo) {
			return fmt.Errorf("application bundle file %q changed after copy", child)
		}
	}
	after, err := directory.Stat()
	if err != nil || !artifactInfoMatches(expected, after) {
		return fmt.Errorf("application bundle directory %q changed while copying", relative)
	}
	return nil
}
