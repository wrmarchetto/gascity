// cmd/gc/cmd_hook_claim_session_store_test.go
//
// Scope: which store gc hook --claim reads the claiming SESSION bead from.
//
// Why this suite exists: session beads live only in the city store, but a
// rig-scoped session claims its work from its rig store, and the claim used to
// read its own session bead through that same rig store. The read answered
// `bead not found` for every rig-scoped claim, and the claim silently dropped
// the claimant's provider, session key and worktree from the work bead
// (ci-d9w7fl, measured on 58 of 58 claimant entries in the astoria-sel4 store).
// Nothing failed, so nothing noticed.
//
// The end-to-end case drives the real cmdHookWithOptions over a bd stand-in
// that serves the session bead ONLY under city coordinates and refuses it
// under rig ones, the way the two real stores answer. A unit test of
// doHookClaim cannot see the one line in cmd_hook.go that resolves the city
// store, which is exactly the line a refactor would drop.
//
// Delegated elsewhere: what the claim stamps from the session bead is pinned
// in cmd_hook_claim_stamp_test.go and cmd_hook_claim_sessions_test.go, and the
// stale-session fence, which already reads the city store, in
// cmd_hook_claim_session_fence_test.go.
//
// Run: go test ./cmd/gc/ -run 'SessionBead|CityHookStore'
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// sessionStoreStandInSession is the claiming session's bead id. Distinct from
// every work-bead id the stand-in serves, so a match on it cannot be a match
// on the work bead's own show.
const sessionStoreStandInSession = "ci-sess9"

// TestRigScopedClaimReadsSessionBeadFromCityStore pins, end to end, that a
// rig-scoped session's claim reads its session bead under CITY store
// coordinates.
//
// The stand-in keys on GC_STORE_SCOPE, which controllerWorkQueryEnv sets to
// "city" or "rig" for every store gc builds, and refuses the session bead
// under rig scope with bd's own not-found shape. It also refuses any verb it
// was not scripted for, so a claim that grows a new bd call fails here rather
// than being handed a success.
func TestRigScopedClaimReadsSessionBeadFromCityStore(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "myrig-repo")
	for _, dir := range []string{filepath.Join(cityDir, ".gc"), rigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cityTOML := fmt.Sprintf(`[workspace]
name = "test-city"

[[rigs]]
name = "myrig"
path = %q

[[agent]]
name = "worker"
dir = "myrig"
`, rigDir)
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	writeExecutable(t, filepath.Join(binDir, "bd"), fmt.Sprintf(`#!/bin/sh
printf 'scope=%%s args=%%s\n' "${GC_STORE_SCOPE:-}" "$*" >> %q
case "$1" in
  ready|list|query|show|update) ;;
  *) printf 'bd stand-in: unscripted subcommand %%s\n' "$1" >&2; exit 3 ;;
esac
case "$*" in
  *"show"*"%[2]s"*)
    if [ "${GC_STORE_SCOPE:-}" = city ]; then
      printf '[{"id":"%[2]s","issue_type":"session","status":"open","metadata":{"provider":"claude","session_key":"key-9"}}]'
    else
      printf 'Error: no issue found matching "%[2]s"\n' >&2
      exit 1
    fi
    ;;
  *"list --status in_progress"*)
    if [ "${GC_STORE_SCOPE:-}" = rig ]; then
      printf '[{"id":"rg-wddx6","status":"in_progress","assignee":"myrig/worker","metadata":{"gc.routed_to":"myrig/worker"}}]'
    else
      printf '[]'
    fi
    ;;
  *"show"*"rg-wddx6"*|*"update rg-wddx6"*)
    printf '[{"id":"rg-wddx6","status":"in_progress","assignee":"myrig/worker","metadata":{"gc.routed_to":"myrig/worker"}}]'
    ;;
  *)
    printf '[]'
    ;;
esac
`, logPath, sessionStoreStandInSession))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_DIR", rigDir)
	t.Setenv("GC_TEMPLATE", "myrig/worker")
	t.Setenv("GC_ALIAS", "myrig/worker")
	t.Setenv("BEADS_ACTOR", "myrig/worker")
	t.Setenv("GC_SESSION_ID", sessionStoreStandInSession)
	t.Setenv("GC_SESSION_NAME", "myrig__worker-"+sessionStoreStandInSession)
	// The session's own BEADS_DIR is its rig store, as it is for every
	// rig-scoped session (docs/bd-store-scoping.md in the city). A session
	// store that inherited it instead of overriding it would read the rig.
	t.Setenv("BEADS_DIR", filepath.Join(rigDir, ".beads"))

	var stdout, stderr bytes.Buffer
	if code := cmdHookWithOptions(nil, hookCommandOptions{Claim: true, JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc hook --claim = %d, want 0; stdout=%q stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"bead_id":"rg-wddx6"`) {
		t.Fatalf("claim did not adopt the rig bead, so the session read was never reached: stdout=%q stderr=%s", stdout.String(), stderr.String())
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("bd stand-in recorded nothing: %v", err)
	}
	cityReads := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, "args=show") || !strings.Contains(line, sessionStoreStandInSession) {
			continue
		}
		if !strings.HasPrefix(line, "scope=city ") {
			t.Errorf("session bead read outside the city store: %s", line)
			continue
		}
		cityReads++
	}
	if cityReads == 0 {
		t.Errorf("no session bead read reached the city store; bd calls:\n%s\nstderr=%s", raw, stderr.String())
	}
	if strings.Contains(stderr.String(), "no issue found") || strings.Contains(stderr.String(), "reading session bead") {
		t.Errorf("claim reported its session bead unreadable: %s", stderr.String())
	}
}

// TestCityHookStoreIsCityScopedForRigAgent pins the store resolution the
// claim's session read depends on: for a rig-scoped agent it is the city,
// whatever BEADS_DIR the calling session inherited.
//
// Asserting the dir alone would pass with a rig-scoped env, because bd honors
// BEADS_DIR over cwd, so the env's own coordinates are asserted as well.
func TestCityHookStoreIsCityScopedForRigAgent(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "dart")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(rigPath, ".beads"))
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "dart", Path: rigPath}},
		Agents:    []config.Agent{{Name: "worker", Dir: "dart"}},
	}

	store, ok := cityHookStore(cityPath, cfg, &cfg.Agents[0], nil)
	if !ok {
		t.Fatal("cityHookStore() resolved no store for a rig-scoped agent")
	}
	if store.dir != cityPath {
		t.Errorf("session store dir = %q, want the city %q", store.dir, cityPath)
	}
	if scope, _ := lookupEnvValue(store.env, "GC_STORE_SCOPE"); scope != "city" {
		t.Errorf("session store GC_STORE_SCOPE = %q, want city", scope)
	}
	if root, _ := lookupEnvValue(store.env, "GC_STORE_ROOT"); root != cityPath {
		t.Errorf("session store GC_STORE_ROOT = %q, want %q", root, cityPath)
	}
	if dir, ok := lookupEnvValue(store.env, "BEADS_DIR"); ok && strings.HasPrefix(dir, rigPath) {
		t.Errorf("session store kept the rig BEADS_DIR %q", dir)
	}
}

// TestSessionBeadReaderRefusesUnresolvedStore pins that the production reader
// never falls back to cwd and BEADS_DIR when no city store was resolved.
//
// The stand-in on PATH would ANSWER the read with a session bead, so a reader
// that ran bd with an empty dir gets a success here and the test fails. A
// stand-in that errored would leave that fallback indistinguishable from the
// refusal.
func TestSessionBeadReaderRefusesUnresolvedStore(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "bd"), fmt.Sprintf(`#!/bin/sh
printf '[{"id":"%[1]s","issue_type":"session","status":"open"}]'
`, sessionStoreStandInSession))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	bead, err := hookReadSessionBeadWithBdStore(context.Background(), "", nil, sessionStoreStandInSession, "myrig/worker")
	if err == nil {
		t.Fatalf("reader with no resolved store returned %+v, want a refusal", bead)
	}
	if !strings.Contains(err.Error(), "no city store") {
		t.Errorf("refusal does not name the missing store: %v", err)
	}
}
