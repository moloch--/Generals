// GeneralsX @feature Codex 10/10/2026 Validate versioned, asset-free runtime packages before local assembly.
package runtimebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion            = 1
	MaxDescriptorBytes       = 8 << 20
	MaxArchiveBytes    int64 = 2 << 30
	MaxExpandedBytes   int64 = 4 << 30
	MaxFiles                 = 30000
)

type Archive struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// File records regular files and relative symlinks. Directories are implicit.
// Symlink hashes cover the UTF-8 target and their size is zero.
type File struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Mode       uint32 `json:"mode"`
	LinkTarget string `json:"link_target,omitempty"`
}

type Descriptor struct {
	SchemaVersion       int               `json:"schema_version"`
	Version             string            `json:"version"`
	Repository          string            `json:"repository"`
	SourceCommit        string            `json:"source_commit"`
	Target              string            `json:"target"`
	EngineArch          string            `json:"engine_arch"`
	LauncherArch        string            `json:"launcher_arch"`
	OnlineEndpoint      string            `json:"online_endpoint"`
	MinimumOS           string            `json:"minimum_os,omitempty"`
	MinimumGLIBC        string            `json:"minimum_glibc,omitempty"`
	OnlineCompatibility int               `json:"online_compatibility"`
	Preset              string            `json:"preset,omitempty"`
	Dependencies        map[string]string `json:"dependencies,omitempty"`
	Archive             Archive           `json:"archive"`
	Files               []File            `json:"files"`
}

var versionPattern = regexp.MustCompile(`^runtime-v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var osVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
var endpointPattern = regexp.MustCompile(`^(?:tls://)?[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$`)
var reservedDevicePattern = regexp.MustCompile(`^(COM|LPT)[1-9¹²³]$`)

func ArtifactStem(version, target string) string {
	return "generalsxzh-runtime-" + version + "-" + target
}

func ReadDescriptor(reader io.Reader) (Descriptor, error) {
	var descriptor Descriptor
	data, err := io.ReadAll(io.LimitReader(reader, MaxDescriptorBytes+1))
	if err != nil {
		return descriptor, err
	}
	if len(data) > MaxDescriptorBytes {
		return descriptor, errors.New("runtime descriptor exceeds size limit")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return descriptor, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return descriptor, fmt.Errorf("decode runtime descriptor: %w", err)
	}
	if err := descriptor.Validate(); err != nil {
		return descriptor, err
	}
	return descriptor, nil
}

func (d Descriptor) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported runtime schema %d", d.SchemaVersion)
	}
	if !versionPattern.MatchString(d.Version) || len(d.Version) > 100 {
		return errors.New("invalid runtime release version")
	}
	if !repositoryPattern.MatchString(d.Repository) || len(d.Repository) > 200 {
		return errors.New("invalid runtime repository")
	}
	if !commitPattern.MatchString(d.SourceCommit) {
		return errors.New("runtime source_commit must be a full lowercase Git SHA")
	}
	engine, launcher := "", ""
	switch d.Target {
	case "linux-amd64":
		engine, launcher = "amd64", "amd64"
	case "macos-arm64":
		engine, launcher = "arm64", "arm64"
	case "windows-x86":
		engine, launcher = "x86", "amd64"
	default:
		return fmt.Errorf("unsupported runtime target %q", d.Target)
	}
	if d.EngineArch != engine || d.LauncherArch != launcher {
		return errors.New("runtime architecture metadata does not match target")
	}
	if d.OnlineCompatibility != 2 {
		return fmt.Errorf("unsupported Online compatibility %d", d.OnlineCompatibility)
	}
	if d.OnlineEndpoint != "" && (len(d.OnlineEndpoint) > 270 || !endpointPattern.MatchString(d.OnlineEndpoint)) {
		return errors.New("invalid built-in Online endpoint")
	}
	for _, value := range []string{d.MinimumOS, d.MinimumGLIBC} {
		if value != "" && !osVersionPattern.MatchString(value) {
			return errors.New("invalid runtime minimum OS/GLIBC version")
		}
	}
	if len(d.Dependencies) > 200 || len(d.Preset) > 100 {
		return errors.New("runtime build metadata exceeds limits")
	}
	for key, value := range d.Dependencies {
		if len(key) == 0 || len(key) > 100 || len(value) > 500 {
			return errors.New("invalid dependency metadata")
		}
	}
	if d.Archive.Name != ArtifactStem(d.Version, d.Target)+".tar.gz" || d.Archive.Size <= 0 || d.Archive.Size > MaxArchiveBytes || !validHash(d.Archive.SHA256) {
		return errors.New("invalid runtime archive identity, size or SHA-256")
	}
	if len(d.Files) == 0 || len(d.Files) > MaxFiles {
		return errors.New("runtime file count exceeds limits")
	}
	entries := make(map[string]File, len(d.Files))
	components := make(map[string]string)
	var total int64
	for _, file := range d.Files {
		if err := validatePath(file.Path); err != nil {
			return err
		}
		if !strings.HasPrefix(file.Path, "runtime/") && !strings.HasPrefix(file.Path, "kit/") {
			return fmt.Errorf("unexpected package root %q", file.Path)
		}
		if strings.HasPrefix(file.Path, "runtime/") {
			if err := validateRuntimePath(strings.TrimPrefix(file.Path, "runtime/"), d.Target); err != nil {
				return err
			}
		}
		if _, exists := entries[file.Path]; exists {
			return fmt.Errorf("duplicate runtime path %q", file.Path)
		}
		parts := strings.Split(file.Path, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			folded := strings.ToLower(prefix)
			if old, exists := components[folded]; exists && old != prefix {
				return fmt.Errorf("case-colliding runtime paths %q and %q", old, prefix)
			}
			components[folded] = prefix
		}
		if file.Mode != 0644 && file.Mode != 0755 && !(file.LinkTarget != "" && file.Mode == 0777) {
			return fmt.Errorf("unsafe runtime file mode for %q", file.Path)
		}
		if !validHash(file.SHA256) || file.Size < 0 || file.Size > MaxExpandedBytes {
			return fmt.Errorf("invalid file size/hash for %q", file.Path)
		}
		total += file.Size
		if total > MaxExpandedBytes {
			return errors.New("runtime expanded size exceeds limit")
		}
		if file.LinkTarget != "" {
			if d.Target == "windows-x86" || !strings.HasPrefix(file.Path, "runtime/") {
				return errors.New("symlinks are permitted only in POSIX runtimes")
			}
			if file.Size != 0 || file.Mode != 0777 || file.SHA256 != hashBytes([]byte(file.LinkTarget)) {
				return fmt.Errorf("invalid symlink metadata for %q", file.Path)
			}
			if err := validateRelativeLink(file.Path, file.LinkTarget); err != nil {
				return err
			}
		}
		entries[file.Path] = file
	}
	for name, file := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := entries[parent]; exists {
				return fmt.Errorf("file/symlink used as parent %q", parent)
			}
		}
		if file.LinkTarget == "" {
			continue
		}
		seen := map[string]bool{name: true}
		for file.LinkTarget != "" {
			name = path.Join(path.Dir(name), file.LinkTarget)
			if seen[name] || len(seen) > 64 {
				return errors.New("runtime symlink loop")
			}
			seen[name] = true
			var exists bool
			file, exists = entries[name]
			if !exists {
				return fmt.Errorf("runtime symlink target is not a manifested file: %q", name)
			}
		}
	}
	entry := "runtime/GeneralsXZH"
	if d.Target == "windows-x86" {
		entry = "runtime/generalszh.exe"
	}
	for _, required := range []string{entry, "kit/scripts/tooling/sfx/go.mod", "kit/scripts/tooling/sfx/go.sum", "kit/scripts/tooling/sfx/cmd/generalsx-sfx/main.go", "kit/scripts/tooling/sfx/cmd/generalsx-sfx-pack/main.go", "kit/LICENSE.md"} {
		file, ok := entries[required]
		if !ok || file.LinkTarget != "" || file.Size == 0 {
			return fmt.Errorf("runtime package missing required file %q", required)
		}
	}
	if d.Target != "windows-x86" && entries[entry].Mode != 0755 {
		return errors.New("POSIX engine must be executable")
	}
	return nil
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
func hashBytes(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }

func validatePath(name string) error {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "/") {
		return fmt.Errorf("unsafe runtime path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, "\\<>:\"|?*") {
			return fmt.Errorf("unsafe runtime path %q", name)
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return fmt.Errorf("unsafe runtime path %q", name)
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || reservedDevicePattern.MatchString(base) {
			return fmt.Errorf("reserved runtime path %q", name)
		}
	}
	return nil
}

func validateRelativeLink(name, target string) error {
	if target == "" || len(target) > 1024 || path.Clean(target) != target || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\:\x00") {
		return fmt.Errorf("unsafe runtime symlink %q", name)
	}
	// Resolve relative to the runtime subtree itself. Assembly removes the
	// outer "runtime/" prefix, so a link that exits and re-enters that prefix
	// (../../runtime/lib/foo.so) would otherwise escape the final stage.
	runtimeRelative := path.Join(path.Dir(strings.TrimPrefix(name, "runtime/")), target)
	if runtimeRelative == ".." || strings.HasPrefix(runtimeRelative, "../") || path.IsAbs(runtimeRelative) {
		return fmt.Errorf("runtime symlink escapes runtime root: %q", name)
	}
	resolved := "runtime/" + runtimeRelative
	return validatePath(resolved)
}

func validateRuntimePath(name, target string) error {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".big", ".bik", ".rep", ".map", ".exe", ".dll"} {
		if strings.HasPrefix(lower, "licenses/") && strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("binary or retail data forbidden in licenses: %q", name)
		}
	}
	if strings.HasPrefix(lower, "licenses/") {
		return nil
	}
	if name == "build-metadata.json" {
		return nil
	}
	if lower == "binkw32.dll" || lower == "mss32.dll" {
		return fmt.Errorf("retail DLL is forbidden in published runtime: %q", name)
	}
	if target == "windows-x86" {
		if name == "generalszh.exe" || !strings.Contains(name, "/") && strings.HasSuffix(lower, ".dll") {
			return nil
		}
	} else {
		if name == "GeneralsXZH" || name == "dxvk.conf" || name == "MoltenVK_icd.json" || name == "Window/Menus/ExtrasMenu.wnd" {
			return nil
		}
		if strings.HasPrefix(name, "lib/") && !strings.Contains(strings.TrimPrefix(name, "lib/"), "/") && (strings.Contains(lower, ".so") || strings.HasSuffix(lower, ".dylib")) {
			return nil
		}
		if target == "macos-arm64" && strings.HasPrefix(name, "fontconfig/") && (strings.HasSuffix(lower, ".conf") || path.Base(name) == "README") {
			return nil
		}
	}
	return fmt.Errorf("file is outside the asset-free runtime allowlist: %q", name)
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("runtime descriptor exceeds JSON depth limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate or invalid runtime descriptor key")
				}
				keys[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return fmt.Errorf("runtime descriptor JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing runtime descriptor JSON")
	}
	return nil
}
