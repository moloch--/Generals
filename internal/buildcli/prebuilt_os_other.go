//go:build !windows

package buildcli

func validateWindowsRuntimeOS(minimum string) error { return nil }
