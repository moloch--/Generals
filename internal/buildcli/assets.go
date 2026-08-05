package buildcli

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	steamCMDWindowsURL         = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip"
	steamCMDLinuxURL           = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"
	steamCMDMacOSURL           = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_osx.tar.gz"
	maxBIGArchiveEntries       = 250_000
	maxBIGArchiveEntryNameSize = 4_096
)

var requiredZeroHourArchives = []string{
	"AudioZH.big",
	"INIZH.big",
	"MapsZH.big",
	"MusicZH.big",
	"ShadersZH.big",
	"SpeechZH.big",
	"TerrainZH.big",
	"TexturesZH.big",
	"W3DZH.big",
	"WindowZH.big",
	"gensecZH.big",
}

var requiredBaseGeneralsArchives = []string{
	filepath.Join("ZH_Generals", "Audio.big"),
	filepath.Join("ZH_Generals", "INI.big"),
	filepath.Join("ZH_Generals", "maps.big"),
	filepath.Join("ZH_Generals", "Music.big"),
	filepath.Join("ZH_Generals", "shaders.big"),
	filepath.Join("ZH_Generals", "Speech.big"),
	filepath.Join("ZH_Generals", "Terrain.big"),
	filepath.Join("ZH_Generals", "Textures.big"),
	filepath.Join("ZH_Generals", "W3D.big"),
	filepath.Join("ZH_Generals", "Window.big"),
	filepath.Join("ZH_Generals", "gensec.big"),
}

type retailLanguageFamily struct {
	zeroHourStem      string
	baseGeneralsStem  string
	zeroHourValue     string
	baseGeneralsValue string
}

// This order matches the engine's language auto-detection precedence. Steam's
// German depot is the sole family whose base Generals archive stem differs
// from the Zero Hour stem: GermanZH.big is paired with German2.big.
var supportedRetailLanguages = []retailLanguageFamily{
	{zeroHourStem: "Brazilian", baseGeneralsStem: "Brazilian", zeroHourValue: "brazilian", baseGeneralsValue: "brazilian"},
	{zeroHourStem: "English", baseGeneralsStem: "English", zeroHourValue: "english", baseGeneralsValue: "english"},
	{zeroHourStem: "German", baseGeneralsStem: "German2", zeroHourValue: "german", baseGeneralsValue: "german2"},
	{zeroHourStem: "French", baseGeneralsStem: "French", zeroHourValue: "french", baseGeneralsValue: "french"},
	{zeroHourStem: "Italian", baseGeneralsStem: "Italian", zeroHourValue: "italian", baseGeneralsValue: "italian"},
	{zeroHourStem: "Spanish", baseGeneralsStem: "Spanish", zeroHourValue: "spanish", baseGeneralsValue: "spanish"},
	{zeroHourStem: "Chinese", baseGeneralsStem: "Chinese", zeroHourValue: "chinese", baseGeneralsValue: "chinese"},
	{zeroHourStem: "Korean", baseGeneralsStem: "Korean", zeroHourValue: "korean", baseGeneralsValue: "korean"},
	{zeroHourStem: "Polish", baseGeneralsStem: "Polish", zeroHourValue: "polish", baseGeneralsValue: "polish"},
}

// GeneralsX @build Codex 04/08/2026 Acquire only user-owned retail data through an interactive Steam session.
func (app application) acquireAssets(ctx context.Context) error {
	if err := validateAssets(app.cfg.assetsDir, app.cfg.target); err == nil {
		fmt.Fprintf(app.runner.stdout, "Using existing retail assets: %s\n", app.cfg.assetsDir)
		return nil
	}
	if app.cfg.skipAssets {
		if app.cfg.dryRun {
			fmt.Fprintf(app.runner.stdout, "[dry-run] require existing Zero Hour assets at %s\n", app.cfg.assetsDir)
			return nil
		}
		return validateAssets(app.cfg.assetsDir, app.cfg.target)
	}
	steamUser := app.cfg.steamUser
	if steamUser == "" {
		if app.cfg.dryRun {
			steamUser = "<STEAM_USER>"
		} else {
			return fmt.Errorf("retail assets are incomplete at %s; pass --steam-user NAME or --skip-assets with --assets-dir", app.cfg.assetsDir)
		}
	}
	if app.cfg.nonInteractive {
		return errors.New("SteamCMD authentication is interactive; prepare assets first and rerun with --non-interactive --skip-assets")
	}
	if !app.cfg.dryRun {
		if err := ensurePrivateDirectory(app.cfg.assetsDir); err != nil {
			return fmt.Errorf("prepare private retail asset directory: %w", err)
		}
	}
	steamCMD, err := app.ensureSteamCMD(ctx)
	if err != nil {
		return err
	}
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] create private Steam install directory %s\n", app.cfg.assetsDir)
	}
	args := []string{
		"+@sSteamCmdForcePlatformType", "windows",
		"+force_install_dir", app.cfg.assetsDir,
		"+login", steamUser,
		"+app_update", zeroHourSteamAppID, "validate",
		"+quit",
	}
	fmt.Fprintln(app.runner.stdout, "SteamCMD will prompt directly for the account password and any Steam Guard challenge.")
	fmt.Fprintln(app.runner.stdout, "The builder never accepts, stores, or logs those secrets.")
	spec := command{name: steamCMD, args: args, dir: app.cfg.steamCMDDir}
	if err := app.runInteractive(ctx, spec, InteractiveSteamAuthentication); err != nil {
		return fmt.Errorf("download Steam app %s: %w", zeroHourSteamAppID, err)
	}
	if app.cfg.dryRun {
		return nil
	}
	return validateAssets(app.cfg.assetsDir, app.cfg.target)
}

func (app application) needsSteamCMD() bool {
	if app.cfg.skipAssets {
		return false
	}
	return validateAssets(app.cfg.assetsDir, app.cfg.target) != nil
}

func (app application) ensureSteamCMD(ctx context.Context) (string, error) {
	managedName := "steamcmd.sh"
	pathName := "steamcmd"
	archiveName := "steamcmd.tar.gz"
	downloadURL := steamCMDLinuxURL
	if app.hostOS == "windows" {
		managedName = "steamcmd.exe"
		pathName = "steamcmd.exe"
		archiveName = "steamcmd.zip"
		downloadURL = steamCMDWindowsURL
	} else if app.hostOS == "darwin" {
		downloadURL = steamCMDMacOSURL
	}
	if app.hostOS == "darwin" {
		if err := app.ensureRosetta(ctx); err != nil {
			return "", err
		}
	}
	managedPath := filepath.Join(app.cfg.steamCMDDir, managedName)
	if info, err := os.Stat(managedPath); err == nil && info.Mode().IsRegular() {
		if !app.cfg.dryRun {
			if err := ensurePrivateDirectory(app.cfg.steamCMDDir); err != nil {
				return "", err
			}
		}
		return managedPath, nil
	}
	if existing, err := exec.LookPath(pathName); err == nil {
		if app.cfg.dryRun {
			fmt.Fprintf(app.runner.stdout, "[dry-run] use private SteamCMD working directory %s\n", app.cfg.steamCMDDir)
		} else if err := ensurePrivateDirectory(app.cfg.steamCMDDir); err != nil {
			return "", err
		}
		return existing, nil
	}
	if !app.cfg.installDeps {
		return "", errors.New("SteamCMD is missing; rerun with --install-deps or provide --steamcmd-dir")
	}
	archivePath := filepath.Join(app.cfg.cacheDir, "downloads", archiveName)
	fmt.Fprintf(app.runner.stdout, "Installing SteamCMD privately in %s\n", app.cfg.steamCMDDir)
	if app.cfg.dryRun {
		fmt.Fprintf(app.runner.stdout, "[dry-run] download %s -> %s\n", downloadURL, archivePath)
		fmt.Fprintf(app.runner.stdout, "[dry-run] extract %s -> %s\n", archivePath, app.cfg.steamCMDDir)
		return managedPath, nil
	}
	if err := downloadFile(ctx, app.http, downloadURL, archivePath, ""); err != nil {
		return "", err
	}
	if strings.HasSuffix(archiveName, ".zip") {
		if err := extractZip(archivePath, app.cfg.steamCMDDir); err != nil {
			return "", fmt.Errorf("install SteamCMD: %w", err)
		}
	} else if err := extractTarGzip(archivePath, app.cfg.steamCMDDir); err != nil {
		return "", fmt.Errorf("install SteamCMD: %w", err)
	}
	resolved, err := findFileCaseInsensitive(app.cfg.steamCMDDir, managedName)
	if err != nil {
		return "", fmt.Errorf("locate installed SteamCMD: %w", err)
	}
	if app.hostOS != "windows" {
		if err := os.Chmod(resolved, 0o700); err != nil {
			return "", fmt.Errorf("make SteamCMD executable: %w", err)
		}
	}
	return resolved, nil
}

func (app application) ensureRosetta(ctx context.Context) error {
	if app.hostOS != "darwin" || app.hostArch != "arm64" {
		return nil
	}
	if app.cfg.dryRun {
		fmt.Fprintln(app.runner.stdout, "> /usr/bin/arch -x86_64 /usr/bin/true")
	}
	probe := exec.CommandContext(ctx, "/usr/bin/arch", "-x86_64", "/usr/bin/true")
	if err := probe.Run(); err == nil {
		return nil
	}
	if !app.cfg.installDeps {
		return errors.New("Valve's macOS SteamCMD bootstrap requires Rosetta 2; rerun with --install-deps --accept-sdk-licenses")
	}
	if !app.cfg.acceptSDKLicenses {
		return errors.New("installing Rosetta 2 requires --accept-sdk-licenses")
	}
	if app.cfg.nonInteractive {
		return errors.New("Rosetta 2 installation may require sudo interaction; install it before using --non-interactive")
	}
	if err := app.runInteractive(ctx, command{
		name: "sudo",
		args: []string{"/usr/sbin/softwareupdate", "--install-rosetta", "--agree-to-license"},
	}, InteractiveDependencyInstallation); err != nil {
		return fmt.Errorf("install Rosetta 2: %w", err)
	}
	return nil
}

func validateAssets(root string, targetOS target) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect retail asset directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("retail asset path %q is not a directory", root)
	}
	for _, relative := range append(append([]string(nil), requiredZeroHourArchives...), requiredBaseGeneralsArchives...) {
		if err := validateBIGArchive(root, relative); err != nil {
			return err
		}
	}
	if err := validateLocalizedArchives(root); err != nil {
		return err
	}
	if targetOS == targetWindows {
		for _, relative := range []string{"binkw32.dll", "mss32.dll"} {
			path, err := findFileCaseInsensitive(root, relative)
			if err != nil {
				return fmt.Errorf("Windows retail runtime requires %s: %w", relative, err)
			}
			if err := validateFileSignature(path, relative, "MZ"); err != nil {
				return err
			}
		}
	}
	return nil
}

// GeneralsX @feature Codex 05/08/2026 Share strict retail validation with graphical frontends.
// ValidateRetailAssets validates an owned asset tree for auto, macos, linux,
// or windows using the same rules as the command-line build.
func ValidateRetailAssets(root, requestedTarget string) error {
	if requestedTarget == "" {
		requestedTarget = "auto"
	}
	resolvedTarget, err := parseTarget(requestedTarget, runtime.GOOS)
	if err != nil {
		return err
	}
	return validateAssets(root, resolvedTarget)
}

func validateLocalizedArchives(root string) error {
	for _, family := range supportedRetailLanguages {
		mainArchive := family.zeroHourStem + "ZH.big"
		if _, err := findFileCaseInsensitive(root, mainArchive); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect localized archive %s: %w", mainArchive, err)
		}
		required := []string{
			mainArchive,
			"Audio" + family.zeroHourStem + "ZH.big",
			"Speech" + family.zeroHourStem + "ZH.big",
			"W3D" + family.zeroHourStem + "ZH.big",
			filepath.Join("ZH_Generals", family.baseGeneralsStem+".big"),
			filepath.Join("ZH_Generals", "Audio"+family.baseGeneralsStem+".big"),
			filepath.Join("ZH_Generals", "Speech"+family.baseGeneralsStem+".big"),
		}
		for _, relative := range required {
			if err := validateBIGArchive(root, relative); err != nil {
				return fmt.Errorf("%s retail language pack is incomplete: %w", family.zeroHourValue, err)
			}
		}
		return nil
	}
	return errors.New("retail asset tree has no supported localized archive (Brazilian, English, German, French, Italian, Spanish, Chinese, Korean, or Polish)")
}

func validateBIGArchive(root, relative string) error {
	path, err := findFileCaseInsensitive(root, relative)
	if err != nil {
		return fmt.Errorf("retail asset tree is missing %s: %w", filepath.ToSlash(relative), err)
	}
	label := filepath.ToSlash(relative)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect retail file %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("retail file %s must be a non-empty regular file", label)
	}
	if info.Size() < 16 {
		return fmt.Errorf("retail file %s has a truncated BIGF header", label)
	}
	if info.Size() > int64(^uint32(0)) {
		return fmt.Errorf("retail file %s exceeds the BIGF 32-bit size limit", label)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open retail file %s: %w", label, err)
	}
	defer file.Close()

	header := make([]byte, 16)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read retail file %s BIGF header: %w", label, err)
	}
	if string(header[:4]) != "BIGF" {
		return fmt.Errorf("retail file %s has an invalid signature", label)
	}
	declaredSize := binary.LittleEndian.Uint32(header[4:8])
	entryCount := binary.BigEndian.Uint32(header[8:12])
	directoryBoundary := binary.BigEndian.Uint32(header[12:16])
	if uint64(declaredSize) != uint64(info.Size()) {
		return fmt.Errorf("retail file %s BIGF size field is %d, actual size is %d", label, declaredSize, info.Size())
	}
	if entryCount == 0 {
		return fmt.Errorf("retail file %s BIGF directory is empty", label)
	}
	if entryCount > maxBIGArchiveEntries {
		return fmt.Errorf("retail file %s BIGF entry count %d exceeds limit %d", label, entryCount, maxBIGArchiveEntries)
	}
	if directoryBoundary < 16 || directoryBoundary > declaredSize {
		return fmt.Errorf("retail file %s has an invalid BIGF directory boundary", label)
	}
	// Every entry consumes at least an eight-byte offset/size pair and a NUL
	// filename terminator. Reject impossible counts before looping over input.
	if uint64(entryCount) > (uint64(directoryBoundary)-16)/9 {
		return fmt.Errorf("retail file %s has an impossible BIGF entry count", label)
	}

	position := uint64(16)
	entryHeader := make([]byte, 8)
	for index := uint32(0); index < entryCount; index++ {
		if position+uint64(len(entryHeader)) > uint64(directoryBoundary) {
			return fmt.Errorf("retail file %s has a truncated BIGF entry %d", label, index)
		}
		if _, err := io.ReadFull(file, entryHeader); err != nil {
			return fmt.Errorf("read retail file %s BIGF entry %d: %w", label, index, err)
		}
		position += uint64(len(entryHeader))
		payloadOffset := binary.BigEndian.Uint32(entryHeader[:4])
		payloadSize := binary.BigEndian.Uint32(entryHeader[4:])

		nameLength := uint64(0)
		for {
			if position >= uint64(directoryBoundary) {
				return fmt.Errorf("retail file %s has an unterminated BIGF entry %d name", label, index)
			}
			var character [1]byte
			if _, err := io.ReadFull(file, character[:]); err != nil {
				return fmt.Errorf("read retail file %s BIGF entry %d name: %w", label, index, err)
			}
			position++
			if character[0] == 0 {
				break
			}
			nameLength++
			if nameLength > maxBIGArchiveEntryNameSize {
				return fmt.Errorf("retail file %s BIGF entry %d name exceeds limit %d", label, index, maxBIGArchiveEntryNameSize)
			}
		}
		if nameLength == 0 {
			return fmt.Errorf("retail file %s has an empty BIGF entry %d name", label, index)
		}
		if payloadOffset < directoryBoundary || uint64(payloadOffset)+uint64(payloadSize) > uint64(declaredSize) {
			return fmt.Errorf("retail file %s has out-of-bounds BIGF entry %d payload", label, index)
		}
	}
	return nil
}

func validateFileSignature(path, label string, signatures ...string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect retail file %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("retail file %s must be a non-empty regular file", label)
	}
	maximumLength := 0
	for _, signature := range signatures {
		if len(signature) > maximumLength {
			maximumLength = len(signature)
		}
	}
	prefix := make([]byte, maximumLength)
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open retail file %s: %w", label, err)
	}
	_, readErr := io.ReadFull(file, prefix)
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read retail file %s signature: %w", label, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close retail file %s: %w", label, closeErr)
	}
	for _, signature := range signatures {
		if string(prefix[:len(signature)]) == signature {
			return nil
		}
	}
	return fmt.Errorf("retail file %s has an invalid signature", label)
}

func findFileCaseInsensitive(root, relative string) (string, error) {
	components := strings.FieldsFunc(filepath.Clean(relative), func(r rune) bool {
		return r == '/' || r == '\\'
	})
	current := root
	for _, component := range components {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", err
		}
		matched := ""
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), component) {
				if matched != "" && matched != entry.Name() {
					return "", fmt.Errorf("case-colliding entries %q and %q", matched, entry.Name())
				}
				matched = entry.Name()
			}
		}
		if matched == "" {
			return "", fs.ErrNotExist
		}
		current = filepath.Join(current, matched)
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", current)
	}
	return current, nil
}
