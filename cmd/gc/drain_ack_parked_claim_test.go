// Package main test: what the drain-ack refusal counts as held work, including
// a claim parked on an open, assigned PM question.
//
// Scope: reconcileSessionBeads over a store, asserting the state_reason the
// refusal writes. Five cases, one per way a claim can relate to a question.
// All five assert the SAME outcome -- the acknowledgement is refused -- which
// is the point: parking does not change the answer, and this file exists to
// keep it that way.
//
// WHY THIS SUITE EXISTS, AND WHAT IT PINS AGAINST. The Stop gate DOES have a
// parked exception (cmd_hook_stop.go evaluateStopGate, case
// parkedOnOpenAssignedQuestion) and this refusal does not, so the pair looks
// asymmetric, and the obvious repair -- give the refusal the same exception --
// was proposed, built and REJECTED under ci-gfc8rk. Two commit bodies already
// decided it and a third measurement disproved the premise:
//
//   - f75e6c710 (ci-eqtxc0) put this exact fork on the record. "Two options
//     were on the table: widen the gate to count pinned open work, or make an
//     agent-originated ack always release the slot [...] This takes the
//     first." It loosened the STOP GATE to match the refusal and declined to
//     loosen the refusal.
//   - 52b502316 (ci-fx4duc) states the invariant the exception would break:
//     in_progress work is NEVER excluded, because
//     preassignHookContinuationGroup hands a session its continuation siblings
//     and releasing the slot hands them to the next occupant. A parked claim is
//     in_progress by construction, so a parked member of a continuation group
//     would drain the slot out from under its siblings.
//   - The premise did not survive measurement. Of the three wedged sessions on
//     2026-09-11, gs-saj5 carried no gc.waiting_on_question and no dependency
//     at all, and gs-ian6's question gs-u302 was closed at 05:18:54Z while
//     ci-k8iv0i held it from 07:52 to 11:15. The exception would have fired on
//     neither. Parking is not what those sessions had in common.
//
// So this is not a suite that would go green over the bug. The wedge it was
// written during lives elsewhere and is tracked elsewhere: the Stop gate
// blocks at most once per stop sequence (the stopHookActive early allow), a
// refused acknowledgement is invisible to the agent that issued it, and
// drain-ack-assigned-work has no exit transition -- ci-07ebae. The codex
// nudge-delivery defect that made the wedge unrecoverable rather than merely
// wasteful is ci-gqvu9q.
//
// If the exception is ever wanted, the case to change is
// ...WithParkedClaimStaysActive below, and changing it should cost an argument
// against the two commits above.
//
// Delegated elsewhere: the Stop gate's parked exception is pinned by the
// parked cases in cmd_hook_stop_test.go, and the ci-fx4duc queue-work
// exclusion by the pool-alias cases in session_reconciler_test.go.
//
// Run:
//
//	go test ./cmd/gc/ -run 'AgentDrainAckWithParked|AgentDrainAckWithUnparked'
package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// listWithoutDependenciesStore drops dependencies from every list row while
// leaving Get whole.
//
// It is not a convenience: it is the production shape. BdStore's
// listIncludesCompleteDependencies() returns false, so `bd list --json` rows
// carry no dependencies in the running city, while MemStore's cloneBead copies
// them. Any future parked check sourced from a list row would therefore be
// vacuously false in production while passing over a bare MemStore -- green
// suite, inert code. The wrapper makes this fixture unable to hide that.
type listWithoutDependenciesStore struct {
	beads.Store
}

func (s listWithoutDependenciesStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	items, err := s.Store.List(query)
	if err != nil {
		return nil, err
	}
	return withoutDependencies(items), nil
}

func (s listWithoutDependenciesStore) ListOpen(status ...string) ([]beads.Bead, error) {
	items, err := s.Store.ListOpen(status...)
	if err != nil {
		return nil, err
	}
	return withoutDependencies(items), nil
}

func withoutDependencies(items []beads.Bead) []beads.Bead {
	stripped := make([]beads.Bead, 0, len(items))
	for _, item := range items {
		item.Dependencies = nil
		stripped = append(stripped, item)
	}
	return stripped
}

// parkedClaimFixture builds the measured shape: a pool-managed ephemeral
// session holding one in-progress claim, plus a question bead it may or may
// not be parked on.
//
// questionStatus and questionAssignee are the question-side knobs; stamped
// decides whether the claim carries gc.waiting_on_question, and linked whether
// the ready-blocking dependency is present. Four independent knobs rather than
// a single "parked" boolean, so a future exception cannot be satisfied by a
// fixture that only ever varies one of them together.
type parkedClaimFixture struct {
	env     *reconcilerTestEnv
	session beads.Bead
	claimID string
}

func newParkedClaimFixture(t *testing.T, questionStatus, questionAssignee string, stamped, linked bool) parkedClaimFixture {
	t.Helper()
	env := newReconcilerTestEnv()
	env.store = listWithoutDependenciesStore{Store: env.store}
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"alias":          "worker",
		"pool_managed":   "true",
		"session_origin": "ephemeral",
	})

	question, err := env.store.Create(beads.Bead{
		Title:    "Choose the measurement instrument surface",
		Type:     "task",
		Status:   "open",
		Assignee: questionAssignee,
	})
	if err != nil {
		t.Fatalf("Create(question): %v", err)
	}
	if questionStatus == "closed" {
		if err := env.store.Close(question.ID); err != nil {
			t.Fatalf("Close(question): %v", err)
		}
	}

	claim := beads.Bead{
		Title:    "prefix hygiene: measure cache reuse",
		Type:     "task",
		Assignee: "worker",
	}
	if stamped {
		claim.Metadata = beads.StringMap{beadmeta.WaitingOnQuestionMetadataKey: question.ID}
	}
	if linked {
		claim.Dependencies = []beads.Dep{{DependsOnID: question.ID, Type: "blocks"}}
	}
	created, err := env.store.Create(claim)
	if err != nil {
		t.Fatalf("Create(claim): %v", err)
	}
	// MemStore.Create forces status "open" and ignores the field, so the claim
	// is promoted afterwards. Leaving it open would be a silently different
	// fixture: an open bead assigned to a pool slot alias is queue work the
	// ci-fx4duc exclusion already drops, so every case here would pass for a
	// reason that has nothing to do with what it claims to test.
	inProgress := "in_progress"
	if err := env.store.Update(created.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("Update(claim status): %v", err)
	}
	return parkedClaimFixture{env: env, session: session, claimID: created.ID}
}

// reconcileOnce runs one tick with the acknowledgement already set and returns
// the session bead afterwards.
func (f parkedClaimFixture) reconcileOnce(t *testing.T) beads.Bead {
	t.Helper()
	dops := newFakeDrainOps()
	if err := dops.setDrainAck("worker"); err != nil {
		t.Fatalf("setDrainAck: %v", err)
	}
	reconcileSessionBeads(
		context.Background(),
		[]beads.Bead{f.session},
		f.env.desiredState,
		map[string]bool{"worker": true},
		f.env.cfg,
		f.env.sp,
		f.env.store,
		dops,
		nil,
		nil,
		f.env.dt,
		nil,
		false,
		nil,
		"",
		nil,
		f.env.clk,
		f.env.rec,
		0,
		0,
		&f.env.stdout,
		&f.env.stderr,
	)
	got, err := f.env.store.Get(f.session.ID)
	if err != nil {
		t.Fatalf("Get session after drain-ack: %v", err)
	}
	return got
}

func (f parkedClaimFixture) requireRefused(t *testing.T, why string) {
	t.Helper()
	got := f.reconcileOnce(t)
	if reason := got.Metadata["state_reason"]; reason != sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q, want %q: %s", reason, sessionpkg.DrainAckAssignedWorkReason, why)
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithParkedClaimStaysActive is the
// case the rejected exception would have flipped, and the only one here that
// is a decision rather than a consequence: a claim parked on an OPEN, ASSIGNED
// question is still held work, so the acknowledgement is still refused.
//
// Read the file comment before changing this. The Stop gate reaching the
// opposite verdict on the same bead is deliberate and is not, on its own, an
// argument -- f75e6c710 examined exactly that asymmetry and chose to keep it.
func TestReconcileSessionBeads_AgentDrainAckWithParkedClaimStaysActive(t *testing.T) {
	newParkedClaimFixture(t, "open", "mayor", true, true).
		requireRefused(t, "a parked claim is in_progress work the session still owns; releasing the slot would hand a continuation group's siblings to the next occupant (52b502316)")
}

// TestReconcileSessionBeads_AgentDrainAckWithParkedClaimOnClosedQuestionStaysActive
// covers the shape two of the three measured wedges actually had: the stamp
// and the link are both present and the question has been ANSWERED.
//
// It is here because it is the case the exception's advocates keep assuming
// away. gs-u302 closed at 05:18:54Z and ci-k8iv0i held gs-ian6 from 07:52 to
// 11:15, so for that entire wedge the claim looked parked to a metadata reader
// and was not parked at all.
func TestReconcileSessionBeads_AgentDrainAckWithParkedClaimOnClosedQuestionStaysActive(t *testing.T) {
	newParkedClaimFixture(t, "closed", "mayor", true, true).
		requireRefused(t, "the question is answered, so the holder owes work again rather than a wait")
}

// TestReconcileSessionBeads_AgentDrainAckWithParkedClaimOnUnassignedQuestionStaysActive
// covers the question-side condition with no symptom of its own. ask-pm.py
// addresses every question to `<rig>/lab.pm` because that string is what
// raises pool demand; an unassigned question spawns nobody, so nothing will
// ever close it and a wait on one would be permanent.
func TestReconcileSessionBeads_AgentDrainAckWithParkedClaimOnUnassignedQuestionStaysActive(t *testing.T) {
	newParkedClaimFixture(t, "open", "", true, true).
		requireRefused(t, "an unassigned question can never be answered, so a wait on one is not a designed park")
}

// TestReconcileSessionBeads_AgentDrainAckWithParkedStampButNoDependencyStaysActive
// covers the stamp outliving the link. Metadata survives the question being
// unlinked or replaced, so gc.waiting_on_question alone never establishes that
// a wait is still wired up -- the hole ci-iyb0gc named in the mutation it
// required of the Stop gate's own exception.
func TestReconcileSessionBeads_AgentDrainAckWithParkedStampButNoDependencyStaysActive(t *testing.T) {
	newParkedClaimFixture(t, "open", "mayor", true, false).
		requireRefused(t, "a stamp with no live blocking dependency is not a park")
}

// TestReconcileSessionBeads_AgentDrainAckWithUnparkedClaimStaysActive is the
// control, and it is the shape gs-saj5 had: an ordinary in-progress claim with
// no parking metadata anywhere. Its session wedged for 86 minutes exactly like
// the parked ones, which is the measurement that disqualified parking as the
// mechanism.
func TestReconcileSessionBeads_AgentDrainAckWithUnparkedClaimStaysActive(t *testing.T) {
	newParkedClaimFixture(t, "open", "mayor", false, true).
		requireRefused(t, "an unparked claim is work the session still owns and must keep refusing -- this is the hole the refusal exists for")
}
