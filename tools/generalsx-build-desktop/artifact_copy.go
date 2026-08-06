package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const desktopCopyNameAttempts = 1000

type completedArtifact struct {
	jobID        string
	target       string
	sourcePath   string
	sourceInfo   fs.FileInfo
	sourceSHA256 [sha256.Size]byte
}

func inspectCompletedArtifact(jobID, sourcePath string) (*completedArtifact, error) {
	return inspectCompletedArtifactContext(context.Background(), jobID, sourcePath)
}

func inspectCompletedArtifactContext(ctx context.Context, jobID, sourcePath string) (*completedArtifact, error) {
	pathInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("inspect source artifact: %w", err)
	}
	if pathInfo.IsDir() {
		if err := validateBundleArtifactRoot(sourcePath, pathInfo); err != nil {
			return nil, err
		}
		digest, err := hashStableBundleArtifact(ctx, sourcePath, pathInfo)
		if err != nil {
			return nil, fmt.Errorf("hash source application bundle: %w", err)
		}
		return &completedArtifact{
			jobID: jobID, sourcePath: sourcePath, sourceInfo: pathInfo, sourceSHA256: digest,
		}, nil
	}
	source, info, err := openStableArtifact(sourcePath)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	digest, err := hashStableArtifact(ctx, sourcePath, source, info)
	if err != nil {
		return nil, fmt.Errorf("hash source artifact: %w", err)
	}
	return &completedArtifact{
		jobID: jobID, sourcePath: sourcePath, sourceInfo: info, sourceSHA256: digest,
	}, nil
}

func validateSourceArtifact(info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source artifact must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("source artifact is not a regular file")
	}
	if info.Size() == 0 {
		return errors.New("source artifact is empty")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("source artifact is not executable")
	}
	return nil
}

func artifactInfoMatches(recorded, current fs.FileInfo) bool {
	return os.SameFile(recorded, current) &&
		recorded.Size() == current.Size() &&
		recorded.ModTime().Equal(current.ModTime()) &&
		recorded.Mode() == current.Mode()
}

// GeneralsX @feature Codex 05/08/2026 Bind destructive post-build actions to the exact recorded artifact bytes.
func revalidateCompletedArtifact(ctx context.Context, completed *completedArtifact) error {
	if completed == nil {
		return errors.New("completed artifact is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if completed.sourceInfo.IsDir() {
		digest, err := hashStableBundleArtifact(ctx, completed.sourcePath, completed.sourceInfo)
		if err != nil {
			return fmt.Errorf("hash recorded application bundle: %w", err)
		}
		if digest != completed.sourceSHA256 {
			return errors.New("application bundle contents changed after it was recorded")
		}
		return nil
	}
	source, currentInfo, err := openStableArtifact(completed.sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	if !artifactInfoMatches(completed.sourceInfo, currentInfo) {
		return errors.New("artifact metadata changed after it was recorded")
	}
	digest, err := hashStableArtifact(ctx, completed.sourcePath, source, currentInfo)
	if err != nil {
		return fmt.Errorf("hash recorded artifact: %w", err)
	}
	if digest != completed.sourceSHA256 {
		return errors.New("artifact contents changed after it was recorded")
	}
	return nil
}

func openStableArtifact(path string) (*os.File, fs.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect source artifact: %w", err)
	}
	if err := validateSourceArtifact(pathInfo); err != nil {
		return nil, nil, err
	}
	source, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open source artifact: %w", err)
	}
	openedInfo, err := source.Stat()
	if err != nil {
		source.Close()
		return nil, nil, fmt.Errorf("inspect opened source artifact: %w", err)
	}
	if !artifactInfoMatches(pathInfo, openedInfo) {
		source.Close()
		return nil, nil, errors.New("source artifact changed before it could be opened safely")
	}
	return source, openedInfo, nil
}

func hashStableArtifact(
	ctx context.Context,
	path string,
	source *os.File,
	recorded fs.FileInfo,
) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: source}); err != nil {
		return digest, err
	}
	if err := ctx.Err(); err != nil {
		return digest, err
	}
	afterRead, err := source.Stat()
	if err != nil {
		return digest, fmt.Errorf("inspect opened source artifact after hashing: %w", err)
	}
	if !artifactInfoMatches(recorded, afterRead) {
		return digest, errors.New("source artifact changed while it was being hashed")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return digest, fmt.Errorf("inspect source artifact after hashing: %w", err)
	}
	if err := validateSourceArtifact(pathInfo); err != nil {
		return digest, err
	}
	if !artifactInfoMatches(recorded, pathInfo) {
		return digest, errors.New("source artifact path changed while it was being hashed")
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// GeneralsX @feature Codex 05/08/2026 Copy only the verified SFX from the matching completed GUI build.
func (a *App) CopyBuildArtifactToDesktop(jobID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", errors.New("build job ID is required")
	}

	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return "", errors.New("desktop application is shutting down")
	}
	completed := a.completedArtifact
	if completed == nil || completed.jobID != jobID {
		a.mu.Unlock()
		return "", errors.New("no verified build artifact is available for this build")
	}
	if a.copyInProgress {
		a.mu.Unlock()
		return "", errors.New("the build artifact is already being copied to Desktop")
	}
	if a.cleanupPlanning {
		a.mu.Unlock()
		return "", errors.New("a cleanup plan is still being prepared")
	}
	if a.cleanupInProgress {
		a.mu.Unlock()
		return "", errors.New("build files are being cleaned up")
	}
	desktopDirectory := a.dependencies.desktopDirectory
	if desktopDirectory == nil {
		a.mu.Unlock()
		return "", errors.New("Desktop directory resolver is unavailable")
	}
	copyArtifact := a.dependencies.copyArtifact
	if copyArtifact == nil {
		a.mu.Unlock()
		return "", errors.New("Desktop artifact copier is unavailable")
	}
	verifyArtifact := a.dependencies.verifyArtifact
	if verifyArtifact == nil {
		a.mu.Unlock()
		return "", errors.New("Desktop artifact verification is unavailable")
	}
	if a.ctx == nil {
		a.mu.Unlock()
		return "", errors.New("desktop runtime is not ready")
	}
	copyContext, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.copyInProgress = true
	a.preparedCleanup = nil
	a.copyCancel = cancel
	a.copyDone = done
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		if a.copyDone == done {
			a.copyInProgress = false
			a.copyCancel = nil
			a.copyDone = nil
		}
		a.mu.Unlock()
		close(done)
	}()

	desktop, err := desktopDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve Desktop directory: %w", err)
	}
	if err := copyContext.Err(); err != nil {
		return "", err
	}
	destination, err := copyArtifact(copyContext, completed, desktop, verifyArtifact)
	if err != nil {
		return "", fmt.Errorf("copy build artifact to Desktop: %w", err)
	}
	// Publication is the commit point: finish recording the already-verified
	// inode even if cancellation arrives immediately after its atomic rename.
	recordContext := context.WithoutCancel(copyContext)
	desktopArtifact, err := inspectCompletedArtifactContext(recordContext, jobID, destination)
	if err != nil {
		return "", fmt.Errorf("inspect Desktop build artifact: %w", err)
	}
	desktopArtifact.target = completed.target
	if desktopArtifact.sourceSHA256 != completed.sourceSHA256 {
		return "", errors.New("Desktop copy does not match the completed build artifact")
	}
	a.mu.Lock()
	if a.completedArtifact != completed {
		a.mu.Unlock()
		return "", errors.New("the completed build changed before its Desktop copy was recorded")
	}
	a.desktopArtifact = desktopArtifact
	a.preparedCleanup = nil
	a.mu.Unlock()
	return destination, nil
}

func copyArtifactToDirectory(sourcePath, destinationDirectory string) (string, error) {
	completed, err := inspectCompletedArtifact("", sourcePath)
	if err != nil {
		return "", err
	}
	return copyCompletedArtifactToDirectory(context.Background(), completed, destinationDirectory, nil)
}

func copyCompletedArtifactToDirectory(
	ctx context.Context,
	completed *completedArtifact,
	destinationDirectory string,
	verifyArtifact verifyArtifactFunc,
) (destination string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	destinationDirectory, err = validateDesktopDirectory(destinationDirectory)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if completed == nil {
		return "", errors.New("completed artifact is unavailable")
	}
	if completed.sourceInfo.IsDir() {
		return copyCompletedBundleToDirectory(ctx, completed, destinationDirectory, verifyArtifact)
	}

	currentInfo, err := os.Lstat(completed.sourcePath)
	if err != nil {
		return "", fmt.Errorf("inspect source artifact: %w", err)
	}
	if !artifactInfoMatches(completed.sourceInfo, currentInfo) {
		return "", errors.New("source artifact changed after the build completed")
	}
	source, openedInfo, err := openStableArtifact(completed.sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	if !artifactInfoMatches(completed.sourceInfo, openedInfo) {
		return "", errors.New("source artifact changed before it could be copied")
	}

	baseName := filepath.Base(completed.sourcePath)
	if baseName == "." || baseName == string(filepath.Separator) {
		return "", errors.New("source artifact has no filename")
	}
	exactDestination := filepath.Join(destinationDirectory, baseName)
	if destinationInfo, statErr := os.Lstat(exactDestination); statErr == nil &&
		destinationInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(openedInfo, destinationInfo) {
		if err := verifyArtifactBeforePublication(ctx, completed, exactDestination, verifyArtifact); err != nil {
			return "", err
		}
		return exactDestination, nil
	}

	temporaryPattern := ".generalsx-copy-*" + filepath.Ext(baseName)
	temporary, err := os.CreateTemp(destinationDirectory, temporaryPattern)
	if err != nil {
		return "", fmt.Errorf("create temporary Desktop copy: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		if published {
			return
		}
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove partial Desktop copy %q: %w", temporaryPath, removeErr))
		}
	}()

	if err := writeTemporaryArtifact(ctx, source, completed, temporary); err != nil {
		return "", err
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
			return "", fmt.Errorf("publish Desktop copy %q: %w", candidate, publishErr)
		}
	}
	return "", fmt.Errorf("could not choose an unused filename after %d attempts", desktopCopyNameAttempts)
}

// GeneralsX @bugfix Codex 05/08/2026 Verify the private Desktop sibling before its no-replace publication.
func verifyArtifactBeforePublication(
	ctx context.Context,
	completed *completedArtifact,
	path string,
	verifyArtifact verifyArtifactFunc,
) error {
	temporary, err := inspectCompletedArtifactContext(ctx, completed.jobID, path)
	if err != nil {
		return fmt.Errorf("inspect temporary Desktop artifact: %w", err)
	}
	temporary.target = completed.target
	if temporary.sourceSHA256 != completed.sourceSHA256 {
		return errors.New("temporary Desktop artifact does not match the completed build")
	}
	if verifyArtifact == nil {
		return nil
	}
	if err := verifyArtifact(ctx, path, completed.target); err != nil {
		return fmt.Errorf("verify temporary Desktop artifact: %w", err)
	}
	if err := revalidateCompletedArtifact(ctx, temporary); err != nil {
		return fmt.Errorf("revalidate temporary Desktop artifact: %w", err)
	}
	return nil
}

func desktopCopyPath(directory, baseName string, attempt int) string {
	if attempt == 0 {
		return filepath.Join(directory, baseName)
	}
	extension := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, extension)
	return filepath.Join(directory, fmt.Sprintf("%s (%d)%s", stem, attempt, extension))
}

func writeTemporaryArtifact(ctx context.Context, source *os.File, completed *completedArtifact, destination *os.File) (err error) {
	defer func() {
		if closeErr := destination.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close temporary Desktop copy: %w", closeErr)
		}
	}()

	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(destination, hash), contextReader{ctx: ctx, reader: source})
	if err != nil {
		return fmt.Errorf("write temporary Desktop copy: %w", err)
	}
	if copied != completed.sourceInfo.Size() {
		return fmt.Errorf("copied %d bytes, expected %d", copied, completed.sourceInfo.Size())
	}
	afterCopy, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect source artifact after copy: %w", err)
	}
	if !artifactInfoMatches(completed.sourceInfo, afterCopy) {
		return errors.New("source artifact changed while it was being copied")
	}
	var copiedDigest [sha256.Size]byte
	copy(copiedDigest[:], hash.Sum(nil))
	if copiedDigest != completed.sourceSHA256 {
		return errors.New("source artifact contents changed after the build completed")
	}
	pathInfo, err := os.Lstat(completed.sourcePath)
	if err != nil {
		return fmt.Errorf("inspect source artifact after copy: %w", err)
	}
	if err := validateSourceArtifact(pathInfo); err != nil {
		return err
	}
	if !artifactInfoMatches(completed.sourceInfo, pathInfo) {
		return errors.New("source artifact path changed while it was being copied")
	}
	if err := destination.Chmod(completed.sourceInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("preserve destination permissions: %w", err)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync temporary Desktop copy: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(contents []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := reader.reader.Read(contents)
	if err == nil {
		if contextErr := reader.ctx.Err(); contextErr != nil {
			return read, contextErr
		}
	}
	return read, err
}
