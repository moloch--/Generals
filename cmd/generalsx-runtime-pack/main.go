// GeneralsX @build Codex 10/10/2026 Create asset-free release runtimes and their authenticated packaging kits.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/moloch--/Generals/internal/runtimebundle"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Runtime packaging failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	var options runtimebundle.Options
	var descriptorPath, archivePath string
	flags := flag.NewFlagSet("generalsx-runtime-pack", flag.ContinueOnError)
	flags.StringVar(&descriptorPath, "verify-descriptor", "", "verify an existing release descriptor and archive without publishing")
	flags.StringVar(&archivePath, "archive", "", "archive to validate with --verify-descriptor")
	flags.StringVar(&options.Target, "target", "", "linux-amd64, macos-arm64, or windows-x86")
	flags.StringVar(&options.RuntimeDir, "runtime-dir", "", "normalized, asset-free runtime directory")
	flags.StringVar(&options.Repository, "repository", "", "GitHub owner/repository publishing the runtime")
	flags.StringVar(&options.Version, "version", "", "immutable runtime-vX.Y.Z release tag")
	flags.StringVar(&options.Commit, "commit", "", "full source Git commit SHA")
	flags.StringVar(&options.OutputDir, "output-dir", "", "directory for release JSON and tar.gz assets")
	flags.StringVar(&options.Repo, "repo", "", "repository containing the tracked packaging kit")
	flags.StringVar(&options.OnlineEndpoint, "online-endpoint", "tls://multiplayer.generals.network", "Online endpoint compiled into the engine")
	flags.StringVar(&options.MinimumOS, "minimum-os", "", "minimum target OS version")
	flags.StringVar(&options.MinimumGLIBC, "minimum-glibc", "", "measured minimum required GLIBC symbol version")
	flags.StringVar(&options.Preset, "preset", "", "CMake preset used to build the runtime")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if descriptorPath != "" {
		for name, value := range map[string]string{"archive": archivePath, "repository": options.Repository, "version": options.Version, "commit": options.Commit, "target": options.Target} {
			if value == "" {
				return fmt.Errorf("--%s is required for verification", name)
			}
		}
		reader, err := os.Open(descriptorPath)
		if err != nil {
			return err
		}
		descriptor, err := runtimebundle.ReadDescriptor(reader)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if descriptor.Repository != options.Repository || descriptor.Version != options.Version || descriptor.SourceCommit != options.Commit || descriptor.Target != options.Target || descriptor.OnlineEndpoint != options.OnlineEndpoint {
			return fmt.Errorf("runtime descriptor does not match expected release identity/configuration")
		}
		parent, err := os.MkdirTemp("", "generalsx-runtime-verify-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(parent)
		if err := descriptor.Extract(ctx, archivePath, filepath.Join(parent, "verified")); err != nil {
			return err
		}
		fmt.Printf("Verified %s (%s)\n", descriptor.Archive.Name, descriptor.SourceCommit)
		return nil
	}
	if archivePath != "" {
		return fmt.Errorf("--archive requires --verify-descriptor")
	}
	for name, value := range map[string]string{"target": options.Target, "runtime-dir": options.RuntimeDir, "repository": options.Repository, "version": options.Version, "commit": options.Commit, "output-dir": options.OutputDir, "repo": options.Repo} {
		if value == "" {
			return fmt.Errorf("--%s is required", name)
		}
	}
	var err error
	options.RuntimeDir, err = filepath.Abs(options.RuntimeDir)
	if err != nil {
		return err
	}
	options.Repo, err = filepath.Abs(options.Repo)
	if err != nil {
		return err
	}
	options.OutputDir, err = filepath.Abs(options.OutputDir)
	if err != nil {
		return err
	}
	descriptor, err := runtimebundle.Generate(ctx, options)
	if err != nil {
		return err
	}
	fmt.Printf("Runtime archive: %s\nDescriptor: %s\nSHA-256: %s\n", filepath.Join(options.OutputDir, descriptor.Archive.Name), filepath.Join(options.OutputDir, runtimebundle.ArtifactStem(descriptor.Version, descriptor.Target)+".json"), descriptor.Archive.SHA256)
	return nil
}
