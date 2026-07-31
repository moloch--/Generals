//go:build !windows

package cache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// validateCacheRootPlatform prevents a different Unix user from replacing a
// cache root through an unsafe ancestor. This is intentionally a same-user
// cache, not a sandbox against another process running as that user.
func validateCacheRootPlatform(path string, info fs.FileInfo) error {
	effectiveUID := uint32(os.Geteuid())
	rootStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner of cache root %q", path)
	}
	if rootStat.Uid != effectiveUID {
		return fmt.Errorf(
			"cache root %q is owned by uid %d; current uid is %d",
			path,
			rootStat.Uid,
			effectiveUID,
		)
	}

	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		ancestorInfo, err := os.Lstat(ancestor)
		if err != nil {
			return fmt.Errorf("inspect cache-root ancestor %q: %w", ancestor, err)
		}
		if !ancestorInfo.IsDir() || ancestorInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cache-root ancestor %q is not a real directory", ancestor)
		}
		ancestorStat, ok := ancestorInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot determine owner of cache-root ancestor %q", ancestor)
		}
		if ancestorStat.Uid != effectiveUID && ancestorStat.Uid != 0 {
			return fmt.Errorf(
				"cache-root ancestor %q is owned by untrusted uid %d",
				ancestor,
				ancestorStat.Uid,
			)
		}
		if ancestorInfo.Mode().Perm()&0o022 != 0 && ancestorInfo.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf(
				"cache-root ancestor %q is group/world-writable without the sticky bit",
				ancestor,
			)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
	}
	return nil
}
