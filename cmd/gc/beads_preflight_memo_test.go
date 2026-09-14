package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

// Scope: the process-wide memo in beads_preflight_memo.go that sits in front
// of the two expensive beads-preflight probes -- the `bd context --json`
// subprocess and the project-id identity dial.
//
// Why this suite exists: every bead-store open builds a FRESH
// contract.PreflightChecker and internal/beads/factory.go runs Check on every
// open, so nothing about the checker's own lifetime tells a reader whether a
// probe repeats. Before the memo, one `gc doctor --json` process paid 38
// `bd context --json` subprocesses and 38 project-id dials for 5 distinct
// scopes. These tests pin the caching AND the two properties that keep it
// honest: an entry dies when its inputs change, and a failure is never cached.
//
// Delegated elsewhere: whether the preflight VERDICT is right is
// internal/beads/contract's business
// (preflight_checker_test.go); this suite only counts probe runs and checks
// which answer comes back. What one store open costs in server connections is
// not represented here at all -- no host-side suite can see a TCP dial -- and
// stays a measurement against a live server.
//
// Run: go test ./cmd/gc/ -run TestPreflight

// countingPreflightProbes records how often each probe ran and refuses to
// invent answers it was not scripted for: an unscripted scope fails the test
// rather than returning a plausible zero value, so a memo keyed on the wrong
// thing shows up as a failure instead of a pass.
type countingPreflightProbes struct {
	t             *testing.T
	scripted      map[string]contract.PreflightBDContext
	projectIDs    map[string]string
	bdContextRuns map[string]int
	projectIDRuns map[string]int
	bdContextErr  error
	projectIDErr  error
}

func newCountingPreflightProbes(t *testing.T) *countingPreflightProbes {
	t.Helper()
	return &countingPreflightProbes{
		t:             t,
		scripted:      make(map[string]contract.PreflightBDContext),
		projectIDs:    make(map[string]string),
		bdContextRuns: make(map[string]int),
		projectIDRuns: make(map[string]int),
	}
}

func (p *countingPreflightProbes) script(scope, projectID string, ctx contract.PreflightBDContext) {
	p.scripted[scope] = ctx
	p.projectIDs[scope] = projectID
}

func (p *countingPreflightProbes) bdContext(scope string) (contract.PreflightBDContext, error) {
	p.bdContextRuns[scope]++
	if p.bdContextErr != nil {
		return contract.PreflightBDContext{}, p.bdContextErr
	}
	value, ok := p.scripted[scope]
	if !ok {
		p.t.Fatalf("bd-context probe called for unscripted scope %q", scope)
	}
	return value, nil
}

func (p *countingPreflightProbes) databaseProjectID(scope string) (string, bool, error) {
	p.projectIDRuns[scope]++
	if p.projectIDErr != nil {
		return "", false, p.projectIDErr
	}
	value, ok := p.projectIDs[scope]
	if !ok {
		p.t.Fatalf("project-id probe called for unscripted scope %q", scope)
	}
	return value, true, nil
}

// newPreflightScope creates a scope root carrying the .beads/metadata.json the
// fingerprint reads, inside a city carrying a city.toml, and returns both paths.
func newPreflightScope(t *testing.T, cityPath, name string) string {
	t.Helper()
	scopeRoot := filepath.Join(cityPath, name)
	if err := os.MkdirAll(filepath.Join(scopeRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir scope %s: %v", name, err)
	}
	writePreflightMetadata(t, scopeRoot, `{"backend":"dolt","project_id":"p-`+name+`"}`)
	return scopeRoot
}

func writePreflightMetadata(t *testing.T, scopeRoot, body string) {
	t.Helper()
	if err := os.WriteFile(scopeMetadataJSONPath(scopeRoot), []byte(body), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

func newPreflightCity(t *testing.T) string {
	t.Helper()
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[city]\nname = \"t\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	return cityPath
}

// TestPreflightProbesRunOncePerScopeWithinAProcess pins the caching itself.
// The checker is rebuilt on every iteration on purpose -- that is what
// production does, once per bead-store open -- so a memo living on the checker
// value would pass a single-checker test and still fail here.
func TestPreflightProbesRunOncePerScopeWithinAProcess(t *testing.T) {
	cityPath := newPreflightCity(t)
	scopeRoot := newPreflightScope(t, cityPath, "hq")
	probes := newCountingPreflightProbes(t)
	probes.script(scopeRoot, "p-hq", contract.PreflightBDContext{Backend: "dolt", BDVersion: "1.2.3"})
	memo := newPreflightMemo()

	const opens = 8
	for i := 0; i < opens; i++ {
		checker := newBeadsPreflightCheckerWithProbes(
			cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
		got, err := checker.BDContext(scopeRoot)
		if err != nil {
			t.Fatalf("open %d: BDContext: %v", i, err)
		}
		if got.BDVersion != "1.2.3" {
			t.Fatalf("open %d: BDVersion = %q, want %q", i, got.BDVersion, "1.2.3")
		}
		id, ok, err := checker.DatabaseProjectID(scopeRoot)
		if err != nil {
			t.Fatalf("open %d: DatabaseProjectID: %v", i, err)
		}
		if !ok || id != "p-hq" {
			t.Fatalf("open %d: DatabaseProjectID = %q,%v, want %q,true", i, id, ok, "p-hq")
		}
	}

	if got := probes.bdContextRuns[scopeRoot]; got != 1 {
		t.Errorf("bd-context probe ran %d times across %d store opens, want 1 (each run is a bd subprocess)", got, opens)
	}
	if got := probes.projectIDRuns[scopeRoot]; got != 1 {
		t.Errorf("project-id probe ran %d times across %d store opens, want 1 (each run is a dolt connection)", got, opens)
	}
}

// TestPreflightProbesRerunWhenScopeMetadataChanges pins the invalidation. A
// cache that only ever holds is indistinguishable from a correct one until an
// operator edits config, which is exactly when a stale backend or endpoint
// verdict does damage.
func TestPreflightProbesRerunWhenScopeMetadataChanges(t *testing.T) {
	cityPath := newPreflightCity(t)
	scopeRoot := newPreflightScope(t, cityPath, "hq")
	probes := newCountingPreflightProbes(t)
	probes.script(scopeRoot, "p-hq", contract.PreflightBDContext{Backend: "dolt"})
	memo := newPreflightMemo()

	checker := newBeadsPreflightCheckerWithProbes(
		cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
	if _, err := checker.BDContext(scopeRoot); err != nil {
		t.Fatalf("first BDContext: %v", err)
	}
	if _, _, err := checker.DatabaseProjectID(scopeRoot); err != nil {
		t.Fatalf("first DatabaseProjectID: %v", err)
	}

	// Rewrite metadata.json with a DIFFERENT length, so the stamp moves even
	// on a filesystem whose mtime granularity is coarser than this test runs.
	writePreflightMetadata(t, scopeRoot, `{"backend":"dolt","project_id":"p-hq","endpoint":"elsewhere:3306"}`)

	checker = newBeadsPreflightCheckerWithProbes(
		cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
	if _, err := checker.BDContext(scopeRoot); err != nil {
		t.Fatalf("post-edit BDContext: %v", err)
	}
	if _, _, err := checker.DatabaseProjectID(scopeRoot); err != nil {
		t.Fatalf("post-edit DatabaseProjectID: %v", err)
	}

	if got := probes.bdContextRuns[scopeRoot]; got != 2 {
		t.Errorf("bd-context probe ran %d times, want 2 (the metadata edit must invalidate)", got)
	}
	if got := probes.projectIDRuns[scopeRoot]; got != 2 {
		t.Errorf("project-id probe ran %d times, want 2 (the metadata edit must invalidate)", got)
	}
}

// TestPreflightMemoDoesNotShareEntriesAcrossScopes pins that the cache is keyed
// by scope. A memo keyed on the city alone would serve the hq verdict for a rig
// bound to a different backend, which is the misbinding preflight exists to
// catch.
func TestPreflightMemoDoesNotShareEntriesAcrossScopes(t *testing.T) {
	cityPath := newPreflightCity(t)
	hq := newPreflightScope(t, cityPath, "hq")
	rig := newPreflightScope(t, cityPath, "rig")
	probes := newCountingPreflightProbes(t)
	probes.script(hq, "p-hq", contract.PreflightBDContext{Backend: "dolt"})
	probes.script(rig, "p-rig", contract.PreflightBDContext{Backend: "doltlite"})
	memo := newPreflightMemo()

	for i := 0; i < 3; i++ {
		checker := newBeadsPreflightCheckerWithProbes(
			cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
		for scope, wantBackend := range map[string]string{hq: "dolt", rig: "doltlite"} {
			got, err := checker.BDContext(scope)
			if err != nil {
				t.Fatalf("BDContext(%s): %v", scope, err)
			}
			if got.Backend != wantBackend {
				t.Fatalf("BDContext(%s).Backend = %q, want %q", scope, got.Backend, wantBackend)
			}
			id, _, err := checker.DatabaseProjectID(scope)
			if err != nil {
				t.Fatalf("DatabaseProjectID(%s): %v", scope, err)
			}
			if want := probes.projectIDs[scope]; id != want {
				t.Fatalf("DatabaseProjectID(%s) = %q, want %q", scope, id, want)
			}
		}
	}

	for _, scope := range []string{hq, rig} {
		if got := probes.bdContextRuns[scope]; got != 1 {
			t.Errorf("bd-context probe ran %d times for %s, want 1", got, scope)
		}
		if got := probes.projectIDRuns[scope]; got != 1 {
			t.Errorf("project-id probe ran %d times for %s, want 1", got, scope)
		}
	}
}

// TestPreflightMemoDoesNotCacheProbeFailures pins the asymmetry that keeps a
// transient outage from becoming a process-lifetime verdict: a dolt server
// unreachable for one open must not degrade every later open in the same
// supervisor until it restarts.
func TestPreflightMemoDoesNotCacheProbeFailures(t *testing.T) {
	cityPath := newPreflightCity(t)
	scopeRoot := newPreflightScope(t, cityPath, "hq")
	probes := newCountingPreflightProbes(t)
	probes.script(scopeRoot, "p-hq", contract.PreflightBDContext{Backend: "dolt"})
	probes.bdContextErr = errors.New("dial tcp 127.0.0.1:31155: connection refused")
	probes.projectIDErr = probes.bdContextErr
	memo := newPreflightMemo()

	checker := newBeadsPreflightCheckerWithProbes(
		cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
	if _, err := checker.BDContext(scopeRoot); err == nil {
		t.Fatalf("BDContext succeeded while the probe was failing")
	}
	if _, _, err := checker.DatabaseProjectID(scopeRoot); err == nil {
		t.Fatalf("DatabaseProjectID succeeded while the probe was failing")
	}

	// The server comes back; the very next open must re-probe and succeed.
	probes.bdContextErr = nil
	probes.projectIDErr = nil
	checker = newBeadsPreflightCheckerWithProbes(
		cityPath, "dolt", memo, probes.bdContext, probes.databaseProjectID)
	got, err := checker.BDContext(scopeRoot)
	if err != nil {
		t.Fatalf("BDContext after recovery: %v", err)
	}
	if got.Backend != "dolt" {
		t.Fatalf("Backend after recovery = %q, want %q", got.Backend, "dolt")
	}
	id, ok, err := checker.DatabaseProjectID(scopeRoot)
	if err != nil || !ok || id != "p-hq" {
		t.Fatalf("DatabaseProjectID after recovery = %q,%v,%v, want %q,true,nil", id, ok, err, "p-hq")
	}

	if got := probes.bdContextRuns[scopeRoot]; got != 2 {
		t.Errorf("bd-context probe ran %d times, want 2 (a failure must not be cached)", got)
	}
	if got := probes.projectIDRuns[scopeRoot]; got != 2 {
		t.Errorf("project-id probe ran %d times, want 2 (a failure must not be cached)", got)
	}
}
