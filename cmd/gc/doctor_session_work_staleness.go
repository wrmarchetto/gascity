package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionWorkStalenessDoctorCheck reports active claim-holding sessions whose
// worker directory has stopped changing beyond the configured claim-holder
// stall horizon. Runtime last_activity is deliberately not used: pane polling
// proves liveness, not work progress.
type sessionWorkStalenessDoctorCheck struct {
	cfg           *config.City
	cityPath      string
	newStore      func(string) (beads.Store, error)
	now           func() time.Time
	lastWorkWrite func(string) (time.Time, error)
}

func (c *sessionWorkStalenessDoctorCheck) Name() string { return "session-work-staleness" }

func (c *sessionWorkStalenessDoctorCheck) CanFix() bool { return false }

func (c *sessionWorkStalenessDoctorCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *sessionWorkStalenessDoctorCheck) WarmupEligible() bool { return false }

func (c *sessionWorkStalenessDoctorCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	result := &doctor.CheckResult{
		Name:     "session-work-staleness",
		Status:   doctor.StatusOK,
		Severity: doctor.SeverityAdvisory,
		Message:  "no stale active claim-holder work detected",
	}
	if c == nil || c.cfg == nil || c.newStore == nil || c.cfg.Session.ClaimHolderStallTimeoutDuration() <= 0 {
		return result
	}

	store, err := c.newStore(c.cityPath)
	if err != nil {
		result.Message = fmt.Sprintf("session work staleness diagnostics skipped: %v", err)
		return result
	}
	all, err := loadSessionModelDoctorBeads(store)
	if err != nil {
		result.Message = fmt.Sprintf("session work staleness diagnostics skipped: %v", err)
		return result
	}

	holders := make(map[string]bool)
	for _, b := range all {
		if b.Status == "in_progress" && !session.IsSessionBeadOrRepairable(b) {
			holders[b.Assignee] = true
		}
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	lastWorkWrite := workerDirLastWrite
	if c.lastWorkWrite != nil {
		lastWorkWrite = c.lastWorkWrite
	}
	threshold := c.cfg.Session.ClaimHolderStallTimeoutDuration()
	for _, b := range all {
		if b.Status == "closed" || !session.IsSessionBeadOrRepairable(b) || b.Metadata["state"] != string(session.StateActive) || !holders[b.ID] {
			continue
		}
		workDir := contract.WorkerDirFromMetadata(b.Metadata)
		if workDir == "" {
			continue
		}
		lastWrite, err := lastWorkWrite(workDir)
		if err != nil || lastWrite.IsZero() || !now.After(lastWrite.Add(threshold)) {
			continue
		}
		result.Details = append(result.Details, fmt.Sprintf(
			"stale-work-output: session %s holds in-progress work but no file under %s has changed since %s (%s ago; threshold %s)",
			b.ID, workDir, lastWrite.UTC().Format(time.RFC3339), now.Sub(lastWrite).Round(time.Second), threshold,
		))
	}
	if len(result.Details) > 0 {
		result.Status = doctor.StatusWarning
		result.Message = fmt.Sprintf("%d active claim-holder session(s) have stale work output", len(result.Details))
	}
	return result
}

// workerDirLastWrite returns the newest regular-file modification under dir.
// Git metadata is excluded because fetches and checkout bookkeeping are not
// agent work output. A directory with no readable regular file has no usable
// progress signal and returns the zero time without an error.
func workerDirLastWrite(dir string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("walking worker directory %q: %w", dir, err)
	}
	return newest, nil
}
