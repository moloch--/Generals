// GeneralsX @feature OpenAI 30/07/2026 Extracts authenticated bundle tars without following archive-controlled paths.
package bundle

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type deferredSymlink struct {
	name   string
	target string
}

type deferredDirectory struct {
	name string
	mode os.FileMode
}

// ExtractTar verifies every tar member against manifest while streaming it
// into dest. Dest must not exist or must be an empty, non-symlink directory.
// Symlinks are created only after all regular files have been written.
func ExtractTar(r io.Reader, dest string, manifest Manifest) error {
	if r == nil {
		return fmt.Errorf("bundle tar reader is nil")
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate extraction manifest: %w", err)
	}
	root, err := prepareDestination(dest)
	if err != nil {
		return err
	}

	tr := tar.NewReader(r)
	var directories []deferredDirectory
	var symlinks []deferredSymlink
	var totalSize int64
	epoch := time.Unix(manifest.Epoch, 0).UTC()

	for i, expected := range manifest.Entries {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("bundle tar ended before manifest entry %q", expected.Path)
		}
		if err != nil {
			return fmt.Errorf("read tar header for manifest entry %q: %w", expected.Path, err)
		}
		if err := verifyHeader(header, expected, epoch, manifest.TargetOS); err != nil {
			return fmt.Errorf("tar entry %d: %w", i, err)
		}
		outputPath, err := destinationPath(root, expected.Path)
		if err != nil {
			return err
		}

		switch expected.Type {
		case EntryDirectory:
			if err := os.Mkdir(outputPath, 0o700); err != nil {
				return fmt.Errorf("create bundle directory %q: %w", expected.Path, err)
			}
			directories = append(directories, deferredDirectory{
				name: outputPath,
				mode: os.FileMode(expected.Mode),
			})
		case EntryFile:
			if expected.Size > defaultLimits.MaxTotalSize-totalSize {
				return fmt.Errorf("extracted data exceeds %d bytes", defaultLimits.MaxTotalSize)
			}
			if err := extractRegularFile(tr, outputPath, expected, epoch); err != nil {
				return err
			}
			totalSize += expected.Size
		case EntrySymlink:
			symlinks = append(symlinks, deferredSymlink{
				name:   outputPath,
				target: filepath.FromSlash(expected.LinkTarget),
			})
		default:
			return fmt.Errorf("unsupported manifest entry type %q", expected.Type)
		}
	}

	if header, err := tr.Next(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("read trailing tar data: %w", err)
		}
		return fmt.Errorf("tar has unexpected entry %q after manifest contents", header.Name)
	}
	if totalSize != manifest.TotalSize {
		return fmt.Errorf("extracted %d bytes; manifest declares %d", totalSize, manifest.TotalSize)
	}

	for _, link := range symlinks {
		if err := os.Symlink(link.target, link.name); err != nil {
			return fmt.Errorf("create bundle symlink %q: %w", link.name, err)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return len(directories[i].name) > len(directories[j].name)
	})
	for _, directory := range directories {
		if err := os.Chmod(directory.name, directory.mode); err != nil {
			return fmt.Errorf("set bundle directory mode %q: %w", directory.name, err)
		}
		if err := os.Chtimes(directory.name, epoch, epoch); err != nil {
			return fmt.Errorf("set bundle directory timestamp %q: %w", directory.name, err)
		}
	}
	if err := verifyExtractedEntrypoint(root, manifest); err != nil {
		return err
	}
	return nil
}

func prepareDestination(dest string) (string, error) {
	if dest == "" {
		return "", fmt.Errorf("bundle extraction destination is empty")
	}
	absolute, err := filepath.Abs(dest)
	if err != nil {
		return "", fmt.Errorf("resolve bundle extraction destination: %w", err)
	}
	info, err := os.Lstat(absolute)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return "", fmt.Errorf("create bundle extraction destination: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("inspect bundle extraction destination: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("bundle extraction destination is a symlink")
	case !info.IsDir():
		return "", fmt.Errorf("bundle extraction destination is not a directory")
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve bundle extraction destination: %w", err)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", fmt.Errorf("read bundle extraction destination: %w", err)
	}
	if len(entries) != 0 {
		return "", fmt.Errorf("bundle extraction destination is not empty")
	}
	return resolved, nil
}

func destinationPath(root, archivePath string) (string, error) {
	output := filepath.Join(root, filepath.FromSlash(archivePath))
	relative, err := filepath.Rel(root, output)
	if err != nil {
		return "", fmt.Errorf("resolve extraction path %q: %w", archivePath, err)
	}
	if relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("extraction path %q escapes destination", archivePath)
	}
	return output, nil
}

func verifyHeader(header *tar.Header, expected Entry, epoch time.Time, targetOS string) error {
	if err := validateArchivePath(header.Name, targetOS); err != nil {
		return err
	}
	if header.Name != expected.Path {
		return fmt.Errorf("path is %q; manifest expects %q", header.Name, expected.Path)
	}
	if header.Mode < 0 || uint32(header.Mode) != expected.Mode {
		return fmt.Errorf("entry %q mode is %#o; manifest expects %#o", expected.Path, header.Mode, expected.Mode)
	}
	if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
		return fmt.Errorf("entry %q has non-normalized ownership metadata", expected.Path)
	}
	if header.Devmajor != 0 || header.Devminor != 0 || len(header.Xattrs) != 0 {
		return fmt.Errorf("entry %q has unsupported device or extended metadata", expected.Path)
	}
	for key := range header.PAXRecords {
		switch key {
		case "path", "linkpath", "mtime", "atime", "ctime", "size", "uid", "gid", "uname", "gname":
		default:
			return fmt.Errorf("entry %q has unsupported PAX metadata %q", expected.Path, key)
		}
	}
	if !header.ModTime.Equal(epoch) || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() {
		return fmt.Errorf("entry %q has non-normalized timestamps", expected.Path)
	}

	switch expected.Type {
	case EntryDirectory:
		if header.Typeflag != tar.TypeDir || header.Size != 0 || header.Linkname != "" {
			return fmt.Errorf("directory %q does not match its manifest metadata", expected.Path)
		}
	case EntryFile:
		if header.Typeflag != tar.TypeReg || header.Size != expected.Size || header.Linkname != "" {
			return fmt.Errorf("file %q does not match its manifest metadata", expected.Path)
		}
	case EntrySymlink:
		if header.Typeflag != tar.TypeSymlink || header.Size != 0 || header.Linkname != expected.LinkTarget {
			return fmt.Errorf("symlink %q does not match its manifest metadata", expected.Path)
		}
		if err := validateLinkTarget(expected.Path, header.Linkname); err != nil {
			return err
		}
	default:
		return fmt.Errorf("entry %q has unsupported type %q", expected.Path, expected.Type)
	}
	return nil
}

func extractRegularFile(r io.Reader, outputPath string, expected Entry, epoch time.Time) error {
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create bundle file %q: %w", expected.Path, err)
	}

	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(file, hash), r, expected.Size)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract bundle file %q after %d bytes: %w", expected.Path, written, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bundle file %q: %w", expected.Path, closeErr)
	}
	if digest := fmt.Sprintf("%x", hash.Sum(nil)); digest != expected.SHA256 {
		return fmt.Errorf("bundle file %q SHA-256 mismatch", expected.Path)
	}
	if err := os.Chmod(outputPath, os.FileMode(expected.Mode)); err != nil {
		return fmt.Errorf("set bundle file mode %q: %w", expected.Path, err)
	}
	if err := os.Chtimes(outputPath, epoch, epoch); err != nil {
		return fmt.Errorf("set bundle file timestamp %q: %w", expected.Path, err)
	}
	return nil
}

func verifyExtractedEntrypoint(root string, manifest Manifest) error {
	entrypointPath, err := destinationPath(root, manifest.Entrypoint)
	if err != nil {
		return err
	}
	info, err := os.Lstat(entrypointPath)
	if err != nil {
		return fmt.Errorf("inspect extracted entrypoint: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("extracted entrypoint %q is not a regular file", manifest.Entrypoint)
	}
	if manifest.WorkDir != "" {
		workDirPath, err := destinationPath(root, manifest.WorkDir)
		if err != nil {
			return err
		}
		info, err := os.Lstat(workDirPath)
		if err != nil {
			return fmt.Errorf("inspect extracted work directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("extracted work directory %q is not a directory", manifest.WorkDir)
		}
	}
	return nil
}
