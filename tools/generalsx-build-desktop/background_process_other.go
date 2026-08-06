//go:build !windows

package main

import "os/exec"

func configureDesktopBackgroundCommand(*exec.Cmd) {}
