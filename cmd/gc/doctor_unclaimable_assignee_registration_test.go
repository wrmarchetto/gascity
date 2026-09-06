package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// Scope: which unclaimable-assignee scopes buildDoctorChecks registers, and
// nothing about what the check reports -- that is internal/doctor's
// checks_unclaimable_assignee_test.go.
//
// The suite exists because the detection and its reachability fail
// independently. A rig scope that is never registered reports nothing, exits
// 0, and is indistinguishable from a rig with no stranded work: exactly the
// state ci-tuy2u9 measured, where as-d5nv sat assigned and unclaimable for
// 9h25m in a store no doctor check read. The per-rig loop is a range over
// cfg.Rigs, so a future edit can drop the registration without touching a
// single line internal/doctor's tests cover.
//
// Run: go test ./cmd/gc/ -run TestBuildDoctorChecksRegistersUnclaimableAssignee

// TestBuildDoctorChecksRegistersUnclaimableAssigneePerRig pins one scope per
// non-suspended rig plus the city, derived from cfg.Rigs rather than from a
// hand-kept list of expected names -- an allowlist would go stale at the next
// rig added and report the rig it had never heard of as expected-absent.
//
// The suspended rig is in the same fixture because the exclusion is
// load-bearing, not tidiness: opening a suspended rig's store bd-auto-starts
// an orphan Dolt server (ga-wzk).
func TestBuildDoctorChecksRegistersUnclaimableAssigneePerRig(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	t.Setenv("GC_DOLT", "skip")
	cfg := &config.City{
		Workspace: config.Workspace{Name: "demo"},
		Rigs: []config.Rig{
			{Name: "astoria-sel4", Path: filepath.Join(cityDir, "rigs", "astoria-sel4")},
			{Name: "dart", Path: filepath.Join(cityDir, "rigs", "dart")},
			{Name: "parked", Path: filepath.Join(cityDir, "rigs", "parked"), SuspendedOnStart: true},
		},
	}

	names := doctorCheckNames(buildDoctorChecks(cityDir, cfg, nil, buildDoctorChecksOpts{
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
		SkipRigDoltChecks:    true,
	}))

	want := []string{"unclaimable-assignee:city"}
	for _, rig := range cfg.Rigs {
		if rig.EffectiveSuspendedOnStart() {
			continue
		}
		want = append(want, "unclaimable-assignee:"+rig.Name)
	}
	var got []string
	for _, name := range names {
		if strings.HasPrefix(name, "unclaimable-assignee:") {
			got = append(got, name)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unclaimable-assignee scopes = %v, want %v", got, want)
	}
}
