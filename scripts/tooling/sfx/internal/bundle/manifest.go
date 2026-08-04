// GeneralsX @feature OpenAI 30/07/2026 Defines the portable self-extracting bundle manifest.
package bundle

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// SchemaVersion is the original game-only manifest schema.
	SchemaVersion = 1
	// OnlineServerSchemaVersion adds an authenticated optional sidecar while
	// retaining read compatibility with schema-v1 bundles.
	OnlineServerSchemaVersion = 2

	// MaxManifestBytes bounds both generated manifests and untrusted parsing.
	MaxManifestBytes = 4 << 20

	maxJSONDepth    = 128
	maxSymlinkDepth = 64
	maxEpoch        = 253_402_300_799 // 9999-12-31T23:59:59Z
)

const CompressionXZ = "xz"

// EntryType identifies the filesystem object represented by an Entry.
type EntryType string

const (
	EntryDirectory EntryType = "directory"
	EntryFile      EntryType = "file"
	EntrySymlink   EntryType = "symlink"
)

// Entry describes one archive member. Paths always use forward slashes and
// never begin with a slash.
type Entry struct {
	Path       string    `json:"path"`
	Type       EntryType `json:"type"`
	Mode       uint32    `json:"mode"`
	Size       int64     `json:"size,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	LinkTarget string    `json:"link_target,omitempty"`
}

// Manifest authenticates the complete, ordered contents of a bundle tar.
type Manifest struct {
	SchemaVersion          int     `json:"schema_version"`
	Product                string  `json:"product"`
	Version                string  `json:"version"`
	TargetOS               string  `json:"target_os"`
	TargetArch             string  `json:"target_arch"`
	Entrypoint             string  `json:"entrypoint"`
	WorkDir                string  `json:"work_dir,omitempty"`
	OnlineServerEntrypoint string  `json:"online_server_entrypoint,omitempty"`
	Epoch                  int64   `json:"epoch"`
	Compression            string  `json:"compression"`
	PayloadSHA256          string  `json:"payload_sha256"`
	PayloadSize            int64   `json:"payload_size"`
	Entries                []Entry `json:"entries"`
	TotalSize              int64   `json:"total_size"`
}

// MarshalManifest validates m and returns deterministic JSON. Struct field
// order is part of schema v1 and therefore stable across calls.
func MarshalManifest(m Manifest) ([]byte, error) {
	if err := m.ValidatePayload(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("bundle manifest exceeds %d bytes", MaxManifestBytes)
	}
	return data, nil
}

// ParseManifest decodes one strict supported manifest document.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > MaxManifestBytes {
		return m, fmt.Errorf("bundle manifest exceeds %d bytes", MaxManifestBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return m, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if err := validateManifestJSONFields(data); err != nil {
		return m, fmt.Errorf("decode bundle manifest: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("decode bundle manifest: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return m, errors.New("decode bundle manifest: trailing JSON value")
		}
		return m, fmt.Errorf("decode bundle manifest trailing data: %w", err)
	}
	if err := m.ValidatePayload(); err != nil {
		return m, err
	}
	return m, nil
}

func validateManifestJSONFields(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	allowedManifestFields := map[string]bool{
		"schema_version": true,
		"product":        true,
		"version":        true,
		"target_os":      true,
		"target_arch":    true,
		"entrypoint":     true,
		"work_dir":       true,
		// GeneralsX @feature Codex 04/08/2026 Authenticate the optional Online server executable in versioned manifests.
		"online_server_entrypoint": true,
		"epoch":                    true,
		"compression":              true,
		"payload_sha256":           true,
		"payload_size":             true,
		"entries":                  true,
		"total_size":               true,
	}
	for field := range object {
		if !allowedManifestFields[field] {
			return fmt.Errorf("unknown JSON field %q", field)
		}
	}
	for _, field := range []string{
		"schema_version", "product", "version", "target_os", "target_arch",
		"entrypoint", "epoch", "compression", "payload_sha256", "payload_size",
		"entries", "total_size",
	} {
		if _, exists := object[field]; !exists {
			return fmt.Errorf("missing JSON field %q", field)
		}
	}

	entriesJSON := object["entries"]
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(entriesJSON, &entries); err != nil {
		return fmt.Errorf("decode entries: %w", err)
	}
	allowedEntryFields := map[string]bool{
		"path":        true,
		"type":        true,
		"mode":        true,
		"size":        true,
		"sha256":      true,
		"link_target": true,
	}
	for index, entry := range entries {
		for field := range entry {
			if !allowedEntryFields[field] {
				return fmt.Errorf("entry %d has unknown JSON field %q", index, field)
			}
		}
		for _, field := range []string{"path", "type", "mode"} {
			if _, exists := entry[field]; !exists {
				return fmt.Errorf("entry %d is missing JSON field %q", index, field)
			}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walkValue func(int) error
	walkValue = func(depth int) error {
		if depth > maxJSONDepth {
			return fmt.Errorf("JSON nesting exceeds %d levels", maxJSONDepth)
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walkValue(depth + 1); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walkValue(depth + 1); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	if err := walkValue(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// Validate checks schema invariants before any archive bytes are trusted.
func (m Manifest) Validate() error {
	return m.validate(false)
}

// ValidatePayload requires the compressed-payload metadata in addition to all
// filesystem invariants. WriteTar leaves these fields empty so its caller can
// populate them while it streams the tar through the selected compressor.
func (m Manifest) ValidatePayload() error {
	return m.validate(true)
}

func (m Manifest) validate(requirePayload bool) error {
	switch m.SchemaVersion {
	case SchemaVersion:
		if m.OnlineServerEntrypoint != "" {
			return fmt.Errorf(
				"online server entrypoint requires bundle manifest schema %d",
				OnlineServerSchemaVersion,
			)
		}
	case OnlineServerSchemaVersion:
	default:
		return fmt.Errorf("unsupported bundle manifest schema %d", m.SchemaVersion)
	}
	if err := validateMetadata("product", m.Product); err != nil {
		return err
	}
	if err := validateMetadata("version", m.Version); err != nil {
		return err
	}
	switch m.TargetOS {
	case "darwin", "linux", "windows":
	default:
		return fmt.Errorf("unsupported target OS %q", m.TargetOS)
	}
	if err := validateTargetArch(m.TargetArch); err != nil {
		return err
	}
	if m.Epoch < 0 || m.Epoch > maxEpoch {
		return fmt.Errorf("bundle epoch is outside the supported range: %d", m.Epoch)
	}
	if err := validatePayloadMetadata(m, requirePayload); err != nil {
		return err
	}
	if len(m.Entries) > defaultLimits.MaxEntries {
		return fmt.Errorf("bundle has %d entries; limit is %d", len(m.Entries), defaultLimits.MaxEntries)
	}
	if err := validateArchivePath(m.Entrypoint, m.TargetOS); err != nil {
		return fmt.Errorf("invalid entrypoint: %w", err)
	}
	if m.WorkDir != "" {
		if err := validateArchivePath(m.WorkDir, m.TargetOS); err != nil {
			return fmt.Errorf("invalid work directory: %w", err)
		}
	}
	if m.OnlineServerEntrypoint != "" {
		if err := validateArchivePath(m.OnlineServerEntrypoint, m.TargetOS); err != nil {
			return fmt.Errorf("invalid online server entrypoint: %w", err)
		}
	}

	entriesByPath := make(map[string]Entry, len(m.Entries))
	foldedPaths := make(map[string]string, len(m.Entries))
	var totalSize int64
	for i, entry := range m.Entries {
		if err := validateArchivePath(entry.Path, m.TargetOS); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
		if i > 0 && m.Entries[i-1].Path >= entry.Path {
			return fmt.Errorf("manifest entries are not strictly sorted at %q", entry.Path)
		}
		if _, exists := entriesByPath[entry.Path]; exists {
			return fmt.Errorf("duplicate manifest path %q", entry.Path)
		}
		if isCaseFoldingTarget(m.TargetOS) {
			folded := strings.ToLower(entry.Path)
			if other, exists := foldedPaths[folded]; exists {
				return fmt.Errorf("case-folding path collision between %q and %q", other, entry.Path)
			}
			foldedPaths[folded] = entry.Path
		}
		if entry.Mode&^uint32(0o777) != 0 {
			return fmt.Errorf("entry %q has unsafe mode %#o", entry.Path, entry.Mode)
		}

		switch entry.Type {
		case EntryDirectory:
			if entry.Size != 0 || entry.SHA256 != "" || entry.LinkTarget != "" {
				return fmt.Errorf("directory %q has file or link metadata", entry.Path)
			}
			if m.TargetOS == "windows" && entry.Mode != 0o755 {
				return fmt.Errorf("Windows directory %q has non-normalized mode %#o", entry.Path, entry.Mode)
			}
			if m.TargetOS != "windows" && entry.Mode&0o100 == 0 {
				return fmt.Errorf("directory %q is not traversable by its owner", entry.Path)
			}
		case EntryFile:
			if entry.Size < 0 || entry.Size > defaultLimits.MaxFileSize {
				return fmt.Errorf("file %q has invalid size %d", entry.Path, entry.Size)
			}
			if !validSHA256(entry.SHA256) {
				return fmt.Errorf("file %q has invalid SHA-256 digest", entry.Path)
			}
			if entry.LinkTarget != "" {
				return fmt.Errorf("file %q has a link target", entry.Path)
			}
			if m.TargetOS == "windows" && entry.Mode != 0o644 {
				return fmt.Errorf("Windows file %q has non-normalized mode %#o", entry.Path, entry.Mode)
			}
			if entry.Size > defaultLimits.MaxTotalSize-totalSize {
				return fmt.Errorf("bundle total size exceeds %d bytes", defaultLimits.MaxTotalSize)
			}
			totalSize += entry.Size
		case EntrySymlink:
			if m.TargetOS == "windows" {
				return fmt.Errorf("Windows bundle contains unsupported symlink %q", entry.Path)
			}
			if entry.Size != 0 || entry.SHA256 != "" {
				return fmt.Errorf("symlink %q has file metadata", entry.Path)
			}
			if err := validateLinkTarget(entry.Path, entry.LinkTarget); err != nil {
				return err
			}
		default:
			return fmt.Errorf("entry %q has unsupported type %q", entry.Path, entry.Type)
		}
		entriesByPath[entry.Path] = entry
	}
	if totalSize != m.TotalSize {
		return fmt.Errorf("manifest total size is %d; entries total %d", m.TotalSize, totalSize)
	}

	for _, entry := range m.Entries {
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			parentEntry, exists := entriesByPath[parent]
			if !exists {
				return fmt.Errorf("entry %q has missing parent directory %q", entry.Path, parent)
			}
			if parentEntry.Type != EntryDirectory {
				return fmt.Errorf("entry %q has non-directory parent %q", entry.Path, parent)
			}
		}
		if entry.Type == EntrySymlink {
			if _, err := resolveManifestLink(entry.Path, entriesByPath, nil); err != nil {
				return err
			}
		}
	}
	if err := validateSymlinkGraph(entriesByPath); err != nil {
		return err
	}
	if err := validateDirectoryTopology(entriesByPath); err != nil {
		return err
	}

	entrypoint, exists := entriesByPath[m.Entrypoint]
	if !exists {
		return fmt.Errorf("entrypoint %q is missing from manifest", m.Entrypoint)
	}
	if entrypoint.Type != EntryFile {
		return fmt.Errorf("entrypoint %q is not a regular file", m.Entrypoint)
	}
	if m.TargetOS != "windows" && entrypoint.Mode&0o100 == 0 {
		return fmt.Errorf("entrypoint %q is not executable", m.Entrypoint)
	}
	if m.WorkDir != "" {
		workDir, exists := entriesByPath[m.WorkDir]
		if !exists {
			return fmt.Errorf("work directory %q is missing from manifest", m.WorkDir)
		}
		if workDir.Type != EntryDirectory {
			return fmt.Errorf("work directory %q is not a directory", m.WorkDir)
		}
	}
	if m.OnlineServerEntrypoint != "" {
		onlineServer, exists := entriesByPath[m.OnlineServerEntrypoint]
		if !exists {
			return fmt.Errorf("online server entrypoint %q is missing from manifest", m.OnlineServerEntrypoint)
		}
		if onlineServer.Type != EntryFile {
			return fmt.Errorf("online server entrypoint %q is not a regular file", m.OnlineServerEntrypoint)
		}
		if m.TargetOS != "windows" && onlineServer.Mode&0o100 == 0 {
			return fmt.Errorf("online server entrypoint %q is not executable", m.OnlineServerEntrypoint)
		}
	}
	return nil
}

func validatePayloadMetadata(m Manifest, required bool) error {
	present := m.Compression != "" || m.PayloadSHA256 != "" || m.PayloadSize != 0
	if !present {
		if required {
			return errors.New("bundle payload metadata is missing")
		}
		return nil
	}
	if m.Compression != CompressionXZ {
		return fmt.Errorf("unsupported payload compression %q", m.Compression)
	}
	if m.PayloadSize <= 0 || m.PayloadSize > defaultLimits.MaxPayloadSize {
		return fmt.Errorf("bundle payload has invalid size %d", m.PayloadSize)
	}
	if !validSHA256(m.PayloadSHA256) {
		return errors.New("bundle payload has invalid SHA-256 digest")
	}
	return nil
}

func validateMetadata(field, value string) error {
	if value == "" {
		return fmt.Errorf("bundle %s must not be empty", field)
	}
	if len(value) > 256 || !utf8.ValidString(value) {
		return fmt.Errorf("bundle %s is invalid", field)
	}
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return fmt.Errorf("bundle %s contains a control character", field)
		}
	}
	return nil
}

func validateTargetArch(value string) error {
	if value == "" || len(value) > 32 {
		return fmt.Errorf("invalid target architecture %q", value)
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return fmt.Errorf("invalid target architecture %q", value)
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateSymlinkGraph(entries map[string]Entry) error {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if entries[name].Type != EntrySymlink {
			continue
		}
		if _, err := resolveManifestLink(name, entries, nil); err != nil {
			return err
		}
	}
	return nil
}

// GeneralsX @bugfix moloch 30/07/2026 Reject symlinked directory topology that can loop during recursive enumeration.
func validateDirectoryTopology(entries map[string]Entry) error {
	adjacent := make(map[string][]string)
	directories := []string{"."}
	adjacent["."] = nil

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := entries[name]
		if entry.Type != EntryDirectory {
			continue
		}
		parent := path.Dir(entry.Path)
		adjacent[parent] = append(adjacent[parent], entry.Path)
		if _, exists := adjacent[entry.Path]; !exists {
			adjacent[entry.Path] = nil
		}
		directories = append(directories, entry.Path)
	}

	for _, name := range names {
		entry := entries[name]
		if entry.Type != EntrySymlink {
			continue
		}
		resolved, err := resolveManifestLink(entry.Path, entries, nil)
		if err != nil {
			return err
		}
		resolvedEntry, exists := entries[resolved]
		if resolved != "." && (!exists || resolvedEntry.Type != EntryDirectory) {
			continue
		}
		parent := path.Dir(entry.Path)
		adjacent[parent] = append(adjacent[parent], resolved)
	}

	const (
		unvisited uint8 = iota
		visiting
		visited
	)
	state := make(map[string]uint8, len(directories))
	var visit func(string) error
	visit = func(directory string) error {
		switch state[directory] {
		case visiting:
			return fmt.Errorf("directory topology cycle includes %q", directory)
		case visited:
			return nil
		}
		state[directory] = visiting
		for _, child := range adjacent[directory] {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[directory] = visited
		return nil
	}

	for _, directory := range directories {
		if err := visit(directory); err != nil {
			return err
		}
	}
	return nil
}

func resolveManifestLink(name string, entries map[string]Entry, resolving map[string]bool) (string, error) {
	entry, exists := entries[name]
	if !exists || entry.Type != EntrySymlink {
		return name, nil
	}
	if resolving == nil {
		resolving = make(map[string]bool)
	}
	if len(resolving) >= maxSymlinkDepth {
		return "", fmt.Errorf("symlink chain at %q exceeds %d links", name, maxSymlinkDepth)
	}
	if resolving[name] {
		return "", fmt.Errorf("symlink cycle includes %q", name)
	}
	resolving[name] = true
	return resolveManifestPath(resolveLinkTarget(name, entry.LinkTarget), entries, resolving)
}

func resolveManifestPath(name string, entries map[string]Entry, resolving map[string]bool) (string, error) {
	if name == "." {
		return ".", nil
	}
	components := strings.Split(name, "/")
	current := ""
	for index, component := range components {
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		entry, exists := entries[current]
		if !exists {
			return "", fmt.Errorf("symlink resolves through missing entry %q", current)
		}
		if entry.Type == EntryFile && index != len(components)-1 {
			return "", fmt.Errorf("symlink resolves through non-directory entry %q", current)
		}
		if entry.Type != EntrySymlink {
			continue
		}
		resolved, err := resolveManifestLink(current, entries, resolving)
		if err != nil {
			return "", err
		}
		remainder := strings.Join(components[index+1:], "/")
		if remainder != "" {
			resolved = path.Join(resolved, remainder)
		}
		return resolveManifestPath(resolved, entries, resolving)
	}
	return current, nil
}
