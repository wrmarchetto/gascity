package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

func TestWorktreeStaleCheckReportsBlockedSlot(t *testing.T) {
	cityPath := t.TempDir()
	slotPath := filepath.Join(cityPath, ".gc", "worktrees", "gascity", "toolsmith-1")
	if err := os.MkdirAll(slotPath, 0o755); err != nil {
		t.Fatalf("create worktree slot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotPath, worktreeStaleFileName), []byte("branch=HEAD\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	result := newWorktreeStaleCheck(cityPath).Run(&doctor.CheckContext{CityPath: cityPath})
	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning", result.Status)
	}
	if result.Severity != doctor.SeverityAdvisory {
		t.Errorf("severity = %v, want advisory", result.Severity)
	}
	if !strings.Contains(result.Message, "gascity/toolsmith-1") {
		t.Errorf("message = %q, want blocked slot name", result.Message)
	}
	if len(result.Details) != 1 || !strings.Contains(result.Details[0], filepath.Join("gascity", "toolsmith-1", worktreeStaleFileName)) {
		t.Errorf("details = %v, want stale marker path", result.Details)
	}
}

func TestWorktreeStaleCheckPassesWithoutMarkers(t *testing.T) {
	cityPath := t.TempDir()
	result := newWorktreeStaleCheck(cityPath).Run(&doctor.CheckContext{CityPath: cityPath})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK: %s", result.Status, result.Message)
	}
}

func TestBuildDoctorChecksRegistersWorktreeStaleCheck(t *testing.T) {
	cityPath := t.TempDir()
	checks := buildDoctorChecks(cityPath, nil, os.ErrInvalid, buildDoctorChecksOpts{
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
	})
	if doctorCheckIndex(doctorCheckNames(checks), "worktree-stale") < 0 {
		t.Fatalf("worktree-stale check not registered: %v", doctorCheckNames(checks))
	}
}
