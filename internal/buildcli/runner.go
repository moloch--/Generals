package buildcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type command struct {
	name string
	args []string
	dir  string
	env  map[string]string
}

type runner struct {
	dryRun     bool
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	hideWindow bool
}

// GeneralsX @build Codex 04/08/2026 Execute tools directly without shell interpolation or credential logging.
func (r runner) run(ctx context.Context, spec command) error {
	if spec.name == "" {
		return errors.New("external command name is empty")
	}
	fmt.Fprintf(r.stdout, "> %s\n", renderCommand(spec))
	if r.dryRun {
		return nil
	}
	cmd := exec.Command(spec.name, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = mergeEnvironment(os.Environ(), spec.env)
	cmd.Stdin = r.stdin
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	configureBackgroundCommand(cmd, r.hideWindow)
	if err := runManagedCommand(ctx, cmd); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(spec.name), err)
	}
	return nil
}

func (r runner) output(ctx context.Context, spec command) (string, error) {
	if r.dryRun {
		fmt.Fprintf(r.stdout, "> %s\n", renderCommand(spec))
		return "", nil
	}
	cmd := exec.Command(spec.name, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = mergeEnvironment(os.Environ(), spec.env)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureBackgroundCommand(cmd, r.hideWindow)
	if err := runManagedCommand(ctx, cmd); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf("run %s: %w: %s", filepath.Base(spec.name), err, detail)
		}
		return "", fmt.Errorf("run %s: %w", filepath.Base(spec.name), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	caseInsensitive := runtime.GOOS == "windows"
	normalize := func(value string) string {
		if caseInsensitive {
			return strings.ToUpper(value)
		}
		return value
	}
	entries := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		normalized := normalize(key)
		entries[normalized] = entry
	}
	for key, value := range overrides {
		normalized := normalize(key)
		entries[normalized] = key + "=" + value
	}
	ordered := make([]string, 0, len(entries))
	for key := range entries {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	result := make([]string, 0, len(ordered))
	for _, key := range ordered {
		result = append(result, entries[key])
	}
	return result
}

func renderCommand(spec command) string {
	parts := make([]string, 0, 1+len(spec.args))
	parts = append(parts, quoteArgument(redactSensitiveArgument(spec.name)))
	for _, argument := range spec.args {
		parts = append(parts, quoteArgument(redactSensitiveArgument(argument)))
	}
	result := strings.Join(parts, " ")
	if spec.dir != "" {
		result = "(cd " + quoteArgument(spec.dir) + " && " + result + ")"
	}
	return result
}

func redactSensitiveArgument(value string) string {
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		authorityStart := scheme + 3
		authorityEnd := len(value)
		if offset := strings.IndexAny(value[authorityStart:], "/?#"); offset >= 0 {
			authorityEnd = authorityStart + offset
		}
		authority := value[authorityStart:authorityEnd]
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			return value[:authorityStart] + "[redacted]" + authority[at:] + value[authorityEnd:]
		}
	}
	if at := strings.IndexByte(value, '@'); at > 0 && !strings.ContainsAny(value[:at], `/\\`) {
		if strings.ContainsRune(value[at+1:], ':') {
			return "[redacted]" + value[at:]
		}
	}
	return value
}

func quoteArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"'\\$`;&|<>()[]{}*?!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
