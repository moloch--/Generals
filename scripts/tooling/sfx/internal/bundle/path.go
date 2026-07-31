// GeneralsX @feature OpenAI 30/07/2026 Centralizes bundle path and link safety checks.
package bundle

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

func validateArchivePath(name, targetOS string) error {
	if name == "" {
		return fmt.Errorf("archive path is empty")
	}
	if len(name) > defaultLimits.MaxPathBytes || !utf8.ValidString(name) {
		return fmt.Errorf("archive path is invalid or exceeds %d bytes", defaultLimits.MaxPathBytes)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("archive path %q contains NUL", name)
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("archive path %q contains a backslash", name)
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("archive path %q is absolute", name)
	}
	if cleaned := path.Clean(name); cleaned != name || cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("archive path %q is not normalized", name)
	}
	if name == ".complete.json" ||
		isCaseFoldingTarget(targetOS) && strings.EqualFold(name, ".complete.json") {
		return fmt.Errorf("archive path %q is reserved for the extraction cache", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("archive path %q has an unsafe component", name)
		}
		for _, r := range component {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("archive path %q contains a control character", name)
			}
		}
		if targetOS == "windows" {
			if err := validateWindowsComponent(component); err != nil {
				return fmt.Errorf("archive path %q: %w", name, err)
			}
		}
	}
	return nil
}

func validateWindowsComponent(component string) error {
	if strings.ContainsAny(component, `<>:"|?*`) {
		return fmt.Errorf("component %q contains a Windows-reserved character", component)
	}
	for _, r := range component {
		if r < 0x20 {
			return fmt.Errorf("component %q contains a control character", component)
		}
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return fmt.Errorf("component %q has a Windows-unsafe suffix", component)
	}
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " ")
	// GeneralsX @bugfix moloch 30/07/2026 Reject Windows console aliases and legacy superscript COM/LPT device names.
	switch strings.ToLower(base) {
	case "con", "prn", "aux", "nul", "conin$", "conout$",
		"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"com¹", "com²", "com³",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9",
		"lpt¹", "lpt²", "lpt³":
		return fmt.Errorf("component %q is a Windows-reserved name", component)
	}
	return nil
}

func validateLinkTarget(entryPath, target string) error {
	if target == "" {
		return fmt.Errorf("symlink %q has an empty target", entryPath)
	}
	if len(target) > defaultLimits.MaxPathBytes || !utf8.ValidString(target) {
		return fmt.Errorf("symlink %q has an invalid or oversized target", entryPath)
	}
	if strings.ContainsRune(target, 0) || strings.Contains(target, `\`) || path.IsAbs(target) {
		return fmt.Errorf("symlink %q has unsafe target %q", entryPath, target)
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("symlink %q target contains a control character", entryPath)
		}
	}
	if path.Clean(target) != target {
		return fmt.Errorf("symlink %q has non-normalized target %q", entryPath, target)
	}
	resolved := resolveLinkTarget(entryPath, target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return fmt.Errorf("symlink %q escapes the bundle root via %q", entryPath, target)
	}
	return nil
}

func resolveLinkTarget(entryPath, target string) string {
	return path.Clean(path.Join(path.Dir(entryPath), target))
}

func isCaseFoldingTarget(targetOS string) bool {
	return targetOS == "darwin" || targetOS == "windows"
}
