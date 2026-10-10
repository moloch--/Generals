// GeneralsX @build Codex 10/10/2026 Publish deterministic engine runtimes with a matching tracked packaging kit.
package runtimebundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Options struct {
	RuntimeDir     string
	Repo           string
	OutputDir      string
	Target         string
	Repository     string
	Version        string
	Commit         string
	OnlineEndpoint string
	MinimumOS      string
	MinimumGLIBC   string
	Preset         string
}

func Generate(ctx context.Context, options Options) (Descriptor, error) {
	d := Descriptor{SchemaVersion: SchemaVersion, Version: options.Version, Repository: options.Repository, SourceCommit: options.Commit, Target: options.Target, OnlineEndpoint: options.OnlineEndpoint, MinimumOS: options.MinimumOS, MinimumGLIBC: options.MinimumGLIBC, OnlineCompatibility: 2, Preset: options.Preset}
	switch options.Target {
	case "linux-amd64":
		d.EngineArch, d.LauncherArch = "amd64", "amd64"
	case "macos-arm64":
		d.EngineArch, d.LauncherArch = "arm64", "arm64"
	case "windows-x86":
		d.EngineArch, d.LauncherArch = "x86", "amd64"
	default:
		return d, errors.New("unsupported runtime target")
	}
	if ctx == nil {
		return d, errors.New("runtime generation requires context")
	}
	if !versionPattern.MatchString(options.Version) || len(options.Version) > 100 || !commitPattern.MatchString(options.Commit) || !repositoryPattern.MatchString(options.Repository) {
		return d, errors.New("invalid runtime version, repository or commit")
	}
	if err := rejectSymlinkAncestors(options.RuntimeDir); err != nil {
		return d, err
	}
	if err := rejectSymlinkAncestors(options.Repo); err != nil {
		return d, err
	}
	if err := os.MkdirAll(options.OutputDir, 0755); err != nil {
		return d, err
	}
	if err := rejectSymlinkAncestors(options.OutputDir); err != nil {
		return d, err
	}
	stem := ArtifactStem(options.Version, options.Target)
	archivePath := filepath.Join(options.OutputDir, stem+".tar.gz")
	descriptorPath := filepath.Join(options.OutputDir, stem+".json")
	for _, name := range []string{archivePath, descriptorPath} {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return d, fmt.Errorf("refusing to replace runtime release asset %s", name)
		}
	}
	stage, err := os.MkdirTemp(options.OutputDir, ".runtime-pack-")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(stage)
	root := filepath.Join(stage, "package")
	if err := os.Mkdir(root, 0700); err != nil {
		return d, err
	}
	if err := copyRuntime(ctx, options.RuntimeDir, filepath.Join(root, "runtime"), options.Target); err != nil {
		return d, err
	}
	if err := copyKit(ctx, options.Repo, root, options.Commit, options.Target); err != nil {
		return d, err
	}
	if err := ValidateRuntime(filepath.Join(root, "runtime"), options.Target); err != nil {
		return d, err
	}
	if err := filepath.WalkDir(root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := File{Path: rel, Mode: 0644}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			file.LinkTarget = filepath.ToSlash(target)
			file.Mode = 0777
			file.SHA256 = hashBytes([]byte(file.LinkTarget))
		} else {
			if !info.Mode().IsRegular() {
				return errors.New("unsupported runtime file type")
			}
			if executablePath(rel, options.Target) {
				file.Mode = 0755
			}
			reader, err := os.Open(full)
			if err != nil {
				return err
			}
			file.SHA256, file.Size, err = fileDigest(contextReader{ctx, io.LimitReader(reader, MaxExpandedBytes+1)})
			closeErr := reader.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if file.Size > MaxExpandedBytes {
				return errors.New("runtime file exceeds size limit")
			}
		}
		d.Files = append(d.Files, file)
		if len(d.Files) > MaxFiles {
			return errors.New("runtime file count exceeds limit")
		}
		return nil
	}); err != nil {
		return d, err
	}
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Path < d.Files[j].Path })
	for _, file := range d.Files {
		if file.Path == "runtime/build-metadata.json" {
			d.Dependencies = map[string]string{"build_metadata_sha256": file.SHA256}
		}
	}
	// Validate every entry before any compression. Archive identity is filled
	// with a syntactically valid placeholder and replaced after compression.
	d.Archive = Archive{Name: stem + ".tar.gz", Size: 1, SHA256: strings.Repeat("0", 64)}
	if err := d.Validate(); err != nil {
		return d, err
	}
	temporaryArchive := filepath.Join(stage, "runtime.tar.gz")
	if err := writeArchive(ctx, root, temporaryArchive, d.Files); err != nil {
		return d, err
	}
	reader, err := os.Open(temporaryArchive)
	if err != nil {
		return d, err
	}
	d.Archive.SHA256, d.Archive.Size, err = fileDigest(contextReader{ctx, io.LimitReader(reader, MaxArchiveBytes+1)})
	closeErr := reader.Close()
	if err != nil {
		return d, err
	}
	if closeErr != nil {
		return d, closeErr
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	// Exercise the consumer against the exact bytes being published.
	if err := d.Extract(ctx, temporaryArchive, filepath.Join(stage, "verified")); err != nil {
		return d, fmt.Errorf("verify produced runtime: %w", err)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return d, err
	}
	data = append(data, '\n')
	if len(data) > MaxDescriptorBytes {
		return d, errors.New("produced descriptor exceeds size limit")
	}
	temporaryDescriptor := filepath.Join(stage, "runtime.json")
	if err := os.WriteFile(temporaryDescriptor, data, 0644); err != nil {
		return d, err
	}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	if err := os.Rename(temporaryArchive, archivePath); err != nil {
		return d, err
	}
	if err := os.Rename(temporaryDescriptor, descriptorPath); err != nil {
		_ = os.Remove(archivePath)
		return d, err
	}
	return d, nil
}

func executablePath(name, target string) bool {
	return strings.HasSuffix(name, ".sh") || target != "windows-x86" && (name == "runtime/GeneralsXZH" || strings.HasPrefix(name, "runtime/lib/"))
}

func copyRuntime(ctx context.Context, source, destination, target string) error {
	return filepath.WalkDir(source, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			return nil
		}
		if err := validatePath(rel); err != nil {
			return err
		}
		if err := validateRuntimePath(rel, target); err != nil {
			return err
		}
		output := filepath.Join(destination, filepath.FromSlash(rel))
		if entry.Type()&os.ModeSymlink != 0 {
			if target == "windows-x86" {
				return errors.New("Windows runtime may not contain symlinks")
			}
			link, err := os.Readlink(full)
			if err != nil {
				return err
			}
			link = filepath.ToSlash(link)
			if err := validateRelativeLink("runtime/"+rel, link); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
				return err
			}
			return os.Symlink(filepath.FromSlash(link), output)
		}
		return copyRegular(ctx, full, output)
	})
}

func copyKit(ctx context.Context, repo, root, commit, target string) error {
	command := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("read packaging kit revision: %w", err)
	}
	if strings.TrimSpace(string(output)) != commit {
		return errors.New("packaging kit HEAD does not match requested source commit")
	}
	command = exec.CommandContext(ctx, "git", "-C", repo, "ls-files", "-z")
	output, err = command.Output()
	if err != nil {
		return fmt.Errorf("list tracked packaging kit: %w", err)
	}
	var selected []string
	for _, name := range strings.Split(string(output), "\x00") {
		if kitPath(name) || target != "windows-x86" && name == "GeneralsZH/Data/Window/Menus/ExtrasMenu.wnd" {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		return errors.New("no tracked packaging kit found")
	}
	arguments := append([]string{"-C", repo, "diff", "--quiet", "HEAD", "--"}, selected...)
	if err := exec.CommandContext(ctx, "git", arguments...).Run(); err != nil {
		return errors.New("packaging kit contains changes not represented by source_commit")
	}
	for _, name := range selected {
		if err := validatePath(name); err != nil {
			return err
		}
		output := filepath.Join(root, "kit", filepath.FromSlash(name))
		if name == "GeneralsZH/Data/Window/Menus/ExtrasMenu.wnd" {
			output = filepath.Join(root, "runtime", "Window", "Menus", "ExtrasMenu.wnd")
		}
		if err := copyRegular(ctx, filepath.Join(repo, filepath.FromSlash(name)), output); err != nil {
			return err
		}
	}
	return nil
}

func kitPath(name string) bool {
	if name == "LICENSE.md" || name == "assets/generalsx-zh_icon.png" || name == "scripts/build/macos/package-sfx-macos-zh-app.sh" {
		return true
	}
	const prefix = "scripts/tooling/sfx/"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rel := strings.TrimPrefix(name, prefix)
	if rel == "go.mod" || rel == "go.sum" || rel == "macos/progress.m" || strings.HasPrefix(rel, "profiles/") && strings.HasSuffix(rel, ".exclude") {
		return true
	}
	if strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/generated/") {
		return false
	}
	if strings.HasPrefix(rel, "cmd/generalsx-sfx/") || strings.HasPrefix(rel, "cmd/generalsx-sfx-pack/") {
		ext := path.Ext(rel)
		return ext == ".go" || ext == ".syso" || ext == ".ico" || ext == ".rc"
	}
	for _, directory := range []string{"bundle", "cache", "launch", "notices", "payload", "progress", "signalctx"} {
		if strings.HasPrefix(rel, "internal/"+directory+"/") && (strings.HasSuffix(rel, ".go") || rel == "internal/notices/THIRD_PARTY_NOTICES.md") {
			return true
		}
	}
	return false
}

func copyRegular(ctx context.Context, source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxExpandedBytes {
		return fmt.Errorf("not a bounded regular package source: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(output, contextReader{ctx, io.LimitReader(input, MaxExpandedBytes+1)})
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > MaxExpandedBytes {
		return errors.New("runtime source exceeds size limit")
	}
	return nil
}

func writeArchive(ctx context.Context, root, output string, files []File) error {
	archive, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer archive.Close()
	compressed := gzip.NewWriter(archive)
	writer := tar.NewWriter(compressed)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &tar.Header{Name: file.Path, Size: file.Size, Mode: int64(file.Mode), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if file.LinkTarget != "" {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = file.LinkTarget
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if file.LinkTarget != "" {
			continue
		}
		input, err := os.Open(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, contextReader{ctx, input})
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return archive.Sync()
}
