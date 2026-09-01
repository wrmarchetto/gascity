package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// defaultDoltBackupArtifactFreshnessMaxAge allows one missed run of the
// configured six-hour Dolt backup order before doctor reports an advisory.
// This mirrors the 12-hour RPO window used by mol-dog-doctor.
const defaultDoltBackupArtifactFreshnessMaxAge = 12 * time.Hour

// DoltBackupCheck verifies that a managed Dolt scope has a configured backup
// remote and, when artifacts exist, that its recovery point is current. `gc
// rig add` provisions a rig but does not register a backup. mol-dog-backup
// auto-configures a local <db>-backup remote on its next run (#3176), so this
// warning self-heals within one backup interval; it catches the
// not-yet-covered window up-front in `gc doctor` and stays loud when the
// backup dog itself is failing.
//
// Two signals satisfy the check; either is sufficient:
//
//   - Filesystem: the local destination in .beads/dolt-backup.json, or the
//     historical <city>/.dolt-backup/<db>/ fallback when no destination is
//     registered, contains a backup artifact newer than the recovery-point
//     window. This matches the destination bd backup sync uses.
//   - Repo state: <managed-dolt-data-dir>/<db>/.dolt/repo_state.json
//     contains a backup entry named <db>-backup. This is the
//     post-registration, pre-sync state.
//
// When neither signal is present, or an existing artifact is stale, the check
// emits StatusWarning. We deliberately do NOT auto-fix: backup destination is
// operator policy (local fs vs S3 vs B2 etc.) and a one-way door.
//
// The check is intended to be registered per non-suspended rig; the
// caller in cmd_doctor.go skips suspended rigs before constructing this
// check.
type DoltBackupCheck struct {
	cityPath         string
	rig              config.Rig
	doltDataDir      string
	checkName        string
	scopeDescription string
	now              func() time.Time
}

// NewDoltBackupCheck creates a per-rig dolt-backup registration check.
func NewDoltBackupCheck(cityPath string, rig config.Rig, doltDataDir string) *DoltBackupCheck {
	return newDoltBackupCheck(cityPath, rig, doltDataDir, "", "", time.Now)
}

// NewCityDoltBackupCheck creates a Dolt backup freshness check for the city
// store, which is not represented by a rig configuration entry.
func NewCityDoltBackupCheck(cityPath, doltDataDir string) *DoltBackupCheck {
	return newDoltBackupCheck(
		cityPath,
		config.Rig{Name: "city", Path: cityPath},
		doltDataDir,
		"city:dolt-backup",
		"city",
		time.Now,
	)
}

func newDoltBackupCheck(cityPath string, rig config.Rig, doltDataDir, checkName, scopeDescription string, now func() time.Time) *DoltBackupCheck {
	if strings.TrimSpace(doltDataDir) == "" {
		doltDataDir = filepath.Join(cityPath, ".beads", "dolt")
	}
	if now == nil {
		now = time.Now
	}
	return &DoltBackupCheck{
		cityPath:         cityPath,
		rig:              rig,
		doltDataDir:      doltDataDir,
		checkName:        checkName,
		scopeDescription: scopeDescription,
		now:              now,
	}
}

// Name returns the check identifier ("rig:<name>:dolt-backup").
func (c *DoltBackupCheck) Name() string {
	if c.checkName != "" {
		return c.checkName
	}
	return "rig:" + c.rig.Name + ":dolt-backup"
}

// Run executes the check.
func (c *DoltBackupCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}

	rigPath := c.normalizedRigPath()

	// An external (non-managed) Dolt endpoint owns its own backups; gc does not
	// manage them, so the local .dolt-backup directory and managed-Dolt
	// repo_state.json signals never apply, and the localhost fix hint below is
	// actively wrong for it. Treat a resolved external endpoint as satisfied
	// rather than warning. Note that External classifies the endpoint's
	// ownership, not its location — an explicit endpoint can resolve to a local
	// host — so the message must not imply a remote machine. See
	// gastownhall/gascity#3868. A resolution error falls through to the
	// local-signal checks so a genuinely missing local backup still surfaces.
	if target, err := contract.ResolveDoltConnectionTarget(fsys.OSFS{}, c.cityPath, rigPath); err == nil && target.External {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("%s: external Dolt endpoint %s:%s — backups assumed self-managed at the endpoint", c.scopeLabel(), target.Host, target.Port)
		return r
	}

	dbName, details := c.resolveDBName(rigPath)
	r.Details = append(r.Details, details...)
	backupDir := filepath.Join(c.cityPath, ".dolt-backup", dbName)
	configuredDestination := false
	if destination, ok := configuredBdBackupDestination(rigPath); ok {
		backupDir = destination
		configuredDestination = true
	}

	// Signal 1: backup directory contains a recent backup artifact. Directory
	// existence alone is not evidence of a working backup: it remains after a
	// sync stops and otherwise makes a stale recovery point look healthy.
	latestArtifact, backupDirHasContent, err := newestBackupArtifactModTime(backupDir)
	switch {
	case err != nil:
		r.Details = append(r.Details, fmt.Sprintf("read backup dir: %v", err))
	case backupDirHasContent:
		age := c.now().Sub(latestArtifact)
		if age > defaultDoltBackupArtifactFreshnessMaxAge {
			r.Status = StatusWarning
			r.Message = fmt.Sprintf("%s: backup artifact is stale (%s old; maximum %s): %s", c.scopeLabel(), age.Round(time.Minute), defaultDoltBackupArtifactFreshnessMaxAge, backupDir)
			r.FixHint = fmt.Sprintf("run the scheduled Dolt backup sync and confirm it writes a current artifact under %s", backupDir)
			return r
		}
		r.Status = StatusOK
		r.Message = fmt.Sprintf("backup artifact is current (%s old): %s", age.Round(time.Minute), backupDir)
		return r
	}

	// Signal 2: backup remote is registered in repo_state.json.
	registered, err := backupRemoteRegistered(c.doltDataDir, dbName)
	switch {
	case err != nil:
		// Treat read errors as "not registered" but record the cause in
		// Details for verbose runs. We still want the warning + fix
		// command to reach the operator.
		r.Details = append(r.Details, fmt.Sprintf("read repo_state.json: %v", err))
	case registered:
		r.Status = StatusOK
		r.Message = fmt.Sprintf("backup remote %q registered (sync pending)", dbName+"-backup")
		return r
	}

	r.Status = StatusWarning
	if configuredDestination {
		r.Message = fmt.Sprintf("%s: configured bd backup has no artifact yet: %s", c.scopeLabel(), backupDir)
		r.FixHint = fmt.Sprintf("run `bd backup sync` from %s and confirm it writes a current artifact under %s", rigPath, backupDir)
		return r
	}
	r.Message = fmt.Sprintf("%s: no dolt backup registered (expected %s)", c.scopeLabel(), backupDir)
	r.FixHint = doltBackupFixHint(dbName, backupDir)
	return r
}

func (c *DoltBackupCheck) scopeLabel() string {
	if c.scopeDescription != "" {
		return c.scopeDescription
	}
	return fmt.Sprintf("rig %q", c.rig.Name)
}

// CanFix returns false. Registering a backup destination is operator
// policy (local fs vs cloud bucket vs offsite); auto-creating a local
// backup would silently bypass that decision.
func (c *DoltBackupCheck) CanFix() bool { return false }

// Fix is a no-op. See CanFix.
func (c *DoltBackupCheck) Fix(_ *CheckContext) error { return nil }

func (c *DoltBackupCheck) normalizedRigPath() string {
	return normalizedRigPath(c.cityPath, c.rig)
}

// resolveDBName returns the rig's Dolt database name from
// .beads/metadata.json, falling back to rig.Name when the metadata is
// missing or unreadable. Falling back preserves a useful warning even
// for rigs whose metadata never landed — the operator can correct the
// db name in the suggested command if needed.
func (c *DoltBackupCheck) resolveDBName(rigPath string) (string, []string) {
	return resolveDoltDBName(c.rig, rigPath)
}

func normalizedRigPath(cityPath string, rig config.Rig) string {
	rigPath := rig.Path
	if !filepath.IsAbs(rigPath) {
		rigPath = filepath.Join(cityPath, rigPath)
	}
	return rigPath
}

func resolveDoltDBName(rig config.Rig, rigPath string) (string, []string) {
	metadataPath := filepath.Join(rigPath, ".beads", "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rig.Name, nil
		}
		return rig.Name, []string{fmt.Sprintf("read metadata.json: %v; using rig name %q", err, rig.Name)}
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return rig.Name, []string{fmt.Sprintf("parse metadata.json: %v; using rig name %q", err, rig.Name)}
	}
	if s := strings.TrimSpace(meta.DoltDatabase); s != "" {
		return s, nil
	}
	return rig.Name, nil
}

// configuredBdBackupDestination returns the local path registered for bd
// backup sync in <scope>/.beads/dolt-backup.json. Only file URLs can be
// inspected for freshness; remote destinations retain the historical fallback
// and repo-state signals.
func configuredBdBackupDestination(scopePath string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(scopePath, ".beads", "dolt-backup.json"))
	if err != nil {
		return "", false
	}
	var registration struct {
		BackupURL string `json:"backup_url"`
	}
	if err := json.Unmarshal(data, &registration); err != nil {
		return "", false
	}
	u, err := url.Parse(registration.BackupURL)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	return filepath.FromSlash(u.Path), true
}

// backupRemoteRegistered reports whether
// <managed-dolt-data-dir>/<db>/.dolt/repo_state.json declares a backup remote
// named "<db>-backup". A missing file returns (false, nil) — that is the
// expected state for a freshly-provisioned rig and not itself an error.
func backupRemoteRegistered(doltDataDir, dbName string) (bool, error) {
	statePath := filepath.Join(doltDataDir, dbName, ".dolt", "repo_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var state struct {
		Backups map[string]json.RawMessage `json:"backups"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("parse %s: %w", statePath, err)
	}
	_, ok := state.Backups[dbName+"-backup"]
	return ok, nil
}

func newestBackupArtifactModTime(path string) (time.Time, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	if !info.IsDir() {
		return time.Time{}, false, nil
	}
	var newest time.Time
	hasArtifact := false
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		hasArtifact = true
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return newest, hasArtifact, nil
}

// doltBackupFixHint returns the multi-line DOLT_BACKUP add+sync
// invocation as a copy-pasteable shell command. The command targets the
// running managed Dolt server (port comes from $GC_DOLT_PORT, which
// `gc dolt status` surfaces); it does not assume the operator has
// stopped the server.
func doltBackupFixHint(dbName, backupDir string) string {
	return fmt.Sprintf(
		"register the backup remote (requires GC_DOLT_PORT from `gc dolt status`):\n"+
			"  DOLT_CLI_PASSWORD='' dolt --host 127.0.0.1 --port ${GC_DOLT_PORT:?set this via gc dolt status} --user root --no-tls sql -q \\\n"+
			"    \"USE \\`%s\\`; \\\n"+
			"     CALL DOLT_BACKUP('add', '%s-backup', 'file://%s'); \\\n"+
			"     CALL DOLT_BACKUP('sync', '%s-backup');\"",
		dbName, dbName, backupDir, dbName,
	)
}
