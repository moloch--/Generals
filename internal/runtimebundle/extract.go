// GeneralsX @feature Codex 10/10/2026 Authenticate runtime archives and publish isolated extraction directories atomically.
package runtimebundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func fileDigest(reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	n, err := io.Copy(hash, reader)
	return hex.EncodeToString(hash.Sum(nil)), n, err
}

// Extract requires an absent destination. The caller can cache the compressed
// archive, but should use a fresh destination for every local packaging run.
func (d Descriptor) Extract(ctx context.Context, archivePath, destination string) error {
	if ctx == nil {
		return errors.New("runtime extraction requires a context")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("runtime extraction destination must not exist")
	}
	parent := filepath.Dir(destination)
	if err := rejectSymlinkAncestors(parent); err != nil {
		return err
	}
	// macOS commonly exposes its temporary directory through /var ->
	// /private/var. Resolve the caller-selected parent once before creating
	// any private stage, then operate exclusively beneath that real path.
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	info, err := os.Lstat(archivePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != d.Archive.Size {
		return errors.New("runtime archive is not a regular file of the expected size")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	digest, size, err := fileDigest(contextReader{ctx, io.LimitReader(archive, d.Archive.Size+1)})
	if err != nil {
		return err
	}
	if size != d.Archive.Size || digest != d.Archive.SHA256 {
		return errors.New("runtime archive SHA-256/size mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".runtime-extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	compressed, err := gzip.NewReader(contextReader{ctx, archive})
	if err != nil {
		return err
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: MaxExpandedBytes + int64(MaxFiles)*4096 + 1}
	reader := tar.NewReader(limited)
	files := make(map[string]File, len(d.Files))
	for _, file := range d.Files {
		files[file.Path] = file
	}
	seen := make(map[string]bool, len(d.Files))
	directories := map[string]bool{}
	for _, file := range d.Files {
		for p := path.Dir(file.Path); p != "."; p = path.Dir(p) {
			directories[p] = true
		}
	}
	var links []File
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read runtime archive: %w", err)
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := validatePath(name); err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("duplicate runtime archive entry %q", name)
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			if !directories[name] || header.Size != 0 {
				return fmt.Errorf("unexpected runtime directory %q", name)
			}
			continue
		}
		file, ok := files[name]
		if !ok {
			return fmt.Errorf("unmanifested runtime archive entry %q", name)
		}
		if header.Mode != int64(file.Mode) || header.Size != file.Size {
			return fmt.Errorf("runtime archive metadata mismatch for %q", name)
		}
		if header.Typeflag == tar.TypeSymlink {
			if file.LinkTarget == "" || header.Linkname != file.LinkTarget {
				return fmt.Errorf("runtime archive symlink mismatch for %q", name)
			}
			links = append(links, file)
			continue
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || file.LinkTarget != "" {
			return fmt.Errorf("unsupported runtime archive entry type for %q", name)
		}
		output := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		writer, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(writer, hash), reader)
		closeErr := writer.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return fmt.Errorf("runtime file SHA-256/size mismatch for %q", name)
		}
		if err := os.Chmod(output, fs.FileMode(file.Mode)); err != nil {
			return err
		}
	}
	// Drain the gzip trailer, rejecting hidden appended payloads and oversized
	// padding rather than accepting the tar reader's first end marker alone.
	padding, err := io.ReadAll(io.LimitReader(limited, 1<<20))
	if err != nil {
		return err
	}
	if len(padding) == 1<<20 || limited.N <= 0 {
		return errors.New("runtime archive has excessive trailing data")
	}
	for _, value := range padding {
		if value != 0 {
			return errors.New("runtime archive has trailing nonzero data")
		}
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	for _, file := range d.Files {
		if !seen[file.Path] {
			return fmt.Errorf("runtime archive missing %q", file.Path)
		}
	}
	for _, file := range links {
		output := filepath.Join(stage, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(file.LinkTarget), output); err != nil {
			return err
		}
	}
	if err := d.VerifyDirectory(stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("runtime destination appeared during extraction")
	}
	return os.Rename(stage, destination)
}

func (d Descriptor) VerifyDirectory(root string) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := rejectSymlinkAncestors(root); err != nil {
		return err
	}
	// As in Extract, use the resolved root for subsequent reads so known
	// system ancestor aliases do not affect the runtime path calculation.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	root = resolvedRoot
	expected := make(map[string]File, len(d.Files))
	dirs := map[string]bool{".": true}
	for _, file := range d.Files {
		expected[file.Path] = file
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	count := 0
	err = filepath.WalkDir(root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if !dirs[rel] {
				return fmt.Errorf("unexpected runtime directory %q", rel)
			}
			return nil
		}
		file, ok := expected[rel]
		if !ok {
			return fmt.Errorf("unexpected runtime file %q", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if file.LinkTarget != "" {
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("runtime symlink replaced: %q", rel)
			}
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if filepath.ToSlash(target) != file.LinkTarget {
				return fmt.Errorf("runtime symlink modified: %q", rel)
			}
		} else {
			if !info.Mode().IsRegular() || info.Size() != file.Size {
				return fmt.Errorf("runtime file type/size mismatch: %q", rel)
			}
			if runtime.GOOS != "windows" && uint32(info.Mode().Perm()) != file.Mode {
				return fmt.Errorf("runtime file mode mismatch: %q", rel)
			}
			reader, err := os.Open(full)
			if err != nil {
				return err
			}
			hash, size, hashErr := fileDigest(io.LimitReader(reader, file.Size+1))
			closeErr := reader.Close()
			if hashErr != nil {
				return hashErr
			}
			if closeErr != nil {
				return closeErr
			}
			if hash != file.SHA256 || size != file.Size {
				return fmt.Errorf("runtime file SHA-256 mismatch: %q", rel)
			}
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(d.Files) {
		return errors.New("runtime package files are missing")
	}
	return ValidateRuntime(filepath.Join(root, "runtime"), d.Target)
}

func rejectSymlinkAncestors(directory string) error {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	leaf, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !leaf.IsDir() || leaf.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime directory is not a real directory: %q", abs)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime directory is not a real directory: %q", abs)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}
