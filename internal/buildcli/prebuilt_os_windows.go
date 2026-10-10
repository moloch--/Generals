//go:build windows

package buildcli

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func validateWindowsRuntimeOS(minimum string) error {
	if minimum == "" {
		return nil
	}
	version := windows.RtlGetVersion()
	actual := fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber)
	if !versionAtLeast(actual, minimum) {
		return fmt.Errorf("runtime requires Windows %s or newer (host %s)", minimum, actual)
	}
	return nil
}
