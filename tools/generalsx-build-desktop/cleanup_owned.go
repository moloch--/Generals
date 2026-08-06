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
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

const cleanupMarkerReadLimit = 64 * 1024
const cleanupOwnershipLedgerReadLimit = 1024 * 1024

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

type cleanupCandidatePolicy uint8

const (
	cleanupOnlyIfCreated cleanupCandidatePolicy = iota + 1
	cleanupGeneratedOutput
	cleanupManagedSourceRoot
	cleanupManagedCacheRoot
	cleanupManagedCacheChild
)

type cleanupCandidateSnapshot struct {
	label, path       string
	sourceRepo        string
	sourceHead        string
	sourceGitState    string
	kind              cleanupPathKind
	markerPlacement   cleanupMarkerPlacement
	policy            cleanupCandidatePolicy
	existedBefore     bool
	persistedMarker   *cleanupOwnershipRecord
	nearestParentPath string
	nearestParentInfo fs.FileInfo
}

type buildCleanupSnapshot struct {
	hostOS, target, assetsPath, repoPath, cachePath, sourceRepo, ownershipLedgerPath string
	defaultRepo, defaultCache                                                        bool
	candidates                                                                       []cleanupCandidateSnapshot
}

type cleanupTreeFingerprint struct {
	digest  [sha256.Size]byte
	bytes   int64
	entries uint64
}

type cleanupOwnedPath struct {
	label, path, sourceRepo, sourceHead, sourceGitState, markerPath, markerRelative string
	kind                                                                            cleanupPathKind
	markerPlacement                                                                 cleanupMarkerPlacement
	markerContents                                                                  []byte
	markerInfo, rootInfo, parentInfo                                                fs.FileInfo
	parentPath, nearestParentPath                                                   string
	nearestParentInfo                                                               fs.FileInfo
	fingerprint                                                                     cleanupTreeFingerprint
	policy                                                                          cleanupCandidatePolicy
	existedBefore                                                                   bool
	previousMarker                                                                  *cleanupOwnershipRecord
	ownershipPersisted                                                              bool
}

type buildCleanupReceipt struct {
	jobID, hostOS, target, assetsPath, desktopPath, repoPath, cachePath, sourceRepo, ownershipLedgerPath string
	desktopInfo                                                                                          fs.FileInfo
	desktopSHA256                                                                                        [sha256.Size]byte
	desktopBytes                                                                                         int64
	prepared                                                                                             bool
	totalBytes                                                                                           int64
	candidates                                                                                           []cleanupOwnedPath
	defaultRepo, defaultCache, ownershipPersisted                                                        bool
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
	policy          cleanupCandidatePolicy
}

type cleanupOwnershipLedger struct {
	Version int                      `json:"version"`
	Records []cleanupOwnershipRecord `json:"records"`
}

type cleanupOwnershipRecord struct {
	Version         int                    `json:"version"`
	Label           string                 `json:"label"`
	Path            string                 `json:"path"`
	Kind            cleanupPathKind        `json:"kind"`
	MarkerPlacement cleanupMarkerPlacement `json:"markerPlacement"`
	Policy          cleanupCandidatePolicy `json:"policy"`
	SourceRepo      string                 `json:"sourceRepo,omitempty"`
	SourceHead      string                 `json:"sourceHead,omitempty"`
	SourceGitState  string                 `json:"sourceGitState,omitempty"`
	MarkerPath      string                 `json:"markerPath"`
	MarkerContents  []byte                 `json:"markerContents"`
}

// GeneralsX @feature Codex 05/08/2026 Snapshot builder destinations before build commands can create or reuse them.
func snapshotBuildCleanup(request BuildRequest, hostOS string) *buildCleanupSnapshot {
	return snapshotBuildCleanupWithOwnership(request, hostOS, "")
}

func snapshotBuildCleanupWithOwnership(request BuildRequest, hostOS, ownershipLedgerPath string) *buildCleanupSnapshot {
	if ownershipLedgerPath != "" {
		if canonical, err := cleanupCanonicalFuturePath(ownershipLedgerPath); err == nil {
			ownershipLedgerPath = canonical
		} else {
			ownershipLedgerPath = ""
		}
	}
	snapshot := &buildCleanupSnapshot{hostOS: hostOS, ownershipLedgerPath: ownershipLedgerPath}
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
	snapshot.repoPath = repo
	snapshot.sourceRepo = strings.TrimSpace(request.SourceRepo)
	cache, err := cleanupCanonicalFuturePath(request.CacheDir)
	if err != nil {
		return snapshot
	}
	snapshot.cachePath = cache
	defaultRepo, defaultCache, defaultsErr := cleanupCanonicalDefaultWorkspacePaths()
	if defaultsErr == nil {
		snapshot.defaultRepo = cleanupSamePath(repo, defaultRepo)
		snapshot.defaultCache = cleanupSamePath(cache, defaultCache)
	}
	if request.AssetsDir != "" {
		snapshot.assetsPath, _ = cleanupCanonicalFuturePath(request.AssetsDir)
	}
	ledger, ledgerErr := cleanupLoadOwnershipLedger(ownershipLedgerPath)
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
	steamCMDPolicy := cleanupOnlyIfCreated
	if cleanupSamePath(steamCMD, filepath.Join(cache, "steamcmd")) {
		steamCMDPolicy = cleanupManagedCacheChild
	}

	raw := []cleanupRawCandidate{
		{"Source checkout", repo, cleanupDirectory, cleanupMarkerInSourceGit, cleanupManagedSourceRoot},
		{"Builder cache", cache, cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheRoot},
	}
	if !request.SkipGameBuild {
		raw = append(raw, cleanupRawCandidate{"Target build directory", filepath.Join(repo, "build", cleanupBuildPreset(target)), cleanupDirectory, cleanupMarkerInside, cleanupGeneratedOutput})
	}
	raw = append(raw,
		cleanupRawCandidate{"Builder downloads", filepath.Join(cache, "downloads"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
		cleanupRawCandidate{"SteamCMD installation", steamCMD, cleanupDirectory, cleanupMarkerInside, steamCMDPolicy},
		cleanupRawCandidate{"Linux vcpkg cache", filepath.Join(cache, "vcpkg-linux"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
		cleanupRawCandidate{"vcpkg checkout", filepath.Join(cache, "vcpkg"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
		cleanupRawCandidate{"Managed Online server sources", filepath.Join(cache, "sources"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
		cleanupRawCandidate{"Managed Online server checkout", filepath.Join(cache, "sources", "generals-server"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
		cleanupRawCandidate{"Vulkan SDK installer cache", filepath.Join(cache, "vulkansdk-installer-1.4.341.1"), cleanupDirectory, cleanupMarkerInside, cleanupManagedCacheChild},
	)
	raw = append(raw, cleanupRawCandidate{"Raw SFX output", output, cleanupRegularFile, cleanupMarkerSidecar, cleanupGeneratedOutput})
	if target == "macos" {
		appOutput := request.AppOutput
		if appOutput == "" {
			appOutput = filepath.Join(repo, "build", "sfx", "GeneralsXZH.app")
		}
		if appOutput, err = cleanupCanonicalFuturePath(appOutput); err == nil {
			// A marker inside the signed app would invalidate its code signature.
			raw = append(raw, cleanupRawCandidate{"macOS app bundle", appOutput, cleanupDirectory, cleanupMarkerSidecar, cleanupGeneratedOutput})
		}
	}
	if target == "macos" || target == "linux" {
		raw = append(raw,
			cleanupRawCandidate{"SFX stage manifest", output + ".stage-contents.txt", cleanupRegularFile, cleanupMarkerSidecar, cleanupGeneratedOutput},
			cleanupRawCandidate{"Portable runtime bundle", filepath.Join(repo, cleanupPortableBundleName(target)), cleanupRegularFile, cleanupMarkerSidecar, cleanupGeneratedOutput},
		)
		if !request.SkipGameBuild {
			raw = append(raw, cleanupRawCandidate{"Target build log", filepath.Join(repo, cleanupBuildLog(target)), cleanupRegularFile, cleanupMarkerSidecar, cleanupGeneratedOutput})
		}
	}
	if request.WithOnlineServer {
		raw = append(raw, cleanupRawCandidate{"Bundled Online server build", filepath.Join(repo, "build", "bootstrap", "online-server", cleanupServerTarget(target)), cleanupDirectory, cleanupMarkerInside, cleanupGeneratedOutput})
	}

	seen := make(map[string]struct{}, len(raw))
	candidates := make([]cleanupCandidateSnapshot, 0, len(raw))
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
		_, statErr := os.Lstat(path)
		existedBefore := statErr == nil
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		parentPath, parentInfo, parentErr := cleanupNearestExistingParent(path)
		if parentErr != nil {
			continue
		}
		var persisted *cleanupOwnershipRecord
		if existedBefore {
			persisted = cleanupMatchingOwnershipRecord(ledger, path, rawCandidate.kind, rawCandidate.markerPlacement)
			if persisted == nil && rawCandidate.policy == cleanupManagedSourceRoot {
				persisted = cleanupMatchingOwnershipRecord(ledger, path, rawCandidate.kind, cleanupMarkerSidecar)
			}
			ledgerClaimsPath := cleanupOwnershipLedgerClaimsPath(ledger, path)
			eligible := persisted != nil || (!ledgerClaimsPath && rawCandidate.policy == cleanupGeneratedOutput)
			if persisted != nil && rawCandidate.policy == cleanupManagedSourceRoot &&
				!cleanupManagedBuilderCloneMatches(path, persisted.SourceRepo, persisted.SourceHead, persisted.SourceGitState) {
				eligible = false
			}
			if persisted != nil && rawCandidate.policy == cleanupManagedCacheRoot &&
				!cleanupDefaultCacheContainsOnlyManagedEntries(path) {
				eligible = false
			}
			if ledgerErr == nil && !ledgerClaimsPath && rawCandidate.policy == cleanupManagedCacheRoot && snapshot.defaultCache && cleanupDefaultCacheContainsOnlyManagedEntries(path) {
				eligible = true
			}
			if ledgerErr == nil && !ledgerClaimsPath && rawCandidate.policy == cleanupManagedCacheChild && snapshot.defaultCache {
				eligible = true
			}
			if ledgerErr == nil && !ledgerClaimsPath && rawCandidate.policy == cleanupManagedSourceRoot && snapshot.defaultRepo &&
				cleanupLooksLikeLegacyBuilderClone(path, request.SourceRepo) {
				eligible = true
			}
			if !eligible {
				continue
			}
		}
		markerPlacement := rawCandidate.markerPlacement
		candidateSourceRepo := ""
		candidateSourceHead := ""
		candidateSourceGitState := ""
		if rawCandidate.policy == cleanupManagedSourceRoot {
			candidateSourceRepo = snapshot.sourceRepo
			if head, ok := cleanupManagedBuilderCloneHead(path, candidateSourceRepo); ok {
				candidateSourceHead = head
				candidateSourceGitState, _ = cleanupManagedGitState(path)
			}
		}
		if persisted != nil {
			markerPlacement = persisted.MarkerPlacement
			candidateSourceRepo = persisted.SourceRepo
			candidateSourceHead = persisted.SourceHead
			candidateSourceGitState = persisted.SourceGitState
		}
		if rawCandidate.policy == cleanupManagedSourceRoot && existedBefore && candidateSourceGitState == "" {
			continue
		}
		candidates = append(candidates, cleanupCandidateSnapshot{
			label: rawCandidate.label, path: path, sourceRepo: candidateSourceRepo, sourceHead: candidateSourceHead, sourceGitState: candidateSourceGitState, kind: rawCandidate.kind,
			markerPlacement: markerPlacement, policy: rawCandidate.policy,
			existedBefore: existedBefore, persistedMarker: persisted,
			nearestParentPath: parentPath, nearestParentInfo: parentInfo,
		})
	}
	if ledgerErr == nil && ledger != nil {
		for index := range ledger.Records {
			record := &ledger.Records[index]
			key := cleanupPathKey(record.Path)
			if _, alreadyConsidered := seen[key]; alreadyConsidered {
				continue
			}
			if err := cleanupValidateOwnershipRecord(*record); err != nil {
				continue
			}
			if record.Policy == cleanupManagedSourceRoot && !cleanupManagedBuilderCloneMatches(record.Path, record.SourceRepo, record.SourceHead, record.SourceGitState) {
				continue
			}
			if record.Policy == cleanupManagedCacheRoot && !cleanupDefaultCacheContainsOnlyManagedEntries(record.Path) {
				continue
			}
			parentPath, parentInfo, err := cleanupNearestExistingParent(record.Path)
			if err != nil {
				continue
			}
			copy := *record
			copy.MarkerContents = append([]byte(nil), record.MarkerContents...)
			candidates = append(candidates, cleanupCandidateSnapshot{
				label: record.Label, path: record.Path, sourceRepo: record.SourceRepo, sourceHead: record.SourceHead, sourceGitState: record.SourceGitState, kind: record.Kind,
				markerPlacement: record.MarkerPlacement, policy: record.Policy,
				existedBefore: true, persistedMarker: &copy,
				nearestParentPath: parentPath, nearestParentInfo: parentInfo,
			})
			seen[key] = struct{}{}
		}
	}
	// Keep nested candidates as policy fallbacks. A source or cache root may be
	// builder-owned at snapshot time but gain user edits before review; only the
	// still-eligible parent should suppress its generated children.
	snapshot.candidates = candidates
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

func cleanupDedupeOwnedCandidates(candidates []cleanupOwnedPath) []cleanupOwnedPath {
	result := make([]cleanupOwnedPath, 0, len(candidates))
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

func cleanupCanonicalDefaultWorkspacePaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	userCache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	repo, err := cleanupCanonicalFuturePath(filepath.Join(home, "GeneralsX", "source"))
	if err != nil {
		return "", "", err
	}
	cache, err := cleanupCanonicalFuturePath(filepath.Join(userCache, "GeneralsX", "builder"))
	if err != nil {
		return "", "", err
	}
	return repo, cache, nil
}

func cleanupDefaultOwnershipLedgerPath() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return cleanupCanonicalFuturePath(filepath.Join(config, "GeneralsX", "Automated Build Tool", "cleanup-ownership-v1.json"))
}

func cleanupDefaultCacheContainsOnlyManagedEntries(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		managed := name == "downloads" || name == "steamcmd" || name == "vcpkg" || name == "vcpkg-linux" ||
			name == "sources" || strings.HasPrefix(name, "vulkansdk-installer-") ||
			strings.HasPrefix(name, ".generalsx-build-cleanup-")
		if !managed {
			return false
		}
	}
	return true
}

func cleanupLooksLikeLegacyBuilderClone(path, expectedRemote string) bool {
	const legacyCloneReadLimit = 1024 * 1024
	if expectedRemote != "https://github.com/moloch--/Generals.git" {
		return false
	}
	if !cleanupLooksLikeManagedBuilderClone(path, expectedRemote) {
		return false
	}
	gitDirectory := filepath.Join(path, ".git")
	reflog, err := cleanupReadSmallRegularFile(filepath.Join(gitDirectory, "logs", "HEAD"), legacyCloneReadLimit)
	return err == nil && cleanupReflogHasBuilderClone(string(reflog), expectedRemote)
}

func cleanupLooksLikeManagedBuilderClone(path, expectedRemote string) bool {
	_, ok := cleanupManagedBuilderCloneHead(path, expectedRemote)
	return ok
}

func cleanupManagedBuilderCloneMatches(path, expectedRemote, expectedHead, expectedGitState string) bool {
	head, ok := cleanupManagedBuilderCloneHead(path, expectedRemote)
	if !ok || expectedHead == "" || head != expectedHead || expectedGitState == "" {
		return false
	}
	gitState, ok := cleanupManagedGitState(path)
	return ok && gitState == expectedGitState
}

func cleanupManagedBuilderCloneHead(path, expectedRemote string) (string, bool) {
	const managedCloneReadLimit = 1024 * 1024
	expectedRemote = strings.TrimSpace(expectedRemote)
	if expectedRemote == "" || strings.ContainsRune(expectedRemote, 0) {
		return "", false
	}
	gitDirectory := filepath.Join(path, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	head, err := cleanupReadSmallRegularFile(filepath.Join(gitDirectory, "HEAD"), managedCloneReadLimit)
	headText := strings.TrimSpace(string(head))
	if err != nil || strings.HasPrefix(headText, "ref:") || len(headText) != 40 {
		return "", false
	}
	if _, err := hex.DecodeString(headText); err != nil {
		return "", false
	}
	fetchHead, err := cleanupReadSmallRegularFile(filepath.Join(gitDirectory, "FETCH_HEAD"), managedCloneReadLimit)
	if err != nil || !cleanupFetchHeadContainsCommit(string(fetchHead), headText) {
		return "", false
	}
	configuration, err := cleanupReadSmallRegularFile(filepath.Join(gitDirectory, "config"), managedCloneReadLimit)
	if err != nil || !cleanupGitConfigHasOrigin(string(configuration), expectedRemote) {
		return "", false
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false
	}
	command := exec.Command(gitPath, "--no-optional-locks", "-C", path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	configureDesktopBackgroundCommand(command)
	output, err := command.Output()
	if err != nil || len(output) != 0 || !cleanupManagedSourceHasOnlyGeneratedIgnoredPaths(path) {
		return "", false
	}
	return headText, true
}

func cleanupFetchHeadContainsCommit(fetchHead, commit string) bool {
	for _, line := range strings.Split(fetchHead, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == commit {
			return true
		}
	}
	return false
}

func cleanupManagedGitState(path string) (string, bool) {
	const refsReadLimit = 16 * 1024 * 1024
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false
	}
	command := exec.Command(gitPath, "--no-optional-locks", "-C", path, "for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(symref)")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	configureDesktopBackgroundCommand(command)
	refs, err := command.Output()
	if err != nil || len(refs) > refsReadLimit {
		return "", false
	}

	logsPath := filepath.Join(path, ".git", "logs")
	logsInfo, err := os.Lstat(logsPath)
	if err != nil || !logsInfo.IsDir() || logsInfo.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	logsRoot, err := os.OpenRoot(logsPath)
	if err != nil {
		return "", false
	}
	defer logsRoot.Close()
	openedLogs, err := logsRoot.Stat(".")
	if err != nil || !os.SameFile(logsInfo, openedLogs) {
		return "", false
	}
	logsFingerprint, err := cleanupFingerprintOpenedRoot(logsRoot, openedLogs)
	if err != nil {
		return "", false
	}

	digest := sha256.New()
	fmt.Fprintf(digest, "refs:%d\x00", len(refs))
	_, _ = digest.Write(refs)
	fmt.Fprintf(digest, "logs:%x:%d:%d\x00", logsFingerprint.digest, logsFingerprint.bytes, logsFingerprint.entries)
	return hex.EncodeToString(digest.Sum(nil)), true
}

func cleanupManagedSourceHasOnlyGeneratedIgnoredPaths(sourcePath string) bool {
	const ignoredPathsReadLimit = 16 * 1024 * 1024
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	command := exec.Command(gitPath, "--no-optional-locks", "-C", sourcePath, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	configureDesktopBackgroundCommand(command)
	output, err := command.Output()
	if err != nil || len(output) > ignoredPathsReadLimit {
		return false
	}
	for _, encoded := range strings.Split(string(output), "\x00") {
		if encoded == "" {
			continue
		}
		if !cleanupManagedSourceIgnoredPathIsGenerated(encoded) {
			return false
		}
	}
	return true
}

func cleanupManagedSourceIgnoredPathIsGenerated(encoded string) bool {
	path := strings.TrimSuffix(filepath.ToSlash(encoded), "/")
	if path == "" || strings.HasPrefix(path, "/") || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
		return false
	}
	for _, directory := range []string{
		"build", "logs", "vcpkg_installed", "GeneralsMD/logs", "Generals/logs",
		".flatpak-builder", "flatpak/staging", "ios/build", "ios/GeneralsXZH.xcodeproj",
		"tools/generalsx-build-desktop/build/bin",
		"tools/generalsx-build-desktop/frontend/node_modules",
		"tools/generalsx-build-desktop/frontend/coverage",
		"tools/generalsx-build-desktop/frontend/dist",
	} {
		if path == directory || strings.HasPrefix(path, directory+"/") {
			return true
		}
	}
	if strings.HasPrefix(path, "cmake-build-") {
		return true
	}
	for _, file := range []string{
		".ninja_deps", ".ninja_log", "build.ninja", "CMakeCache.txt", "Makefile",
		"cmake_install.cmake", "install_manifest.txt", "compile_commands.json",
		"CPackConfig.cmake", "CPackSourceConfig.cmake",
		"tools/generalsx-build-desktop/frontend/package.json.md5",
		cleanupPortableBundleName("macos"), cleanupPortableBundleName("linux"),
	} {
		if path == file {
			return true
		}
	}
	return false
}

func cleanupGitConfigHasOrigin(configuration, expectedRemote string) bool {
	section := ""
	for _, line := range strings.Split(configuration, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(trimmed)
			continue
		}
		if section != `[remote "origin"]` {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "url") && strings.TrimSpace(value) == expectedRemote {
			return true
		}
	}
	return false
}

func cleanupReflogHasBuilderClone(reflog, expectedRemote string) bool {
	sawClone := false
	for _, line := range strings.Split(reflog, "\n") {
		switch {
		case strings.Contains(line, "clone: from "+expectedRemote):
			sawClone = true
		case sawClone && strings.Contains(line, "checkout: moving from ") && strings.HasSuffix(line, " to FETCH_HEAD"):
			return true
		}
	}
	return false
}

func cleanupReadSmallRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("cleanup provenance file is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !artifactInfoMatches(info, opened) {
		return nil, errors.New("cleanup provenance file changed while opened")
	}
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(contents)) > limit {
		return nil, errors.New("cleanup provenance file is too large")
	}
	after, err := file.Stat()
	if err != nil || !artifactInfoMatches(info, after) {
		return nil, errors.New("cleanup provenance file changed while read")
	}
	return contents, nil
}

func cleanupLoadOwnershipLedger(path string) (*cleanupOwnershipLedger, error) {
	ledger := &cleanupOwnershipLedger{Version: 1}
	if path == "" {
		return ledger, nil
	}
	canonical, err := cleanupCanonicalFuturePath(path)
	if err != nil || !cleanupSamePath(canonical, path) || cleanupDangerousRoot(canonical) {
		return nil, errors.New("cleanup ownership ledger path is invalid")
	}
	contents, err := cleanupReadSmallRegularFile(canonical, cleanupOwnershipLedgerReadLimit)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		if _, statErr := os.Lstat(canonical); errors.Is(statErr, fs.ErrNotExist) {
			return ledger, nil
		}
		return nil, fmt.Errorf("read cleanup ownership ledger: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil || !cleanupOwnershipModeIsPrivate(info) {
		return nil, errors.New("cleanup ownership ledger is not private")
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(ledger); err != nil {
		return nil, fmt.Errorf("decode cleanup ownership ledger: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("cleanup ownership ledger contains trailing data")
	}
	if ledger.Version != 1 || len(ledger.Records) > 256 {
		return nil, errors.New("cleanup ownership ledger has an unsupported shape")
	}
	seen := make(map[string]struct{}, len(ledger.Records))
	for index := range ledger.Records {
		record := &ledger.Records[index]
		if err := cleanupValidateOwnershipRecordMetadata(*record); err != nil {
			return nil, fmt.Errorf("validate cleanup ownership record: %w", err)
		}
		key := cleanupPathKey(record.Path)
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("cleanup ownership ledger contains duplicate paths")
		}
		seen[key] = struct{}{}
	}
	return ledger, nil
}

func cleanupValidateOwnershipRecord(record cleanupOwnershipRecord) error {
	if err := cleanupValidateOwnershipRecordMetadata(record); err != nil {
		return err
	}
	actual, err := cleanupReadSmallRegularFile(record.MarkerPath, cleanupMarkerReadLimit)
	if err != nil || string(actual) != string(record.MarkerContents) {
		return errors.New("ownership marker and private ledger disagree")
	}
	root, err := os.Lstat(record.Path)
	if err != nil || root.Mode()&os.ModeSymlink != 0 ||
		(record.Kind == cleanupDirectory && !root.IsDir()) ||
		(record.Kind == cleanupRegularFile && !root.Mode().IsRegular()) {
		return errors.New("owned cleanup path is unavailable")
	}
	return nil
}

func cleanupValidateOwnershipRecordMetadata(record cleanupOwnershipRecord) error {
	if record.Version != 1 || strings.TrimSpace(record.Label) == "" ||
		(record.Kind != cleanupDirectory && record.Kind != cleanupRegularFile) ||
		(record.MarkerPlacement != cleanupMarkerInside && record.MarkerPlacement != cleanupMarkerInSourceGit && record.MarkerPlacement != cleanupMarkerSidecar) ||
		(record.Policy < cleanupOnlyIfCreated || record.Policy > cleanupManagedCacheChild) {
		return errors.New("ownership record has invalid metadata")
	}
	if record.Policy == cleanupManagedSourceRoot {
		if strings.TrimSpace(record.SourceRepo) == "" || len(record.SourceHead) != 40 || len(record.SourceGitState) != sha256.Size*2 {
			return errors.New("managed source ownership has no expected remote, commit, or Git state")
		}
		if _, err := hex.DecodeString(record.SourceHead); err != nil {
			return errors.New("managed source ownership commit is invalid")
		}
		if _, err := hex.DecodeString(record.SourceGitState); err != nil {
			return errors.New("managed source ownership Git state is invalid")
		}
	}
	path, err := cleanupCanonicalFuturePath(record.Path)
	if err != nil || !cleanupSamePath(path, record.Path) || cleanupDangerousRoot(path) {
		return errors.New("ownership record path is invalid")
	}
	markerPath, err := cleanupCanonicalFuturePath(record.MarkerPath)
	if err != nil || !cleanupSamePath(markerPath, record.MarkerPath) {
		return errors.New("ownership marker path is invalid")
	}
	markerParent := path
	if record.MarkerPlacement == cleanupMarkerInSourceGit {
		markerParent = filepath.Join(path, ".git")
	} else if record.MarkerPlacement == cleanupMarkerSidecar {
		markerParent = filepath.Dir(path)
	}
	if !cleanupSamePath(filepath.Dir(markerPath), markerParent) {
		return errors.New("ownership marker is outside its expected parent")
	}
	var marker cleanupOwnershipMarker
	decoder := json.NewDecoder(strings.NewReader(string(record.MarkerContents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return errors.New("ownership marker contents are invalid")
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("ownership marker contains trailing data")
	}
	if marker.Version != 1 || marker.Path != path || len(marker.Token) != 64 {
		return errors.New("ownership marker does not match its path")
	}
	if _, err := hex.DecodeString(marker.Token); err != nil {
		return errors.New("ownership marker token is invalid")
	}
	if filepath.Base(markerPath) != ".generalsx-build-cleanup-"+marker.Token+".json" {
		return errors.New("ownership marker filename does not match its token")
	}
	return nil
}

func cleanupMatchingOwnershipRecord(ledger *cleanupOwnershipLedger, path string, kind cleanupPathKind, placement cleanupMarkerPlacement) *cleanupOwnershipRecord {
	if ledger == nil {
		return nil
	}
	for index := range ledger.Records {
		record := &ledger.Records[index]
		if cleanupSamePath(record.Path, path) && record.Kind == kind && record.MarkerPlacement == placement && cleanupValidateOwnershipRecord(*record) == nil {
			copy := *record
			copy.MarkerContents = append([]byte(nil), record.MarkerContents...)
			return &copy
		}
	}
	return nil
}

func cleanupOwnershipLedgerClaimsPath(ledger *cleanupOwnershipLedger, path string) bool {
	if ledger == nil {
		return false
	}
	for _, record := range ledger.Records {
		if cleanupSamePath(record.Path, path) {
			return true
		}
	}
	return false
}

func cleanupLoadReceiptOwnership(receipt *buildCleanupReceipt) (*cleanupOwnershipLedger, error) {
	if receipt == nil || receipt.ownershipLedgerPath == "" {
		return &cleanupOwnershipLedger{Version: 1}, nil
	}
	return cleanupLoadOwnershipLedger(receipt.ownershipLedgerPath)
}

func cleanupValidateCandidateReceiptOwnership(receipt *buildCleanupReceipt, ledger *cleanupOwnershipLedger, candidate cleanupOwnedPath) error {
	if receipt == nil || receipt.ownershipLedgerPath == "" {
		return nil
	}
	if !candidate.ownershipPersisted {
		return fmt.Errorf("cleanup ownership was not persisted for %s", candidate.label)
	}
	record := cleanupMatchingOwnershipRecord(ledger, candidate.path, candidate.kind, candidate.markerPlacement)
	if record == nil || !cleanupSamePath(record.MarkerPath, candidate.markerPath) || string(record.MarkerContents) != string(candidate.markerContents) {
		return fmt.Errorf("private cleanup ownership record changed for %s", candidate.label)
	}
	return nil
}

func cleanupValidateReceiptOwnership(receipt *buildCleanupReceipt) (*cleanupOwnershipLedger, error) {
	ledger, err := cleanupLoadReceiptOwnership(receipt)
	if err != nil {
		return nil, err
	}
	for _, candidate := range receipt.candidates {
		if err := cleanupValidateCandidateReceiptOwnership(receipt, ledger, candidate); err != nil {
			return nil, err
		}
	}
	return ledger, nil
}

func cleanupWriteOwnershipLedger(path string, ledger *cleanupOwnershipLedger) error {
	if path == "" {
		return nil
	}
	if ledger == nil {
		return errors.New("cleanup ownership ledger is unavailable")
	}
	canonical, err := cleanupCanonicalFuturePath(path)
	if err != nil || !cleanupSamePath(canonical, path) || cleanupDangerousRoot(canonical) {
		return errors.New("cleanup ownership ledger path is invalid")
	}
	if len(ledger.Records) > 256 {
		return errors.New("cleanup ownership ledger has too many records")
	}
	seen := make(map[string]struct{}, len(ledger.Records))
	for _, record := range ledger.Records {
		if err := cleanupValidateOwnershipRecordMetadata(record); err != nil {
			return fmt.Errorf("validate cleanup ownership record before writing: %w", err)
		}
		key := cleanupPathKey(record.Path)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("cleanup ownership ledger contains duplicate paths")
		}
		seen[key] = struct{}{}
	}
	if len(ledger.Records) == 0 {
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("cleanup ownership ledger path changed")
			}
			return os.Remove(path)
		} else if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else {
			return err
		}
	}
	parent := filepath.Dir(canonical)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || !cleanupOwnershipModeIsPrivate(parentInfo) {
		return errors.New("cleanup ownership ledger directory is not private")
	}
	ledger.Version = 1
	contents, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	token, err := cleanupRandomHex(16)
	if err != nil {
		return err
	}
	temporary := filepath.Join(parent, ".cleanup-ownership-"+token+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if existing, err := os.Lstat(path); err == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 || !cleanupOwnershipModeIsPrivate(existing) {
			return errors.New("cleanup ownership ledger path changed")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func cleanupOwnershipModeIsPrivate(info fs.FileInfo) bool {
	// Windows does not expose ACL privacy through fs.FileMode permission bits.
	// The ledger still has to be a non-symlink entry beneath the current user's
	// config directory; Unix hosts additionally require no group/other access.
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0
}

// GeneralsX @feature Codex 05/08/2026 Convert successfully created absent destinations into ownership receipts.
func finalizeBuildCleanup(jobID string, snapshot *buildCleanupSnapshot) *buildCleanupReceipt {
	receipt := &buildCleanupReceipt{jobID: jobID}
	if snapshot == nil || strings.TrimSpace(jobID) == "" {
		return receipt
	}
	receipt.hostOS, receipt.target, receipt.assetsPath = snapshot.hostOS, snapshot.target, snapshot.assetsPath
	receipt.repoPath, receipt.cachePath, receipt.ownershipLedgerPath = snapshot.repoPath, snapshot.cachePath, snapshot.ownershipLedgerPath
	receipt.sourceRepo = snapshot.sourceRepo
	receipt.defaultRepo, receipt.defaultCache = snapshot.defaultRepo, snapshot.defaultCache
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
		label: candidate.label, path: candidate.path, sourceRepo: candidate.sourceRepo, sourceHead: candidate.sourceHead, sourceGitState: candidate.sourceGitState, kind: candidate.kind,
		markerPlacement: candidate.markerPlacement, rootInfo: rootInfo,
		parentPath: parentPath, parentInfo: parentInfo,
		nearestParentPath: candidate.nearestParentPath, nearestParentInfo: candidate.nearestParentInfo,
		policy: candidate.policy, existedBefore: candidate.existedBefore, previousMarker: candidate.persistedMarker,
	}
	if candidate.policy == cleanupManagedSourceRoot {
		currentHead, ok := cleanupManagedBuilderCloneHead(candidate.path, candidate.sourceRepo)
		if !ok || (candidate.sourceHead != "" && currentHead != candidate.sourceHead) {
			return cleanupOwnedPath{}, errors.New("managed source checkout changed before ownership was recorded")
		}
		currentGitState, ok := cleanupManagedGitState(candidate.path)
		if !ok || (candidate.sourceGitState != "" && currentGitState != candidate.sourceGitState) {
			return cleanupOwnedPath{}, errors.New("managed source Git metadata changed before ownership was recorded")
		}
		owned.sourceHead = currentHead
		owned.sourceGitState = currentGitState
	}
	markerParent, markerRelativeParent := candidate.path, "."
	if candidate.markerPlacement == cleanupMarkerInSourceGit {
		markerParent, markerRelativeParent = filepath.Join(candidate.path, ".git"), ".git"
	} else if candidate.markerPlacement == cleanupMarkerSidecar {
		markerParent, markerRelativeParent = parentPath, ""
	}
	markerParentInfo, err := os.Lstat(markerParent)
	if candidate.markerPlacement == cleanupMarkerInSourceGit &&
		(err != nil || !markerParentInfo.IsDir() || markerParentInfo.Mode()&os.ModeSymlink != 0) {
		// A cancelled clone may have created its destination before Git created a
		// usable .git directory. Keep provenance beside that partial root so a
		// later successful build can still offer it for explicit cleanup.
		owned.markerPlacement = cleanupMarkerSidecar
		markerParent, markerRelativeParent = parentPath, ""
		markerParentInfo, err = os.Lstat(markerParent)
	}
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

func persistBuildCleanupOwnership(receipt *buildCleanupReceipt, includeExistingGenerated bool) error {
	if receipt == nil || receipt.ownershipLedgerPath == "" {
		return nil
	}
	ledger, err := cleanupLoadOwnershipLedger(receipt.ownershipLedgerPath)
	if err != nil {
		return err
	}
	selected := make([]int, 0, len(receipt.candidates))
	for index := range receipt.candidates {
		candidate := &receipt.candidates[index]
		if candidate.existedBefore && candidate.policy == cleanupGeneratedOutput && !includeExistingGenerated {
			continue
		}
		record := cleanupOwnershipRecord{
			Version: 1, Label: candidate.label, Path: candidate.path, Kind: candidate.kind,
			MarkerPlacement: candidate.markerPlacement, Policy: candidate.policy, SourceRepo: candidate.sourceRepo, SourceHead: candidate.sourceHead, SourceGitState: candidate.sourceGitState, MarkerPath: candidate.markerPath,
			MarkerContents: append([]byte(nil), candidate.markerContents...),
		}
		if err := cleanupValidateOwnershipRecord(record); err != nil {
			return err
		}
		selected = append(selected, index)
	}
	if len(selected) == 0 {
		discardBuildCleanupReceipt(receipt)
		return nil
	}
	kept := make([]cleanupOwnershipRecord, 0, len(ledger.Records)+len(selected))
	for _, record := range ledger.Records {
		replaced := false
		for _, index := range selected {
			candidate := receipt.candidates[index]
			if cleanupPathsOverlap(candidate.path, record.Path) {
				replaced = true
				break
			}
		}
		if !replaced {
			kept = append(kept, record)
		}
	}
	for _, index := range selected {
		candidate := &receipt.candidates[index]
		kept = append(kept, cleanupOwnershipRecord{
			Version: 1, Label: candidate.label, Path: candidate.path, Kind: candidate.kind,
			MarkerPlacement: candidate.markerPlacement, Policy: candidate.policy, SourceRepo: candidate.sourceRepo, SourceHead: candidate.sourceHead, SourceGitState: candidate.sourceGitState, MarkerPath: candidate.markerPath,
			MarkerContents: append([]byte(nil), candidate.markerContents...),
		})
	}
	sort.Slice(kept, func(i, j int) bool { return cleanupPathKey(kept[i].Path) < cleanupPathKey(kept[j].Path) })
	ledger.Records = kept
	if err := cleanupWriteOwnershipLedger(receipt.ownershipLedgerPath, ledger); err != nil {
		return err
	}
	for _, index := range selected {
		candidate := &receipt.candidates[index]
		candidate.ownershipPersisted = true
		if previous := candidate.previousMarker; previous != nil && !cleanupSamePath(previous.MarkerPath, candidate.markerPath) {
			if contents, readErr := cleanupReadSmallRegularFile(previous.MarkerPath, cleanupMarkerReadLimit); readErr == nil && string(contents) == string(previous.MarkerContents) {
				_ = os.Remove(previous.MarkerPath)
			}
		}
	}
	receipt.ownershipPersisted = true
	discardBuildCleanupReceipt(receipt)
	return nil
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
		if candidate.ownershipPersisted {
			continue
		}
		if cleanupValidateMarkerPath(candidate.markerPath, candidate.markerInfo, candidate.markerContents) == nil {
			_ = os.Remove(candidate.markerPath)
		}
	}
}

func cleanupValidateDisposalPolicy(receipt *buildCleanupReceipt, candidate cleanupOwnedPath) error {
	switch candidate.policy {
	case cleanupManagedSourceRoot:
		if !cleanupManagedBuilderCloneMatches(candidate.path, candidate.sourceRepo, candidate.sourceHead, candidate.sourceGitState) {
			return errors.New("managed source checkout is no longer a clean detached builder clone")
		}
	case cleanupManagedCacheRoot:
		if !cleanupDefaultCacheContainsOnlyManagedEntries(candidate.path) {
			return errors.New("managed cache contains an unknown top-level entry")
		}
	}
	return nil
}

// GeneralsX @feature Codex 05/08/2026 Prepare an immutable cleanup plan only after the Desktop SFX remains verified.
func prepareBuildCleanup(receipt *buildCleanupReceipt, desktop *completedArtifact) (BuildCleanupPlan, *buildCleanupReceipt, error) {
	if receipt == nil || strings.TrimSpace(receipt.jobID) == "" {
		return BuildCleanupPlan{}, nil, errors.New("build cleanup receipt is unavailable")
	}
	ownershipLedger, err := cleanupLoadReceiptOwnership(receipt)
	if err != nil {
		return BuildCleanupPlan{}, nil, err
	}
	desktopPath, desktopResolved, desktopInfo, err := cleanupVerifyDesktopArtifact(context.Background(), receipt.jobID, desktop)
	if err != nil {
		return BuildCleanupPlan{}, nil, err
	}
	prepared := cleanupCloneReceipt(receipt)
	prepared.desktopPath, prepared.desktopInfo, prepared.desktopSHA256, prepared.desktopBytes, prepared.prepared = desktopPath, desktopInfo, desktop.sourceSHA256, desktop.sourceBytes, true
	prepared.totalBytes = 0
	prepared.candidates = prepared.candidates[:0]
	plan := BuildCleanupPlan{JobID: receipt.jobID, DesktopCopyPath: desktopPath, Entries: make([]BuildCleanupEntry, 0, len(receipt.candidates))}
	eligible := make([]cleanupOwnedPath, 0, len(receipt.candidates))
	for _, candidate := range receipt.candidates {
		if _, statErr := os.Lstat(candidate.path); errors.Is(statErr, fs.ErrNotExist) {
			continue
		} else if statErr != nil {
			return BuildCleanupPlan{}, nil, fmt.Errorf("inspect %s: %w", candidate.label, statErr)
		}
		if err := cleanupValidateCandidateReceiptOwnership(receipt, ownershipLedger, candidate); err != nil {
			return BuildCleanupPlan{}, nil, err
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
		if cleanupValidateDisposalPolicy(receipt, candidate) != nil {
			// User edits in a managed source root or unknown cache siblings revoke
			// whole-root cleanup without blocking independent generated paths.
			continue
		}
		eligible = append(eligible, candidate)
	}
	for _, candidate := range cleanupDedupeOwnedCandidates(eligible) {
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
		if previous := clone.candidates[index].previousMarker; previous != nil {
			copy := *previous
			copy.MarkerContents = append([]byte(nil), previous.MarkerContents...)
			clone.candidates[index].previousMarker = &copy
		}
	}
	return &clone
}

func cleanupVerifyDesktopArtifact(ctx context.Context, jobID string, desktop *completedArtifact) (string, string, fs.FileInfo, error) {
	if desktop == nil || desktop.jobID != jobID {
		return "", "", nil, errors.New("the matching Desktop build artifact is not verified")
	}
	if err := revalidateCompletedArtifact(ctx, desktop); err != nil {
		return "", "", nil, fmt.Errorf("revalidate Desktop build artifact: %w", err)
	}
	absolute, err := filepath.Abs(desktop.sourcePath)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Desktop build artifact: %w", err)
	}
	absolute = filepath.Clean(absolute)
	current, err := os.Lstat(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspect Desktop build artifact: %w", err)
	}
	if desktop.sourceInfo.IsDir() {
		if err := validateBundleArtifactRoot(absolute, current); err != nil {
			return "", "", nil, fmt.Errorf("validate Desktop application bundle: %w", err)
		}
	} else if err := validateSourceArtifact(current); err != nil {
		return "", "", nil, fmt.Errorf("validate Desktop build artifact: %w", err)
	}
	if !artifactInfoMatches(desktop.sourceInfo, current) {
		return "", "", nil, errors.New("Desktop build artifact changed after it was verified")
	}
	opened, err := os.Open(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("open Desktop build artifact: %w", err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil || !artifactInfoMatches(desktop.sourceInfo, openedInfo) {
		return "", "", nil, errors.New("Desktop build artifact changed before cleanup")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Desktop build artifact: %w", err)
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
	if recorded == nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !artifactInfoMatches(recorded, current) {
		return errors.New("ownership marker identity changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ownership marker: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !artifactInfoMatches(recorded, openedInfo) {
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
	ownershipLedger, err := cleanupValidateReceiptOwnership(receipt)
	if err != nil {
		return "", fmt.Errorf("validate private cleanup ownership: %w", err)
	}
	desktop := &completedArtifact{
		jobID: receipt.jobID, target: receipt.target, sourcePath: receipt.desktopPath, sourceInfo: receipt.desktopInfo,
		sourceSHA256: receipt.desktopSHA256, sourceBytes: receipt.desktopBytes,
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
		if err := cleanupValidateDisposalPolicy(receipt, candidate); err != nil {
			return "", fmt.Errorf("validate %s disposal policy: %w", candidate.label, err)
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
		if err := cleanupValidateDisposalPolicy(receipt, candidate); err != nil {
			return "", fmt.Errorf("revalidate %s disposal policy: %w", candidate.label, err)
		}
		if err := cleanupExecuteOwnedPath(ctx, candidate); err != nil {
			return "", fmt.Errorf("remove %s: %w", candidate.label, err)
		}
	}
	if err := cleanupPruneEmptyBuildParents(receipt); err != nil {
		return "", fmt.Errorf("prune empty build directories: %w", err)
	}
	if _, _, _, err := cleanupVerifyDesktopArtifact(ctx, receipt.jobID, desktop); err != nil {
		return "", fmt.Errorf("Desktop SFX verification after cleanup: %w", err)
	}
	if err := cleanupConsumeOwnershipLedger(receipt, ownershipLedger); err != nil {
		return "", fmt.Errorf("update private cleanup ownership: %w", err)
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
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !artifactInfoMatches(candidate.markerInfo, current) {
		return errors.New("internal ownership marker identity changed")
	}
	file, err := root.Open(candidate.markerRelative)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !artifactInfoMatches(candidate.markerInfo, opened) {
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

func cleanupConsumeOwnershipLedger(receipt *buildCleanupReceipt, ledger *cleanupOwnershipLedger) error {
	if receipt == nil || receipt.ownershipLedgerPath == "" {
		return nil
	}
	if ledger == nil {
		return errors.New("cleanup ownership ledger is unavailable")
	}
	kept := make([]cleanupOwnershipRecord, 0, len(ledger.Records))
	for _, record := range ledger.Records {
		removed := false
		for _, candidate := range receipt.candidates {
			if cleanupPathsOverlap(candidate.path, record.Path) {
				removed = true
				break
			}
		}
		if !removed {
			kept = append(kept, record)
		}
	}
	ledger.Records = kept
	return cleanupWriteOwnershipLedger(receipt.ownershipLedgerPath, ledger)
}

func cleanupPruneEmptyBuildParents(receipt *buildCleanupReceipt) error {
	if receipt == nil {
		return nil
	}
	for _, candidate := range receipt.candidates {
		parent := filepath.Dir(candidate.path)
		boundary := ""
		switch {
		case receipt.repoPath != "" && cleanupPathWithin(receipt.repoPath, candidate.path) && !cleanupSamePath(receipt.repoPath, candidate.path):
			boundary = receipt.repoPath
		case receipt.cachePath != "" && cleanupPathWithin(receipt.cachePath, candidate.path) && !cleanupSamePath(receipt.cachePath, candidate.path):
			boundary = receipt.cachePath
		}
		for boundary != "" && cleanupPathWithin(boundary, parent) && !cleanupSamePath(boundary, parent) {
			removed, err := cleanupRemoveEmptyDirectory(parent)
			if err != nil {
				return err
			}
			if !removed {
				break
			}
			parent = filepath.Dir(parent)
		}
	}
	if receipt.defaultRepo {
		if _, err := os.Lstat(receipt.repoPath); errors.Is(err, fs.ErrNotExist) {
			_, _ = cleanupRemoveEmptyDirectory(filepath.Dir(receipt.repoPath))
		}
	}
	if receipt.defaultCache {
		if _, err := os.Lstat(receipt.cachePath); errors.Is(err, fs.ErrNotExist) {
			_, _ = cleanupRemoveEmptyDirectory(filepath.Dir(receipt.cachePath))
		}
	}
	return nil
}

func cleanupRemoveEmptyDirectory(path string) (bool, error) {
	path, err := cleanupCanonicalFuturePath(path)
	if err != nil || cleanupDangerousRoot(path) {
		return false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	parentPath := filepath.Dir(path)
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return false, nil
	}
	defer parent.Close()
	openedParent, err := parent.Stat(".")
	if err != nil || !os.SameFile(parentInfo, openedParent) {
		return false, nil
	}
	base := filepath.Base(path)
	current, err := parent.Lstat(base)
	if err != nil || !os.SameFile(info, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if err := parent.Remove(base); err != nil {
		// Unknown entries, concurrent use, and permission changes all make pruning
		// optional. Never broaden cleanup to force removal of a parent.
		return false, nil
	}
	return true, nil
}
