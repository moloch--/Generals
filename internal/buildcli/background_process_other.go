//go:build !windows

package buildcli

import "os/exec"

func configureBackgroundCommand(*exec.Cmd, bool) {}
