package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

const cleanupMarkerReadLimit = 64 * 1024

type cleanupPathKind uint8

const (
	cleanupDirectory cleanupPathKind = iota + 1
	cleanupRegularFile
)

type cleanupMarkerPlacement uint8

const (
	cleanupMarkerInside cleanupMarkerPlacement = iota + 1
	cleanupMarkerInSourceGit
	cleanupMarkerSidecar
)

type cleanupCandidateSnapshot struct {
	label, path       string
	kind              cleanupPathKind
	markerPlacement   cleanupMarkerPlacement
	nearestParentPath string
	nearestParentInfo fs.FileInfo
}

type buildCleanupSnapshot struct {
	hostOS, target, assetsPath string
	candidates                 []cleanupCandidateSnapshot
}

type cleanupTreeFingerprint struct {
	digest  [sha256.Size]byte
	bytes   int64
	entries uint64
}

type cleanupOwnedPath struct {
	label, path, markerPath, markerRelative string
	kind                                    cleanupPathKind
	markerPlacement                         cleanupMarkerPlacement
	markerContents                          []byte
	markerInfo, rootInfo, parentInfo        fs.FileInfo
	parentPath, nearestParentPath           string
	nearestParentInfo                       fs.FileInfo
	fingerprint                             cleanupTreeFingerprint
}

type buildCleanupReceipt struct {
	jobID, hostOS, target, assetsPath, desktopPath string
	desktopInfo                                    fs.FileInfo
	desktopSHA256                                  [sha256.Size]byte
	prepared                                       bool
	totalBytes                                     int64
	candidates                                     []cleanupOwnedPath
}

// GeneralsX @feature Codex 05/08/2026 Describe only builder-owned paths approved for post-build removal.
type BuildCleanupPlan struct {
	JobID           string              `json:"jobId"`
	PlanID          string              `json:"planId"`
	DesktopCopyPath string              `json:"desktopCopyPath"`
	Entries         []BuildCleanupEntry `json:"entries"`
}

type BuildCleanupEntry struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type cleanupOwnershipMarker struct {
	Version int    `json:"version"`
	JobID   string `json:"jobId"`
	Token   string `json:"token"`
	Path    string `json:"path"`
}

type cleanupRawCandidate struct {
	label, path     string
	kind            cleanupPathKind
	markerPlacement cleanupMarkerPlacement
}

// GeneralsX @feature Codex 05/08/2026 Snapshot absent builder destinations before build commands can create them.
func snapshotBuildCleanup(request BuildRequest, hostOS string) *buildCleanupSnapshot {
	snapshot := &buildCleanupSnapshot{hostOS: hostOS}
	if request.DryRun {
		return snapshot
	}
	target, err := resolveTarget(request.Target, hostOS)
	if err != nil {
		return snapshot
	}
	snapshot.target = target
	repo, err := cleanupCanonicalFuturePath(request.RepoRoot)
	if err != nil {
		return snapshot
	}
	cache, err := cleanupCanonicalFuturePath(request.CacheDir)
	if err != nil {
		return snapshot
	}
	if request.AssetsDir != "" {
		snapshot.assetsPath, _ = cleanupCanonicalFuturePath(request.AssetsDir)
	}
	output := request.Output
	if output == "" {
		output = filepath.Join(repo, "build", "sfx", cleanupDefaultOutputName(target))
	}
	output, err = cleanupCanonicalFuturePath(output)
	if err != nil {
		return snapshot
	}
	steamCMD := request.SteamCMDDir
	if steamCMD == "" {
		steamCMD = filepath.Join(cache, "steamcmd")
	}
	steamCMD, err = cleanupCanonicalFuturePath(steamCMD)
	if err != nil {
		return snapshot
	}

	raw := []cleanupRawCandidate{
		{"Source checkout", repo, cleanupDirectory, cleanupMarkerInSourceGit},
		{"Builder cache", cache, cleanupDirectory, cleanupMarkerInside},
	}
	if !request.SkipGameBuild {
		raw = append(raw, cleanupRawCandidate{"Target build directory", filepath.Join(repo, "build", cleanupBuildPreset(target)), cleanupDirectory, cleanupMarkerInside})
	}
	raw = append(raw, cleanupRawCandidate{"Builder downloads", filepath.Join(cache, "downloads"), cleanupDirectory, cleanupMarkerInside})
	raw = append(raw, cleanupRawCandidate{"SteamCMD installation", steamCMD, cleanupDirectory, cleanupMarkerInside})
	if target == "linux" {
		raw = append(raw, cleanupRawCandidate{"Linux vcpkg cache", filepath.Join(cache, "vcpkg-linux"), cleanupDirectory, cleanupMarkerInside})
	} else {
		raw = append(raw, cleanupRawCandidate{"vcpkg checkout", filepath.Join(cache, "vcpkg"), cleanupDirectory, cleanupMarkerInside})
	}
	if request.WithOnlineServer && request.OnlineServerSource == "" {
		raw = append(raw,
			cleanupRawCandidate{"Managed Online server sources", filepath.Join(cache, "sources"), cleanupDirectory, cleanupMarkerInside},
			cleanupRawCandidate{"Managed Online server checkout", filepath.Join(cache, "sources", "generals-server"), cleanupDirectory, cleanupMarkerInside},
		)
	}
	if target == "macos" {
		raw = append(raw, cleanupRawCandidate{
			"Vulkan SDK installer cache", filepath.Join(cache, "vulkansdk-installer-1.4.341.1"), cleanupDirectory, cleanupMarkerInside,
		})
	}
	raw = append(raw, cleanupRawCandidate{"Raw SFX output", output, cleanupRegularFile, cleanupMarkerSidecar})
	if target == "macos" {
		appOutput := request.AppOutput
		if appOutput == "" {
			appOutput = filepath.Join(repo, "build", "sfx", "GeneralsXZH.app")
		}
		if appOutput, err = cleanupCanonicalFuturePath(appOutput); err == nil {
			// A marker inside the signed app would invalidate its code signature.
			raw = append(raw, cleanupRawCandidate{"macOS app bundle", appOutput, cleanupDirectory, cleanupMarkerSidecar})
		}
	}
	if target == "macos" || target == "linux" {
		raw = append(raw,
			cleanupRawCandidate{"SFX stage manifest", output + ".stage-contents.txt", cleanupRegularFile, cleanupMarkerSidecar},
			cleanupRawCandidate{"Portable runtime bundle", filepath.Join(repo, cleanupPortableBundleName(target)), cleanupRegularFile, cleanupMarkerSidecar},
		)
		if !request.SkipGameBuild {
			raw = append(raw, cleanupRawCandidate{"Target build log", filepath.Join(repo, cleanupBuildLog(target)), cleanupRegularFile, cleanupMarkerSidecar})
		}
	}
	if request.WithOnlineServer {
		raw = append(raw, cleanupRawCandidate{"Bundled Online server build", filepath.Join(repo, "build", "bootstrap", "online-server", cleanupServerTarget(target)), cleanupDirectory, cleanupMarkerInside})
	}

	seen := make(map[string]struct{}, len(raw))
	absent := make([]cleanupCandidateSnapshot, 0, len(raw))
	for _, rawCandidate := range raw {
		path, pathErr := cleanupCanonicalFuturePath(rawCandidate.path)
		if pathErr != nil || cleanupDangerousRoot(path) {
			continue
		}
		key := cleanupPathKey(path)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, statErr := os.Lstat(path); statErr == nil || !errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		parentPath, parentInfo, parentErr := cleanupNearestExistingParent(path)
		if parentErr != nil {
			continue
		}
		absent = append(absent, cleanupCandidateSnapshot{
			label: rawCandidate.label, path: path, kind: rawCandidate.kind,
			markerPlacement:   rawCandidate.markerPlacement,
			nearestParentPath: parentPath, nearestParentInfo: parentInfo,
		})
	}
	snapshot.candidates = cleanupDedupeCandidates(absent)
	return snapshot
}

func cleanupDefaultOutputName(target string) string {
	switch target {
	case "macos":
		return "GeneralsXZH-macos-arm64-sfx"
	case "windows":
		return "GeneralsXZH-windows-amd64-sfx.exe"
	default:
		return "GeneralsXZH-linux-amd64-sfx"
	}
}

func cleanupBuildPreset(target string) string {
	switch target {
	case "macos":
		return "macos-vulkan"
	case "windows":
		return "win32-vcpkg"
	default:
		return "linux64-deploy"
	}
}

func cleanupPortableBundleName(target string) string {
	if target == "macos" {
		return "GeneralsXZH-macos-arm64.zip"
	}
	return "GeneralsXZH-linux-x86_64.tar.gz"
}

func cleanupBuildLog(target string) string {
	if target == "macos" {
		return filepath.Join("logs", "build_zh_macos-vulkan.log")
	}
	return filepath.Join("logs", "build_zh_linux64-deploy_docker.log")
}

func cleanupServerTarget(target string) string {
	switch target {
	case "macos":
		return "darwin-arm64"
	case "windows":
		return "windows-amd64"
	default:
		return "linux-amd64"
	}
}

func cleanupCanonicalFuturePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("cleanup path is empty or invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolveDesktopPath(absolute)), nil
}

func cleanupNearestExistingParent(path string) (string, fs.FileInfo, error) {
	for candidate := filepath.Dir(path); ; candidate = filepath.Dir(candidate) {
		info, err := os.Lstat(candidate)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", nil, fmt.Errorf("nearest existing parent %q is not a real directory", candidate)
			}
			return candidate, info, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", nil, fmt.Errorf("no existing parent for %q", path)
		}
	}
}

func cleanupDedupeCandidates(candidates []cleanupCandidateSnapshot) []cleanupCandidateSnapshot {
	result := make([]cleanupCandidateSnapshot, 0, len(candidates))
	for index, candidate := range candidates {
		covered := false
		for otherIndex, other := range candidates {
			if index != otherIndex && other.kind == cleanupDirectory && !cleanupSamePath(other.path, candidate.path) && cleanupPathWithin(other.path, candidate.path) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, candidate)
		}
	}
	return result
}

func cleanupPathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func cleanupPathKey(path string) string {
	key := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func cleanupSamePath(first, second string) bool {
	relative, err := filepath.Rel(first, second)
	return err == nil && relative == "."
}

func cleanupPathsOverlap(first, second string) bool {
	return cleanupPathWithin(first, second) || cleanupPathWithin(second, first)
}

func cleanupDangerousRoot(path string) bool {
	clean := filepath.Clean(path)
	return clean == "" || filepath.Dir(clean) == clean
}

// GeneralsX @feature Codex 05/08/2026 Convert successfully created absent destinations into ownership receipts.
func finalizeBuildCleanup(jobID string, snapshot *buildCleanupSnapshot) *buildCleanupReceipt {
	receipt := &buildCleanupReceipt{jobID: jobID}
	if snapshot == nil || strings.TrimSpace(jobID) == "" {
		return receipt
	}
	receipt.hostOS, receipt.target, receipt.assetsPath = snapshot.hostOS, snapshot.target, snapshot.assetsPath
	for _, candidate := range snapshot.candidates {
		if owned, err := cleanupFinalizeCandidate(jobID, candidate); err == nil {
			receipt.candidates = append(receipt.candidates, owned)
		}
	}
	return receipt
}

func cleanupFinalizeCandidate(jobID string, candidate cleanupCandidateSnapshot) (cleanupOwnedPath, error) {
	if err := cleanupValidateRecordedDirectory(candidate.nearestParentPath, candidate.nearestParentInfo); err != nil {
		return cleanupOwnedPath{}, err
	}
	rootInfo, err := cleanupValidateRealPath(candidate.nearestParentPath, candidate.path, candidate.kind)
	if err != nil {
		return cleanupOwnedPath{}, err
	}
	parentPath := filepath.Dir(candidate.path)
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return cleanupOwnedPath{}, errors.New("cleanup parent is not a real directory")
	}
	owned := cleanupOwnedPath{
		label: candidate.label, path: candidate.path, kind: candidate.kind,
		markerPlacement: candidate.markerPlacement, rootInfo: rootInfo,
		parentPath: parentPath, parentInfo: parentInfo,
		nearestParentPath: candidate.nearestParentPath, nearestParentInfo: candidate.nearestParentInfo,
	}
	markerParent, markerRelativeParent := candidate.path, "."
	if candidate.markerPlacement == cleanupMarkerInSourceGit {
		markerParent, markerRelativeParent = filepath.Join(candidate.path, ".git"), ".git"
	} else if candidate.markerPlacement == cleanupMarkerSidecar {
		markerParent, markerRelativeParent = parentPath, ""
	}
	markerParentInfo, err := os.Lstat(markerParent)
	if err != nil || !markerParentInfo.IsDir() || markerParentInfo.Mode()&os.ModeSymlink != 0 {
		return cleanupOwnedPath{}, errors.New("ownership marker parent is not a real directory")
	}
	token, err := cleanupRandomHex(32)
	if err != nil {
		return cleanupOwnedPath{}, err
	}
	markerName := ".generalsx-build-cleanup-" + token + ".json"
	owned.markerPath = filepath.Join(markerParent, markerName)
	if markerRelativeParent != "" {
		owned.markerRelative = filepath.Join(markerRelativeParent, markerName)
	}
	owned.markerContents, err = json.Marshal(cleanupOwnershipMarker{Version: 1, JobID: jobID, Token: token, Path: candidate.path})
	if err != nil {
		return cleanupOwnedPath{}, err
	}
	owned.markerContents = append(owned.markerContents, '\n')
	if err := cleanupCreateMarker(owned.markerPath, owned.markerContents); err != nil {
		return cleanupOwnedPath{}, err
	}
	keepMarker := false
	defer func() {
		if !keepMarker {
			_ = os.Remove(owned.markerPath)
		}
	}()
	owned.markerInfo, err = os.Lstat(owned.markerPath)
	if err != nil || !owned.markerInfo.Mode().IsRegular() || owned.markerInfo.Mode()&os.ModeSymlink != 0 {
		return cleanupOwnedPath{}, errors.New("ownership marker is not a real file")
	}
	owned.rootInfo, err = os.Lstat(candidate.path)
	if err != nil || !os.SameFile(rootInfo, owned.rootInfo) {
		return cleanupOwnedPath{}, errors.New("cleanup root changed while ownership was recorded")
	}
	owned.parentInfo, err = os.Lstat(parentPath)
	if err != nil || !os.SameFile(parentInfo, owned.parentInfo) {
		return cleanupOwnedPath{}, errors.New("cleanup parent changed while ownership was recorded")
	}
	keepMarker = true
	return owned, nil
}

func cleanupCreateMarker(path string, contents []byte) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	return file.Sync()
}

func cleanupRandomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func discardBuildCleanupReceipt(receipt *buildCleanupReceipt) {
	if receipt == nil {
		return
	}
	for _, candidate := range receipt.candidates {
		if cleanupValidateMarkerPath(candidate.markerPath, candidate.markerInfo, candidate.markerContents) == nil {
			_ = os.Remove(candidate.markerPath)
		}
	}
}

// GeneralsX @feature Codex 05/08/2026 Prepare an immutable cleanup plan only after the Desktop SFX remains verified.
func prepareBuildCleanup(receipt *buildCleanupReceipt, desktop *completedArtifact) (BuildCleanupPlan, *buildCleanupReceipt, error) {
	if receipt == nil || strings.TrimSpace(receipt.jobID) == "" {
		return BuildCleanupPlan{}, nil, errors.New("build cleanup receipt is unavailable")
	}
	desktopPath, desktopResolved, desktopInfo, err := cleanupVerifyDesktopArtifact(context.Background(), receipt.jobID, desktop)
	if err != nil {
		return BuildCleanupPlan{}, nil, err
	}
	prepared := cleanupCloneReceipt(receipt)
	prepared.desktopPath, prepared.desktopInfo, prepared.desktopSHA256, prepared.prepared = desktopPath, desktopInfo, desktop.sourceSHA256, true
	prepared.totalBytes = 0
	prepared.candidates = prepared.candidates[:0]
	plan := BuildCleanupPlan{JobID: receipt.jobID, DesktopCopyPath: desktopPath, Entries: make([]BuildCleanupEntry, 0, len(receipt.candidates))}
	for _, candidate := range receipt.candidates {
		if _, statErr := os.Lstat(candidate.path); errors.Is(statErr, fs.ErrNotExist) {
			if candidate.markerPlacement == cleanupMarkerSidecar && cleanupValidateMarkerPath(candidate.markerPath, candidate.markerInfo, candidate.markerContents) == nil {
				_ = os.Remove(candidate.markerPath)
			}
			continue
		}
		currentInfo, err := cleanupValidateOwnedPath(candidate)
		if err != nil {
			return BuildCleanupPlan{}, nil, fmt.Errorf("validate %s: %w", candidate.label, err)
		}
		if cleanupPathsOverlap(candidate.path, desktopResolved) ||
			(receipt.assetsPath != "" && cleanupPathsOverlap(candidate.path, receipt.assetsPath)) ||
			(candidate.kind == cleanupRegularFile && os.SameFile(currentInfo, desktopInfo)) {
			// Keep the base receipt and its marker intact. A preview must not
			// mutate ownership state or prevent a later protected-path retry.
			continue
		}
		fingerprint, err := cleanupFingerprintOwnedPath(candidate)
		if err != nil {
			return BuildCleanupPlan{}, nil, fmt.Errorf("inventory %s: %w", candidate.label, err)
		}
		candidate.fingerprint = fingerprint
		if fingerprint.bytes > math.MaxInt64-prepared.totalBytes {
			return BuildCleanupPlan{}, nil, errors.New("cleanup byte count overflow")
		}
		prepared.totalBytes += fingerprint.bytes
		prepared.candidates = append(prepared.candidates, candidate)
		plan.Entries = append(plan.Entries, BuildCleanupEntry{Label: candidate.label, Path: candidate.path})
	}
	return plan, prepared, nil
}

func cleanupCloneReceipt(receipt *buildCleanupReceipt) *buildCleanupReceipt {
	clone := *receipt
	clone.candidates = append([]cleanupOwnedPath(nil), receipt.candidates...)
	for index := range clone.candidates {
		clone.candidates[index].markerContents = append([]byte(nil), clone.candidates[index].markerContents...)
	}
	return &clone
}

func cleanupVerifyDesktopArtifact(ctx context.Context, jobID string, desktop *completedArtifact) (string, string, fs.FileInfo, error) {
	if desktop == nil || desktop.jobID != jobID {
		return "", "", nil, errors.New("the matching Desktop SFX copy is not verified")
	}
	if err := revalidateCompletedArtifact(ctx, desktop); err != nil {
		return "", "", nil, fmt.Errorf("revalidate Desktop SFX copy: %w", err)
	}
	absolute, err := filepath.Abs(desktop.sourcePath)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Desktop SFX copy: %w", err)
	}
	absolute = filepath.Clean(absolute)
	current, err := os.Lstat(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspect Desktop SFX copy: %w", err)
	}
	if err := validateSourceArtifact(current); err != nil {
		return "", "", nil, fmt.Errorf("validate Desktop SFX copy: %w", err)
	}
	if !artifactInfoMatches(desktop.sourceInfo, current) {
		return "", "", nil, errors.New("Desktop SFX copy changed after it was verified")
	}
	opened, err := os.Open(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("open Desktop SFX copy: %w", err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil || !artifactInfoMatches(desktop.sourceInfo, openedInfo) {
		return "", "", nil, errors.New("Desktop SFX copy changed before cleanup")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Desktop SFX copy: %w", err)
	}
	return absolute, filepath.Clean(resolved), openedInfo, nil
}

func cleanupValidateOwnedPath(candidate cleanupOwnedPath) (fs.FileInfo, error) {
	if err := cleanupValidateRecordedDirectory(candidate.nearestParentPath, candidate.nearestParentInfo); err != nil {
		return nil, err
	}
	current, err := cleanupValidateRealPath(candidate.nearestParentPath, candidate.path, candidate.kind)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(candidate.rootInfo, current) {
		return nil, errors.New("cleanup root identity changed")
	}
	if err := cleanupValidateRecordedDirectory(candidate.parentPath, candidate.parentInfo); err != nil {
		return nil, fmt.Errorf("cleanup parent changed: %w", err)
	}
	if err := cleanupValidateMarkerPath(candidate.markerPath, candidate.markerInfo, candidate.markerContents); err != nil {
		return nil, err
	}
	return current, nil
}

func cleanupValidateRecordedDirectory(path string, recorded fs.FileInfo) error {
	if recorded == nil {
		return errors.New("recorded directory identity is unavailable")
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(recorded, current) {
		return fmt.Errorf("directory identity changed at %q", path)
	}
	return nil
}

func cleanupValidateRealPath(parent, path string, kind cleanupPathKind) (fs.FileInfo, error) {
	relative, err := filepath.Rel(parent, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("cleanup path %q escaped its recorded parent", path)
	}
	current := parent
	parts := strings.Split(relative, string(filepath.Separator))
	var info fs.FileInfo
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("cleanup path contains an invalid component")
		}
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("cleanup path contains symbolic link %q", current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("cleanup path component %q is not a directory", current)
		}
	}
	if kind == cleanupDirectory && !info.IsDir() {
		return nil, errors.New("cleanup root is not a real directory")
	}
	if kind == cleanupRegularFile && !info.Mode().IsRegular() {
		return nil, errors.New("cleanup root is not a regular file")
	}
	if kind != cleanupDirectory && kind != cleanupRegularFile {
		return nil, errors.New("cleanup root has an unsupported kind")
	}
	return info, nil
}

func cleanupValidateMarkerPath(path string, recorded fs.FileInfo, contents []byte) error {
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect ownership marker: %w", err)
	}
	if recorded == nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(recorded, current) {
		return errors.New("ownership marker identity changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ownership marker: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(recorded, openedInfo) {
		return errors.New("ownership marker changed before it could be read")
	}
	actual, err := io.ReadAll(io.LimitReader(file, cleanupMarkerReadLimit+1))
	if err != nil {
		return fmt.Errorf("read ownership marker: %w", err)
	}
	if len(actual) > cleanupMarkerReadLimit || string(actual) != string(contents) {
		return errors.New("ownership marker contents changed")
	}
	return nil
}

func cleanupFingerprintOwnedPath(candidate cleanupOwnedPath) (cleanupTreeFingerprint, error) {
	current, err := os.Lstat(candidate.path)
	if err != nil {
		return cleanupTreeFingerprint{}, err
	}
	if !os.SameFile(candidate.rootInfo, current) {
		return cleanupTreeFingerprint{}, errors.New("cleanup root identity changed before inventory")
	}
	if candidate.kind == cleanupRegularFile {
		return cleanupFingerprintAbsoluteFile(candidate.path, current)
	}
	root, err := os.OpenRoot(candidate.path)
	if err != nil {
		return cleanupTreeFingerprint{}, err
	}
	defer root.Close()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(candidate.rootInfo, rootInfo) {
		return cleanupTreeFingerprint{}, errors.New("cleanup root changed while it was opened")
	}
	return cleanupFingerprintOpenedRoot(root, rootInfo)
}

type cleanupFingerprintBuilder struct {
	h       hash.Hash
	bytes   int64
	entries uint64
}

func cleanupFingerprintAbsoluteFile(path string, expected fs.FileInfo) (cleanupTreeFingerprint, error) {
	builder := cleanupFingerprintBuilder{h: sha256.New()}
	if err := builder.addMetadata(".", expected, ""); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return cleanupTreeFingerprint{}, err
	}
	defer file.Close()
	if err := cleanupHashOpenedFile(file, expected, builder.h); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	return builder.result(), nil
}

func cleanupFingerprintOpenedRoot(root *os.Root, rootInfo fs.FileInfo) (cleanupTreeFingerprint, error) {
	builder := cleanupFingerprintBuilder{h: sha256.New()}
	if err := builder.addMetadata(".", rootInfo, ""); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	rootDevice, hasRootDevice := cleanupDeviceNumber(rootInfo)
	if err := cleanupFingerprintDirectory(root, ".", rootInfo, rootDevice, hasRootDevice, &builder); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	return builder.result(), nil
}

func cleanupFingerprintDirectory(root *os.Root, relative string, expected fs.FileInfo, rootDevice uint64, hasRootDevice bool, builder *cleanupFingerprintBuilder) error {
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	openedInfo, statErr := directory.Stat()
	if statErr != nil || !openedInfo.IsDir() || !os.SameFile(expected, openedInfo) {
		directory.Close()
		return errors.New("cleanup directory changed while inventoried")
	}
	if hasRootDevice {
		if device, ok := cleanupDeviceNumber(openedInfo); !ok || device != rootDevice {
			directory.Close()
			return errors.New("cleanup tree crosses a filesystem boundary")
		}
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		child := entry.Name()
		if relative != "." {
			child = filepath.Join(relative, child)
		}
		info, err := root.Lstat(child)
		if err != nil {
			return err
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = root.Readlink(child)
			if err != nil {
				return err
			}
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("cleanup tree contains unsupported special entry %q", child)
		}
		if err := builder.addMetadata(child, info, linkTarget); err != nil {
			return err
		}
		if info.IsDir() {
			if err := cleanupFingerprintDirectory(root, child, info, rootDevice, hasRootDevice, builder); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			file, err := root.Open(child)
			if err != nil {
				return err
			}
			hashErr := cleanupHashOpenedFile(file, info, builder.h)
			closeErr := file.Close()
			if hashErr != nil {
				return hashErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}

func cleanupHashOpenedFile(file *os.File, expected fs.FileInfo, destination hash.Hash) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) || opened.Size() != expected.Size() || !opened.ModTime().Equal(expected.ModTime()) {
		return errors.New("cleanup file changed while opened for inventory")
	}
	if _, err := io.Copy(destination, file); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(expected, after) || after.Size() != expected.Size() || !after.ModTime().Equal(expected.ModTime()) {
		return errors.New("cleanup file changed while its contents were inventoried")
	}
	return nil
}

func (builder *cleanupFingerprintBuilder) addMetadata(path string, info fs.FileInfo, linkTarget string) error {
	if info.Mode().IsRegular() {
		if info.Size() < 0 || info.Size() > math.MaxInt64-builder.bytes {
			return errors.New("cleanup byte count overflow")
		}
		builder.bytes += info.Size()
	}
	builder.entries++
	fmt.Fprintf(builder.h, "%d:%s\x00%d\x00%d\x00%d\x00%d:%s\x00", len(path), path, uint32(info.Mode()), info.Size(), info.ModTime().UnixNano(), len(linkTarget), linkTarget)
	return nil
}

func (builder cleanupFingerprintBuilder) result() cleanupTreeFingerprint {
	var digest [sha256.Size]byte
	copy(digest[:], builder.h.Sum(nil))
	return cleanupTreeFingerprint{digest: digest, bytes: builder.bytes, entries: builder.entries}
}

func cleanupDeviceNumber(info fs.FileInfo) (uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName("Dev")
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(field.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	default:
		return 0, false
	}
}

// GeneralsX @feature Codex 05/08/2026 Quarantine and remove unchanged receipted paths through os.Root handles.
func executeBuildCleanup(ctx context.Context, receipt *buildCleanupReceipt) (string, error) {
	if receipt == nil || !receipt.prepared {
		return "", errors.New("build cleanup plan was not prepared")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	desktop := &completedArtifact{
		jobID: receipt.jobID, sourcePath: receipt.desktopPath, sourceInfo: receipt.desktopInfo,
		sourceSHA256: receipt.desktopSHA256,
	}
	_, desktopResolved, desktopInfo, err := cleanupVerifyDesktopArtifact(ctx, receipt.jobID, desktop)
	if err != nil {
		return "", err
	}
	// Validate every entry before deleting the first one, then validate each
	// entry again immediately before its own quarantine operation.
	for _, candidate := range receipt.candidates {
		current, err := cleanupValidateOwnedPath(candidate)
		if err != nil {
			return "", fmt.Errorf("validate %s before cleanup: %w", candidate.label, err)
		}
		if cleanupPathsOverlap(candidate.path, desktopResolved) ||
			(receipt.assetsPath != "" && cleanupPathsOverlap(candidate.path, receipt.assetsPath)) ||
			(candidate.kind == cleanupRegularFile && os.SameFile(current, desktopInfo)) {
			return "", fmt.Errorf("cleanup path %q now overlaps protected data", candidate.path)
		}
		fingerprint, err := cleanupFingerprintOwnedPath(candidate)
		if err != nil || fingerprint != candidate.fingerprint {
			return "", fmt.Errorf("%s changed after cleanup was reviewed", candidate.label)
		}
	}
	for _, candidate := range receipt.candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := cleanupExecuteOwnedPath(ctx, candidate); err != nil {
			return "", fmt.Errorf("remove %s: %w", candidate.label, err)
		}
	}
	if _, _, _, err := cleanupVerifyDesktopArtifact(ctx, receipt.jobID, desktop); err != nil {
		return "", fmt.Errorf("Desktop SFX verification after cleanup: %w", err)
	}
	if len(receipt.candidates) == 0 {
		return "No builder-owned paths were eligible for cleanup.", nil
	}
	pathWord := "paths"
	if len(receipt.candidates) == 1 {
		pathWord = "path"
	}
	return fmt.Sprintf("Removed %d builder-owned %s (%d bytes).", len(receipt.candidates), pathWord, receipt.totalBytes), nil
}

func cleanupExecuteOwnedPath(ctx context.Context, candidate cleanupOwnedPath) error {
	if _, err := cleanupValidateOwnedPath(candidate); err != nil {
		return err
	}
	fingerprint, err := cleanupFingerprintOwnedPath(candidate)
	if err != nil || fingerprint != candidate.fingerprint {
		return errors.New("cleanup path changed immediately before quarantine")
	}
	parent, err := os.OpenRoot(candidate.parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	openedParent, err := parent.Stat(".")
	if err != nil || !os.SameFile(candidate.parentInfo, openedParent) {
		return errors.New("cleanup parent changed while opened")
	}
	base := filepath.Base(candidate.path)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return errors.New("cleanup root has an invalid basename")
	}
	current, err := parent.Lstat(base)
	if err != nil || !os.SameFile(candidate.rootInfo, current) {
		return errors.New("cleanup root changed before quarantine")
	}
	token, err := cleanupRandomHex(16)
	if err != nil {
		return err
	}
	quarantine := ".generalsx-cleanup-quarantine-" + token
	if _, err := parent.Lstat(quarantine); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return errors.New("cleanup quarantine name unexpectedly exists")
	}
	if err := parent.Rename(base, quarantine); err != nil {
		return err
	}
	renamed := true
	restore := func(cause error) error {
		if !renamed {
			return cause
		}
		if _, statErr := parent.Lstat(base); errors.Is(statErr, fs.ErrNotExist) {
			if restoreErr := parent.Rename(quarantine, base); restoreErr != nil {
				return errors.Join(cause, fmt.Errorf("restore quarantine %q: %w", filepath.Join(candidate.parentPath, quarantine), restoreErr))
			}
			renamed = false
		}
		return cause
	}
	quarantinedInfo, err := parent.Lstat(quarantine)
	if err != nil || !os.SameFile(candidate.rootInfo, quarantinedInfo) {
		return restore(errors.New("quarantined cleanup root has the wrong identity"))
	}
	if err := ctx.Err(); err != nil {
		return restore(err)
	}

	if candidate.kind == cleanupRegularFile {
		fingerprint, err := cleanupFingerprintRootFile(parent, quarantine, quarantinedInfo)
		if err != nil || fingerprint != candidate.fingerprint {
			return restore(errors.New("quarantined file changed before removal"))
		}
		if err := parent.Remove(quarantine); err != nil {
			return restore(err)
		}
		renamed = false
	} else {
		quarantinedRoot, err := parent.OpenRoot(quarantine)
		if err != nil {
			return restore(err)
		}
		rootInfo, statErr := quarantinedRoot.Stat(".")
		if statErr != nil || !os.SameFile(candidate.rootInfo, rootInfo) {
			quarantinedRoot.Close()
			return restore(errors.New("quarantined directory changed while opened"))
		}
		if candidate.markerPlacement != cleanupMarkerSidecar {
			if err := cleanupValidateMarkerInRoot(quarantinedRoot, candidate); err != nil {
				quarantinedRoot.Close()
				return restore(err)
			}
		}
		fingerprint, err := cleanupFingerprintOpenedRoot(quarantinedRoot, rootInfo)
		if err != nil || fingerprint != candidate.fingerprint {
			quarantinedRoot.Close()
			return restore(errors.New("quarantined directory changed before removal"))
		}
		entries, err := cleanupRootEntries(quarantinedRoot)
		if err != nil {
			quarantinedRoot.Close()
			return restore(err)
		}
		for _, name := range entries {
			if err := ctx.Err(); err != nil {
				quarantinedRoot.Close()
				return restore(err)
			}
			if err := quarantinedRoot.RemoveAll(name); err != nil {
				quarantinedRoot.Close()
				return restore(err)
			}
		}
		if err := quarantinedRoot.Close(); err != nil {
			return restore(err)
		}
		remaining, err := parent.Lstat(quarantine)
		if err != nil || !os.SameFile(candidate.rootInfo, remaining) {
			return restore(errors.New("quarantined directory changed before final removal"))
		}
		if err := parent.Remove(quarantine); err != nil {
			return restore(err)
		}
		renamed = false
	}
	if candidate.markerPlacement == cleanupMarkerSidecar && cleanupValidateMarkerPath(candidate.markerPath, candidate.markerInfo, candidate.markerContents) == nil {
		_ = os.Remove(candidate.markerPath)
	}
	return nil
}

func cleanupFingerprintRootFile(root *os.Root, name string, expected fs.FileInfo) (cleanupTreeFingerprint, error) {
	builder := cleanupFingerprintBuilder{h: sha256.New()}
	if err := builder.addMetadata(".", expected, ""); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	file, err := root.Open(name)
	if err != nil {
		return cleanupTreeFingerprint{}, err
	}
	defer file.Close()
	if err := cleanupHashOpenedFile(file, expected, builder.h); err != nil {
		return cleanupTreeFingerprint{}, err
	}
	return builder.result(), nil
}

func cleanupValidateMarkerInRoot(root *os.Root, candidate cleanupOwnedPath) error {
	if candidate.markerRelative == "" {
		return errors.New("internal ownership marker has no relative path")
	}
	current, err := root.Lstat(candidate.markerRelative)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(candidate.markerInfo, current) {
		return errors.New("internal ownership marker identity changed")
	}
	file, err := root.Open(candidate.markerRelative)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(candidate.markerInfo, opened) {
		return errors.New("internal ownership marker changed while opened")
	}
	contents, err := io.ReadAll(io.LimitReader(file, cleanupMarkerReadLimit+1))
	if err != nil || len(contents) > cleanupMarkerReadLimit || string(contents) != string(candidate.markerContents) {
		return errors.New("internal ownership marker contents changed")
	}
	return nil
}

func cleanupRootEntries(root *os.Root) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}
