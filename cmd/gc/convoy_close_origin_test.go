package main

// Scope: the convoy autoclose attribution record -- the origin enum, the
// per-origin close_reason prose, and the three production call sites that must
// each write a DIFFERENT record.
//
// Why the suite exists: three independent collectors used to stamp one
// close_reason, so a closed convoy could not say which of them collected it.
// That ambiguity was read as evidence the event-driven handler had fired when
// it had not (ci-zz26, ci-eh7h). A test that recomputes its expectation from
// convoyAutocloseReasonFor cannot catch the regression, because a body that
// ignores its origin argument still satisfies it -- so every expected string
// below is a hand-written literal.
//
// Delegated elsewhere: whether a convoy is collectable at all belongs to
// convoyIsComplete (cmd_convoy_test.go); store-resolution across city and rig
// stores belongs to provider_store_resolution_test.go.
//
// Run: go test ./cmd/gc/ -count=1 -run 'ConvoyAutocloseReasonsAreDistinct|ConvoyCloseOriginNormalizes|CloseConvoyWithOrigin|Convoy(Sweep|CloseHook|Controller)CallSite'
//
// A bare -run 'Origin' is NOT the command: cmd/gc has unrelated tests whose
// names contain Origin (session origin metadata, git remote origin), and the
// nine cases this pattern selects are the whole of this file plus
// TestCloseConvoyWithOriginBdStoreForwardsOneUpdateThenClose, which lives in
// cmd_convoy_test.go next to the closeConvoyWithReason tests it contrasts with.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// bdOnCloseValidatorFloor is bd's validation.on-close=error minimum reason
// length. Written as a literal here rather than imported from bd: the point of
// the assertion is that OUR strings clear a threshold bd owns, and reading the
// threshold from bd would make the test pass automatically if bd ever reported
// zero.
const bdOnCloseValidatorFloor = 20

// TestConvoyAutocloseReasonsAreDistinctPerOrigin pins the exact prose each
// origin renders. The literals are duplicated from the implementation on
// purpose -- that duplication IS the test. A shared constant would let a body
// that ignores its origin argument (the ci-eh7h bug) pass.
func TestConvoyAutocloseReasonsAreDistinctPerOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin convoyCloseOrigin
		want   string
	}{
		{
			name:   "sweep",
			origin: convoyCloseOriginSweep,
			want:   "convoy autoclose: collected by the gc convoy check sweep",
		},
		{
			name:   "controller",
			origin: convoyCloseOriginController,
			want:   "convoy autoclose: collected by the controller bead.closed handler",
		},
		{
			name:   "close hook",
			origin: convoyCloseOriginCloseHook,
			want:   "convoy autoclose: collected by the gc convoy autoclose hook",
		},
		{
			name:   "unrecorded",
			origin: convoyCloseOriginUnrecorded,
			want:   "convoy autoclose: collector not recorded",
		},
	}

	seen := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convoyAutocloseReasonFor(tc.origin)
			if got != tc.want {
				t.Errorf("convoyAutocloseReasonFor(%q) = %q, want %q", tc.origin, got, tc.want)
			}
			if len(got) < bdOnCloseValidatorFloor {
				t.Errorf("reason %q is %d chars, below bd's validation.on-close floor of %d -- this branch cannot close under validation.on-close=error",
					got, len(got), bdOnCloseValidatorFloor)
			}
		})
		if prior, dup := seen[tc.want]; dup {
			t.Errorf("origins %q and %q render the same reason %q -- the collision this type exists to prevent", prior, tc.name, tc.want)
		}
		seen[tc.want] = tc.name
	}
}

// TestConvoyCloseOriginNormalizesUnknownToUnrecorded pins that an origin from a
// newer gc, or a typo, degrades to "not recorded" instead of being passed
// through as if it were an authoritative collector. Rendering an unknown value
// as a real actor is the same class of lie as the collision itself.
func TestConvoyCloseOriginNormalizesUnknownToUnrecorded(t *testing.T) {
	for _, raw := range []string{"", "  ", "reconciler", "Sweep", "controller ", "doctor-fix"} {
		got := normalizeConvoyCloseOrigin(raw)
		if raw == "controller " {
			// Surrounding whitespace is trimmed, so this one IS a real origin.
			if got != convoyCloseOriginController {
				t.Errorf("normalizeConvoyCloseOrigin(%q) = %q, want %q after trim", raw, got, convoyCloseOriginController)
			}
			continue
		}
		if got != convoyCloseOriginUnrecorded {
			t.Errorf("normalizeConvoyCloseOrigin(%q) = %q, want unrecorded", raw, got)
		}
	}
}

// TestCloseConvoyWithOriginStampsOriginAndActor pins the machine-readable half
// of the record. The reason prose is for a human reading `bd show`; the origin
// and actor keys are what a query can group by, and the actor is what tells a
// hand-run sweep from the doctor fix's sweep -- a distinction the sweep itself
// cannot see, since the doctor fix execs `gc convoy check` verbatim.
func TestCloseConvoyWithOriginStampsOriginAndActor(t *testing.T) {
	t.Setenv("GC_ALIAS", "mayor")

	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}

	if err := closeConvoyWithOrigin(store, convoy.ID, convoyCloseOriginSweep); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(convoy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "closed" {
		t.Fatalf("convoy Status = %q, want closed", got.Status)
	}
	if want := "sweep"; got.Metadata[convoyCloseOriginMetadataKey] != want {
		t.Errorf("metadata[%s] = %q, want %q", convoyCloseOriginMetadataKey, got.Metadata[convoyCloseOriginMetadataKey], want)
	}
	if want := "mayor"; got.Metadata[convoyCloseActorMetadataKey] != want {
		t.Errorf("metadata[%s] = %q, want %q", convoyCloseActorMetadataKey, got.Metadata[convoyCloseActorMetadataKey], want)
	}
	if want := "convoy autoclose: collected by the gc convoy check sweep"; got.Metadata["close_reason"] != want {
		t.Errorf("metadata[close_reason] = %q, want %q", got.Metadata["close_reason"], want)
	}
}

// TestCloseConvoyWithOriginLeavesConvoyOpenOnMetadataFailure pins that the
// attribution record is written BEFORE the close and that a failure to write it
// aborts the close. The alternative -- close first, stamp after -- leaves a
// window in which a closed convoy carries no origin at all, which is the state
// this whole record exists to eliminate. Mirrors the same ordering rule as
// session.DrainAckCloseOverlay.
func TestCloseConvoyWithOriginLeavesConvoyOpenOnMetadataFailure(t *testing.T) {
	base := beads.NewMemStore()
	convoy, err := base.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}

	// failingSetMetadataBatchStore (soft_reload_test.go) refuses only the named
	// id and delegates every other write to the real store, so a batch this
	// test did not intend to break is not handed a silent pass.
	err = closeConvoyWithOrigin(failingSetMetadataBatchStore{Store: base, failID: convoy.ID}, convoy.ID, convoyCloseOriginSweep)
	if err == nil {
		t.Fatal("closeConvoyWithOrigin returned nil, want the metadata error")
	}
	if !strings.Contains(err.Error(), "stamping convoy "+convoy.ID+" close attribution") {
		t.Fatalf("error = %v, want close-attribution context naming the convoy", err)
	}

	got, err := base.Get(convoy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" {
		t.Fatalf("convoy Status = %q, want open -- an unattributable close must not happen at all", got.Status)
	}
}

// TestCloseConvoyWithOriginClearsAStaleOriginOnRecollect pins the reopen path.
// A convoy closed by one collector, reopened, and re-collected by a caller with
// no origin must not still name the first collector. Asserted as "not the stale
// value" AND on the reason prose, because the failure mode is a stale record
// that reads as current -- checking only that the key exists would pass against
// it.
func TestCloseConvoyWithOriginClearsAStaleOriginOnRecollect(t *testing.T) {
	t.Setenv("GC_ALIAS", "mayor")

	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := closeConvoyWithOrigin(store, convoy.ID, convoyCloseOriginSweep); err != nil {
		t.Fatal(err)
	}
	if err := store.Reopen(convoy.ID); err != nil {
		t.Fatal(err)
	}

	// A caller that declared no origin: the case a future third reactive path
	// would hit if it forgot to name itself.
	if err := closeConvoyWithOrigin(store, convoy.ID, convoyCloseOriginUnrecorded); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(convoy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale := got.Metadata[convoyCloseOriginMetadataKey]; stale == "sweep" {
		t.Errorf("metadata[%s] = %q after re-collect -- the previous incarnation's collector survived", convoyCloseOriginMetadataKey, stale)
	}
	if want := "convoy autoclose: collector not recorded"; got.Metadata["close_reason"] != want {
		t.Errorf("metadata[close_reason] = %q, want %q", got.Metadata["close_reason"], want)
	}
}

// --- the three production call sites ---
//
// The three tests below are the load-bearing ones for ci-eh7h. Each drives a
// REAL entry point rather than calling closeConvoyWithOrigin with a
// hand-supplied origin, because the defect was never in the renderer -- it was
// in the call sites all naming the same value. Asserting on the renderer alone
// would have passed against the collapsed code.

// TestConvoySweepCallSiteRecordsSweepOrigin drives `gc convoy check`.
func TestConvoySweepCallSiteRecordsSweepOrigin(t *testing.T) {
	t.Setenv("GC_ALIAS", "mayor")

	store := beads.NewMemStore()
	convoy, _ := store.Create(beads.Bead{Title: "batch", Type: "convoy"})
	child, _ := store.Create(beads.Bead{Title: "task A", ParentID: convoy.ID})
	if err := store.Close(child.ID); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := doConvoyCheck(store, events.Discard, &stdout, &stderr); code != 0 {
		t.Fatalf("doConvoyCheck = %d, want 0; stderr: %s", code, stderr.String())
	}

	assertConvoyAttribution(t, store, convoy.ID,
		"convoy autoclose: collected by the gc convoy check sweep", "sweep", "mayor")
}

// TestConvoyCloseHookCallSiteRecordsCloseHookOrigin drives the standalone
// `gc convoy autoclose <id>` CLI -- the pre-#3248 bd on_close hook entry point.
// It goes through doConvoyAutoclose (not the -With core) so the origin the CLI
// plumbing chooses is what gets asserted; calling the core directly would pin
// only the argument this test itself supplied.
func TestConvoyCloseHookCallSiteRecordsCloseHookOrigin(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_ALIAS", "toolsmith-2")

	cityDir := t.TempDir()
	if err := ensureScopedFileStoreLayout(cityDir); err != nil {
		t.Fatal(err)
	}
	writeProviderAwareTestCity(t, cityDir, `[workspace]
name = "demo"
`)
	if err := ensurePersistedScopeLocalFileStore(cityDir); err != nil {
		t.Fatal(err)
	}
	store, err := openScopeLocalFileStore(cityDir)
	if err != nil {
		t.Fatal(err)
	}
	convoy, err := store.Create(beads.Bead{Title: "deploy", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.Create(beads.Bead{Title: "task", Type: "task", ParentID: convoy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(child.ID); err != nil {
		t.Fatal(err)
	}
	chdirProviderAwareTest(t, cityDir)

	var stdout, stderr bytes.Buffer
	doConvoyAutoclose(child.ID, &stdout, &stderr)

	reloaded, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatal(err)
	}
	assertConvoyAttribution(t, reloaded, convoy.ID,
		"convoy autoclose: collected by the gc convoy autoclose hook", "close-hook", "toolsmith-2")
}

// TestConvoyControllerCallSiteRecordsControllerOrigin drives the controller's
// in-process bead.closed handler (runBeadCloseAutoclose, #3248). This is the
// one origin that proves the automatic mechanism fired on its own -- the exact
// claim ci-zz26 could not check, and the reason this test exists as a separate
// case from the close-hook one even though both share doConvoyAutocloseWith.
func TestConvoyControllerCallSiteRecordsControllerOrigin(t *testing.T) {
	prev := beadCloseAutocloseDispatch
	beadCloseAutocloseDispatch = func(fn func()) { fn() } // synchronous in tests
	t.Cleanup(func() { beadCloseAutocloseDispatch = prev })
	t.Setenv("GC_ALIAS", "controller")

	backing := beads.NewMemStore()
	convoy, err := backing.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := backing.Create(beads.Bead{Title: "task A", ParentID: convoy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := backing.Close(child.ID); err != nil {
		t.Fatal(err)
	}

	closedPayload, err := json.Marshal(beads.Bead{ID: child.ID, Title: "task A", Status: "closed", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	cs := &controllerState{
		beadStores: map[string]beads.Store{"test": backing},
		pokeCh:     make(chan struct{}, 1),
	}
	cs.applyBeadEventToStores(events.Event{
		Type:    events.BeadClosed,
		Actor:   "agent",
		Subject: child.ID,
		Payload: closedPayload,
	})

	assertConvoyAttribution(t, backing, convoy.ID,
		"convoy autoclose: collected by the controller bead.closed handler", "controller", "controller")
}

// assertConvoyAttribution checks the full record one call site wrote. It takes
// the expected strings as arguments so each caller supplies its own literals --
// deriving them here from the origin would reintroduce the recomputation the
// suite header rejects.
func assertConvoyAttribution(t *testing.T, store beads.Store, convoyID, wantReason, wantOrigin, wantActor string) {
	t.Helper()

	got, err := store.Get(convoyID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "closed" {
		t.Fatalf("convoy Status = %q, want closed", got.Status)
	}
	if got.Metadata["close_reason"] != wantReason {
		t.Errorf("metadata[close_reason] = %q, want %q", got.Metadata["close_reason"], wantReason)
	}
	if got.Metadata[convoyCloseOriginMetadataKey] != wantOrigin {
		t.Errorf("metadata[%s] = %q, want %q", convoyCloseOriginMetadataKey, got.Metadata[convoyCloseOriginMetadataKey], wantOrigin)
	}
	if got.Metadata[convoyCloseActorMetadataKey] != wantActor {
		t.Errorf("metadata[%s] = %q, want %q", convoyCloseActorMetadataKey, got.Metadata[convoyCloseActorMetadataKey], wantActor)
	}
}
