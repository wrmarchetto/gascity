package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/citylayout"
)

type managedDoltRuntimeLayout struct {
	PackStateDir string
	DataDir      string
	LogFile      string
	StateFile    string
	PIDFile      string
	LockFile     string
	ConfigFile   string
}

func resolveManagedDoltRuntimeLayout(cityPath string) (managedDoltRuntimeLayout, error) {
	cityPath = filepath.Clean(strings.TrimSpace(cityPath))
	if cityPath == "" || cityPath == "." {
		return managedDoltRuntimeLayout{}, fmt.Errorf("missing --city")
	}
	cityPath = normalizePathForCompare(cityPath)

	packStateDir := strings.TrimSpace(os.Getenv("GC_PACK_STATE_DIR"))
	if packStateDir == "" {
		if runtimeDir := strings.TrimSpace(os.Getenv("GC_CITY_RUNTIME_DIR")); runtimeDir != "" {
			packStateDir = filepath.Join(runtimeDir, "packs", "dolt")
		} else {
			packStateDir = citylayout.PackStateDir(cityPath, "dolt")
		}
	}
	dataDir := defaultEnvPath("GC_DOLT_DATA_DIR", filepath.Join(beadsDirForScope(cityPath), "dolt"))
	logFile := defaultEnvPath("GC_DOLT_LOG_FILE", filepath.Join(packStateDir, "dolt.log"))
	stateFile := defaultEnvPath("GC_DOLT_STATE_FILE", filepath.Join(packStateDir, "dolt-provider-state.json"))
	pidFile := defaultEnvPath("GC_DOLT_PID_FILE", filepath.Join(packStateDir, "dolt.pid"))
	lockFile := defaultEnvPath("GC_DOLT_LOCK_FILE", filepath.Join(packStateDir, "dolt.lock"))
	configFile := defaultEnvPath("GC_DOLT_CONFIG_FILE", filepath.Join(packStateDir, "dolt-config.yaml"))

	return managedDoltRuntimeLayout{
		PackStateDir: packStateDir,
		DataDir:      dataDir,
		LogFile:      logFile,
		StateFile:    stateFile,
		PIDFile:      pidFile,
		LockFile:     lockFile,
		ConfigFile:   configFile,
	}, nil
}

// beadsDirForScope returns the .beads directory a scope's managed store lives
// in, following .beads/redirect when the scope defers to another store.
//
// worktree-setup.sh writes that file on every worktree it provisions, naming
// the rig's real .beads, and it is the recorded intent: this tree shares the
// parent's store rather than owning one. No Go path read it, so a scope that
// resolved to a worktree got its own empty database -- measured on
// city/toolsmith-codex-1 as a 32K .beads/dolt holding only .dolt/ and
// .doltcfg/ beside a redirect naming the populated store.
//
// Honored here rather than at scope resolution because the two fixes answer
// different questions. isWorktreeOfEnclosingCity stops a worktree BECOMING a
// scope; this stops any scope that has deferred from acquiring a second store,
// including the worktrees and PR clones that are legitimately their own scope
// under dolt_scope_watchdog.go's one-server-per-scope contract.
//
// A blank, unreadable or missing redirect leaves the scope's own .beads in
// place. Blank is not hypothetical -- a truncated write leaves a zero-length
// file, and treating that as a target would point the store at the filesystem
// root. A relative target resolves against the scope; bd writes absolute paths
// today, so that branch is defense rather than observed behavior.
func beadsDirForScope(cityPath string) string {
	own := filepath.Join(cityPath, ".beads")
	raw, err := os.ReadFile(filepath.Join(own, "redirect"))
	if err != nil {
		return own
	}
	target := strings.TrimSpace(string(raw))
	if target == "" {
		return own
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(cityPath, target)
	}
	return filepath.Clean(target)
}

func defaultEnvPath(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return normalizePathForCompare(value)
	}
	return normalizePathForCompare(fallback)
}

func doltRuntimeLayoutFields(layout managedDoltRuntimeLayout) []string {
	return []string{
		"GC_PACK_STATE_DIR\t" + layout.PackStateDir,
		"GC_DOLT_DATA_DIR\t" + layout.DataDir,
		"GC_DOLT_LOG_FILE\t" + layout.LogFile,
		"GC_DOLT_STATE_FILE\t" + layout.StateFile,
		"GC_DOLT_PID_FILE\t" + layout.PIDFile,
		"GC_DOLT_LOCK_FILE\t" + layout.LockFile,
		"GC_DOLT_CONFIG_FILE\t" + layout.ConfigFile,
	}
}
