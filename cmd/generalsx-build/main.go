// GeneralsX @build Codex 04/08/2026 Provide one Go entrypoint for host setup, retail acquisition, and SFX builds.
package main

import (
	"context"
	"os"

	"github.com/moloch--/Generals/internal/buildcli"
)

func main() {
	os.Exit(buildcli.Main(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
