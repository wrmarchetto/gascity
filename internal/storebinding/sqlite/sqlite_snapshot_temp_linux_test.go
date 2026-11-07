//go:build linux

package sqlite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestStaleSQLiteSnapshotSweepToleratesOnlyARootAnotherCallerAlreadyRemoved(t *testing.T) {
	t.Parallel()
	ownerPrefix := "gascity-graph-inspection-" + strconv.Itoa(os.Geteuid()) + "-"
	stalePrefix := ownerPrefix + strconv.Itoa(pidAboveKernelCeiling(t)) + "-"

	t.Run("a root removed after the listing counts as swept", func(t *testing.T) {
		dir := t.TempDir()
		vanished := filepath.Join(dir, stalePrefix+"a")
		remaining := filepath.Join(dir, stalePrefix+"b")
		for _, root := range []string{vanished, remaining} {
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
				t.Fatalf("seeding stale root: %v", err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("listing seeded parent: %v", err)
		}
		if len(entries) != 2 || entries[0].Name() != filepath.Base(vanished) {
			t.Fatalf("listing = %v, want [%s %s]", entries,
				filepath.Base(vanished), filepath.Base(remaining))
		}

		if err := os.RemoveAll(vanished); err != nil {
			t.Fatalf("removing root as the other caller: %v", err)
		}
		if _, err := entries[0].Info(); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("listed entry stats as %v after removal, want not-exist: "+
				"os.ReadDir stated it at listing time on this filesystem, "+
				"so the race cannot be represented here", err)
		}

		if err := sweepStaleSQLiteSnapshotRoots(dir, ownerPrefix, entries); err != nil {
			t.Fatalf("sweep failed on a root another caller removed: %v", err)
		}
		if _, err := os.Lstat(remaining); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stale root listed after the vanished one survived: lstat = %v", err)
		}
	})

	t.Run("a stat failure other than absence stays fatal", func(t *testing.T) {
		dir := t.TempDir()
		name := stalePrefix + "c"
		entry := scriptedDirEntry{name: name, infoErr: &fs.PathError{
			Op: "lstat", Path: filepath.Join(dir, name), Err: syscall.EIO,
		}}

		err := sweepStaleSQLiteSnapshotRoots(dir, ownerPrefix, []os.DirEntry{entry})
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("sweep returned %v for an EIO stat failure, want it surfaced", err)
		}
	})
}

// pidAboveKernelCeiling returns a pid no process can hold.
func pidAboveKernelCeiling(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Fatalf("reading pid_max: %v", err)
	}
	ceiling, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parsing pid_max %q: %v", data, err)
	}
	return ceiling + 1
}

// scriptedDirEntry is a directory entry whose stat fails with infoErr.
type scriptedDirEntry struct {
	name    string
	infoErr error
}

func (e scriptedDirEntry) Name() string               { return e.name }
func (e scriptedDirEntry) IsDir() bool                { return true }
func (e scriptedDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e scriptedDirEntry) Info() (fs.FileInfo, error) { return nil, e.infoErr }
