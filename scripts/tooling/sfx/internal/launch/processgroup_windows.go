//go:build windows

package launch

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}
