package buildcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

var runtimeRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var runtimeVersionPattern = regexp.MustCompile(`^runtime-v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?$`)

// GeneralsX @feature Codex 10/10/2026 Keep prebuilt selection separate from compilation and require a named trusted publisher.
func validateRuntimeConfig(cfg config) error {
	if cfg.buildMode != "" && cfg.buildMode != "source" && cfg.buildMode != "prebuilt" {
		return errors.New("--build-mode must be source or prebuilt")
	}
	if cfg.buildMode != "prebuilt" {
		if cfg.runtimeOffline {
			return errors.New("--runtime-offline requires --build-mode prebuilt")
		}
		return nil
	}
	if !runtimeRepositoryPattern.MatchString(cfg.runtimeRepository) {
		return errors.New("--runtime-repo must name a public GitHub publisher as OWNER/REPO")
	}
	if cfg.runtimeVersion != "stable" && !runtimeVersionPattern.MatchString(cfg.runtimeVersion) {
		return errors.New("--runtime-version must be stable or an exact runtime-vX.Y.Z release tag")
	}
	if cfg.runtimeOffline && cfg.runtimeVersion == "stable" {
		return errors.New("--runtime-offline requires an exact runtime version; stable requires online release discovery")
	}
	if cfg.skipGameBuild || cfg.withOnlineServer || cfg.onlineServerSource != "" || cfg.keepWindowsStage {
		return errors.New("prebuilt mode does not support --skip-game-build, --with-online-server, --online-server-source, or --keep-windows-stage; use --build-mode source")
	}
	if cfg.onlineEndpoint != defaultOnlineEndpoint {
		return fmt.Errorf("prebuilt mode uses the published Online default %s; use source mode for a different saved default, or -onlineServer when launching the game", defaultOnlineEndpoint)
	}
	if pathsOverlap(cfg.cacheDir, filepath.Dir(cfg.output)) || (cfg.appOutput != "" && pathsOverlap(cfg.cacheDir, cfg.appOutput)) {
		return errors.New("prebuilt outputs must not overlap the runtime cache")
	}
	if cfg.appOutput != "" && pathsOverlap(cfg.output, cfg.appOutput) {
		return errors.New("raw SFX output must be outside the macOS app output")
	}
	return nil
}

func runtimeTarget(value target) string {
	switch value {
	case targetWindows:
		return "windows-x86"
	case targetMacOS:
		return "macos-arm64"
	default:
		return "linux-amd64"
	}
}

func runtimeAssetStem(version string, value target) string {
	return "generalsxzh-runtime-" + version + "-" + runtimeTarget(value)
}

type runtimeRelease struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Runtime downloads deliberately use a public, fixed GitHub API origin and no credentials.
// Tests replace the HTTP transport rather than opening an arbitrary runtime URL option.
func (app application) runtimeHTTP() *http.Client {
	client := app.http
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("runtime download redirected too many times")
		}
		if request.URL.Scheme != "https" || request.URL.User != nil {
			return errors.New("runtime downloads require HTTPS redirects without credentials")
		}
		return nil
	}
	return &copy
}

func (app application) runtimeGET(ctx context.Context, location string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "GeneralsX-builder/runtime-v1")
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := app.runtimeHTTP().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime request returned HTTP %s (check release availability and GitHub rate limits)", response.Status)
	}
	if response.ContentLength > limit {
		return nil, errors.New("runtime metadata exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("runtime metadata exceeds size limit")
	}
	return body, nil
}

func (app application) resolveRuntimeRelease(ctx context.Context) (runtimeRelease, error) {
	api := "https://api.github.com/repos/" + app.cfg.runtimeRepository + "/releases"
	if app.cfg.runtimeVersion != "stable" {
		body, err := app.runtimeGET(ctx, api+"/tags/"+url.PathEscape(app.cfg.runtimeVersion), 4<<20)
		if err != nil {
			return runtimeRelease{}, err
		}
		var result runtimeRelease
		if err := json.Unmarshal(body, &result); err != nil {
			return result, err
		}
		if result.Draft || result.Tag != app.cfg.runtimeVersion {
			return result, errors.New("GitHub returned an unexpected or draft runtime release")
		}
		return result, nil
	}
	var newest runtimeRelease
	for page := 1; page <= 10; page++ {
		body, err := app.runtimeGET(ctx, fmt.Sprintf("%s?per_page=100&page=%d", api, page), 8<<20)
		if err != nil {
			return newest, err
		}
		var releases []runtimeRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return newest, err
		}
		for _, release := range releases {
			if release.Draft || release.Prerelease || !runtimeVersionPattern.MatchString(release.Tag) {
				continue
			}
			if newest.Tag == "" || release.Published.After(newest.Published) {
				newest = release
			}
		}
		if len(releases) < 100 {
			break
		}
		if page == 10 {
			return newest, errors.New("runtime discovery exceeded 1000 releases; select --runtime-version explicitly")
		}
	}
	if newest.Tag == "" {
		return newest, errors.New("no published stable runtime exists; select a runtime release or use --build-mode source")
	}
	return newest, nil
}

func (app application) releaseAsset(release runtimeRelease, name string) (string, int64, error) {
	expected := "https://github.com/" + app.cfg.runtimeRepository + "/releases/download/" + release.Tag + "/" + name
	var found string
	var size int64
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		if found != "" || asset.URL != expected || asset.Size <= 0 {
			return "", 0, fmt.Errorf("invalid or duplicate runtime release asset %s", name)
		}
		found, size = asset.URL, asset.Size
	}
	if found == "" {
		return "", 0, fmt.Errorf("runtime release %s has no %s; choose another version or use --build-mode source", release.Tag, name)
	}
	return found, size, nil
}

func (app application) runtimeCache(version string) (string, error) {
	result := app.cfg.cacheDir
	parts := append([]string{"runtimes"}, strings.Split(app.cfg.runtimeRepository, "/")...)
	parts = append(parts, version, runtimeTarget(app.cfg.target))
	for _, part := range parts {
		result = filepath.Join(result, part)
		if err := ensurePrivateDirectory(result); err != nil {
			return "", err
		}
	}
	return result, nil
}

func (app application) resolveRuntime(ctx context.Context) (runtimebundle.Descriptor, string, error) {
	var descriptor runtimebundle.Descriptor
	version := app.cfg.runtimeVersion
	var release runtimeRelease
	var body []byte
	var err error
	if !app.cfg.runtimeOffline {
		release, err = app.resolveRuntimeRelease(ctx)
		if err != nil {
			return descriptor, "", err
		}
		version = release.Tag
	}
	cache, err := app.runtimeCache(version)
	if err != nil {
		return descriptor, "", err
	}
	metadataPath := filepath.Join(cache, "release.json")
	if app.cfg.runtimeOffline {
		if err := requireRegularFile(metadataPath); err != nil {
			return descriptor, "", fmt.Errorf("cached runtime metadata: %w", err)
		}
		file, err := os.Open(metadataPath)
		if err != nil {
			return descriptor, "", err
		}
		body, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
		_ = file.Close()
		if err != nil || len(body) > 8<<20 {
			return descriptor, "", errors.New("cannot read bounded cached runtime metadata")
		}
	} else {
		location, size, err := app.releaseAsset(release, runtimeAssetStem(version, app.cfg.target)+".json")
		if err != nil {
			return descriptor, "", err
		}
		if size > 8<<20 {
			return descriptor, "", errors.New("runtime manifest is too large")
		}
		body, err = app.runtimeGET(ctx, location, 8<<20)
		if err != nil {
			return descriptor, "", err
		}
		if int64(len(body)) != size {
			return descriptor, "", errors.New("runtime manifest size differs from release metadata")
		}
	}
	descriptor, err = runtimebundle.ReadDescriptor(bytes.NewReader(body))
	if err != nil {
		return descriptor, "", err
	}
	if descriptor.Version != version || descriptor.Repository != app.cfg.runtimeRepository || descriptor.Target != runtimeTarget(app.cfg.target) || descriptor.Archive.Name != runtimeAssetStem(version, app.cfg.target)+".tar.gz" {
		return descriptor, "", errors.New("runtime manifest does not match the selected repository, release, target, and archive")
	}
	if descriptor.OnlineEndpoint != app.cfg.onlineEndpoint {
		return descriptor, "", errors.New("runtime Online default differs from the requested endpoint; use --build-mode source")
	}
	if descriptor.OnlineCompatibility != 2 {
		return descriptor, "", errors.New("runtime has an unsupported Online compatibility generation; update the builder")
	}
	if err := app.validateRuntimePlatform(ctx, descriptor); err != nil {
		return descriptor, "", err
	}
	// A previously used release tag must never change identity beneath an explicit selection.
	if _, err := os.Lstat(metadataPath); err == nil {
		if err := requireRegularFile(metadataPath); err != nil {
			return descriptor, "", err
		}
		old, err := os.Open(metadataPath)
		if err != nil {
			return descriptor, "", err
		}
		previous, readErr := runtimebundle.ReadDescriptor(old)
		_ = old.Close()
		if readErr != nil {
			return descriptor, "", fmt.Errorf("cached runtime descriptor is damaged: %w", readErr)
		}
		if previous.Archive.SHA256 != descriptor.Archive.SHA256 || previous.SourceCommit != descriptor.SourceCommit {
			return descriptor, "", errors.New("published runtime changed under an existing version; use a newly versioned release")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return descriptor, "", err
	}
	archive := filepath.Join(cache, descriptor.Archive.SHA256+".tar.gz")
	reportProgress(app.reporter, ProgressPhaseRuntimeDownload, "Downloading "+version+" for "+descriptor.Target)
	if err := verifyRuntimeArchive(archive, descriptor); err == nil {
		fmt.Fprintf(app.runner.stdout, "Using verified cached runtime: %s\n", version)
	} else {
		if app.cfg.runtimeOffline {
			return descriptor, "", fmt.Errorf("cached runtime is missing or corrupt: %w", err)
		}
		location, size, err := app.releaseAsset(release, descriptor.Archive.Name)
		if err != nil {
			return descriptor, "", err
		}
		if size != descriptor.Archive.Size {
			return descriptor, "", errors.New("runtime archive size differs from the manifest")
		}
		if err := app.downloadRuntime(ctx, location, archive, descriptor); err != nil {
			return descriptor, "", err
		}
	}
	if !app.cfg.runtimeOffline {
		if err := publishRuntimeMetadata(metadataPath, body); err != nil {
			return descriptor, "", err
		}
	}
	fmt.Fprintf(app.runner.stdout, "Runtime: %s, engine %s, commit %s, SHA-256 %s\n", version, descriptor.EngineArch, descriptor.SourceCommit, descriptor.Archive.SHA256)
	return descriptor, archive, nil
}

func requireRegularFile(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	return nil
}

func verifyRuntimeArchive(name string, descriptor runtimebundle.Descriptor) error {
	if err := requireRegularFile(name); err != nil {
		return err
	}
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	if info.Size() != descriptor.Archive.Size {
		return errors.New("runtime archive size mismatch")
	}
	digest, err := fileSHA256(name)
	if err != nil {
		return err
	}
	if digest != descriptor.Archive.SHA256 {
		return errors.New("runtime archive SHA-256 mismatch")
	}
	return nil
}

func (app application) downloadRuntime(ctx context.Context, location, destination string, descriptor runtimebundle.Descriptor) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "GeneralsX-builder/runtime-v1")
	response, err := app.runtimeHTTP().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download runtime: HTTP %s", response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != descriptor.Archive.Size {
		return errors.New("runtime download Content-Length mismatch")
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".runtime-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	digest := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, descriptor.Archive.Size+1))
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != descriptor.Archive.Size || hex.EncodeToString(digest.Sum(nil)) != descriptor.Archive.SHA256 {
		return errors.New("runtime download failed size/SHA-256 verification")
	}
	if err := requireRegularFile(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		// Another builder may have atomically published the same immutable bytes.
		if verifyRuntimeArchive(destination, descriptor) == nil {
			return nil
		}
		return err
	}
	return nil
}

func publishRuntimeMetadata(destination string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".runtime-metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(body)
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		if previous, readErr := os.ReadFile(destination); readErr == nil && bytes.Equal(previous, body) {
			return nil
		}
		return err
	}
	return nil
}
