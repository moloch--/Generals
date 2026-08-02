// GeneralsX @test Codex 02/08/2026 Keep every launcher dependency represented in the embedded notice.
package notices

import (
	"strings"
	"testing"
)

func TestEmbeddedThirdPartyNotices(t *testing.T) {
	for _, dependency := range []string{"github.com/ulikunitz/xz", "golang.org/x/sys"} {
		if !strings.Contains(Text, dependency) {
			t.Errorf("embedded third-party notice omits %s", dependency)
		}
	}
}
