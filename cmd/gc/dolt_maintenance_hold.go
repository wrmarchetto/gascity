package main

// Maintenance hold for the managed Dolt server (ci-lg7q7t).
//
// An offline `dolt gc --full` needs the managed sql-server DOWN for its whole
// run, and before this hold nothing could keep it down: the controller patrol
// tick, every agent `gc bd` env build, and the bd transport retry on
// "connection refused" each restart it within seconds (root-caused on
// ci-wgre2l). The hold is one file that every one of those doors reads before
// it starts or recovers a server, plus the two spawners underneath them
// (`gc dolt-state start-managed` / `recover-managed`), so a provider script
// invoked by hand is refused too.
//
// REJECTED: a bead, or a hold label on one. The bead store IS the server being
// held down, so every reader would find the hold unreadable exactly while it is
// in force. REJECTED: an environment switch like GC_DOLT=skip. It is
// per-process, so the already-running supervisor never sees it -- the gap
// ci-wgre2l measured. A file under the city directory is the one medium every
// gc process of this city reads without the server.
//
// This is NOT a status file in the AGENTS.md sense. It records operator intent
// and carries its own expiry, it does not claim a process is alive. A holder
// that crashes leaves a file that lapses at expires_at on its own, so the city
// is never left without beads past the window the holder asked for.
//
// Invariants, pinned by dolt_maintenance_hold_test.go and, against a real
// sql-server, dolt_maintenance_hold_integration_test.go:
//   - An expired hold is inert. Readers ignore it; nothing needs to delete it.
//   - An unreadable or unparseable hold file REFUSES, never proceeds. Restarting
//     a server under a running `dolt gc` is the data-loss case the hold exists
//     to prevent, and atomic writes make a torn file a non-event in practice.
//   - Every refusal names the holder, the expiry, and the release command.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/fsys"
)

// managedDoltMaintenanceHoldFile is relative to the city root, NOT to
// GC_CITY_RUNTIME_DIR or GC_PACK_STATE_DIR. Both are per-process environment
// that tests and runbooks repoint, and a hold that two processes resolve to
// two different paths is no hold.
const managedDoltMaintenanceHoldFile = ".gc/dolt-maintenance-hold.json"

// maxManagedDoltMaintenanceHold bounds a single hold. The offline GC this was
// built for was estimated at ~15 min on a 20.7 GB hq (ci-wgre2l), so 4h is
// generous headroom while still guaranteeing a forgotten hold cannot strand
// the city for a day. Extending means releasing and taking a new hold, which
// is deliberate: it re-reads the operator's intent rather than a stale one.
const maxManagedDoltMaintenanceHold = 4 * time.Hour

type managedDoltMaintenanceHold struct {
	Holder    string    `json:"holder"`     // session name, agent, or $USER
	Reason    string    `json:"reason"`     // free text, shown in every refusal
	SetAt     time.Time `json:"set_at"`     // UTC
	ExpiresAt time.Time `json:"expires_at"` // UTC, hard lapse
}

func managedDoltMaintenanceHoldPath(cityPath string) string {
	return filepath.Join(cityPath, managedDoltMaintenanceHoldFile)
}

// managedDoltMaintenanceHoldNow is the clock every hold reader uses, so a test
// can lapse a hold without sleeping through its window.
var managedDoltMaintenanceHoldNow = time.Now

// errManagedDoltMaintenanceHold is the refusal every recovery door returns.
type errManagedDoltMaintenanceHold struct {
	path string
	hold managedDoltMaintenanceHold
	// readErr is set when the file exists but cannot be trusted. The door
	// still refuses, and the message says why.
	readErr error
}

func (e *errManagedDoltMaintenanceHold) Error() string {
	if e.readErr != nil {
		return fmt.Sprintf("managed dolt maintenance hold file %s is unreadable (%v); "+
			"refusing to start or recover the server. If no maintenance is running, "+
			"remove the hold with: gc maintenance release", e.path, e.readErr)
	}
	return fmt.Sprintf("managed dolt is under a maintenance hold by %s until %s (%s); "+
		"refusing to start or recover the server. Release early with: gc maintenance release",
		e.hold.Holder, e.hold.ExpiresAt.UTC().Format(time.RFC3339), e.hold.Reason)
}

func isManagedDoltMaintenanceHold(err error) bool {
	var held *errManagedDoltMaintenanceHold
	return errors.As(err, &held)
}

// readManagedDoltMaintenanceHold returns the hold as recorded, expired or not.
// Returns (hold, present, err): present is false only when no file exists.
func readManagedDoltMaintenanceHold(cityPath string) (managedDoltMaintenanceHold, bool, error) {
	data, err := os.ReadFile(managedDoltMaintenanceHoldPath(cityPath))
	if errors.Is(err, os.ErrNotExist) {
		return managedDoltMaintenanceHold{}, false, nil
	}
	if err != nil {
		return managedDoltMaintenanceHold{}, true, err
	}
	var hold managedDoltMaintenanceHold
	if err := json.Unmarshal(data, &hold); err != nil {
		return managedDoltMaintenanceHold{}, true, fmt.Errorf("parse: %w", err)
	}
	// A zero expiry would read as "lapsed in year 1" and silently release.
	// Treat it as corrupt so the door refuses instead.
	if hold.ExpiresAt.IsZero() {
		return managedDoltMaintenanceHold{}, true, fmt.Errorf("missing expires_at")
	}
	return hold, true, nil
}

// managedDoltMaintenanceHoldRefusal returns nil when no unexpired hold is in
// force, and an *errManagedDoltMaintenanceHold otherwise. This is the single
// predicate every start and recover door calls.
func managedDoltMaintenanceHoldRefusal(cityPath string) error {
	if strings.TrimSpace(cityPath) == "" {
		return nil
	}
	hold, present, err := readManagedDoltMaintenanceHold(cityPath)
	if !present {
		return nil
	}
	path := managedDoltMaintenanceHoldPath(cityPath)
	if err != nil {
		return &errManagedDoltMaintenanceHold{path: path, readErr: err}
	}
	if !managedDoltMaintenanceHoldNow().Before(hold.ExpiresAt) {
		return nil
	}
	return &errManagedDoltMaintenanceHold{path: path, hold: hold}
}

func writeManagedDoltMaintenanceHold(cityPath string, hold managedDoltMaintenanceHold) error {
	data, err := json.MarshalIndent(hold, "", "  ")
	if err != nil {
		return err
	}
	path := managedDoltMaintenanceHoldPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fsys.OSFS{}, path, append(data, '\n'), 0o644)
}

// removeManagedDoltMaintenanceHold returns (removed, err).
func removeManagedDoltMaintenanceHold(cityPath string) (bool, error) {
	err := os.Remove(managedDoltMaintenanceHoldPath(cityPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func managedDoltMaintenanceHolderIdentity() string {
	for _, key := range []string{"GC_SESSION_NAME", "GC_AGENT", "USER"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return "unknown"
}

// managedDoltMaintenanceHoldStatusLine returns the `gc status` banner for an
// active or unreadable hold, and "" when no hold is in force.
func managedDoltMaintenanceHoldStatusLine(cityPath string) string {
	err := managedDoltMaintenanceHoldRefusal(cityPath)
	var held *errManagedDoltMaintenanceHold
	if !errors.As(err, &held) {
		return ""
	}
	if held.readErr != nil {
		return "Maintenance hold: " + held.Error()
	}
	return fmt.Sprintf("Maintenance hold: managed Dolt is held offline by %s until %s (%s). Release: gc maintenance release",
		held.hold.Holder, held.hold.ExpiresAt.UTC().Format(time.RFC3339), held.hold.Reason)
}
