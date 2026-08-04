package buildcli

import (
	"strings"
	"testing"
)

func TestRenderCommandQuotesArguments(t *testing.T) {
	t.Parallel()
	got := renderCommand(command{
		name: "tool",
		args: []string{"plain", "two words", "it's-safe"},
		dir:  "/tmp/source tree",
	})
	for _, required := range []string{"(cd '/tmp/source tree' &&", "two words", "'it'\\''s-safe'"} {
		if !strings.Contains(got, required) {
			t.Fatalf("renderCommand() = %q, missing %q", got, required)
		}
	}
}

func TestMergeEnvironmentOverridesValue(t *testing.T) {
	t.Parallel()
	merged := mergeEnvironment([]string{"A=old", "B=keep"}, map[string]string{"A": "new"})
	joined := strings.Join(merged, "\n")
	if !strings.Contains(joined, "A=new") || strings.Contains(joined, "A=old") || !strings.Contains(joined, "B=keep") {
		t.Fatalf("mergeEnvironment() = %q", joined)
	}
}

func TestRenderCommandRedactsGitURLCredentials(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{
		"https://build-user:secret-token@example.invalid/repo.git",
		"secret-token@example.invalid:owner/repo.git",
	} {
		rendered := renderCommand(command{name: "git", args: []string{"clone", remote}})
		if strings.Contains(rendered, "secret-token") || !strings.Contains(rendered, "[redacted]@example.invalid") {
			t.Fatalf("renderCommand() leaked or failed to mark credentials: %q", rendered)
		}
	}
}
