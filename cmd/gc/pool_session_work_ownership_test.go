package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// Scope: the ownership question "does this live session already hold work",
// which decides whether the pool planner may hand the session to somebody
// else. It covers the predicate itself and the one production caller that
// consumes it, reusablePoolSessionInfo. It delegates the claim side to
// cmd_hook_test.go and the slot allocator to
// pool_wake_slot_rehome_test.go.
//
// Why the suite exists: a work bead is assigned under the identity
// session.AssigneeIdentifier picks, which is ALIAS FIRST -- for a pool slot
// that is "toolsmith-2", the name `gc hook --claim` writes. Two predicates
// answer this question and until ci-me7as9 they disagreed:
// poolRequestResumesAssignedWorkInfo iterated the full identity set, while
// sessionBeadHasAssignedWorkInfo compared three fields by hand and the alias
// was not among them. A live, awake slot holding its own in-progress bead
// therefore read as free for reuse.
//
// The whole suite is derived from session.AssigneeIdentities rather than from
// a list of shapes written out here, because that is the set the production
// identity resolution actually uses. A hand-kept corpus rots at the next
// identity form added -- alias_history was added once already, and it is the
// form that would rot first, since a slot acquires a prior alias only by
// being renamed.
//
// Run: go test ./cmd/gc/ -count=1 -run 'TestOwnership|TestLiveSlot'
func poolSessionInfoWithAliases(t *testing.T, id, alias string, priorAliases string) sessionpkg.Info {
	t.Helper()
	metadata := map[string]string{
		"template":     "rig/worker",
		"session_name": "gc-" + id,
		"pool_managed": "true",
		"pool_slot":    "2",
		"alias":        alias,
		"state":        "awake",
	}
	if priorAliases != "" {
		metadata["alias_history"] = priorAliases
	}
	return sessiontest.SeedBead(t, beads.Bead{
		ID:       id,
		Type:     sessionpkg.BeadType,
		Status:   "open",
		Labels:   []string{sessionpkg.LabelSession},
		Metadata: metadata,
	})
}

// TestOwnershipPredicateCoversEveryAssigneeIdentity pins that a session is
// found to hold work assigned under ANY identity it answers to.
//
// Both sides come from session.AssigneeIdentities: the loop below asks the
// predicate about a bead assigned to each identity in turn, so a form added
// there is covered here the day it is added and no allowlist has to be
// remembered. Asserting a fixed list of three or four spellings instead would
// be the same mistake the predicate itself made.
//
// The unmatched case is not decoration. Without it a predicate that simply
// returned true would satisfy every other assertion in this test.
func TestOwnershipPredicateCoversEveryAssigneeIdentity(t *testing.T) {
	info := poolSessionInfoWithAliases(t, "ga-slot2", "rig/worker-2", "rig/worker-5")

	identities := sessionpkg.AssigneeIdentities(info)
	if len(identities) < 4 {
		t.Fatalf("AssigneeIdentities = %v, want at least id, session_name, alias and a prior alias -- the fixture no longer exercises the set it is derived from", identities)
	}
	for _, identity := range identities {
		work := []beads.Bead{{ID: "wb", Status: "in_progress", Assignee: identity}}
		if !sessionBeadHasAssignedWorkInfo(work, info) {
			t.Errorf("sessionBeadHasAssignedWorkInfo(assignee=%q) = false, want true -- %q is an identity this session answers to, so the work is already its own", identity, identity)
		}
	}

	stranger := []beads.Bead{{ID: "wb", Status: "in_progress", Assignee: "rig/worker-7"}}
	if sessionBeadHasAssignedWorkInfo(stranger, info) {
		t.Error("sessionBeadHasAssignedWorkInfo(assignee=\"rig/worker-7\") = true, want false -- a sibling slot's bead is not this session's work")
	}
	if sessionBeadHasAssignedWorkInfo(nil, info) {
		t.Error("sessionBeadHasAssignedWorkInfo(nil) = true, want false -- ownership is a property of the work set, not of the session alone")
	}
}

// TestOwnershipPredicatesAgreeOnTheSameBead pins that the two predicates
// answering "is this the session's own work" cannot disagree.
//
// They are separately reachable and separately maintained:
// poolRequestResumesAssignedWorkInfo guards the resume tier's preferred
// session, sessionBeadHasAssignedWorkInfo guards reuse. A divergence is
// invisible from either end -- each is green in its own tests -- and its
// consequence is that the SAME bead proves a session must be preserved and
// proves it is free to hand away.
func TestOwnershipPredicatesAgreeOnTheSameBead(t *testing.T) {
	info := poolSessionInfoWithAliases(t, "ga-slot2", "rig/worker-2", "rig/worker-5")
	for _, identity := range sessionpkg.AssigneeIdentities(info) {
		work := []beads.Bead{{ID: "wb", Status: "in_progress", Assignee: identity}}
		request := SessionRequest{Tier: "resume", SessionBeadID: info.ID, WorkBeadID: "wb"}
		resumes := poolRequestResumesAssignedWorkInfo(request, work, info)
		holds := sessionBeadHasAssignedWorkInfo(work, info)
		if resumes != holds {
			t.Errorf("assignee %q: poolRequestResumesAssignedWorkInfo = %v but sessionBeadHasAssignedWorkInfo = %v -- one bead cannot both justify preserving this session and leave it free to hand away", identity, resumes, holds)
		}
	}
}

// TestLiveSlotHoldingItsOwnBeadIsNotReusable pins the seam that actually
// failed: the planner must not offer a running slot's session to another
// request while that slot's own in-progress bead is outstanding.
//
// Driven through reusablePoolSessionInfo rather than the predicate alone
// because the predicate is only one of eight exclusions there, and a fix that
// satisfied the predicate while the reuse path read something else would pass
// the unit test above and change nothing. The session is deliberately open
// and awake -- the measured population is live owners, not crashed ones, so a
// fixture with a closed session bead would be excluded by an earlier arm and
// prove nothing about this one.
//
// The second case is the over-correction guard: an idle slot with no bead of
// its own must stay reusable, or the pool stops recycling sessions and grows
// a fresh one for every request.
func TestLiveSlotHoldingItsOwnBeadIsNotReusable(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{poolAgent("worker", "rig", intPtr(3), 0)}}
	held := poolSessionInfoWithAliases(t, "ga-slot2", "rig/worker-2", "")
	idle := poolSessionInfoWithAliases(t, "ga-slot3", "rig/worker-3", "")

	bp := &agentBuildParams{
		city:         cfg,
		agents:       cfg.Agents,
		sessionBeads: newSessionBeadSnapshotFromInfos([]sessionpkg.Info{held, idle}),
		assignedWorkBeads: []beads.Bead{
			{ID: "wb-own", Status: "in_progress", Assignee: "rig/worker-2"},
		},
	}

	if reusablePoolSessionInfo(bp, &cfg.Agents[0], "rig/worker", held, map[string]bool{}) {
		t.Error("a live slot holding its own in-progress bead read as reusable; another request takes it, and the request carrying that bead falls through to a fresh create on a slot the bead is not addressed to")
	}
	if !reusablePoolSessionInfo(bp, &cfg.Agents[0], "rig/worker", idle, map[string]bool{}) {
		t.Error("an idle slot with no bead of its own read as unreusable; the pool would mint a new session for every request instead of recycling")
	}
}
