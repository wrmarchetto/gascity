// cmd/gc/idle_nudge_claimed_trigger_test.go
//
// Pins the two halves of isUnclaimedTrigger separately, so a sweep can tell
// which one declined.
//
// Why this suite exists apart from idle_nudge_test.go: that file's
// TestNudgeStalledPoolClaims_NeverTouchesWorkingSlot builds its trigger bead
// with status in_progress AND assignee equal to the session name, so either
// guard alone satisfies it. Deleting the status guard -- the single change
// ci-8ajvxn's root-cause investigation recommended -- leaves it green.
// Measured by mutation 2026-10-10 (verdict table in the ci-8ajvxn commit
// body).
//
// The assignee half is inert on a real fleet and that is the whole point of
// splitting them. runNudgeBackstop passes the session bead's `session_name`
// as sessName (nudge_backstop.go), which for a pool worker is a per-instance
// runtime name, while the bead it claims is assigned to the slot ALIAS. The
// two strings never match, so in production the status guard is the ONLY
// thing standing between the backstop and a slot that is working normally.
//
// Delegated elsewhere: grace, backoff, attempt capping and marker
// bookkeeping are idle_nudge_test.go's; this suite asserts only which
// trigger states resolve to "leave it alone".
//
//	go test ./cmd/gc/ -run TestNudgeStalledPoolClaims_Declines
package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
)

// idleClaimPastGraceSession returns the pool session of idleClaimPoolSession
// with the backstop marker already observed, which is the state a nudge would
// fire from on the next tick. Without it the engine spends the tick starting
// its grace clock and every case below passes for the wrong reason.
func idleClaimPastGraceSession(observedAt time.Time) beads.Bead {
	session := idleClaimPoolSession()
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = "1"
	session.Metadata[idleClaimNudgeAtKey] = observedAt.Format(time.RFC3339)
	return session
}

// A trigger bead someone has claimed is not the backstop's business, and the
// claim is proved by status alone. The assignee here is the pool slot alias
// rather than the session name precisely so the assignee guard cannot fire:
// this case dies only while the status guard stands.
func TestNudgeStalledPoolClaims_DeclinesTriggerClaimedUnderAPoolAlias(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPastGraceSession(base)
	work := []beads.Bead{{ID: "work-a", Status: "in_progress", Assignee: "pool-alias-a"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(time.Hour)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, idleClaimTestCfg(), store, []beads.Bead{session}, work, nil, clk.Now(), &out)

	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("Nudge calls = %d, want 0: an in_progress trigger is claimed work however it is addressed", got)
	}
	if got := mustGetTestBead(t, store, session.ID).Metadata[idleClaimNudgeTriggerKey]; got != "" {
		t.Fatalf("idle claim marker trigger = %q, want cleared", got)
	}
}

// The mirror case, and the one that keeps the test above from passing on an
// inert backstop: same session, same clock, same marker, trigger still open
// and addressed to nobody this slot answers to. A backstop that never fires
// fails here.
func TestNudgeStalledPoolClaims_DeclinesNothingWhileTheTriggerIsStillOpen(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPastGraceSession(base)
	work := []beads.Bead{{ID: "work-a", Status: "open", Assignee: "pool-alias-a"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(time.Hour)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, idleClaimTestCfg(), store, []beads.Bead{session}, work, nil, clk.Now(), &out)

	if got := sp.CountCalls("Nudge", "session-a"); got != 1 {
		t.Fatalf("Nudge calls = %d, want 1: an open unclaimed trigger past grace is exactly what the backstop is for", got)
	}
}

// The second half of isUnclaimedTrigger: an OPEN bead already carrying this
// session's own name is a claim mid-flight, not a stall. Status cannot
// decline it, so this case dies only while the assignee guard stands.
func TestNudgeStalledPoolClaims_DeclinesOpenTriggerAlreadyAddressedToThisSession(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPastGraceSession(base)
	work := []beads.Bead{{ID: "work-a", Status: "open", Assignee: "session-a"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(time.Hour)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, idleClaimTestCfg(), store, []beads.Bead{session}, work, nil, clk.Now(), &out)

	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("Nudge calls = %d, want 0: the slot is mid-claim on its own trigger", got)
	}
	if got := mustGetTestBead(t, store, session.ID).Metadata[idleClaimNudgeTriggerKey]; got != "" {
		t.Fatalf("idle claim marker trigger = %q, want cleared", got)
	}
}
