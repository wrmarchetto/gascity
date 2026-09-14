package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scope: the invariant that the identity environment `gc hook <agent>` builds
// on its EXPLICIT-TARGET branch always leaves the Stop gate something to probe.
//
// Why this suite exists, and it is a near-miss rather than a hypothetical. The
// Stop gate finds a session's outstanding claims with stopGateHeldClaimsQuery,
// which is identity-only:
//
//	for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do
//	  [ -z "$id" ] || for st in in_progress open; do bd list --status "$st" --assignee="$id" ...
//
// GC_SESSION_ID is already empty on that branch, so the gate rides on the other
// two. An attempted fix for city bead ci-uh14i2 blanked BOTH of them for
// multi-session pools, and the result was not a visible failure: with all three
// empty the loop body never runs, `held` comes back `[]` and `known` comes back
// TRUE, so probeStopGateOutstanding reports an ESTABLISHED absence rather than
// an error. The gate then allows a turn to end while the session still holds a
// claim -- a confident wrong verdict, which is worse than a failure, and
// exactly the stall the gate exists to prevent.
//
// That attempt was caught only by a Stop-gate test in another file asserting an
// unrelated verdict, and by four cmd_hook tests asserting the identity export
// for their own reasons. Nothing pinned the invariant itself, which is why
// breaking it read as "five unrelated tests went red" instead of "the Stop gate
// is now blind". This suite names it.
//
// It asserts a LOWER BOUND, deliberately: at least one identity survives, not
// which one or what it equals. What the values should BE is contested -- the
// bare name of a multi-session pool is the pool door and feeding it to the work
// query's hold-transparent ready arm is the ci-uh14i2 defect -- and pinning a
// specific value here would cement one side of a decision this suite has no
// business making. Any future narrowing of these values is free to change them
// and must still leave the gate a probe.
//
// What it delegates elsewhere: the exported values themselves are
// TestCmdHookExportsResolvedIdentityForFixedAgentQuery's and its rig-context
// sibling; the Stop gate's allow/block verdicts are cmd_hook_stop_test.go's;
// which beads a demand predicate admits is
// build_desired_state_pool_alias_demand_test.go's.
//
// Run: go test ./cmd/gc/ -run StopGateIdentitySurvives

// stopGateProbeIdentities returns the identity values stopGateHeldClaimsQuery
// iterates, as `gc hook <agent>` exported them on the explicit-target branch.
//
// The work_query is the capture mechanism because it is the one place that
// observes this exact environment; the values are recorded rather than the
// query's answer, so the assertion does not depend on a bead store at all.
//
// explicitZero writes `max_active_sessions = 0` out, as opposed to omitting the
// key. The two mean opposite things -- an absent value is UNLIMITED capacity
// (Agent.HasUnlimitedSessionCapacity), an explicit zero disables ephemeral
// session creation -- so collapsing them would leave the shape that most
// resembles a singleton untested.
func stopGateProbeIdentities(t *testing.T, agentName string, maxActive int, explicitZero bool) []string {
	t.Helper()
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	cityDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "identity")
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	capacity := ""
	if maxActive > 0 || explicitZero {
		capacity = fmt.Sprintf("max_active_sessions = %d\n", maxActive)
	}
	// A TOML literal string: the query carries shell quotes and a path, which a
	// basic string would need escaped twice over.
	cityToml := fmt.Sprintf(`[workspace]
name = "test-city"

[[agent]]
name = %q
%swork_query = 'printf "%%s\n%%s\n%%s" "${GC_SESSION_ID:-}" "${GC_SESSION_NAME:-}" "${GC_ALIAS:-}" > %s; printf "[]"'
`, agentName, capacity, outPath)
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityToml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_CITY", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newHookCmd(&stdout, &stderr)
	cmd.SetArgs([]string{agentName})
	_ = cmd.Execute() //nolint:errcheck // an empty offer exits nonzero; the env is what is under test

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("work query did not record its identity env: %v; stderr=%s", err, stderr.String())
	}
	got := strings.Split(string(raw), "\n")
	if len(got) != 3 {
		t.Fatalf("identity record %q does not carry the three values stopGateHeldClaimsQuery iterates", raw)
	}
	return got
}

// TestStopGateIdentitySurvivesAnExplicitHookProbe requires every agent shape to
// leave the Stop gate at least one identity to probe.
//
// Every shape, not just the pool: the blanking that motivated this was keyed on
// agent capacity, so a suite covering one shape would have gone green over it.
// The shapes are the four the capacity predicates actually distinguish, and
// three of them are boundaries rather than variations -- undeclared capacity is
// unlimited and behaves like a pool, an explicit zero disables session creation
// and behaves like a singleton.
func TestStopGateIdentitySurvivesAnExplicitHookProbe(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxActive    int
		explicitZero bool
	}{
		{name: "multi-session pool", maxActive: 3},
		{name: "singleton", maxActive: 1},
		{name: "no capacity declared, so unlimited", maxActive: 0},
		{name: "max_active_sessions = 0", maxActive: 0, explicitZero: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := stopGateProbeIdentities(t, "worker", tc.maxActive, tc.explicitZero)
			for _, id := range ids {
				if strings.TrimSpace(id) != "" {
					return
				}
			}
			t.Errorf("every identity stopGateHeldClaimsQuery iterates is empty (%q); the gate's loop body never runs, so it reports an ESTABLISHED absence of held work and lets a turn end on an outstanding claim", ids)
		})
	}
}

// TestStopGateIdentityProbeRecordsAllThreeSlots is a guard on the guard. The
// test above passes vacuously if the capture ever stops observing all three
// values -- a renamed variable, a reordered query -- because "at least one
// non-empty" is satisfied by a partial record just as well as a whole one.
//
// So the names are asserted against the query the gate actually runs, rather
// than against a copy of the list. A variable renamed on one side and not the
// other fails here instead of silently shrinking the invariant above.
func TestStopGateIdentityProbeRecordsAllThreeSlots(t *testing.T) {
	for _, name := range []string{"GC_SESSION_ID", "GC_SESSION_NAME", "GC_ALIAS"} {
		if !strings.Contains(stopGateHeldClaimsQuery, "$"+name) {
			t.Errorf("stopGateHeldClaimsQuery no longer iterates %s; the capture in this file records it and the invariant above is scoped to it, so both must be updated together", name)
		}
	}
}
