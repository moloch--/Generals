//go:build !gxpacked

// GeneralsX @build moloch 30/07/2026 Provide a buildable launcher when no generated payload is present.
package payload

import (
	"io/fs"
)

const (
	ManifestPath = "generated/manifest.json"
	ArchivePath  = "generated/payload.tar.xz"
)

// Files is intentionally empty in development builds. The pack command copies
// this module to a temporary workspace, generates the payload, and enables the
// gxpacked build tag.
var Files fs.FS = missingFS{}

type missingFS struct{}

func (missingFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}
