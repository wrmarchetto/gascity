//go:build windows

package doctor

import "fmt"

func hostRootFreeBytes(path string) (int64, error) {
	return 0, fmt.Errorf("statfs unavailable on this platform for %q", path)
}
