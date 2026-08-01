//go:build !darwin

// GeneralsX @feature Codex 01/08/2026 Keep progress presentation optional on non-macOS launchers.
package progress

// Open returns a no-op Reporter on platforms without a packaged progress
// helper.
func Open() *Reporter {
	return newNoopReporter()
}
