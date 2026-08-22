//go:build !windows

package doctor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func hostRootFreeBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return int64(stat.Bavail * uint64(stat.Bsize)), nil
}
