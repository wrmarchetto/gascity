package main

import (
	"bytes"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the assignee half of the session-ending work-release sweep -- which of
// a closing session's assignee identities the sweep may take back, and which it
// must leave in place. Status handling (in_progress -> open) and the route
// fallback are pinned elsewhere: session_beads_handoff_orphan_test.go owns the
// run_target restore, session_beads_close_release_guard_test.go owns the
// cached-enumeration CAS guard, session_beads_mail_release_test.go owns the mail
// exclusion. This suite delegates all of those and asserts only ownership.
//
// Why it exists (ci-8vx85v): `gc session close` stripped the assignee from
// every bead addressed to the closing session's POOL-SLOT ALIAS, not only from
// the claims it held. Measured 2026-09-04 -- closing one session stripped five
// beads, a supervisor restart stripped nine across two rig stores. The stripped
// beads stayed open, lost their route, reached no pool door, and needed a manual
// re-assign sweep.
//
// Run: go test ./cmd/gc/ -run 'SessionClose.*Addressed|SessionEndingRelease'

// closeSweepSeat is the identity vocabulary of one closing session, split the
// way the sweep must split it. The alias is a pool INSTANCE name: the next
// session in that slot bears it again, which is exactly why an address on it
// outlives the session that happened to be sitting there.
const (
	closeSweepAlias       = "worker-2"
	closeSweepSessionName = "worker-cs-1"
	closeSweepNamed       = "reviewer"
)

func newCloseSweepSessionBead(t *testing.T, store beads.Store) beads.Bead {
	t.Helper()
	sessionBead, err := store.Create(beads.Bead{
		Title:  "pool worker",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name":              closeSweepSessionName,
			"alias":                     closeSweepAlias,
			"configured_named_identity": closeSweepNamed,
			"template":                  "worker",
			"state":                     "active",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	return sessionBead
}

// mkCloseSweepWork creates one work bead in the given status. Beads are created
// open and promoted, because the store stamps status on create independently.
func mkCloseSweepWork(t *testing.T, store beads.Store, title, assignee, status string) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:    title,
		Type:     "task",
		Status:   "open",
		Assignee: assignee,
	})
	if err != nil {
		t.Fatalf("create work bead %q: %v", title, err)
	}
	if status != "open" {
		if err := store.Update(b.ID, beads.UpdateOpts{Status: &status}); err != nil {
			t.Fatalf("set %q status %q: %v", title, status, err)
		}
	}
	return b
}

// assertCloseSweepWork checks one bead's post-sweep state. Status is not a
// parameter because "open" is the universal post-condition: a session that has
// ended must leave nothing in_progress under it, whatever the sweep decided
// about the address. wantAssignee is where the cases differ.
func assertCloseSweepWork(t *testing.T, store beads.Store, id, wantAssignee, why string) {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if got.Assignee != wantAssignee {
		t.Errorf("%s: Assignee = %q, want %q (%s)", id, got.Assignee, wantAssignee, why)
	}
	if got.Status != "open" {
		t.Errorf("%s: Status = %q, want open -- a session that ended left work in_progress under it (%s)", id, got.Status, why)
	}
}

// TestSessionEndingReleaseKeepsWorkAddressedToTheSeat drives the reconciler's
// closed-session sweep over one session bead carrying all four identity forms,
// with both a durable-identity address and an ephemeral-identity address in both
// statuses. It is the both-directions case the bead demands: flipping any single
// expectation below must fail on the fixed source, and the two "cleared" rows
// must fail on a build that stops releasing anything.
func TestSessionEndingReleaseKeepsWorkAddressedToTheSeat(t *testing.T) {
	store := beads.NewMemStore()
	sessionBead := newCloseSweepSessionBead(t, store)

	// Addressed to the seat, never claimed: the routing decision of whoever
	// filed it, and not the closing session's to discard.
	aliasOpen := mkCloseSweepWork(t, store, "addressed to the slot alias", closeSweepAlias, "open")
	namedOpen := mkCloseSweepWork(t, store, "addressed to the named identity", closeSweepNamed, "open")
	// Genuinely claimed by this session through its seat. The assignee on an
	// in_progress bead is the claim's own artifact, not the filer's address, so
	// releasing it in full is what puts the work back in front of every slot.
	aliasClaim := mkCloseSweepWork(t, store, "claimed on the slot alias", closeSweepAlias, "in_progress")
	// Addressed to identities that die with the session: no future session ever
	// bears them, so these must be fully released or they strand.
	idOpen := mkCloseSweepWork(t, store, "addressed to the session bead", sessionBead.ID, "open")
	nameClaim := mkCloseSweepWork(t, store, "claimed on the runtime name", closeSweepSessionName, "in_progress")

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	assertCloseSweepWork(t, store, aliasOpen.ID, closeSweepAlias,
		"a bead addressed to a pool slot is addressed to the slot, not to whichever session was sitting in it")
	assertCloseSweepWork(t, store, namedOpen.ID, closeSweepNamed,
		"a configured named identity is re-borne by the next session configured under it")
	assertCloseSweepWork(t, store, aliasClaim.ID, "",
		"a claim releases in full: the hook admits routed work only while the assignee is empty, so keeping the alias would pin pool work to one slot")
	assertCloseSweepWork(t, store, idOpen.ID, "",
		"no future session bears this session bead's ID, so the address would strand the work")
	assertCloseSweepWork(t, store, nameClaim.ID, "",
		"a runtime session_name dies with the session, so its claim releases completely")
}

// TestSessionEndingReleaseStripsEverySeatIdentityWhenTheSeatIsRetired pins the
// carve-out: when the SEAT itself is going away -- a [[named_session]] deleted
// from config -- the durable identities are durable no longer, and work
// addressed to them must be released or nothing will ever bear the address
// again. Without this the fix above would trade the measured strip for a silent
// strand on the config-removal path.
func TestSessionEndingReleaseStripsEverySeatIdentityWhenTheSeatIsRetired(t *testing.T) {
	store := beads.NewMemStore()
	sessionBead := newCloseSweepSessionBead(t, store)

	aliasOpen := mkCloseSweepWork(t, store, "addressed to the slot alias", closeSweepAlias, "open")
	namedOpen := mkCloseSweepWork(t, store, "addressed to the named identity", closeSweepNamed, "open")

	var stderr bytes.Buffer
	unclaimWorkAssignedToRetiredSessionBead(store, nil, sessionBead, "", seatRetired, &stderr)

	assertCloseSweepWork(t, store, aliasOpen.ID, "",
		"the seat is deleted from config, so its alias is borne by nobody ever again")
	assertCloseSweepWork(t, store, namedOpen.ID, "",
		"the named identity is deleted from config, so an address on it reaches nobody")
}

// TestCmdSessionCloseKeepsWorkAddressedToTheSeat is the same invariant checked
// in the medium the defect was reported in: the `gc session close` CLI, end to
// end, through its own store open/close cycle. The unit case above cannot catch
// a regression that reintroduces the strip in cmdSessionClose's own call.
func TestCmdSessionCloseKeepsWorkAddressedToTheSeat(t *testing.T) {
	cityDir := t.TempDir()
	writePhase0InterfaceCity(t, cityDir, `[workspace]
name = "test-city"

[beads]
provider = "file"

[[agent]]
name = "worker"
start_command = "true"
max_active_sessions = 3
`)
	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_DIR", t.TempDir())
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	sessionBead := newCloseSweepSessionBead(t, store)

	aliasOpen := mkCloseSweepWork(t, store, "addressed to the slot alias", closeSweepAlias, "open")
	aliasClaim := mkCloseSweepWork(t, store, "claimed on the slot alias", closeSweepAlias, "in_progress")
	idClaim := mkCloseSweepWork(t, store, "claimed on the session bead", sessionBead.ID, "in_progress")

	var stdout, stderr bytes.Buffer
	if code := cmdSessionClose([]string{sessionBead.ID}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionClose = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	reopened, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("reopen city store: %v", err)
	}
	assertCloseSweepWork(t, reopened, aliasOpen.ID, closeSweepAlias,
		"gc session close must not discard a routing decision it did not make")
	assertCloseSweepWork(t, reopened, aliasClaim.ID, "",
		"a claim returns to the queue unassigned so any slot can take it")
	assertCloseSweepWork(t, reopened, idClaim.ID, "",
		"work claimed under the session's own ID still returns to the queue unassigned")

	// The other half of releasing that claim: it must land somewhere a worker
	// can find it. idClaim carries no route of its own, so the close has to
	// stamp the closing session's own template as the run_target fallback --
	// restoreCarriedWorkRoutes backfills gc.routed_to from it. This CLI door
	// passed no fallback at all until ci-8vx85v, which is what turned a released
	// claim into work that was open, unassigned AND unrouted.
	gotIDClaim, err := reopened.Get(idClaim.ID)
	if err != nil {
		t.Fatalf("get released claim: %v", err)
	}
	if got := gotIDClaim.Metadata["gc.run_target"]; got != "worker" {
		t.Errorf("released claim gc.run_target = %q, want %q; an unrouted release reaches no pool door", got, "worker")
	}
}

// TestSeatIdentitySplitCoversEveryAssigneeIdentity pins the property that makes
// the split safe to extend: it is a filter, so the two halves reassemble into
// exactly the identity list the rest of cmd/gc sweeps. A future identity added
// to sessionBeadAssigneeIdentities is then swept by one half or the other -- the
// failure this guards is a rewrite that re-reads the metadata instead, where a
// new identity would belong to neither half and be swept by nothing, silently.
//
// The fixture carries every identity form at once, with values chosen so a
// misfiled one is visible by name rather than by position.
func TestSeatIdentitySplitCoversEveryAssigneeIdentity(t *testing.T) {
	sessionBead := beads.Bead{
		ID: "gc-seat",
		Metadata: map[string]string{
			"session_name":              closeSweepSessionName,
			"alias":                     closeSweepAlias,
			"configured_named_identity": closeSweepNamed,
			"alias_history":             "worker-9",
		},
	}

	all := compactSessionAssignmentIdentifiers(sessionBeadAssigneeIdentities(sessionBead))
	ephemeral, durable := sessionSeatIdentityScope(sessionBead)

	if len(all) != len(ephemeral)+len(durable) {
		t.Fatalf("split lost or duplicated identities: all=%v ephemeral=%v durable=%v", all, ephemeral, durable)
	}
	seen := map[string]string{}
	for _, id := range ephemeral {
		seen[id] = "ephemeral"
	}
	for _, id := range durable {
		if half, dup := seen[id]; dup {
			t.Fatalf("identity %q classified twice (%s and durable)", id, half)
		}
		seen[id] = "durable"
	}
	for _, id := range all {
		if _, ok := seen[id]; !ok {
			t.Errorf("identity %q is swept by neither half", id)
		}
	}

	want := map[string]string{
		"gc-seat":             "ephemeral",
		closeSweepSessionName: "ephemeral",
		closeSweepAlias:       "durable",
		closeSweepNamed:       "durable",
		"worker-9":            "durable",
	}
	for id, half := range want {
		if got := seen[id]; got != half {
			t.Errorf("identity %q classified %q, want %q", id, got, half)
		}
	}
}

// TestOrphanSweepStillReleasesRoutedSeatAddressWhenNoSessionBearsIt records the
// BOUNDARY of the session-close fix rather than extending it, so the next reader
// is not misled into thinking a seat address is now inviolable everywhere.
//
// releaseOrphanedPoolAssignments runs on every reconcile tick and answers a
// different question: not "may this closing session take the address back" but
// "is any live session bearing this address at all". A ROUTED open bead on a
// slot alias whose slot is currently cold is still released there -- it keeps
// gc.routed_to, so it degrades from one-slot to any-slot rather than stranding,
// and the release fires bead.dead_assignee_reopened where the close path fired
// nothing. That is why the measured ci-8vx85v beads were the UNROUTED ones: the
// sweep's own `template == ""` guard skips those, so the close path was the only
// door that could silently strip them.
//
// If this ever needs to change, it is a separate decision about the orphan sweep
// with its own scale-down consequences, and this test is the place it announces
// itself.
func TestOrphanSweepStillReleasesRoutedSeatAddressWhenNoSessionBearsIt(t *testing.T) {
	store := beads.NewMemStore()
	poolCfg := &config.City{Agents: []config.Agent{{
		Name:              "worker",
		MinActiveSessions: intPtr(0),
		MaxActiveSessions: intPtr(3),
	}}}

	routed, err := store.Create(beads.Bead{
		Title:    "routed and addressed to a cold slot",
		Type:     "task",
		Status:   "open",
		Assignee: closeSweepAlias,
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create routed work: %v", err)
	}
	unrouted, err := store.Create(beads.Bead{
		Title:    "addressed to a cold slot, no route",
		Type:     "task",
		Status:   "open",
		Assignee: closeSweepAlias,
	})
	if err != nil {
		t.Fatalf("create unrouted work: %v", err)
	}

	// No open sessions: the slot is cold, so nothing bears the alias.
	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, poolCfg, "", nil,
		[]beads.Bead{routed, unrouted}, nil, nil, nil,
	)

	var releasedIDs []string
	for _, r := range released {
		releasedIDs = append(releasedIDs, r.ID)
	}
	if len(releasedIDs) != 1 || releasedIDs[0] != routed.ID {
		t.Fatalf("released = %v, want exactly [%s]; the unrouted bead is the one the close path must protect, because the orphan sweep cannot see it", releasedIDs, routed.ID)
	}
	assertCloseSweepWork(t, store, unrouted.ID, closeSweepAlias,
		"unrouted seat-addressed work is invisible to the orphan sweep, so session close is its only exposure")
}
