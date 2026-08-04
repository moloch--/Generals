// GeneralsX @build OpenAI 30/07/2026 Build deterministic self-extracting launchers without modifying the source module.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/bundle"
	"github.com/moloch--/Generals/scripts/tooling/sfx/internal/signalctx"
	"github.com/ulikunitz/xz"
)

const (
	defaultMaxEmbedBytes = int64(1_900_000_000)
	launcherPackage      = "./cmd/generalsx-sfx"
	generatedPayloadDir  = "internal/payload/generated"
)

var errPayloadTooLarge = errors.New("compressed payload exceeds maximum embedded size")

type packConfig struct {
	source                 string
	output                 string
	target                 string
	entrypoint             string
	workDir                string
	onlineServerEntrypoint string
	product                string
	version                string
	excludeFile            string
	moduleRoot             string
	compression            string
	maxEmbedBytes          int64
}

type exclusionRule struct {
	value   string
	subtree bool
}

type boundedDigestWriter struct {
	destination io.Writer
	digest      hash.Hash
	max         int64
	written     int64
	limitErr    error
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(arguments []string, stdout, stderr io.Writer) int {
	ctx, stop := signalctx.NotifyContext(context.Background())
	defer stop()
	if err := run(ctx, arguments, stdout, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "generalsx-sfx-pack: %v\n", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return errors.New("packer requires a non-nil context")
	}
	config, err := parseFlags(arguments, stderr)
	if err != nil {
		return err
	}

	targetOS, targetArch, err := parseTarget(config.target)
	if err != nil {
		return err
	}
	epoch, err := sourceDateEpoch()
	if err != nil {
		return err
	}
	if config.compression != "xz" {
		return fmt.Errorf("unsupported compression %q; only xz is supported", config.compression)
	}
	if config.maxEmbedBytes <= 0 {
		return errors.New("maximum embedded payload size must be positive")
	}
	if err := validateProductID(config.product); err != nil {
		return err
	}

	sourceRoot, err := resolveDirectory(config.source, "payload source")
	if err != nil {
		return err
	}
	outputPath, err := prepareOutputPath(config.output)
	if err != nil {
		return err
	}
	moduleRoot, err := resolveModuleRoot(config.moduleRoot)
	if err != nil {
		return err
	}
	for _, protected := range []struct {
		label string
		root  string
	}{
		{label: "payload source", root: sourceRoot},
		{label: "SFX module", root: moduleRoot},
	} {
		if pathWithin(protected.root, outputPath) {
			return fmt.Errorf(
				"output path %q is inside the %s tree %q",
				outputPath,
				protected.label,
				protected.root,
			)
		}
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	exclude, err := parseExclusionProfile(config.excludeFile)
	if err != nil {
		return err
	}

	temporaryRoot, err := createPrivatePackerWorkspace(
		outputPath,
		sourceRoot,
		moduleRoot,
	)
	if err != nil {
		return fmt.Errorf("create private packer workspace: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)

	temporaryModule := filepath.Join(temporaryRoot, "module")
	if err := copyModule(moduleRoot, temporaryModule, outputPath); err != nil {
		return fmt.Errorf("copy launcher module: %w", err)
	}

	generatedDir := filepath.Join(temporaryModule, filepath.FromSlash(generatedPayloadDir))
	if err := os.MkdirAll(generatedDir, 0o700); err != nil {
		return fmt.Errorf("create generated payload directory: %w", err)
	}
	if err := os.Chmod(generatedDir, 0o700); err != nil {
		return fmt.Errorf("make generated payload directory private: %w", err)
	}

	symlinkMode := bundle.PreserveSymlinks
	if targetOS == "windows" {
		symlinkMode = bundle.RejectSymlinks
	}
	packOptions := bundle.PackOptions{
		Context:                ctx,
		Product:                config.product,
		Version:                config.version,
		TargetOS:               targetOS,
		TargetArch:             targetArch,
		Entrypoint:             config.entrypoint,
		WorkDir:                config.workDir,
		OnlineServerEntrypoint: config.onlineServerEntrypoint,
		Epoch:                  epoch,
		Exclude:                exclude,
		SymlinkMode:            symlinkMode,
	}

	xzExecutable := ""
	if candidate, lookupErr := exec.LookPath("xz"); lookupErr == nil {
		xzExecutable = candidate
	} else if !errors.Is(lookupErr, exec.ErrNotFound) {
		return fmt.Errorf("locate xz compressor: %w", lookupErr)
	}
	compressorDescription := "pure-Go xz fallback"
	if xzExecutable != "" {
		compressorDescription = xzExecutable
	}
	_, _ = fmt.Fprintf(
		stderr,
		"GeneralsX SFX pack: packing %s for %s/%s with %s...\n",
		sourceRoot,
		targetOS,
		targetArch,
		compressorDescription,
	)

	payloadPath := filepath.Join(generatedDir, "payload.tar.xz")
	manifest, payloadDigest, payloadSize, err := writeCompressedPayload(
		ctx,
		sourceRoot,
		payloadPath,
		packOptions,
		config.maxEmbedBytes,
		xzExecutable,
	)
	if err != nil {
		return fmt.Errorf("generate compressed payload: %w", err)
	}
	manifest.Compression = config.compression
	manifest.PayloadSHA256 = payloadDigest
	manifest.PayloadSize = payloadSize

	manifestBytes, err := bundle.MarshalManifest(manifest)
	if err != nil {
		return fmt.Errorf("marshal generated manifest: %w", err)
	}
	manifestPath := filepath.Join(generatedDir, "manifest.json")
	if err := writeExclusiveFile(manifestPath, manifestBytes, 0o600); err != nil {
		return fmt.Errorf("write generated manifest: %w", err)
	}

	if payloadSize > config.maxEmbedBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", errPayloadTooLarge, payloadSize, config.maxEmbedBytes)
	}
	_, _ = fmt.Fprintf(
		stderr,
		"GeneralsX SFX pack: packed %d bytes; linking launcher to %s...\n",
		payloadSize,
		outputPath,
	)
	if err := buildLauncher(ctx, temporaryModule, outputPath, targetOS, targetArch); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(stdout, outputPath)
	return nil
}

func parseFlags(arguments []string, stderr io.Writer) (packConfig, error) {
	var config packConfig
	flags := flag.NewFlagSet("generalsx-sfx-pack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&config.source, "source", "", "payload source directory")
	flags.StringVar(&config.output, "output", "", "final self-extracting launcher path")
	flags.StringVar(&config.target, "target", "", "target platform as os/arch")
	flags.StringVar(&config.entrypoint, "entry", "", "payload-relative native entrypoint")
	flags.StringVar(&config.workDir, "workdir", "", "payload-relative working directory")
	// GeneralsX @feature Codex 04/08/2026 Declare an optional target-native Online server sidecar in the authenticated payload.
	flags.StringVar(
		&config.onlineServerEntrypoint,
		"online-server-entry",
		"",
		"optional payload-relative Online server entrypoint",
	)
	flags.StringVar(&config.product, "product", "", "bundle product identifier")
	flags.StringVar(&config.version, "version", "", "bundle version")
	flags.StringVar(&config.excludeFile, "exclude", "", "optional exclusion profile")
	flags.StringVar(&config.moduleRoot, "module", "", "SFX Go module root (inferred when omitted)")
	flags.StringVar(&config.compression, "compression", "xz", "payload compression")
	flags.Int64Var(
		&config.maxEmbedBytes,
		"max-embed-bytes",
		defaultMaxEmbedBytes,
		"maximum compressed payload bytes embedded in the launcher",
	)
	if err := flags.Parse(arguments); err != nil {
		return packConfig{}, err
	}
	if flags.NArg() != 0 {
		return packConfig{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	required := []struct {
		name  string
		value string
	}{
		{"source", config.source},
		{"output", config.output},
		{"target", config.target},
		{"entry", config.entrypoint},
		{"product", config.product},
		{"version", config.version},
	}
	for _, field := range required {
		if field.value == "" {
			return packConfig{}, fmt.Errorf("-%s is required", field.name)
		}
	}
	return config, nil
}

func parseTarget(value string) (string, string, error) {
	targetOS, targetArch, found := strings.Cut(value, "/")
	if !found || targetOS == "" || targetArch == "" || strings.Contains(targetArch, "/") {
		return "", "", fmt.Errorf("target %q must use exactly os/arch", value)
	}
	switch targetOS {
	case "darwin", "linux", "windows":
	default:
		return "", "", fmt.Errorf("unsupported target OS %q", targetOS)
	}
	for _, character := range targetArch {
		if !((character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-') {
			return "", "", fmt.Errorf("invalid target architecture %q", targetArch)
		}
	}
	return targetOS, targetArch, nil
}

func validateProductID(value string) error {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, ".") {
		return fmt.Errorf("product %q is not a safe cache identifier", value)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("product %q is not a safe cache identifier", value)
	}
	return nil
}

func sourceDateEpoch() (time.Time, error) {
	value, present := os.LookupEnv("SOURCE_DATE_EPOCH")
	if !present || value == "" {
		return time.Unix(0, 0).UTC(), nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, fmt.Errorf("invalid SOURCE_DATE_EPOCH %q", value)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func resolveDirectory(value, label string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is empty", label)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s symlinks: %w", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory", label, value)
	}
	return filepath.Clean(resolved), nil
}

func prepareOutputPath(value string) (string, error) {
	if value == "" {
		return "", errors.New("output path is empty")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return "", fmt.Errorf("output path %q does not name a file", value)
	}

	if info, err := os.Lstat(absolute); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("output path %q is not a regular file", absolute)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect output path: %w", err)
	}

	resolvedParent, err := resolveFutureDirectory(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve output directory symlinks: %w", err)
	}
	outputPath := filepath.Join(resolvedParent, filepath.Base(absolute))
	return filepath.Clean(outputPath), nil
}

func resolveFutureDirectory(value string) (string, error) {
	var missing []string
	for candidate := filepath.Clean(value); ; candidate = filepath.Dir(candidate) {
		_, err := os.Lstat(candidate)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%q is not a directory", candidate)
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("no existing ancestor for %q", value)
		}
		missing = append(missing, filepath.Base(candidate))
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func createPrivatePackerWorkspace(
	outputPath string,
	protectedRoots ...string,
) (string, error) {
	outputDirectory := filepath.Clean(filepath.Dir(outputPath))
	resolvedOutputDirectory, err := resolveDirectory(outputDirectory, "output directory")
	if err != nil {
		return "", err
	}
	if resolvedOutputDirectory != outputDirectory {
		return "", fmt.Errorf(
			"output directory changed from %q to %q",
			outputDirectory,
			resolvedOutputDirectory,
		)
	}

	for _, protectedRoot := range protectedRoots {
		if pathWithin(protectedRoot, outputDirectory) {
			return "", fmt.Errorf(
				"output directory %q overlaps protected tree %q",
				outputDirectory,
				protectedRoot,
			)
		}
	}

	temporaryRoot, err := os.MkdirTemp(
		resolvedOutputDirectory,
		"."+filepath.Base(outputPath)+".pack-",
	)
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(temporaryRoot)
		}
	}()
	if err := os.Chmod(temporaryRoot, 0o700); err != nil {
		return "", fmt.Errorf("make packer workspace private: %w", err)
	}

	resolvedTemporaryRoot, err := filepath.EvalSymlinks(temporaryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve packer workspace: %w", err)
	}
	resolvedTemporaryRoot = filepath.Clean(resolvedTemporaryRoot)
	if !pathWithin(resolvedOutputDirectory, resolvedTemporaryRoot) {
		return "", fmt.Errorf(
			"packer workspace %q escaped output directory %q",
			resolvedTemporaryRoot,
			resolvedOutputDirectory,
		)
	}
	for _, protectedRoot := range protectedRoots {
		if pathWithin(protectedRoot, resolvedTemporaryRoot) ||
			pathWithin(resolvedTemporaryRoot, protectedRoot) {
			return "", fmt.Errorf(
				"packer workspace %q overlaps protected tree %q",
				resolvedTemporaryRoot,
				protectedRoot,
			)
		}
	}

	remove = false
	return resolvedTemporaryRoot, nil
}

func resolveModuleRoot(explicit string) (string, error) {
	if explicit != "" {
		root, err := resolveDirectory(explicit, "SFX module root")
		if err != nil {
			return "", err
		}
		if err := validateModuleRoot(root); err != nil {
			return "", fmt.Errorf("validate SFX module root: %w", err)
		}
		return root, nil
	}

	var starts []string
	if current, err := os.Getwd(); err == nil {
		starts = append(starts, current)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}
	for _, start := range starts {
		if root := findModuleRoot(start); root != "" {
			return root, nil
		}
	}
	return "", errors.New("could not infer SFX module root; pass -module explicitly")
}

func findModuleRoot(start string) string {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for directory := filepath.Clean(absolute); ; directory = filepath.Dir(directory) {
		candidates := []string{
			directory,
			filepath.Join(directory, "scripts", "tooling", "sfx"),
		}
		for _, candidate := range candidates {
			if err := validateModuleRoot(candidate); err == nil {
				resolved, resolveErr := filepath.EvalSymlinks(candidate)
				if resolveErr == nil {
					return filepath.Clean(resolved)
				}
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return ""
}

func validateModuleRoot(root string) error {
	if err := requireRegularFile(filepath.Join(root, "go.mod")); err != nil {
		return err
	}
	if err := requireRegularFile(filepath.Join(root, "internal", "payload", "packed.go")); err != nil {
		return err
	}
	launcherInfo, err := os.Lstat(filepath.Join(root, "cmd", "generalsx-sfx"))
	if err != nil {
		return err
	}
	if !launcherInfo.IsDir() || launcherInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", filepath.Join(root, "cmd", "generalsx-sfx"))
	}
	return nil
}

func requireRegularFile(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a regular file", name)
	}
	return nil
}

func parseExclusionProfile(name string) (bundle.ExcludeFunc, error) {
	if name == "" {
		return nil, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open exclusion profile %q: %w", name, err)
	}
	defer file.Close()

	var rules []exclusionRule
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, `\`) || strings.ContainsRune(line, 0) || strings.HasPrefix(line, "/") {
			return nil, fmt.Errorf("exclusion profile %q line %d has an unsafe path", name, lineNumber)
		}

		rule := exclusionRule{value: line}
		if strings.HasSuffix(line, "/") {
			rule.subtree = true
			rule.value = strings.TrimSuffix(line, "/")
			if rule.value == "" || path.Clean(rule.value) != rule.value ||
				rule.value == "." || rule.value == ".." || strings.HasPrefix(rule.value, "../") {
				return nil, fmt.Errorf("exclusion profile %q line %d has an unsafe subtree", name, lineNumber)
			}
		} else if _, err := path.Match(rule.value, ""); err != nil {
			return nil, fmt.Errorf("exclusion profile %q line %d has invalid pattern: %w", name, lineNumber, err)
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read exclusion profile %q: %w", name, err)
	}

	return func(name string, _ fs.DirEntry) (bool, error) {
		for _, rule := range rules {
			if rule.subtree {
				if name == rule.value || strings.HasPrefix(name, rule.value+"/") {
					return true, nil
				}
				continue
			}
			matched, err := path.Match(rule.value, name)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}, nil
}

func copyModule(sourceRoot, destinationRoot, outputPath string) error {
	if err := os.Mkdir(destinationRoot, 0o700); err != nil {
		return fmt.Errorf("create module destination: %w", err)
	}

	outputRelative := ""
	if relative, err := filepath.Rel(sourceRoot, outputPath); err == nil &&
		relative != "." && !filepath.IsAbs(relative) &&
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		outputRelative = filepath.Clean(relative)
	}
	generatedRelative := filepath.FromSlash(generatedPayloadDir)

	err := filepath.WalkDir(sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return fmt.Errorf("make module path relative: %w", err)
		}
		if relative == "." {
			return nil
		}
		relative = filepath.Clean(relative)

		if relative == generatedRelative ||
			strings.HasPrefix(relative, generatedRelative+string(filepath.Separator)) ||
			(outputRelative != "" && relative == outputRelative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := os.Lstat(sourcePath)
		if err != nil {
			return fmt.Errorf("lstat module path %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("module path %q is a symlink", relative)
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		switch {
		case info.IsDir():
			if err := os.Mkdir(destinationPath, 0o700); err != nil {
				return fmt.Errorf("create module directory %q: %w", relative, err)
			}
		case info.Mode().IsRegular():
			if err := copyRegularFile(sourcePath, destinationPath, info); err != nil {
				return fmt.Errorf("copy module file %q: %w", relative, err)
			}
		default:
			return fmt.Errorf("module path %q is a special filesystem node", relative)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func copyRegularFile(sourcePath, destinationPath string, expected os.FileInfo) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	openedInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expected, openedInfo) ||
		openedInfo.Size() != expected.Size() {
		return errors.New("source file changed while copying")
	}

	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, expected.Mode().Perm())
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = destination.Close()
		if !complete {
			_ = os.Remove(destinationPath)
		}
	}()

	written, err := io.Copy(destination, source)
	if err != nil {
		return err
	}
	if written != expected.Size() {
		return fmt.Errorf("copied %d bytes; expected %d", written, expected.Size())
	}
	afterInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(openedInfo, afterInfo) || afterInfo.Size() != expected.Size() {
		return errors.New("source file changed while copying")
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := os.Chmod(destinationPath, expected.Mode().Perm()); err != nil {
		return err
	}
	complete = true
	return nil
}

func writeCompressedPayload(
	ctx context.Context,
	sourceRoot string,
	outputPath string,
	options bundle.PackOptions,
	maxBytes int64,
	xzExecutable string,
) (bundle.Manifest, string, int64, error) {
	if ctx == nil {
		return bundle.Manifest{}, "", 0, errors.New("compressed payload requires a non-nil context")
	}
	if maxBytes <= 0 {
		return bundle.Manifest{}, "", 0, errors.New("maximum embedded payload size must be positive")
	}
	options.Context = ctx
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return bundle.Manifest{}, "", 0, fmt.Errorf("create compressed payload: %w", err)
	}
	complete := false
	defer func() {
		_ = output.Close()
		if !complete {
			_ = os.Remove(outputPath)
		}
	}()

	sink := &boundedDigestWriter{
		destination: output,
		digest:      sha256.New(),
		max:         maxBytes,
	}
	var manifest bundle.Manifest
	if xzExecutable != "" {
		manifest, err = writeTarWithExternalXZ(ctx, sourceRoot, sink, options, xzExecutable)
	} else {
		manifest, err = writeTarWithGoXZ(ctx, sourceRoot, sink, options)
	}
	if sink.limitErr != nil {
		return bundle.Manifest{}, "", 0, sink.limitErr
	}
	if err != nil {
		return bundle.Manifest{}, "", 0, err
	}
	if sink.written > maxBytes {
		return bundle.Manifest{}, "", 0, fmt.Errorf(
			"%w: %d bytes exceeds %d",
			errPayloadTooLarge,
			sink.written,
			maxBytes,
		)
	}
	if err := output.Sync(); err != nil {
		return bundle.Manifest{}, "", 0, fmt.Errorf("sync compressed payload: %w", err)
	}
	if err := output.Close(); err != nil {
		return bundle.Manifest{}, "", 0, fmt.Errorf("close compressed payload: %w", err)
	}
	complete = true
	return manifest, hex.EncodeToString(sink.digest.Sum(nil)), sink.written, nil
}

func writeTarWithGoXZ(
	ctx context.Context,
	sourceRoot string,
	destination io.Writer,
	options bundle.PackOptions,
) (bundle.Manifest, error) {
	compressor, err := xz.NewWriter(&contextWriter{ctx: ctx, writer: destination})
	if err != nil {
		return bundle.Manifest{}, fmt.Errorf("initialize Go xz compressor: %w", err)
	}
	manifest, packErr := bundle.WriteTar(sourceRoot, compressor, options)
	closeErr := compressor.Close()
	if packErr != nil {
		return bundle.Manifest{}, packErr
	}
	if closeErr != nil {
		return bundle.Manifest{}, fmt.Errorf("finish Go xz payload: %w", closeErr)
	}
	return manifest, nil
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *contextWriter) Write(data []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(data)
}

func writeTarWithExternalXZ(
	ctx context.Context,
	sourceRoot string,
	destination io.Writer,
	options bundle.PackOptions,
	xzExecutable string,
) (bundle.Manifest, error) {
	command := exec.CommandContext(ctx, xzExecutable, "-T1", "-6", "-c")
	command.Env = removeEnvironment(os.Environ(), "XZ_DEFAULTS", "XZ_OPT")
	command.Stdout = destination
	var stderr cappedBuffer
	stderr.limit = 64 * 1024
	command.Stderr = &stderr

	stdin, err := command.StdinPipe()
	if err != nil {
		return bundle.Manifest{}, fmt.Errorf("open xz input: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return bundle.Manifest{}, fmt.Errorf("start xz compressor: %w", err)
	}

	manifest, packErr := bundle.WriteTar(sourceRoot, stdin, options)
	closeErr := stdin.Close()
	waitErr := command.Wait()
	if packErr != nil {
		return bundle.Manifest{}, packErr
	}
	if closeErr != nil && waitErr == nil {
		return bundle.Manifest{}, fmt.Errorf("close xz input: %w", closeErr)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return bundle.Manifest{}, fmt.Errorf("xz compressor failed: %w: %s", waitErr, detail)
		}
		return bundle.Manifest{}, fmt.Errorf("xz compressor failed: %w", waitErr)
	}
	return manifest, nil
}

func (writer *boundedDigestWriter) Write(data []byte) (int, error) {
	if writer.limitErr != nil {
		return 0, writer.limitErr
	}
	if int64(len(data)) > writer.max-writer.written {
		writer.limitErr = fmt.Errorf(
			"%w: compressed output would exceed %d bytes",
			errPayloadTooLarge,
			writer.max,
		)
		return 0, writer.limitErr
	}
	written, err := writer.destination.Write(data)
	if written > 0 {
		_, _ = writer.digest.Write(data[:written])
		writer.written += int64(written)
	}
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return written, err
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return originalLength, nil
}

func (buffer *cappedBuffer) String() string {
	return buffer.buffer.String()
}

func writeExclusiveFile(name string, contents []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(name)
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
	complete = true
	return nil
}

func buildLauncher(
	ctx context.Context,
	moduleRoot string,
	outputPath string,
	targetOS string,
	targetArch string,
) error {
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("locate Go compiler: %w", err)
	}

	outputDir := filepath.Dir(outputPath)
	temporaryOutput, err := os.CreateTemp(outputDir, "."+filepath.Base(outputPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create same-filesystem launcher output: %w", err)
	}
	temporaryPath := temporaryOutput.Name()
	if err := temporaryOutput.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close launcher output placeholder: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()

	command := exec.CommandContext(
		ctx,
		goExecutable,
		"build",
		"-tags", "gxpacked",
		"-buildvcs=false",
		"-trimpath",
		"-ldflags", "-s -w -buildid=",
		"-o", temporaryPath,
		launcherPackage,
	)
	command.Dir = moduleRoot
	buildEnvironment := map[string]string{
		"CGO_ENABLED":  "0",
		"GOOS":         targetOS,
		"GOARCH":       targetArch,
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFLAGS":      "",
		"GOTOOLCHAIN":  "local",
		"GOWORK":       "off",
	}
	switch targetArch {
	case "amd64":
		buildEnvironment["GOAMD64"] = "v1"
	case "386":
		buildEnvironment["GO386"] = "sse2"
	case "arm64":
		buildEnvironment["GOARM64"] = "v8.0"
	}
	command.Env = overrideEnvironment(os.Environ(), buildEnvironment)
	var buildOutput cappedBuffer
	buildOutput.limit = 256 * 1024
	command.Stdout = &buildOutput
	command.Stderr = &buildOutput
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(buildOutput.String())
		if detail != "" {
			return fmt.Errorf("build %s launcher: %w: %s", targetOS+"/"+targetArch, err, detail)
		}
		return fmt.Errorf("build %s launcher: %w", targetOS+"/"+targetArch, err)
	}

	info, err := os.Lstat(temporaryPath)
	if err != nil {
		return fmt.Errorf("inspect built launcher: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("Go build did not produce a non-empty regular launcher")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporaryPath, 0o700); err != nil {
			return fmt.Errorf("make launcher private and executable: %w", err)
		}
	}
	if err := syncRegularFile(temporaryPath); err != nil {
		return fmt.Errorf("sync built launcher: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("publish launcher to %q: %w", outputPath, err)
	}
	published = true
	bestEffortSyncDirectory(outputDir)
	return nil
}

func overrideEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		overridden := false
		for override := range overrides {
			if key == override || (runtime.GOOS == "windows" && strings.EqualFold(key, override)) {
				overridden = true
				break
			}
		}
		if !overridden {
			result = append(result, entry)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

func removeEnvironment(environment []string, names ...string) []string {
	removed := make(map[string]struct{}, len(names))
	for _, name := range names {
		if runtime.GOOS == "windows" {
			name = strings.ToUpper(name)
		}
		removed[name] = struct{}{}
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if _, discard := removed[key]; !discard {
			result = append(result, entry)
		}
	}
	return result
}

func syncRegularFile(name string) error {
	// GeneralsX @bugfix Codex 04/08/2026 Open Windows launchers with write access before flushing them to stable storage.
	// FlushFileBuffers requires a writable Windows handle even when the file was
	// just produced and closed by go build. A read-only handle returns
	// ERROR_ACCESS_DENIED for executable files on native Windows hosts.
	file, err := os.OpenFile(name, syncRegularFileOpenFlags(runtime.GOOS), 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncRegularFileOpenFlags(hostOS string) int {
	if hostOS == "windows" {
		return os.O_RDWR
	}
	return os.O_RDONLY
}

func bestEffortSyncDirectory(name string) {
	directory, err := os.Open(name)
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}
