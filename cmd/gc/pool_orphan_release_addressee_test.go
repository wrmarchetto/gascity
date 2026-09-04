package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Scope: what releaseOrphanedPoolAssignments may take back from a bead whose
// assignee names a POOL rather than a session. Everything else about the sweep
// -- the CAS/recheck write, the detached probe, the store-ref fan-out, workflow
// roots -- is pinned in pool_session_name_test.go and delegated here.
//
// Why it exists (ci-vcornx). A bead hand-assigned to a pool's bare name, i.e.
// cold-pool demand, was released on EVERY reconcile tick whenever its
// gc.routed_to named some OTHER agent. Observed in the supervisor's own log:
//
//	as-7uha assignee=astoria-sel4/lab.engineer routed=bench-operator-codex status=open
//	released orphaned pool work: as-7uha
//
// It was re-assigned by hand three times and cleared three times, roughly every
// 100 seconds, and the rig simply stopped: nothing goes red, because the release
// happens on the tick AFTER each doctor check passes. Unsetting the route fixed
// it instantly -- the assignee stuck and the pool went from 0 sizings to 9.
//
// The bead reported this as a CROSS-STORE defect. It is not: reproduced here
// with two plain city agents in one store, so the store boundary is incidental
// and only the assignee-versus-route comparison is load-bearing. Recorded
// because the title says otherwise.
//
// Run: go test ./cmd/gc/ -run 'OrphanReleaseColdPool|OrphanReleaseStillReleases'

// TestOrphanReleaseColdPoolDemandSurvivesAnyRoute is the primary regression. The
// three rows differ only in gc.routed_to, so a build that keeps the address for
// one route and drops it for another cannot pass.
func TestOrphanReleaseColdPoolDemandSurvivesAnyRoute(t *testing.T) {
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
		{Name: "other-agent", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
	}}

	mk := func(title string, metadata map[string]string) beads.Bead {
		t.Helper()
		b, err := store.Create(beads.Bead{
			Title:    title,
			Type:     "task",
			Status:   "open",
			Assignee: "worker",
			Metadata: metadata,
		})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return b
	}

	ownRoute := mk("routed to the pool it is addressed to", map[string]string{"gc.routed_to": "worker"})
	// The measured shape: addressed to one pool, routed to a different agent
	// that has no session. Before the fix this row -- and only this row -- lost
	// its assignee on every tick.
	foreignRoute := mk("routed to another agent entirely", map[string]string{"gc.routed_to": "other-agent"})
	noRoute := mk("no route at all", nil)

	// No open sessions anywhere: the pool is cold, which is the state cold-pool
	// demand exists to end.
	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, "", nil,
		[]beads.Bead{ownRoute, foreignRoute, noRoute}, nil, nil, nil,
	)
	if len(released) != 0 {
		t.Errorf("released = %v, want none; a bare pool name in the assignee is an address, not a dead session's claim", released)
	}
	for _, b := range []beads.Bead{ownRoute, foreignRoute, noRoute} {
		got, err := store.Get(b.ID)
		if err != nil {
			t.Fatalf("get %s: %v", b.ID, err)
		}
		if got.Assignee != "worker" {
			t.Errorf("%q: Assignee = %q, want %q -- deleting it discards the operator's recorded addressee, which is the cost pool_alias_demand.go rejects by name", b.Title, got.Assignee, "worker")
		}
		if got.Status != "open" {
			t.Errorf("%q: Status = %q, want open", b.Title, got.Status)
		}
	}
}

// TestOrphanReleaseStillReleasesDeadSessionAssignments is the other direction
// the bead demands, and the reason the fix keys on "names a configured agent"
// rather than on "has a route". A slot alias and a runtime session name are
// concrete session assignments: when no live session bears one, the work is
// genuinely abandoned and must return to the queue, or the fix trades a silent
// strip for a silent stall.
func TestOrphanReleaseStillReleasesDeadSessionAssignments(t *testing.T) {
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
	}}

	mk := func(title, assignee string) beads.Bead {
		t.Helper()
		b, err := store.Create(beads.Bead{
			Title:    title,
			Type:     "task",
			Status:   "open",
			Assignee: assignee,
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return b
	}

	// "worker-2" is a pool SLOT, not the pool: no configured agent bears that
	// name, so nothing re-addresses it if the slot never returns.
	slot := mk("assigned to a slot whose session is gone", "worker-2")
	// A name matching no agent and no live session at all.
	stranger := mk("assigned to an identity nothing bears", "worker-gone-42")

	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, "", nil,
		[]beads.Bead{slot, stranger}, nil, nil, nil,
	)
	if len(released) != 2 {
		t.Fatalf("released %d beads, want 2; abandoned session assignments must still return to the queue", len(released))
	}
	for _, b := range []beads.Bead{slot, stranger} {
		got, err := store.Get(b.ID)
		if err != nil {
			t.Fatalf("get %s: %v", b.ID, err)
		}
		if got.Assignee != "" {
			t.Errorf("%q: Assignee = %q, want cleared", b.Title, got.Assignee)
		}
		if got.Metadata["gc.routed_to"] != "worker" {
			t.Errorf("%q: gc.routed_to = %q, want preserved so the pool can re-claim it", b.Title, got.Metadata["gc.routed_to"])
		}
	}
}

// TestOrphanReleaseKeepsAddressOnSuspendedPoolButNotRemovedOne separates the two
// ways a pool can stop serving, because only one of them makes an address
// unreachable and the narrower isKnownPoolTemplate predicate would conflate
// them.
//
// A SUSPENDED agent is paused: the address must survive, or every bead parked on
// a pool is stripped for the duration of the suspension. That is how the
// ci-vcornx beads were produced in the first place -- the 2026-09-04 xhigh brake
// suspended a whole agent family. An agent REMOVED from config is retired: no
// session will ever bear its name again, so its address is as unreachable as a
// dead slot alias and must release.
func TestOrphanReleaseKeepsAddressOnSuspendedPoolButNotRemovedOne(t *testing.T) {
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{
		{Name: "paused", Suspended: true, MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
		{Name: "router", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
	}}

	mk := func(title, assignee string) beads.Bead {
		t.Helper()
		b, err := store.Create(beads.Bead{
			Title:    title,
			Type:     "task",
			Status:   "open",
			Assignee: assignee,
			Metadata: map[string]string{"gc.routed_to": "router"},
		})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return b
	}

	suspended := mk("addressed to a suspended pool", "paused")
	// "deleted-agent" appears in no [[agent]] block: the config-removal case.
	removed := mk("addressed to an agent no longer in config", "deleted-agent")

	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, "", nil,
		[]beads.Bead{suspended, removed}, nil, nil, nil,
	)
	if len(released) != 1 || released[0].ID != removed.ID {
		t.Fatalf("released = %v, want exactly [%s]; a suspension is a pause, a config removal is a retirement", released, removed.ID)
	}

	gotSuspended, err := store.Get(suspended.ID)
	if err != nil {
		t.Fatalf("get suspended: %v", err)
	}
	if gotSuspended.Assignee != "paused" {
		t.Errorf("suspended-pool Assignee = %q, want %q; the address must outlive the suspension that made it unservable", gotSuspended.Assignee, "paused")
	}
	gotRemoved, err := store.Get(removed.ID)
	if err != nil {
		t.Fatalf("get removed: %v", err)
	}
	if gotRemoved.Assignee != "" {
		t.Errorf("removed-agent Assignee = %q, want cleared", gotRemoved.Assignee)
	}
}

// TestPoolAliasWorkStaysClaimableRegardlessOfRoute settles the bead's SECOND
// reported defect, which turns out to be the first one wearing a different hat.
//
// ci-vcornx reported that `gc session new <pool>` "may yield an ADHOC slot that
// cannot claim that pool's work": a session created from the pool drained with
// "no work available" while a ready bead sat addressed to exactly that pool. The
// adhoc naming is real and deterministic -- sessionExplicitNameForNewSession
// generates an "-adhoc-<hash>" explicit name whenever the agent supports
// multiple sessions and no --alias was given, which is why the same command on a
// single-session agent produced a pool-slot target instead. But the adhoc
// identity is NOT what made the bead unclaimable, and this test is why: an adhoc
// worker resolves its config through GC_TEMPLATE, so its RouteTargets still
// carry the bare pool name, and the pool-alias admission rule keys on the
// assignee alone. A foreign gc.routed_to does not hide the bead.
//
// What remains, then, is that the bead had NO assignee at the moment that
// session looked -- the every-tick strip fixed above. The drain and the strip
// are one defect. If a future session sees the drain again with the assignee
// intact, this test is the one that says to look somewhere other than routing.
func TestPoolAliasWorkStaysClaimableRegardlessOfRoute(t *testing.T) {
	pool := &config.Agent{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)}
	// The route targets an adhoc-named worker computes for itself: the bare pool
	// name arrives via the resolved template, not via the session's own identity.
	routeTargets := hookClaimAgentRouteTargets(pool, nil, []string{pool.QualifiedName()})

	parked := beads.Bead{
		ID:       "as-7uha",
		Status:   "open",
		Assignee: "worker",
		Metadata: map[string]string{"gc.routed_to": "some-other-agent"},
	}
	if got := hookCandidatePoolAlias(parked, routeTargets); got != "worker" {
		t.Fatalf("hookCandidatePoolAlias = %q, want %q; a foreign route must not hide pool-addressed work from the pool's own claim", got, "worker")
	}
	if !hookCandidateClaimable(parked, routeTargets) {
		t.Fatal("hookCandidateClaimable = false; the pool-alias tier admits work by assignee, so the route is not what refused this bead")
	}

	// The same bead after the every-tick strip: no assignee, and a route naming
	// an agent this pool is not. Now nothing admits it, which is the drain the
	// bead observed.
	stripped := parked
	stripped.Assignee = ""
	if hookCandidateClaimable(stripped, routeTargets) {
		t.Fatal("hookCandidateClaimable = true for stripped work; the fixture no longer reproduces the drain, so this test has stopped testing it")
	}
}

// TestPoolAliasDemandStillIgnoresAssignedRoutedWork records a SECOND, separate
// gap that the orphan-sweep fix above does NOT close, so nobody reads this bead
// as having made a cold pool wake.
//
// The in-process controller demand reader discards the routed target whenever a
// bead has an assignee (build_desired_state.go) and then asks
// controllerDemandPoolAliasTarget, which declines ANY assigned bead that also
// carries a route -- the foreign-route case as a "concrete handoff" (#2527), and
// the own-route case because it is not a wisp. So as-7uha raises zero demand for
// its own pool and zero for the agent it is routed to, even with its assignee
// now preserved. It is worked only once some slot of that pool is awake for
// another reason, because the CLAIM side and the shell demand form
// (bdReadyPoolAliasDemandShell, `bd ready --assignee="$target"`) both ignore the
// route entirely and admit it -- pinned by
// TestPoolAliasWorkStaysClaimableRegardlessOfRoute above.
//
// That worker-can-claim / controller-cannot-see split is the divergence
// poolDemandCountShell's own doc calls out by name ("counting a shape the worker
// can claim but the reconciler cannot see leaves the bead unworked with no
// session ever spawned", ci-rdbw). Closing it means choosing which side moves,
// and the wrong choice is a wake/drain spawn storm (PR #1516) across every pool
// in the city -- so it is filed as its own bead rather than decided here.
//
// WHAT THAT BEAD (ci-77oav9) CONCLUDED, so nobody re-derives it: neither half
// is the gap it looked like, and the own half is the opposite of unintended.
//
// The OWN-route zero is #2527's invariant, reached by deliberate reversal.
// TestDefaultScaleCheckCountsDoesNotTreatTemplateAssigneeAsDemand
// (build_desired_state_test.go) pins this exact fixture -- assignee == route ==
// template -- at demand 0, and it REPLACED a #1991 test that asserted 1 for the
// identical bead. dispatch.md states the residual as policy: the shell form
// counts that bead where this reader counts zero, and #2527 wins for the shape.
// The wisp carve-out is the one narrow exception, added when a producer of that
// shape appeared.
//
// Generalizing the carve-out was measured and is worse than the status quo, not
// merely disallowed. The wake-known-identity tier in pool_desired_state.go
// ALREADY serves the own-route shape: with routed == assignee == template the
// bead survives that tier's routedTo != template gate, normalizes onto the
// template and emits a request. New-tier demand and wake requests are added,
// never max'd (see the note there that resume requests "must not be deducted"),
// so admitting the bead here yields TWO sessions for ONE bead -- measured over
// a cold city with `go test -overlay`, sessions 1 -> 2 -- of which one wins the
// compare-and-swap claim and the other drains. That is the PR #1516 wake/drain
// shape this file's editing constraint names. The no-route case needs this tier
// precisely because it does NOT survive that gate.
//
// There is also a live non-wisp producer that must keep raising zero:
// cmd_sling_test.go's polecat->refinery done sequence writes assignee == route
// == "saitoc/refinery", where refinery is a PLAIN POOL agent with no
// [[named_sessions]] entry -- so poolAliasDemandEligible's named-session
// exclusion does not catch it. The rule those three places state together is:
// a caller that wants pool demand clears Assignee and sets gc.routed_to.
//
// The remaining real gap is a READINESS-capture asymmetry, not a routing one,
// and it is filed separately. An orphaned in_progress bead is always recovered
// (appendInProgressWorkUnique marks it ready); an orphaned OPEN bead assigned to
// a dead slot is captured only by appendOpenRoutedWorkUnique, which never calls
// markReadyAssigned, so filterAssignedWorkBeadsForPoolDemand drops it and it
// reaches the wake tier only through the fully-cold-city Ready() fallback.
// Normalizing the assignee here is NOT the fix for it: ci-rdbw's objection
// stands, and a respawned slot finds such a bead through its own identity
// (standardAssignedReadyWorkQueryScript probes bd ready --assignee=$GC_ALIAS).
//
// This test asserts what the code does today, and both routed rows are an
// ENDORSEMENT rather than a tripwire: they pin #2527's boundary. A green run
// here no longer means "the gap is still open".
func TestPoolAliasDemandStillIgnoresAssignedRoutedWork(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
		{Name: "other-agent", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
	}}
	templates := map[string]struct{}{"worker": {}, "other-agent": {}}

	mk := func(route string) beads.Bead {
		b := beads.Bead{ID: "as-7uha", Status: "open", Type: "task", Assignee: "worker", Metadata: map[string]string{}}
		if route != "" {
			b.Metadata["gc.routed_to"] = route
		}
		return b
	}

	if got := controllerDemandPoolAliasTarget(cfg, mk("other-agent"), templates); got != "" {
		t.Errorf("foreign-routed pool work demand target = %q, want %q; a bead routed elsewhere has left this pool (#2527)", got, "")
	}
	if got := controllerDemandPoolAliasTarget(cfg, mk("worker"), templates); got != "" {
		t.Errorf("own-routed pool work demand target = %q, want %q; this is #2527's invariant, not an accident of the wisp carve-out -- see the header before changing it", got, "")
	}
	// The unrouted address is the one shape that does raise demand, which is why
	// unsetting the route on as-7uha took its pool from 0 sizings to 9.
	if got := controllerDemandPoolAliasTarget(cfg, mk(""), templates); got != "worker" {
		t.Errorf("unrouted pool work demand target = %q, want %q; this is the path that still wakes a cold pool", got, "worker")
	}
}
