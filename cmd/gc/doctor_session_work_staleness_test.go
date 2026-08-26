package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/session"
)

func TestSessionWorkStalenessDoctorCheck(t *testing.T) {
	now := time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC)
	store := beads.NewMemStore()

	stale, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":                       string(session.StateActive),
			beadmeta.WorkerDirMetadataKey: "/work/stale",
		},
	})
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}
	staleWork, err := store.Create(beads.Bead{Type: "task", Assignee: stale.ID})
	if err != nil {
		t.Fatalf("create stale assigned work: %v", err)
	}
	inProgress := "in_progress"
	if err := store.Update(staleWork.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark stale assigned work in progress: %v", err)
	}

	fresh, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":                       string(session.StateActive),
			beadmeta.WorkerDirMetadataKey: "/work/fresh",
		},
	})
	if err != nil {
		t.Fatalf("create fresh session: %v", err)
	}
	freshWork, err := store.Create(beads.Bead{Type: "task", Assignee: fresh.ID})
	if err != nil {
		t.Fatalf("create fresh assigned work: %v", err)
	}
	if err := store.Update(freshWork.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark fresh assigned work in progress: %v", err)
	}

	unknown, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":                       string(session.StateActive),
			beadmeta.WorkerDirMetadataKey: "/work/unknown",
		},
	})
	if err != nil {
		t.Fatalf("create unknown session: %v", err)
	}
	unknownWork, err := store.Create(beads.Bead{Type: "task", Assignee: unknown.ID})
	if err != nil {
		t.Fatalf("create unknown assigned work: %v", err)
	}
	if err := store.Update(unknownWork.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark unknown assigned work in progress: %v", err)
	}

	unclaimed, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":                       string(session.StateActive),
			beadmeta.WorkerDirMetadataKey: "/work/unclaimed",
		},
	})
	if err != nil {
		t.Fatalf("create unclaimed session: %v", err)
	}

	check := &sessionWorkStalenessDoctorCheck{
		cfg: &config.City{Session: config.SessionConfig{ClaimHolderStallTimeout: "20m"}},
		newStore: func(string) (beads.Store, error) {
			return store, nil
		},
		now: func() time.Time { return now },
		lastWorkWrite: func(dir string) (time.Time, error) {
			switch dir {
			case "/work/stale":
				return now.Add(-21 * time.Minute), nil
			case "/work/fresh":
				return now.Add(-19 * time.Minute), nil
			default:
				return time.Time{}, errors.New("not observable")
			}
		},
	}

	result := check.Run(&doctor.CheckContext{})
	if result.Status != doctor.StatusWarning || result.Severity != doctor.SeverityAdvisory {
		t.Fatalf("result = %#v, want advisory warning", result)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, stale.ID) || !strings.Contains(details, "stale-work-output") {
		t.Fatalf("details = %q, want stale session finding", details)
	}
	if strings.Contains(details, fresh.ID) || strings.Contains(details, unknown.ID) || strings.Contains(details, unclaimed.ID) {
		t.Fatalf("details = %q, must omit fresh, unobservable, and unclaimed sessions", details)
	}
}

func TestSessionWorkStalenessDoctorCheckDisabledWithoutClaimHolderThreshold(t *testing.T) {
	check := &sessionWorkStalenessDoctorCheck{cfg: &config.City{}, newStore: func(string) (beads.Store, error) {
		t.Fatal("disabled check must not open the store")
		return nil, nil
	}}
	if result := check.Run(&doctor.CheckContext{}); result.Status != doctor.StatusOK {
		t.Fatalf("result = %#v, want disabled check to pass", result)
	}
}

func TestWorkerDirLastWriteIgnoresGitMetadata(t *testing.T) {
	dir := t.TempDir()
	workFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(workFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("create git metadata dir: %v", err)
	}
	gitFile := filepath.Join(gitDir, "index")
	if err := os.WriteFile(gitFile, []byte("metadata"), 0o644); err != nil {
		t.Fatalf("write git metadata file: %v", err)
	}
	workTime := time.Date(2026, 8, 26, 2, 30, 0, 0, time.UTC)
	if err := os.Chtimes(workFile, workTime, workTime); err != nil {
		t.Fatalf("set work file time: %v", err)
	}
	gitTime := workTime.Add(time.Hour)
	if err := os.Chtimes(gitFile, gitTime, gitTime); err != nil {
		t.Fatalf("set git metadata time: %v", err)
	}

	got, err := workerDirLastWrite(dir)
	if err != nil {
		t.Fatalf("workerDirLastWrite: %v", err)
	}
	if !got.Equal(workTime) {
		t.Fatalf("last write = %s, want source file time %s", got, workTime)
	}
}

func TestBuildDoctorChecksRegistersSessionWorkStaleness(t *testing.T) {
	checks := buildDoctorChecks(t.TempDir(), &config.City{}, nil, buildDoctorChecksOpts{
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
	})
	for _, check := range checks {
		if check.Name() == "session-work-staleness" {
			return
		}
	}
	t.Fatal("buildDoctorChecks did not register session-work-staleness")
}
