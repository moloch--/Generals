package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultLinuxSFXBuilderImage = "generalsx/linux-builder:latest"
	macOSSFXBundleIdentifier    = "com.generalsx.generalsxzh.sfx"
	macOSSFXExecutableName      = "GeneralsXZH"
	macOSSFXIconName            = "GeneralsXZH.icns"
	macOSSFXProgressHelperName  = "GeneralsX-SFX-Progress"
	macOSSFXPlistReadLimit      = 1024 * 1024
	macOSSFXIconReadLimit       = 64 * 1024 * 1024
	macOSSFXIconElementLimit    = 32 * 1024 * 1024
)

var dockerContainerIDPattern = regexp.MustCompile(`\A[0-9a-f]{12,64}\z`)

type macOSSFXApp struct {
	bundlePath     string
	executablePath string
}

type macOSSFXSignatureVerifier func(context.Context, string) error

// GeneralsX @feature Codex 05/08/2026 Verify native SFX files directly and macOS-hosted Linux SFX files in the existing builder container.
func verifySFXArtifact(ctx context.Context, path, target, hostOS string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == targetForHost(hostOS) {
		if target == "macos" && strings.EqualFold(filepath.Ext(path), ".app") {
			return verifyMacOSSFXApp(ctx, path, verifyMacOSSFXCodeSignature)
		}
		return runSFXVerifyCommand(ctx, path, "--sfx-verify")
	}
	if hostOS == "darwin" && target == "linux" {
		return verifyLinuxSFXArtifactWithDocker(ctx, path)
	}
	return fmt.Errorf("cannot verify a %s SFX on a %s host", target, hostOS)
}

// GeneralsX @feature Codex 05/08/2026 Verify the sealed Finder bundle before invoking its direct SFX executable.
func verifyMacOSSFXApp(ctx context.Context, path string, verifySignature macOSSFXSignatureVerifier) error {
	if verifySignature == nil {
		return errors.New("macOS SFX app signature verifier is unavailable")
	}
	app, err := resolveMacOSSFXApp(path)
	if err != nil {
		return err
	}
	if err := verifySignature(ctx, app.bundlePath); err != nil {
		return fmt.Errorf("verify macOS SFX app code signature: %w", err)
	}
	// Revalidate the fixed bundle layout after codesign has read the tree so a
	// swapped nested executable is never launched on the strength of an older check.
	revalidated, err := resolveMacOSSFXApp(app.bundlePath)
	if err != nil {
		return fmt.Errorf("revalidate macOS SFX app after code-signature verification: %w", err)
	}
	if revalidated.executablePath != app.executablePath {
		return errors.New("macOS SFX app executable changed during verification")
	}
	if err := runSFXVerifyCommand(ctx, revalidated.executablePath, "--sfx-verify"); err != nil {
		return fmt.Errorf("verify embedded macOS SFX: %w", err)
	}
	return nil
}

// GeneralsX @feature Codex 05/08/2026 Resolve only the signed GeneralsXZH bundle layout emitted by the macOS packager.
func resolveMacOSSFXApp(path string) (macOSSFXApp, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return macOSSFXApp{}, errors.New("macOS SFX app path is empty or invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return macOSSFXApp{}, fmt.Errorf("resolve macOS SFX app path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if !strings.EqualFold(filepath.Ext(absolute), ".app") {
		return macOSSFXApp{}, fmt.Errorf("macOS SFX app path must end in .app: %s", absolute)
	}
	pathInfo, err := os.Lstat(absolute)
	if err != nil {
		return macOSSFXApp{}, fmt.Errorf("inspect macOS SFX app: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return macOSSFXApp{}, errors.New("macOS SFX app must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return macOSSFXApp{}, fmt.Errorf("resolve macOS SFX app: %w", err)
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || resolvedInfo.Mode()&os.ModeSymlink != 0 || !resolvedInfo.IsDir() || !os.SameFile(pathInfo, resolvedInfo) {
		return macOSSFXApp{}, errors.New("macOS SFX app changed while its root was resolved")
	}

	root, err := os.OpenRoot(resolved)
	if err != nil {
		return macOSSFXApp{}, fmt.Errorf("open macOS SFX app: %w", err)
	}
	defer root.Close()
	openedRoot, err := root.Stat(".")
	if err != nil || !openedRoot.IsDir() || !os.SameFile(resolvedInfo, openedRoot) {
		return macOSSFXApp{}, errors.New("macOS SFX app changed while it was opened")
	}

	for _, directory := range []string{
		"Contents",
		filepath.Join("Contents", "MacOS"),
		filepath.Join("Contents", "Resources"),
		filepath.Join("Contents", "Helpers"),
	} {
		if err := requireMacOSSFXDirectory(root, directory); err != nil {
			return macOSSFXApp{}, err
		}
	}
	plistPath := filepath.Join("Contents", "Info.plist")
	plist, err := readMacOSSFXPlist(root, plistPath)
	if err != nil {
		return macOSSFXApp{}, err
	}
	for _, required := range []struct {
		key      string
		expected string
	}{
		{key: "CFBundleIdentifier", expected: macOSSFXBundleIdentifier},
		{key: "CFBundleExecutable", expected: macOSSFXExecutableName},
		{key: "CFBundleIconFile", expected: macOSSFXIconName},
	} {
		if actual := plist[required.key]; actual != required.expected {
			return macOSSFXApp{}, fmt.Errorf("macOS SFX app %s is %q, expected %q", required.key, actual, required.expected)
		}
	}

	executableRelative := filepath.Join("Contents", "MacOS", macOSSFXExecutableName)
	iconRelative := filepath.Join("Contents", "Resources", macOSSFXIconName)
	helperRelative := filepath.Join("Contents", "Helpers", macOSSFXProgressHelperName)
	if err := requireMacOSSFXFile(root, executableRelative, true); err != nil {
		return macOSSFXApp{}, err
	}
	if err := requireMacOSSFXIcon(root, iconRelative); err != nil {
		return macOSSFXApp{}, err
	}
	if err := requireMacOSSFXFile(root, helperRelative, true); err != nil {
		return macOSSFXApp{}, err
	}

	return macOSSFXApp{
		bundlePath:     resolved,
		executablePath: filepath.Join(resolved, executableRelative),
	}, nil
}

func requireMacOSSFXDirectory(root *os.Root, relative string) error {
	info, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("inspect macOS SFX app directory %q: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("macOS SFX app directory %q is not a real directory", relative)
	}
	directory, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("open macOS SFX app directory %q: %w", relative, err)
	}
	openedInfo, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil || !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("macOS SFX app directory %q changed while it was opened", relative)
	}
	if closeErr != nil {
		return fmt.Errorf("close macOS SFX app directory %q: %w", relative, closeErr)
	}
	return nil
}

func requireMacOSSFXFile(root *os.Root, relative string, executable bool) error {
	info, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("inspect macOS SFX app file %q: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("macOS SFX app file %q is not a real file", relative)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("macOS SFX app file %q is empty", relative)
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("macOS SFX app file %q is not executable", relative)
	}
	file, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("open macOS SFX app file %q: %w", relative, err)
	}
	openedInfo, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) ||
		openedInfo.Size() != info.Size() || !openedInfo.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("macOS SFX app file %q changed while it was opened", relative)
	}
	if closeErr != nil {
		return fmt.Errorf("close macOS SFX app file %q: %w", relative, closeErr)
	}
	return nil
}

func requireMacOSSFXIcon(root *os.Root, relative string) error {
	if err := requireMacOSSFXFile(root, relative, false); err != nil {
		return err
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("inspect macOS SFX app icon: %w", err)
	}
	if info.Size() > macOSSFXIconReadLimit {
		return fmt.Errorf("macOS SFX app icon exceeds %d bytes", macOSSFXIconReadLimit)
	}
	file, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("open macOS SFX app icon: %w", err)
	}
	var header [8]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		file.Close()
		return fmt.Errorf("read macOS SFX app ICNS header: %w", err)
	}
	if !bytes.Equal(header[:4], []byte("icns")) || int64(binary.BigEndian.Uint32(header[4:])) != info.Size() {
		file.Close()
		return errors.New("macOS SFX app icon is not a valid ICNS container")
	}
	remaining := info.Size() - int64(len(header))
	validImages := 0
	for remaining > 0 {
		var elementHeader [8]byte
		if _, err := io.ReadFull(file, elementHeader[:]); err != nil {
			file.Close()
			return fmt.Errorf("read macOS SFX app ICNS element: %w", err)
		}
		elementLength := int64(binary.BigEndian.Uint32(elementHeader[4:]))
		if elementLength < int64(len(elementHeader)) || elementLength > remaining {
			file.Close()
			return errors.New("macOS SFX app icon contains an invalid ICNS element length")
		}
		payloadLength := elementLength - int64(len(elementHeader))
		elementType := string(elementHeader[:4])
		expectedPixels, imageElement := macOSSFXPNGIconDimensions[elementType]
		if imageElement {
			if payloadLength <= 0 || payloadLength > macOSSFXIconElementLimit {
				file.Close()
				return fmt.Errorf("macOS SFX app ICNS image %q has an invalid payload length", elementType)
			}
			payload := make([]byte, int(payloadLength))
			if _, err := io.ReadFull(file, payload); err != nil {
				file.Close()
				return fmt.Errorf("read macOS SFX app ICNS image %q: %w", elementType, err)
			}
			configuration, err := png.DecodeConfig(bytes.NewReader(payload))
			if err != nil || configuration.Width != expectedPixels || configuration.Height != expectedPixels {
				file.Close()
				return fmt.Errorf("macOS SFX app ICNS image %q is not a valid %dx%d PNG", elementType, expectedPixels, expectedPixels)
			}
			decoded, err := png.Decode(bytes.NewReader(payload))
			if err != nil || decoded.Bounds().Dx() != expectedPixels || decoded.Bounds().Dy() != expectedPixels {
				file.Close()
				return fmt.Errorf("macOS SFX app ICNS image %q is not a complete %dx%d PNG", elementType, expectedPixels, expectedPixels)
			}
			validImages++
		} else if _, err := io.CopyN(io.Discard, file, payloadLength); err != nil {
			file.Close()
			return fmt.Errorf("skip macOS SFX app ICNS element %q: %w", elementType, err)
		}
		remaining -= elementLength
	}
	afterRead, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !macOSSFXFileInfoMatches(info, afterRead) {
		return errors.New("macOS SFX app icon changed while its ICNS contents were read")
	}
	if closeErr != nil {
		return fmt.Errorf("close macOS SFX app icon: %w", closeErr)
	}
	if validImages == 0 {
		return errors.New("macOS SFX app icon contains no supported ICNS image representation")
	}
	return nil
}

var macOSSFXPNGIconDimensions = map[string]int{
	"icp4": 16,
	"icp5": 32,
	"ic11": 32,
	"icp6": 64,
	"ic12": 64,
	"ic07": 128,
	"ic08": 256,
	"ic13": 256,
	"ic09": 512,
	"ic14": 512,
	"ic10": 1024,
}

func readMacOSSFXPlist(root *os.Root, relative string) (map[string]string, error) {
	if err := requireMacOSSFXFile(root, relative, false); err != nil {
		return nil, err
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, fmt.Errorf("inspect macOS SFX app plist: %w", err)
	}
	if info.Size() > macOSSFXPlistReadLimit {
		return nil, fmt.Errorf("macOS SFX app plist exceeds %d bytes", macOSSFXPlistReadLimit)
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, fmt.Errorf("open macOS SFX app plist: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	decoder := xml.NewDecoder(io.LimitReader(file, macOSSFXPlistReadLimit+1))
	pendingKey := ""
	sawPlist := false
	sawDictionary := false
	sawDictionaryEnd := false

parseDictionary:
	for {
		token, decodeErr := decoder.Token()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("parse macOS SFX app plist: %w", decodeErr)
		}
		if end, ok := token.(xml.EndElement); ok {
			if sawDictionary && end.Name.Local == "dict" {
				sawDictionaryEnd = true
				break parseDictionary
			}
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !sawPlist {
			if start.Name.Local != "plist" {
				return nil, errors.New("macOS SFX app Info.plist has no plist root")
			}
			sawPlist = true
			continue
		}
		if !sawDictionary {
			if start.Name.Local != "dict" {
				return nil, errors.New("macOS SFX app Info.plist has no root dictionary")
			}
			sawDictionary = true
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return nil, fmt.Errorf("parse macOS SFX app plist key: %w", err)
			}
			pendingKey = key
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return nil, fmt.Errorf("parse macOS SFX app plist string: %w", err)
			}
			if pendingKey != "" {
				if _, duplicate := values[pendingKey]; duplicate {
					return nil, fmt.Errorf("macOS SFX app plist contains duplicate key %q", pendingKey)
				}
				values[pendingKey] = value
				pendingKey = ""
			}
		default:
			pendingKey = ""
			if err := decoder.Skip(); err != nil {
				return nil, fmt.Errorf("skip unsupported macOS SFX app plist value: %w", err)
			}
		}
	}
	if !sawPlist || !sawDictionary || !sawDictionaryEnd {
		return nil, errors.New("macOS SFX app Info.plist is incomplete")
	}
	sawPlistEnd := false
	for {
		token, decodeErr := decoder.Token()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("parse macOS SFX app plist trailer: %w", decodeErr)
		}
		switch value := token.(type) {
		case xml.StartElement:
			return nil, fmt.Errorf("macOS SFX app Info.plist contains unexpected element %q after its root dictionary", value.Name.Local)
		case xml.EndElement:
			if sawPlistEnd || value.Name.Local != "plist" {
				return nil, errors.New("macOS SFX app Info.plist has an invalid root ending")
			}
			sawPlistEnd = true
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return nil, errors.New("macOS SFX app Info.plist contains unexpected text after its root dictionary")
			}
		}
	}
	if !sawPlistEnd {
		return nil, errors.New("macOS SFX app Info.plist has no closing plist element")
	}
	afterRead, err := file.Stat()
	if err != nil || !macOSSFXFileInfoMatches(info, afterRead) {
		return nil, errors.New("macOS SFX app plist changed while it was read")
	}
	return values, nil
}

func macOSSFXFileInfoMatches(recorded, current os.FileInfo) bool {
	return recorded != nil && current != nil && os.SameFile(recorded, current) &&
		recorded.Size() == current.Size() && recorded.ModTime().Equal(current.ModTime()) &&
		recorded.Mode() == current.Mode()
}

func verifyMacOSSFXCodeSignature(ctx context.Context, path string) error {
	// Use the immutable system tool path so GUI verification never resolves a caller-controlled codesign binary.
	return runSFXVerifyCommand(
		ctx,
		"/usr/bin/codesign",
		macOSSFXCodeSignatureArguments(path)...,
	)
}

func macOSSFXCodeSignatureArguments(path string) []string {
	return []string{"--verify", "--deep", "--strict", "--verbose=2", path}
}

func verifyLinuxSFXArtifactWithDocker(ctx context.Context, path string) error {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return errors.New("Docker is required to verify the Linux SFX built on macOS")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve Linux SFX path: %w", err)
	}
	if strings.ContainsRune(absolute, ':') {
		return fmt.Errorf("Linux SFX path %q cannot be safely mounted by Docker", absolute)
	}
	workspace, err := os.MkdirTemp("", "generalsx-sfx-docker-verify-")
	if err != nil {
		return fmt.Errorf("create Docker verification workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	temporary := filepath.Join(workspace, "tmp")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		return fmt.Errorf("create Docker verification temporary directory: %w", err)
	}
	cidFile := filepath.Join(workspace, "container.cid")
	image := strings.TrimSpace(os.Getenv("GX_SFX_LINUX_BUILDER_IMAGE"))
	if image == "" {
		image = defaultLinuxSFXBuilderImage
	}
	arguments := linuxSFXDockerVerifyArguments(absolute, temporary, cidFile, image, currentDockerUser())
	defer stopDockerVerificationContainer(docker, cidFile)
	if err := runSFXVerifyCommand(ctx, docker, arguments...); err != nil {
		return fmt.Errorf("verify Linux SFX in %s: %w", image, err)
	}
	return nil
}

func linuxSFXDockerVerifyArguments(path, temporary, cidFile, image, user string) []string {
	arguments := []string{
		"run", "--rm", "--pull=never", "--platform", "linux/amd64",
		"--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", "64",
		"--env", "HOME=/tmp/home", "--env", "TMPDIR=/tmp",
		"--volume", path + ":/sfx:ro", "--volume", temporary + ":/tmp:rw",
		"--cidfile", cidFile, "--entrypoint", "/sfx",
	}
	if user != "" {
		arguments = append(arguments, "--user", user)
	}
	return append(arguments, image, "--sfx-verify")
}

func stopDockerVerificationContainer(docker, cidFile string) {
	contents, err := os.ReadFile(cidFile)
	if err != nil {
		return
	}
	containerID := strings.TrimSpace(string(contents))
	if !dockerContainerIDPattern.MatchString(containerID) {
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanupContext, docker, "rm", "--force", containerID).Run()
}

func runSFXVerifyCommand(ctx context.Context, executable string, arguments ...string) error {
	var output boundedOutput
	output.remaining = cleanupVerifierOutputLimit
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = &output
	command.Stderr = &output
	configureDesktopBackgroundCommand(command)
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		message := strings.TrimSpace(output.buffer.String())
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (output *boundedOutput) Write(contents []byte) (int, error) {
	written := len(contents)
	if output.remaining > 0 {
		keep := min(output.remaining, len(contents))
		_, _ = output.buffer.Write(contents[:keep])
		output.remaining -= keep
	}
	return written, nil
}
