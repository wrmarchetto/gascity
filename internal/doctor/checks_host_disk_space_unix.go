//go:build !windows

package doctor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// HostRootFreeBytes reports the bytes available to unprivileged users on the
// filesystem holding path.
func HostRootFreeBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return int64(stat.Bavail * uint64(stat.Bsize)), nil
}

// hostPathDevice reports the device holding path, so two paths on one
// filesystem are measured once.
func hostPathDevice(path string) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return 0, fmt.Errorf("stat %q: %w", path, err)
	}
	return stat.Dev, nil
}
