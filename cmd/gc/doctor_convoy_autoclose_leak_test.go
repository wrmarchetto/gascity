package main

// Scope: the doctor gate that fails when a convoy has become collectable but
// was never collected, and the shared completeness predicate it sits on.
//
// The suite exists because a leaked convoy is invisible from every surface an
// operator or agent actually reads: it is Ready-visible and looks exactly like
// a live dispatch, and the only way to tell them apart is to open the child.
// The gate is the mechanical replacement for that manual inspection, so these
// tests pin the two things that make it worth having -- that it fails on the
// observed leak shape, and that it stays silent on the three healthy shapes it
// would otherwise flag.
//
// Delegated elsewhere: the collectors themselves. Whether the event-driven
// autoclose and `gc convoy check` correctly close a complete convoy is pinned
// in cmd_convoy_test.go. This suite covers only detection, the shared
// predicate, and the --fix remediation path.
//
// Run: go test ./cmd/gc/ -run 'ConvoyAutocloseLeak|ConvoyIsComplete'

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/events"
)

// leakFixtureStore builds the four convoy shapes the gate must tell apart, in
// the linkage sling actually uses (tracks deps), not the legacy ParentID form:
//
//	gc-1 leaked      one child, closed        -> the bug
//	gc-3 live        one child, open          -> a dispatch in flight
//	gc-5 owned       one child, closed        -> its owner lands it
//	gc-7 childless   no children              -> sling mid-mint
func leakFixtureStore(t *testing.T) beads.Store {
	t.Helper()
	store := beads.NewMemStore()
	_, _ = store.Create(beads.Bead{Title: "sling-gc-2", Type: "convoy"}) // gc-1
	_, _ = store.Create(beads.Bead{Title: "finished work"})              // gc-2
	_, _ = store.Create(beads.Bead{Title: "sling-gc-4", Type: "convoy"}) // gc-3
	_, _ = store.Create(beads.Bead{Title: "work in flight"})             // gc-4
	_, _ = store.Create(beads.Bead{Title: "owned batch", Type: "convoy", // gc-5
		Labels: []string{"owned"}})
	_, _ = store.Create(beads.Bead{Title: "owned child"})                   // gc-6
	_, _ = store.Create(beads.Bead{Title: "sling-pending", Type: "convoy"}) // gc-7

	requireNoError(t, store.DepAdd("gc-1", "gc-2", "tracks"))
	requireNoError(t, store.DepAdd("gc-3", "gc-4", "tracks"))
	requireNoError(t, store.DepAdd("gc-5", "gc-6", "tracks"))

	requireNoError(t, store.Close("gc-2"))
	requireNoError(t, store.Close("gc-6"))
	return store
}

func newLeakCheckForStore(store beads.Store) *convoyAutocloseLeakCheck {
	return newConvoyAutocloseLeakCheck(&config.City{}, "/city",
		func(string) (beads.Store, error) { return store, nil })
}

// TestConvoyAutocloseLeakGateFailsOnUncollectedConvoy pins the reproduction
// shape measured in the city store (convoy ci-lw0o, child ci-87kv closed 35+
// minutes earlier with the convoy untouched): an open convoy whose every child
// is terminal must fail the gate and must fail it BLOCKING, because
// `gc doctor` derives its exit code from BlockingFailed alone. An advisory
// severity here would print the identical line and still exit 0, which is the
// silent-pass this gate exists to prevent.
func TestConvoyAutocloseLeakGateFailsOnUncollectedConvoy(t *testing.T) {
	res := newLeakCheckForStore(leakFixtureStore(t)).Run(&doctor.CheckContext{})

	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError: %#v", res.Status, res)
	}
	if res.Severity != doctor.SeverityBlocking {
		t.Fatalf("Severity = %v, want SeverityBlocking (gc doctor exits 1 only on BlockingFailed)", res.Severity)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "gc-1") {
		t.Errorf("Details must name the leaked convoy gc-1:\n%s", strings.Join(res.Details, "\n"))
	}
	if res.FixHint == "" {
		t.Error("FixHint must name the remediation command; a gate that fails without one strands the operator")
	}
}

// TestConvoyAutocloseLeakGateIgnoresHealthyConvoys pins the three shapes that
// are NOT leaks. This is the half of the gate that decides whether it survives
// contact with a real city: a gate that also fires on live dispatches gets
// muted, and a muted gate is worth less than none. The childless case is the
// subtle one -- sling mints the convoy bead before attaching its child, so
// "zero children, all of them closed" is vacuously true and would flag every
// dispatch during its mint window.
func TestConvoyAutocloseLeakGateIgnoresHealthyConvoys(t *testing.T) {
	res := newLeakCheckForStore(leakFixtureStore(t)).Run(&doctor.CheckContext{})

	details := strings.Join(res.Details, "\n")
	for id, why := range map[string]string{
		"gc-3": "convoy with an open child is a dispatch in flight",
		"gc-5": "owned convoy is closed by gc convoy land, not autoclose",
		"gc-7": "childless convoy is sling mid-mint, not a leak",
	} {
		if strings.Contains(details, id) {
			t.Errorf("gate flagged %s: %s\n%s", id, why, details)
		}
	}
	if !strings.Contains(res.Message, "1") {
		t.Errorf("Message = %q, want a count of exactly 1 leaked convoy", res.Message)
	}
}

// TestConvoyAutocloseLeakGatePassesOnCleanStore pins that the gate is silent
// when there is nothing to collect. Without this, a gate that always fails is
// indistinguishable from one that works, and it would fail the whole `gc
// doctor` run on every healthy city.
func TestConvoyAutocloseLeakGatePassesOnCleanStore(t *testing.T) {
	store := beads.NewMemStore()
	_, _ = store.Create(beads.Bead{Title: "sling-gc-2", Type: "convoy"}) // gc-1
	_, _ = store.Create(beads.Bead{Title: "work in flight"})             // gc-2
	requireNoError(t, store.DepAdd("gc-1", "gc-2", "tracks"))

	res := newLeakCheckForStore(store).Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK on a city with no leaks: %#v", res.Status, res)
	}
}

// TestConvoyAutocloseLeakFixCollectsAndIsIdempotent pins that --fix actually
// collects the leak and that a second run finds nothing. Idempotence is the
// property that lets this run unattended: the check is the last observer in a
// system whose reliability argument is that independent observers converge on
// the same state.
func TestConvoyAutocloseLeakFixCollectsAndIsIdempotent(t *testing.T) {
	store := leakFixtureStore(t)
	check := newLeakCheckForStore(store)

	if !check.CanFix() {
		t.Fatal("CanFix = false; a gate for a condition with a known mechanical remedy must offer it")
	}
	if res := check.Run(&doctor.CheckContext{}); res.Status != doctor.StatusError {
		t.Fatalf("precondition: want a leak before fixing, got %#v", res)
	}
	if err := check.Fix(&doctor.CheckContext{}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	b, err := store.Get("gc-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "closed" {
		t.Errorf("convoy gc-1 Status = %q after Fix, want closed", b.Status)
	}
	// The close must be attributable to the collector, not merely "closed":
	// cities running validation.on-close=error reject a terse reason, and the
	// audit trail is how a later operator tells an autoclose from a hand close.
	if got := b.Metadata["close_reason"]; got != convoyAutocloseReason {
		t.Errorf("close_reason = %q, want %q", got, convoyAutocloseReason)
	}
	// The healthy convoys must survive the fix untouched.
	for _, id := range []string{"gc-3", "gc-5", "gc-7"} {
		h, err := store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if h.Status != "open" {
			t.Errorf("Fix closed healthy convoy %s (status %q)", id, h.Status)
		}
	}

	if res := check.Run(&doctor.CheckContext{}); res.Status != doctor.StatusOK {
		t.Errorf("second Run after Fix = %v, want StatusOK (fix must converge): %#v", res.Status, res)
	}
}

// TestConvoyAutocloseLeakStoreErrorDoesNotGate pins that an unreachable store
// is reported as a warning rather than a blocking failure. A store error means
// the leak set is UNKNOWN, and failing closed there would wedge `gc doctor`
// -- and anything gated on it -- whenever Dolt is briefly unavailable.
func TestConvoyAutocloseLeakStoreErrorDoesNotGate(t *testing.T) {
	check := newConvoyAutocloseLeakCheck(&config.City{}, "/city",
		func(string) (beads.Store, error) { return nil, fmt.Errorf("store unreachable") })

	res := check.Run(&doctor.CheckContext{})
	if res.Status == doctor.StatusError && res.Severity == doctor.SeverityBlocking {
		t.Fatalf("unreachable store must not block: %#v", res)
	}
	if !strings.Contains(res.Message, "unknown") {
		t.Errorf("Message = %q, want it to say the leak set is unknown, not that there are none", res.Message)
	}
}

// TestConvoyIsCompleteMatchesTheCollector pins the gate and the collector to
// one predicate. They are separate code paths that must agree forever: if the
// gate's idea of "leaked" drifts from the collector's idea of "collectable",
// the gate goes green while the leak continues -- which is the exact failure
// mode that let this bug run unobserved. Asserting agreement here is what
// makes the extraction load-bearing rather than cosmetic.
func TestConvoyIsCompleteMatchesTheCollector(t *testing.T) {
	gateStore := leakFixtureStore(t)
	sweepStore := leakFixtureStore(t)

	gate := newLeakCheckForStore(gateStore).Run(&doctor.CheckContext{})
	gateFlagged := strings.Join(gate.Details, "\n")

	// Drive the real sweep over an identical store and compare what it closed
	// against what the gate flagged.
	code := doConvoyCheckAcrossStoresJSON(
		[]convoyStoreView{{path: "/city", store: sweepStore}},
		events.Discard, false, &strings.Builder{}, &strings.Builder{})
	if code != 0 {
		t.Fatalf("doConvoyCheckAcrossStoresJSON = %d, want 0", code)
	}

	for _, id := range []string{"gc-1", "gc-3", "gc-5", "gc-7"} {
		b, err := sweepStore.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		sweptClosed := b.Status == "closed"
		gateSaidLeaked := strings.Contains(gateFlagged, id)
		if sweptClosed != gateSaidLeaked {
			t.Errorf("%s: sweep closed=%v but gate flagged=%v -- predicate drift",
				id, sweptClosed, gateSaidLeaked)
		}
	}
}
