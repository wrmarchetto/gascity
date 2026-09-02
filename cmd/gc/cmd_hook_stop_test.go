// cmd/gc/cmd_hook_stop_test.go
//
// Scope: the Stop-event gate in cmd_hook_stop.go -- the verdict function and
// the stdin payload parse that feeds it.
//
// Why this suite exists: the gate's whole value is the branch that BLOCKS,
// and that branch is the one a green suite most easily fails to exercise. A
// stop-gate test asserting only that clean sessions pass cannot distinguish a
// working gate from a hook that never fires, so TestStopGateBlocks* are the
// load-bearing cases here and every one of them was observed failing against
// a deliberately inert evaluateStopGate before the real one was written.
//
// The gate's fail-open paths are equally pinned: a gate that blocks on an
// unanswerable store query would wedge every session in the city at once,
// which is strictly worse than the stall it exists to fix.
//
// Delegated elsewhere: the federated store resolution and the
// assigned-in-progress query itself are the claim path's, covered by
// cmd_hook_test.go and internal/config/workquery_test.go. The wiring of the
// hook into the managed settings file is pinned in
// internal/hooks/hooks_test.go.
//
// Run: go test ./cmd/gc/ -run TestStopGate
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// stopGateHoldingWork is a session mid-contract: it claimed a bead and has
// not closed it. Used as the base for the blocking cases so each test varies
// exactly one fact away from it.
func stopGateHoldingWork() stopGateFacts {
	return stopGateFacts{
		sessionID:     "ci-l1ua",
		sessionOrigin: sessionOriginEphemeral,
		outstanding:   []beads.Bead{{ID: "ci-yxpd", Status: "in_progress"}},
	}
}

// TestStopGateBlocksTurnWhileClaimedWorkIsUnclosed pins the primary
// invariant: a session that ends its turn holding claimed work does not get
// to stop.
//
// This is the first observed shape of the bug -- an agent wrote the fix, then
// ended the turn before commit and close. Asserting the reason NAMES the bead
// is part of the invariant, not cosmetic: the reason is the agent's only
// prompt to finish, and a block that does not say which bead is outstanding
// sends it back to re-derive that from scratch.
func TestStopGateBlocksTurnWhileClaimedWorkIsUnclosed(t *testing.T) {
	verdict := evaluateStopGate(stopGateHoldingWork())
	if !verdict.block {
		t.Fatal("gate allowed the turn to end while claimed work was unclosed")
	}
	if !strings.Contains(verdict.reason, "ci-yxpd") {
		t.Errorf("block reason does not name the outstanding bead: %q", verdict.reason)
	}
}

// TestStopGateExemptsOpenPMSittingBeads pins the deliberately narrow
// exception for beads that hold a PM conversation open across turns.
//
// The surrounding closing-contract gate remains load-bearing: this test uses
// otherwise-blocking facts and varies only the existing sitting marker. If
// the marker set is inverted or omitted, the allow assertions fail; if the
// entire gate is removed, TestStopGateBlocksTurnWhileClaimedWorkIsUnclosed
// continues to fail.
func TestStopGateExemptsOpenPMSittingBeads(t *testing.T) {
	for _, label := range []string{"pm-init", "pm-plan", "pm-chat"} {
		t.Run(label, func(t *testing.T) {
			facts := stopGateHoldingWork()
			facts.sessionOrigin = "named"
			facts.outstanding = []beads.Bead{{
				ID:     "ci-pm-sitting",
				Status: "in_progress",
				Labels: []string{label},
			}}

			if evaluateStopGate(facts).block {
				t.Fatalf("gate blocked open %q sitting work", label)
			}
		})
	}
}

// TestStopGateStillBlocksUnlabeledAndClosedPMSittingBeads pins both edges of
// the exception: only open beads carrying one of the established PM sitting
// labels are exempt from the closing-contract block.
func TestStopGateStillBlocksUnlabeledAndClosedPMSittingBeads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		labels []string
	}{
		{name: "unlabeled", status: "in_progress"},
		{name: "unrelated label", status: "in_progress", labels: []string{"pm-review"}},
		{name: "closed pm chat", status: "closed", labels: []string{"pm-chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := stopGateHoldingWork()
			facts.sessionOrigin = "named"
			facts.outstanding = []beads.Bead{{
				ID:     "ci-not-exempt",
				Status: tc.status,
				Labels: tc.labels,
			}}

			if !evaluateStopGate(facts).block {
				t.Fatalf("gate allowed non-exempt outstanding work: status=%q labels=%v", tc.status, tc.labels)
			}
		})
	}
}

// TestStopGateBlocksEphemeralSessionThatSkippedDrainAck pins the second
// observed shape: the bead WAS closed and the turn still ended one command
// early, leaving the session holding its agent's concurrency slot with
// nothing left to do.
//
// Constructed with an empty outstanding list on purpose -- it must fail if
// the gate only ever checks for open beads, which is the narrower fix the
// bug report's proposed shape would have produced.
func TestStopGateBlocksEphemeralSessionThatSkippedDrainAck(t *testing.T) {
	facts := stopGateHoldingWork()
	facts.outstanding = nil

	verdict := evaluateStopGate(facts)
	if !verdict.block {
		t.Fatal("gate allowed an ephemeral session to end its turn without acknowledging drain")
	}
	if !strings.Contains(verdict.reason, "gc runtime drain-ack") {
		t.Errorf("block reason does not name the remaining command: %q", verdict.reason)
	}
}

// TestStopGateAllowsReentryWhileStopHookActive pins loop safety.
//
// The provider re-enters this hook with stop_hook_active set after a block.
// A gate that re-blocks on the same unchanged facts spins a genuinely stuck
// agent forever, so one block per stop sequence is the entire budget. The
// facts here are the blocking ones with ONLY that flag flipped, which is what
// makes the test able to catch a gate that checks the flag too late.
func TestStopGateAllowsReentryWhileStopHookActive(t *testing.T) {
	facts := stopGateHoldingWork()
	facts.stopHookActive = true

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked a re-entered stop; a stuck agent would spin forever")
	}
}

// TestStopGateAllowsIdleSessionHoldingNoWork pins the no-op requirement that
// keeps the gate off long-lived named sessions.
//
// The settings file carrying this hook is shared by every session in the
// city. A named session that idles at its prompt with no claimed work is
// behaving correctly; blocking it traps it in a loop it cannot exit, because
// there is no bead for it to close to get out.
func TestStopGateAllowsIdleSessionHoldingNoWork(t *testing.T) {
	facts := stopGateFacts{sessionID: "ci-mayor", sessionOrigin: "named"}

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked an idle session with no claimed work")
	}
}

// TestStopGateAllowsNonEphemeralSessionWithUnacknowledgedDrain pins that the
// drain-ack half of the gate is scoped to ephemeral workers.
//
// Only an ephemeral session owes the controller a drain-ack, because only its
// exit releases an agent concurrency slot. A named holder never acknowledges
// a drain it was never sent, so gating on drainAcked alone -- without the
// origin check -- would block the mayor on every single turn boundary
// forever. That is the failure this test exists to catch.
func TestStopGateAllowsNonEphemeralSessionWithUnacknowledgedDrain(t *testing.T) {
	for _, origin := range []string{"named", "manual", ""} {
		facts := stopGateFacts{sessionID: "ci-mayor", sessionOrigin: origin}
		if evaluateStopGate(facts).block {
			t.Errorf("gate blocked a %q session over an unacknowledged drain", origin)
		}
	}
}

// TestStopGateAllowsEphemeralSessionThatAcknowledgedDrain pins the clean exit
// path: a worker that closed its bead and ran drain-ack is finished and stops
// without interference.
func TestStopGateAllowsEphemeralSessionThatAcknowledgedDrain(t *testing.T) {
	facts := stopGateFacts{
		sessionID:     "ci-l1ua",
		sessionOrigin: sessionOriginEphemeral,
		drainAcked:    true,
	}

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked a session that completed its closing contract")
	}
}

// TestStopGateAllowsSessionAwaitingRestart pins that gc runtime
// request-restart is a legitimate place to end a turn.
//
// A session that has asked the controller to restart it is waiting to be
// killed; its bead is deliberately still open and will be re-served to the
// replacement. Blocking it would prompt it to finish work it has already
// decided it lacks the context to finish.
func TestStopGateAllowsSessionAwaitingRestart(t *testing.T) {
	facts := stopGateHoldingWork()
	facts.restartRequested = true

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked a session waiting to be restarted by the controller")
	}
}

// TestStopGateAllowsWhenOutstandingWorkIsUnknown pins the fail-open rule for
// the one fact the gate cannot establish locally.
//
// unknownWork is deliberately a separate field from an empty outstanding
// list: "nothing outstanding" and "could not tell" must reach opposite
// verdicts. A gate that treats an unanswerable store query as a reason to
// block would wedge every session in the city simultaneously the moment the
// store went down -- strictly worse than the single stalled session it exists
// to fix.
func TestStopGateAllowsWhenOutstandingWorkIsUnknown(t *testing.T) {
	facts := stopGateFacts{
		sessionID:     "ci-l1ua",
		sessionOrigin: sessionOriginEphemeral,
		unknownWork:   true,
	}

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked on an unanswerable work query instead of failing open")
	}
}

// TestStopGateAllowsSessionWithoutCityIdentity pins that the gate ignores
// anything that is not a managed agent session.
//
// An operator running the provider by hand in the city directory picks up the
// same settings file and therefore the same Stop hook, but has no
// GC_SESSION_ID and no closing contract. Gating that session would block a
// human's own turns.
func TestStopGateAllowsSessionWithoutCityIdentity(t *testing.T) {
	facts := stopGateFacts{sessionOrigin: sessionOriginEphemeral, outstanding: []beads.Bead{{ID: "ci-yxpd", Status: "in_progress"}}}

	if evaluateStopGate(facts).block {
		t.Fatal("gate blocked a session with no city identity")
	}
}

// TestStopGateOutstandingReasonNamesDrainAckOnlyForEphemeral pins that the
// block text does not hand a named session a command that is not part of its
// contract.
func TestStopGateOutstandingReasonNamesDrainAckOnlyForEphemeral(t *testing.T) {
	ephemeral := stopGateOutstandingReason(stopGateHoldingWork())
	if !strings.Contains(ephemeral, "gc runtime drain-ack") {
		t.Errorf("ephemeral block reason omits the drain-ack step: %q", ephemeral)
	}

	named := stopGateHoldingWork()
	named.sessionOrigin = "named"
	if strings.Contains(stopGateOutstandingReason(named), "gc runtime drain-ack") {
		t.Error("named-session block reason tells it to drain-ack, which is not its contract")
	}
}

// TestReadStopHookActiveParsesProviderPayload pins the stdin contract the
// loop guard depends on.
//
// The absent-payload case asserts false rather than true deliberately: the
// provider always writes the payload on a pipe, so an absent payload means a
// manual invocation with no turn to loop. Reporting true there would disable
// the gate outright for any provider that changed its payload shape, and it
// would do so silently.
func TestReadStopHookActiveParsesProviderPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"active", `{"hook_event_name":"Stop","stop_hook_active":true}`, true},
		{"inactive", `{"hook_event_name":"Stop","stop_hook_active":false}`, false},
		{"absent field", `{"hook_event_name":"Stop"}`, false},
		{"empty payload", "", false},
		{"unparseable payload", "not json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readStopHookActive(bytes.NewBufferString(tc.payload)); got != tc.want {
				t.Errorf("readStopHookActive(%q) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

// TestCmdHookStopWritesReasonOnlyWhenBlocking pins the provider-facing
// contract: exit 2 with the reason on stderr blocks the stop and feeds that
// text back to the agent, and any other exit lets the turn end. A block that
// wrote nothing would stop the turn with no explanation, which reads to the
// agent as an unexplained refusal.
func TestCmdHookStopWritesReasonOnlyWhenBlocking(t *testing.T) {
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_SESSION_ORIGIN", "")

	var stderr bytes.Buffer
	code := cmdHookStop(bytes.NewBufferString(`{"stop_hook_active":false}`), &stderr)
	if code != stopGateAllowExitCode {
		t.Fatalf("exit code = %d, want %d for a session with no city identity", code, stopGateAllowExitCode)
	}
	if stderr.Len() != 0 {
		t.Errorf("allowing gate wrote to stderr: %q", stderr.String())
	}
}

// TestStopGateSessionSignalsFailOpenWhenSessionContextIsUnavailable pins that
// an unreadable session context reports the drain as ACKNOWLEDGED.
//
// The three ways this function can fail to establish drain state -- no session
// context, no session provider, an unreadable drain flag -- are the same fact
// and must reach the same verdict. Reporting "not acked" for any of them
// manufactures a block out of an infrastructure fault: an ephemeral session
// that has legitimately closed all its work would be told to run drain-ack
// because the gate could not read whether it already had. That fires on every
// ephemeral session in the city at once when the session store is down, which
// is the fleet-wide wedge this gate's fail-open rule exists to prevent.
//
// Asserted on the two EARLY failures specifically. The isDrainAcked failure
// below them already returned acked; these two returned the opposite, so a
// test written only against the third would have passed over the defect.
func TestStopGateSessionSignalsFailOpenWhenSessionContextIsUnavailable(t *testing.T) {
	t.Setenv("GC_SESSION_ID", "ci-l1ua")
	t.Setenv("GC_SESSION_NAME", "")
	t.Setenv("GC_TMUX_SESSION", "")

	var stderr bytes.Buffer
	restart, acked := readStopGateSessionSignals(&stderr)

	if restart {
		t.Error("unreadable session context reported a restart request")
	}
	if !acked {
		t.Fatal("unreadable session context reported the drain as unacknowledged; an ephemeral session holding no work would be blocked by an infrastructure fault")
	}
}

// TestCmdHookStopHonorsStopHookActiveWithoutTouchingTheStore pins that the
// loop guard short-circuits BEFORE any store or session-provider access.
//
// Ordering matters beyond cost: the re-entry path must stay answerable when
// the store is down, otherwise the escape hatch from a block depends on the
// same infrastructure whose failure produced the block. The environment here
// names a session whose city does not exist, so any store access would error
// and write to stderr.
func TestCmdHookStopHonorsStopHookActiveWithoutTouchingTheStore(t *testing.T) {
	t.Setenv("GC_SESSION_ID", "ci-l1ua")
	t.Setenv("GC_SESSION_NAME", "toolsmith-ci-l1ua")
	t.Setenv("GC_SESSION_ORIGIN", sessionOriginEphemeral)
	t.Setenv("GC_CITY", t.TempDir())
	t.Setenv("GC_CITY_PATH", t.TempDir())

	var stderr bytes.Buffer
	code := cmdHookStop(bytes.NewBufferString(`{"stop_hook_active":true}`), &stderr)
	if code != stopGateAllowExitCode {
		t.Fatalf("exit code = %d, want %d on a re-entered stop", code, stopGateAllowExitCode)
	}
	if stderr.Len() != 0 {
		t.Errorf("re-entry path reached the store: %q", stderr.String())
	}
}
