//go:build !windows

package main

import (
	"fmt"
	"os"
)

func currentDockerUser() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}
