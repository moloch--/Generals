//go:build windows

package buildcli

import "golang.org/x/sys/windows"

func ignoreGracefulTermination() {}

func processExists(pid int) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	result, err := windows.WaitForSingleObject(process, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}
