//go:build gxpacked

// GeneralsX @build moloch 30/07/2026 Embed the generated game payload in the final self-extracting executable.
package payload

import (
	"embed"
	"io/fs"
)

const (
	ManifestPath = "generated/manifest.json"
	ArchivePath  = "generated/payload.tar.xz"
)

//go:embed generated/manifest.json generated/payload.tar.xz
var embedded embed.FS

var Files fs.FS = embedded
