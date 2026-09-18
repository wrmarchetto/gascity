package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Scope: the controller's per-tick `assignedWorkBeads:` diagnostic, which is
// the only per-bead account of why a pool is the size it is. This suite exists
// because that account was read by an operator and led to a false conclusion
// recorded as fact on a P1 (city bead ci-x2pg3p): three parked beads printed as
// `status=open` with no other field, the reader wrote "not a hold label and not
// a deferred status", and an hour went into a controller that was sizing
// correctly the whole time.
//
// It delegates the sizing arithmetic itself to build_desired_state_test.go and
// pool_desired_state_test.go; nothing here asserts a count.
//
// What a host suite cannot represent, so it is not mistaken for redundant: the
// live falsehood was produced by NativeDoltStore, whose mapBdStatus collapses
// bd's `deferred` status to `open` and erases the raw value before the
// diagnostic ever sees it (internal/beads/native_dolt_store.go, the comment
// above the StatusDeferred branch says so). MemStore does not collapse, so no
// host test can drive the collapse itself. These tests therefore fix the bead
// in the shape the diagnostic RECEIVES it -- Status already "open", the
// deferral surviving only in DeferUntil -- which is exactly what reached the
// print site for city bead ci-2qb5bb (store: status=deferred,
// defer_until=2026-09-16T10:19:01Z; diagnostic: status=open).
//
// Run: go test ./cmd/gc/ -run DemandDiagnostic

// demandDiagnosticCity is a one-pool city whose scale_check reports no demand,
// so the only thing that can vary the diagnostic is the work bead itself.
func demandDiagnosticCity() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			StartCommand:      "true",
			MaxActiveSessions: intPtr(3),
			ScaleCheck:        "printf 0",
		}},
	}
}

// assignedWorkRowFor returns the `assignedWorkBeads:` row the tick printed for
// beadID. Failing here rather than returning empty keeps a diagnostic that
// stopped printing rows from reading as a row that passed every assertion.
func assignedWorkRowFor(t *testing.T, stderr, beadID string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, beadID+" ") {
			return trimmed
		}
	}
	t.Fatalf("no assignedWorkBeads row for %s; stderr:\n%s", beadID, stderr)
	return ""
}

// runDemandDiagnosticTick builds desired state once over a store holding work
// and returns the store-assigned bead ID alongside what the tick wrote to
// stderr.
//
// The ID comes back from Create rather than from the fixture because MemStore
// mints its own ("gc-1") and silently ignores a caller-supplied one -- a test
// that greps for its own literal finds no row and fails for the wrong reason.
//
// gc.routed_to is set here, on every fixture, and that is load-bearing rather
// than boilerplate. Assigned work that is NOT ready reaches the diagnostic only
// through the open-routed orphan-release pass, which requires the route; an
// unrouted deferred bead is dropped before the print site and the tick reports
// "0 beads". Both live beads that provoked this suite carried a route
// (ci-2qb5bb and ci-4326yh printed routed=toolsmith), so the route is part of
// the shape under test, not a convenience.
func runDemandDiagnosticTick(t *testing.T, work beads.Bead) (string, string) {
	t.Helper()
	store := beads.NewMemStore()
	if work.Metadata == nil {
		work.Metadata = map[string]string{}
	}
	work.Metadata[beadmeta.RoutedToMetadataKey] = "worker"
	created, err := store.Create(work)
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	clk := &clock.Fake{Time: demandDiagnosticNow}
	var stderr bytes.Buffer
	buildDesiredState("test-city", t.TempDir(), clk.Now().UTC(), demandDiagnosticCity(), runtime.NewFake(), store, &stderr)
	return created.ID, stderr.String()
}

// demandDiagnosticNow is the ONE instant this suite is written against: the
// tick's injected beaconTime and the base every deferral fixture is offset
// from.
//
// Pinning the tick to real now rather than to a chosen date is the fix, and
// the reason is that the row is formatted from TWO clocks. `ready=` comes from
// the store's own readiness computation, which reads the real clock; `defer=`
// comes from beads.IsDeferred against the injected beaconTime
// (assigned_work_scope.go). In production those are the same instant. A fake
// tick clock separates them, and a fixture landing between the two makes the
// row report `ready=true defer=<future>` -- a row contradicting itself, which
// is the exact class of falsehood this suite was written to remove.
//
// That gap is what expired here. The fixtures were absolute dates chosen to
// sit just after the fake 2026-09-14 tick clock, which made them future for
// `defer=` and, at the time of writing, future for `ready=` too. Real time
// passed 2026-09-16T10:19:01Z and only the second half changed, so the suite
// began asserting the opposite of its own comment and took `go test ./cmd/gc/`
// and the unit-cmd-gc-1-of-6 push-gate shard red for everyone (ci-f4dx5h).
// Nothing announced it: an assertion whose correctness expires on a calendar
// date has no step that notices the date arriving.
//
// Read once into a var rather than called per fixture so every arm in a run
// shares one base, and a suite that straddles midnight cannot put two arms on
// opposite sides of it.
var demandDiagnosticNow = time.Now().UTC()

// deferralFixtureOffset is a DAY rather than a moment, which the
// expired-deferral arm's own comment already required: an hour would let clock
// skew or a slow tick put a fixture on the wrong side, and a skewed pass looks
// exactly like a real one.
const deferralFixtureOffset = 24 * time.Hour

func liveDeferral() time.Time { return demandDiagnosticNow.Add(deferralFixtureOffset) }

func lapsedDeferral() time.Time { return demandDiagnosticNow.Add(-deferralFixtureOffset) }

// TestDemandDiagnosticRowReportsHoldLabelsThatSuppressDemand pins the half of
// the misdiagnosis that a hold label caused. A hold label is what the pool's
// demand tier excludes on (internal/config/workquery.go, over
// beadmeta.DispatchHoldLabels), so a row that omits it cannot be used to decide
// whether the bead is raising demand -- which is the one question the
// diagnostic exists to answer.
//
// The assertion is on the label the bead actually carries rather than on a
// literal, so adding a fourth canonical hold value cannot leave this test
// green over a row that still hides it.
func TestDemandDiagnosticRowReportsHoldLabelsThatSuppressDemand(t *testing.T) {
	for _, hold := range beadmeta.DispatchHoldLabels {
		t.Run(hold, func(t *testing.T) {
			id, stderr := runDemandDiagnosticTick(t, beads.Bead{
				Title:    "parked work",
				Type:     "task",
				Status:   "open",
				Assignee: "worker",
				Labels:   []string{hold},
			})
			row := assignedWorkRowFor(t, stderr, id)
			if !strings.Contains(row, hold) {
				t.Errorf("assignedWorkBeads row = %q, missing hold label %q that suppresses this bead's demand", row, hold)
			}
		})
	}
}

// TestDemandDiagnosticRowReportsDeferralTheStatusFieldLost pins the other half.
// The bead arrives at the print site with Status already collapsed to "open"
// and the deferral surviving only in DeferUntil, so printing Status alone
// asserts something the store contradicts. The fixture's DeferUntil is in the
// future relative to the tick clock, which is what makes the bead genuinely
// deferred rather than an expired deferral that should resurface.
func TestDemandDiagnosticRowReportsDeferralTheStatusFieldLost(t *testing.T) {
	deferUntil := liveDeferral()
	id, stderr := runDemandDiagnosticTick(t, beads.Bead{
		Title:      "deferred work",
		Type:       "task",
		Status:     "open",
		Assignee:   "worker",
		DeferUntil: &deferUntil,
	})
	row := assignedWorkRowFor(t, stderr, id)
	if !strings.Contains(row, "defer") {
		t.Errorf("assignedWorkBeads row = %q, does not report the future defer_until; the row's status field says %q and the store says deferred", row, "open")
	}
}

// TestDemandDiagnosticRowOmitsAnExpiredDeferral is the boundary twin of the
// test above, and the reason it exists is that its absence leaves the whole
// deferral assertion satisfiable by `wb.DeferUntil != nil`. An expired
// deferral does NOT suppress demand -- beads.IsDeferred re-checks the clock
// precisely so it can resurface -- so a row that reported it would be naming a
// reason for a bead that is in fact raising demand, which is the same class of
// falsehood this suite was written to remove.
//
// The fixture's defer_until is BEFORE the tick clock, and the two differ by a
// day rather than by a moment so the arm cannot pass on clock skew.
func TestDemandDiagnosticRowOmitsAnExpiredDeferral(t *testing.T) {
	expired := lapsedDeferral()
	id, stderr := runDemandDiagnosticTick(t, beads.Bead{
		Title:      "work whose deferral has lapsed",
		Type:       "task",
		Status:     "open",
		Assignee:   "worker",
		DeferUntil: &expired,
	})
	row := assignedWorkRowFor(t, stderr, id)
	if strings.Contains(row, "defer") {
		t.Errorf("assignedWorkBeads row = %q, reports a deferral that expired before the tick", row)
	}
}

// TestDemandDiagnosticRowReportsWhetherTheBeadIsRaisingDemand pins the field
// that answers the question directly. The controller already computes
// per-bead wake-demand readiness for this exact tick (ReadyAssigned, keyed by
// storeScopedBeadKey) and the diagnostic dropped it, so the operator was left
// re-deriving from a status field that could not support the inference.
//
// Both arms run against the SAME tick shape and differ only in the bead, so a
// row that hardcodes either verdict fails one of them. A single-arm version of
// this test passes over a diagnostic that prints a constant.
func TestDemandDiagnosticRowReportsWhetherTheBeadIsRaisingDemand(t *testing.T) {
	deferUntil := liveDeferral()
	cases := []struct {
		name      string
		bead      beads.Bead
		wantReady bool
	}{
		{
			name: "plain open assigned work is ready",
			bead: beads.Bead{
				Title: "live work", Type: "task",
				Status: "open", Assignee: "worker",
			},
			wantReady: true,
		},
		{
			name: "deferred assigned work is not ready",
			bead: beads.Bead{
				Title: "parked work", Type: "task",
				Status: "open", Assignee: "worker", DeferUntil: &deferUntil,
			},
			wantReady: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, stderr := runDemandDiagnosticTick(t, tc.bead)
			row := assignedWorkRowFor(t, stderr, id)
			want := "ready=false"
			if tc.wantReady {
				want = "ready=true"
			}
			if !strings.Contains(row, want) {
				t.Errorf("assignedWorkBeads row = %q, want %q", row, want)
			}
		})
	}
}
