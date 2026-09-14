package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scope: the identity `gc hook <agent>` presents to the work query on its
// EXPLICIT-TARGET branch, for a pool whose real sessions carry SUFFIXED slot
// names.
//
// Why this suite exists. That branch is the pool door -- `gc hook <pool>` with
// no session of its own, which is how the city's stall guardrail asks a pool
// whether a starting session would find work. It exported the BARE pool name,
// and no session of a multi-slot pool is ever named that: the slots are
// worker-1, worker-2, worker-3. The bare name then landed in the work query's
// own-identity arm, which does NOT exclude hold labels -- deliberately, per
// internal/beadmeta/hold_labels.go, because that arm hands a session back its
// OWN in-flight work and a hold must not strand it. So the door was offered
// held beads that no real slot would be offered, and the guardrail counted
// them as claimable work (ci-45nrw8, split from ci-uh14i2).
//
// The second identity was worse and was found while fixing the first:
// GC_SESSION_NAME came from cliSessionName, whose store lookup
// (findSessionNameByTemplate) returns the FIRST open session bead carrying the
// pool's template -- a LIVE slot's session name, picked by list order. The
// door therefore presented a running slot's identity, non-deterministically.
//
// What it delegates elsewhere: that at least one identity survives for the
// Stop gate to probe is cmd_hook_stop_gate_identity_test.go's lower bound, and
// this suite must keep satisfying it; the Stop gate's allow/block verdicts are
// cmd_hook_stop_test.go's; which beads a demand predicate admits is
// build_desired_state_pool_alias_demand_test.go's.
//
// What it cannot represent: the beads a real bd returns. The stand-in below
// answers every query with an empty array, so these tests pin the QUESTIONS
// the door asks, never the rows. The row-level agreement between the door and
// a real slot is demonstrated against a live store and recorded on ci-45nrw8.
//
// Run: go test ./cmd/gc/ -run PoolDoor

// poolDoorCityWithAgent writes a city carrying one agent named `worker` with
// the supplied extra agent TOML, and returns the city path.
//
// Separate from stopGateProbeIdentities in the sibling file rather than shared
// with it: that helper takes a session cap and owns a work_query of its own,
// because its invariant is about the exported values alone. These tests need
// arbitrary agent keys (namepool, min_active_sessions) and the DEFAULT work
// query, so they would have had to bend that helper's contract out of shape.
func poolDoorCityWithAgent(t *testing.T, agentTOML string) string {
	t.Helper()
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	cityTOML := "[workspace]\nname = \"test-city\"\n\n[[agent]]\nname = \"worker\"\n" + agentTOML
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	return cityDir
}

// poolDoorIdentities runs `gc hook worker` against a city whose agent carries
// agentTOML and returns the GC_SESSION_ID / GC_SESSION_NAME / GC_ALIAS values
// the work query observed.
//
// The work query is overridden to record rather than query, because the values
// are what this half of the suite is about; the tier-level half below uses the
// default query and a bd stand-in instead.
func poolDoorIdentities(t *testing.T, agentTOML string, env map[string]string) []string {
	t.Helper()
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	// After clearGCEnv, which wipes every live GC_ key a caller might have
	// inherited -- including the one case below cares about.
	for k, v := range env {
		t.Setenv(k, v)
	}
	outPath := filepath.Join(t.TempDir(), "identity")
	// A TOML literal string: the query carries shell quotes and a path, which
	// a basic string would need escaped twice over.
	query := fmt.Sprintf(
		`work_query = 'printf "%%s\n%%s\n%%s" "${GC_SESSION_ID:-}" "${GC_SESSION_NAME:-}" "${GC_ALIAS:-}" > %s; printf "[]"'`+"\n",
		outPath)
	cityDir := poolDoorCityWithAgent(t, agentTOML+query)
	t.Setenv("GC_CITY", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newHookCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"worker"})
	_ = cmd.Execute() //nolint:errcheck // an empty offer exits nonzero; the env is what is under test

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("work query did not record its identity env: %v; stderr=%s", err, stderr.String())
	}
	got := strings.Split(string(raw), "\n")
	if len(got) != 3 {
		t.Fatalf("identity record %q does not carry three values", raw)
	}
	return got
}

// TestPoolDoorPresentsNoIdentityARealSlotCannotCarry requires that on the
// explicit-target branch of a pool minting suffixed slot names, no exported
// identity is the bare pool name.
//
// The bare name is the whole defect: it is the one string that reaches the
// hold-transparent own-identity arm while belonging to no session, so a held
// bead parked on the pool alias is offered to the door and to nobody else.
//
// The shapes are the ones config.Agent.SupportsExpandedSessionIdentities
// actually distinguishes. `namepool_names` is included because a namepool
// draws slot names from a list rather than synthesizing `worker-N`, so a fix
// keyed on the numeric suffix alone would go green over it.
func TestPoolDoorPresentsNoIdentityARealSlotCannotCarry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
	}{
		{name: "capped multi-slot pool", agent: "max_active_sessions = 3\nmin_active_sessions = 0\n"},
		{name: "uncapped pool", agent: "min_active_sessions = 0\n"},
		{name: "namepool", agent: "max_active_sessions = 2\nnamepool_names = [\"ada\", \"grace\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := poolDoorIdentities(t, tc.agent, nil)
			for i, id := range ids {
				if strings.TrimSpace(id) == "worker" {
					t.Errorf("identity %d is the bare pool name %q; no session of this pool carries it, and the work query's own-identity arm does not exclude hold labels, so the door is offered held work no slot would be", i, id)
				}
			}
			nonEmpty := false
			for _, id := range ids {
				if strings.TrimSpace(id) != "" {
					nonEmpty = true
				}
			}
			if !nonEmpty {
				t.Error("every identity is empty; that is the reverted ci-uh14i2 shape, which blinds the Stop gate")
			}
		})
	}
}

// TestPoolDoorKeepsTheBareNameWhereASessionCarriesIt is the other half, and it
// is the one that keeps crash recovery.
//
// For these shapes the configured name IS a running session's identity, so
// `bd list --status in_progress --assignee=worker` is the only tier that finds
// work a crashed session left behind -- `bd ready` excludes in_progress by
// design, so no hold-excluding tier can serve it. Narrowing the identity here
// would strand that work silently.
func TestPoolDoorKeepsTheBareNameWhereASessionCarriesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
	}{
		{name: "singleton pool", agent: "max_active_sessions = 1\nmin_active_sessions = 1\n"},
		{name: "named-session singleton", agent: "max_active_sessions = 1\n"},
		{name: "session creation disabled", agent: "max_active_sessions = 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := poolDoorIdentities(t, tc.agent, nil)
			for _, id := range ids {
				if strings.TrimSpace(id) == "worker" {
					return
				}
			}
			t.Errorf("no identity is the configured name (%q); a session of this shape IS named that, so its in_progress crash-recovery tier now matches nothing", ids)
		})
	}
}

// bdArgvRecorder installs a `bd` stand-in on PATH that appends one bracketed
// line per invocation to the returned log path and answers every query with an
// empty array.
//
// It REFUSES an unrecognized subcommand rather than answering it, so a tier
// added in a shape this suite does not know about surfaces in the log instead
// of passing silently. Answering `[]` to the recognized ones is deliberate and
// is the limit of what this stand-in proves: the assertions below read the
// QUESTIONS asked, never the rows returned.
func bdArgvRecorder(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd-argv.log")
	writeExecutable(t, filepath.Join(binDir, "bd"), `#!/bin/sh
{ for a in "$@"; do printf '[%s]' "$a"; done; printf '\n'; } >> "$BD_ARGV_LOG"
case "$1" in
  ready|list|query|show) printf '[]' ;;
  *) printf 'bd stand-in: unscripted subcommand %s\n' "$1" >&2; exit 3 ;;
esac
`)
	t.Setenv("BD_ARGV_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// TestPoolDoorAsksAboutThePoolNameOnlyThroughAHoldExcludingTier pins the
// defect at the level it is measured on the live store: which predicate the
// pool name reaches.
//
// Two tiers query `--assignee=worker`. The pool-alias demand tier
// (bdReadyPoolAliasDemandShell) carries `--exclude-label` for every
// beadmeta.DispatchHoldLabels value and is what a real slot's claim falls
// through to. The assigned tiers carry none, deliberately, and must therefore
// never be handed a name that is not a session's own.
//
// The discriminator is the presence of `--exclude-label` in the SAME argv, not
// the tier's position, because the two tiers are otherwise the same question.
func TestPoolDoorAsksAboutThePoolNameOnlyThroughAHoldExcludingTier(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	logPath := bdArgvRecorder(t)
	cityDir := poolDoorCityWithAgent(t, "max_active_sessions = 3\nmin_active_sessions = 0\n")
	t.Setenv("GC_CITY", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newHookCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"worker"})
	_ = cmd.Execute() //nolint:errcheck // an empty offer exits nonzero

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("bd stand-in recorded nothing: %v; stderr=%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	holdExcluding := 0
	for _, line := range lines {
		if !strings.Contains(line, "[--assignee=worker]") {
			continue
		}
		if strings.Contains(line, "[--exclude-label]") {
			holdExcluding++
			continue
		}
		t.Errorf("the door asked a hold-transparent question about the pool name: %s", line)
	}
	if holdExcluding == 0 {
		t.Errorf("no hold-excluding query named the pool name at all; the pool-alias demand tier is the one path by which hand-assigned pool work reaches a slot, and it is now missing. recorded:\n%s", raw)
	}
}

// TestExplicitHookStillResolvesASessionNameWhereItIsNotADoor keeps the branch
// the fix does NOT take under test.
//
// The two sibling identity-export tests in cmd_hook_test.go used to cover this
// and now cover the door instead, because the agent shape they declare (no
// max_active_sessions, so unlimited capacity) turns out to be a door. Without
// this case, narrowing PoolDoorProbeIdentity to fire for EVERY shape would go
// green: nothing else exercises cliSessionName from an explicit target.
func TestExplicitHookStillResolvesASessionNameWhereItIsNotADoor(t *testing.T) {
	ids := poolDoorIdentities(t, "max_active_sessions = 1\n", map[string]string{"GC_TMUX_SESSION": "host-session"})
	if strings.TrimSpace(ids[1]) != "host-session" {
		t.Errorf("GC_SESSION_NAME = %q, want the resolved session name %q; an agent whose sessions carry its configured name is not a pool door and must still resolve one", ids[1], "host-session")
	}
}

// TestPoolDoorClaimWritesANameASessionCanCarry pins the half of the fix that
// nothing else can observe: the door identity is a QUERY stand-in and must
// never be written.
//
// `gc hook <pool> --claim` derives its assignee from the same GC_ALIAS the
// work query reads. Letting the stand-in through would stamp it onto the bead,
// minting by hand exactly the defect
// internal/doctor/checks_unclaimable_assignee.go exists to report -- an
// assignee no session can carry, which no queue ever returns again and nothing
// logs. The query tiers legitimately pass the stand-in as `--assignee=`, so
// the assertion is scoped to the WRITE verb rather than to the string.
func TestPoolDoorClaimWritesANameASessionCanCarry(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	// Refuses an unrecognized subcommand rather than answering it, so a claim
	// path that grows a new bd verb surfaces here instead of being handed a
	// success it never scripted.
	writeExecutable(t, filepath.Join(binDir, "bd"), fmt.Sprintf(`#!/bin/sh
printf 'actor=%%s args=%%s\n' "${BEADS_ACTOR:-}" "$*" >> %q
case "$1" in
  ready|list|query|show|update) ;;
  *) printf 'bd stand-in: unscripted subcommand %%s\n' "$1" >&2; exit 3 ;;
esac
case "$*" in
  *"gc.routed_to=worker"*"--unassigned"*)
    printf '[{"id":"hw-door","status":"open","metadata":{"gc.routed_to":"worker"}}]'
    ;;
  *"update hw-door"*|*"show --json hw-door"*)
    printf '[{"id":"hw-door","status":"in_progress","assignee":"%%s","metadata":{"gc.routed_to":"worker"}}]' "${BEADS_ACTOR:-}"
    ;;
  *)
    printf '[]'
    ;;
esac
`, logPath))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cityDir := poolDoorCityWithAgent(t, "max_active_sessions = 3\nmin_active_sessions = 0\n")
	t.Setenv("GC_CITY", cityDir)

	var stdout, stderr bytes.Buffer
	if code := cmdHookWithOptions([]string{"worker"}, hookCommandOptions{Claim: true, JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc hook worker --claim = %d, want 0; stdout=%q stderr=%s", code, stdout.String(), stderr.String())
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("bd stand-in recorded nothing: %v; stderr=%s", err, stderr.String())
	}
	writes := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, "args=update ") {
			continue
		}
		writes++
		if strings.Contains(line, poolDoorProbeMarker) {
			t.Errorf("a claim wrote the pool-door stand-in: %s", line)
		}
		if !strings.Contains(line, "actor=worker ") {
			t.Errorf("a claim ran under an actor other than the resolved agent: %s", line)
		}
	}
	if writes == 0 {
		t.Fatalf("no bd write ran at all, so this test asserted nothing; recorded:\n%s", raw)
	}
}

// poolDoorProbeMarker is the substring that identifies a pool-door identity.
// Spelled here rather than imported from internal/config so a rename of the
// suffix over there cannot silently make the assertion above match nothing --
// it would fail this file's other tests first, which name the whole value.
const poolDoorProbeMarker = ":pool-door"

// TestACallerNamingItselfIsNotTreatedAsAPoolDoor pins the gate that separates
// the door from a session asking about itself.
//
// A pool instance whose GC_TEMPLATE is unset takes the SAME explicit-target
// branch with GC_AGENT set to its own instance name, and its agent config is
// pool-shaped, so a fix keyed on the agent shape alone substitutes a stand-in
// for a live session's identity and takes that session's in-flight work off
// its own hook. That is this change's own failure mode, inverted, and it went
// unnoticed until two rig-scope tests in cmd_hook_test.go went red for what
// looked like an unrelated reason.
func TestACallerNamingItselfIsNotTreatedAsAPoolDoor(t *testing.T) {
	for _, key := range []string{"GC_ALIAS", "GC_AGENT", "GC_SESSION_NAME"} {
		t.Run(key, func(t *testing.T) {
			ids := poolDoorIdentities(t, "max_active_sessions = 3\nmin_active_sessions = 0\n",
				map[string]string{key: "worker"})
			for _, id := range ids {
				if strings.Contains(id, poolDoorProbeMarker) {
					t.Fatalf("caller presenting %s=worker was handed a pool-door stand-in %q; its own in-flight work is now off its hook", key, id)
				}
			}
			for _, id := range ids {
				if strings.TrimSpace(id) == "worker" {
					return
				}
			}
			t.Errorf("identities %q carry neither the caller's own name nor a stand-in", ids)
		})
	}
}
