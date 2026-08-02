//go:build !darwin && !windows

// GeneralsX @feature Codex 01/08/2026 Keep progress presentation optional on platforms without a native presenter.
package progress

// Open returns a no-op Reporter on platforms without a packaged progress
// helper.
func Open() *Reporter {
	return newNoopReporter()
}
