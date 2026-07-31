//go:build windows

package cache

import "io/fs"

func validateCacheRootPlatform(_ string, _ fs.FileInfo) error {
	return nil
}
