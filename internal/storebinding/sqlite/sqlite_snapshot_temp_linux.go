//go:build linux

package sqlite

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func makeRecoverableSQLiteSnapshotTempDir(dir, pattern string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	ownerPrefix := pattern + strconv.Itoa(os.Geteuid()) + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading SQLite snapshot temp parent: %w", err)
	}
	if err := sweepStaleSQLiteSnapshotRoots(dir, ownerPrefix, entries); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, ownerPrefix+strconv.Itoa(os.Getpid())+"-")
}

// sweepStaleSQLiteSnapshotRoots removes the roots among entries, listed from
// dir, that a dead process of this user left behind.
func sweepStaleSQLiteSnapshotRoots(dir, ownerPrefix string, entries []os.DirEntry) error {
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), ownerPrefix) {
			continue
		}
		remainder := strings.TrimPrefix(entry.Name(), ownerPrefix)
		separator := strings.IndexByte(remainder, '-')
		if separator <= 0 {
			continue
		}
		pid, err := strconv.Atoi(remainder[:separator])
		if err != nil || pid <= 0 || sqliteSnapshotProcessAlive(pid) {
			continue
		}
		info, err := entry.Info()
		// Callers sharing dir race to sweep the same root, so a root gone
		// since the listing was swept by another caller.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stating stale SQLite snapshot root: %w", err)
		}
		if !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("removing stale SQLite snapshot root: %w", err)
		}
	}
	return nil
}

func sqliteSnapshotProcessAlive(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
