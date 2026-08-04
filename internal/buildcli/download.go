package buildcli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func downloadFile(ctx context.Context, client *http.Client, sourceURL, destination, expectedSHA256 string) error {
	if expectedSHA256 != "" {
		if digest, err := fileSHA256(destination); err == nil && digest == expectedSHA256 {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	request.Header.Set("User-Agent", "GeneralsX-builder/1")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", sourceURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %s", sourceURL, response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return fmt.Errorf("create temporary download: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary download: %w", err)
	}
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, digest), response.Body); err != nil {
		return fmt.Errorf("write download: %w", err)
	}
	actualDigest := hex.EncodeToString(digest.Sum(nil))
	if expectedSHA256 != "" && actualDigest != expectedSHA256 {
		return fmt.Errorf("download SHA-256 mismatch: expected %s, got %s", expectedSHA256, actualDigest)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close download: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish download: %w", err)
	}
	published = true
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func extractZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open ZIP archive: %w", err)
	}
	defer reader.Close()
	return publishExtractedDirectory(destination, func(stage string) error {
		type pendingSymlink struct {
			path   string
			target string
			name   string
		}
		var symlinks []pendingSymlink
		for _, member := range reader.File {
			path, err := secureArchivePath(stage, member.Name)
			if err != nil {
				return err
			}
			mode := member.Mode()
			if mode&os.ModeSymlink != 0 {
				source, err := member.Open()
				if err != nil {
					return err
				}
				contents, readErr := io.ReadAll(io.LimitReader(source, 4097))
				closeErr := source.Close()
				if readErr != nil {
					return readErr
				}
				if closeErr != nil {
					return closeErr
				}
				if len(contents) == 0 || len(contents) > 4096 {
					return fmt.Errorf("ZIP symlink %q has an invalid target length", member.Name)
				}
				target := string(contents)
				if err := validateArchiveSymlink(stage, path, target, member.Name); err != nil {
					return err
				}
				symlinks = append(symlinks, pendingSymlink{path: path, target: target, name: member.Name})
				continue
			}
			if member.FileInfo().IsDir() {
				if err := os.MkdirAll(path, 0o755); err != nil {
					return err
				}
				continue
			}
			if !mode.IsRegular() {
				return fmt.Errorf("ZIP member %q is not a regular file", member.Name)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			source, err := member.Open()
			if err != nil {
				return err
			}
			permissions := mode.Perm() &^ 0o022
			if permissions == 0 {
				permissions = 0o644
			}
			destinationFile, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
			if err != nil {
				_ = source.Close()
				return err
			}
			_, copyErr := io.Copy(destinationFile, source)
			closeErr := destinationFile.Close()
			sourceCloseErr := source.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if sourceCloseErr != nil {
				return sourceCloseErr
			}
		}
		for _, symlink := range symlinks {
			if err := os.MkdirAll(filepath.Dir(symlink.path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(filepath.FromSlash(symlink.target), symlink.path); err != nil {
				return fmt.Errorf("create ZIP symlink %q: %w", symlink.name, err)
			}
		}
		return nil
	})
}

func extractTarGzip(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	return publishExtractedDirectory(destination, func(stage string) error {
		type pendingSymlink struct {
			path   string
			target string
			name   string
		}
		var symlinks []pendingSymlink
		for {
			header, err := archive.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("read tar member: %w", err)
			}
			path, err := secureArchivePath(stage, header.Name)
			if err != nil {
				return err
			}
			switch header.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(path, 0o755); err != nil {
					return err
				}
			case tar.TypeReg, tar.TypeRegA:
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				permissions := os.FileMode(header.Mode)&0o755 | 0o400
				permissions &^= 0o022
				destinationFile, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
				if err != nil {
					return err
				}
				written, copyErr := io.CopyN(destinationFile, archive, header.Size)
				closeErr := destinationFile.Close()
				if copyErr != nil {
					return copyErr
				}
				if written != header.Size {
					return fmt.Errorf("tar member %q size changed", header.Name)
				}
				if closeErr != nil {
					return closeErr
				}
			case tar.TypeSymlink:
				if err := validateArchiveSymlink(stage, path, header.Linkname, header.Name); err != nil {
					return err
				}
				symlinks = append(symlinks, pendingSymlink{path: path, target: header.Linkname, name: header.Name})
			default:
				return fmt.Errorf("tar member %q has unsupported type %d", header.Name, header.Typeflag)
			}
		}
		for _, symlink := range symlinks {
			if err := os.MkdirAll(filepath.Dir(symlink.path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(filepath.FromSlash(symlink.target), symlink.path); err != nil {
				return fmt.Errorf("create tar symlink %q: %w", symlink.name, err)
			}
		}
		return nil
	})
}

func validateArchiveSymlink(root, path, target, memberName string) error {
	if target == "" || strings.ContainsRune(target, 0) || hasArchiveVolumePrefix(target) || strings.HasPrefix(target, "/") || filepath.IsAbs(target) || strings.Contains(target, "\\") {
		return fmt.Errorf("archive symlink %q has unsafe target %q", memberName, target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
	if !pathWithin(root, resolved) {
		return fmt.Errorf("archive symlink %q escapes extraction root", memberName)
	}
	return nil
}

func secureArchivePath(root, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || hasArchiveVolumePrefix(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || filepath.IsAbs(name) {
		return "", fmt.Errorf("archive member has unsafe path %q", name)
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == "." {
		return root, nil
	}
	path := filepath.Join(root, cleaned)
	if !pathWithin(root, path) {
		return "", fmt.Errorf("archive member %q escapes extraction root", name)
	}
	return path, nil
}

func hasArchiveVolumePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z'))
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func publishExtractedDirectory(destination string, extract func(string) error) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".extract-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return err
	}
	if err := extract(stage); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("refusing to replace existing extraction directory %q", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	published = true
	return nil
}
