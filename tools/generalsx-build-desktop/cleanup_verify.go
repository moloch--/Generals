package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultLinuxSFXBuilderImage = "generalsx/linux-builder:latest"

var dockerContainerIDPattern = regexp.MustCompile(`\A[0-9a-f]{12,64}\z`)

// GeneralsX @feature Codex 05/08/2026 Verify native SFX files directly and macOS-hosted Linux SFX files in the existing builder container.
func verifySFXArtifact(ctx context.Context, path, target, hostOS string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == targetForHost(hostOS) {
		return runSFXVerifyCommand(ctx, path, "--sfx-verify")
	}
	if hostOS == "darwin" && target == "linux" {
		return verifyLinuxSFXArtifactWithDocker(ctx, path)
	}
	return fmt.Errorf("cannot verify a %s SFX on a %s host", target, hostOS)
}

func verifyLinuxSFXArtifactWithDocker(ctx context.Context, path string) error {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return errors.New("Docker is required to verify the Linux SFX built on macOS")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve Linux SFX path: %w", err)
	}
	if strings.ContainsRune(absolute, ':') {
		return fmt.Errorf("Linux SFX path %q cannot be safely mounted by Docker", absolute)
	}
	workspace, err := os.MkdirTemp("", "generalsx-sfx-docker-verify-")
	if err != nil {
		return fmt.Errorf("create Docker verification workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	temporary := filepath.Join(workspace, "tmp")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		return fmt.Errorf("create Docker verification temporary directory: %w", err)
	}
	cidFile := filepath.Join(workspace, "container.cid")
	image := strings.TrimSpace(os.Getenv("GX_SFX_LINUX_BUILDER_IMAGE"))
	if image == "" {
		image = defaultLinuxSFXBuilderImage
	}
	arguments := linuxSFXDockerVerifyArguments(absolute, temporary, cidFile, image, currentDockerUser())
	defer stopDockerVerificationContainer(docker, cidFile)
	if err := runSFXVerifyCommand(ctx, docker, arguments...); err != nil {
		return fmt.Errorf("verify Linux SFX in %s: %w", image, err)
	}
	return nil
}

func linuxSFXDockerVerifyArguments(path, temporary, cidFile, image, user string) []string {
	arguments := []string{
		"run", "--rm", "--pull=never", "--platform", "linux/amd64",
		"--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", "64",
		"--env", "HOME=/tmp/home", "--env", "TMPDIR=/tmp",
		"--volume", path + ":/sfx:ro", "--volume", temporary + ":/tmp:rw",
		"--cidfile", cidFile, "--entrypoint", "/sfx",
	}
	if user != "" {
		arguments = append(arguments, "--user", user)
	}
	return append(arguments, image, "--sfx-verify")
}

func stopDockerVerificationContainer(docker, cidFile string) {
	contents, err := os.ReadFile(cidFile)
	if err != nil {
		return
	}
	containerID := strings.TrimSpace(string(contents))
	if !dockerContainerIDPattern.MatchString(containerID) {
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanupContext, docker, "rm", "--force", containerID).Run()
}

func runSFXVerifyCommand(ctx context.Context, executable string, arguments ...string) error {
	var output boundedOutput
	output.remaining = cleanupVerifierOutputLimit
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		message := strings.TrimSpace(output.buffer.String())
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (output *boundedOutput) Write(contents []byte) (int, error) {
	written := len(contents)
	if output.remaining > 0 {
		keep := min(output.remaining, len(contents))
		_, _ = output.buffer.Write(contents[:keep])
		output.remaining -= keep
	}
	return written, nil
}
