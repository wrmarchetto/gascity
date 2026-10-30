//go:build windows

package doctor

import "fmt"

// HostRootFreeBytes reports the bytes available to unprivileged users on the
// filesystem holding path.
func HostRootFreeBytes(path string) (int64, error) {
	return 0, fmt.Errorf("statfs unavailable on this platform for %q", path)
}

// hostPathDevice reports the device holding path. Unavailable here, so only
// the root filesystem is measured.
func hostPathDevice(path string) (uint64, error) {
	return 0, fmt.Errorf("stat unavailable on this platform for %q", path)
}
