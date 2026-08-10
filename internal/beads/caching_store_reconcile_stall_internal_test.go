package beads

// Scope: telling a WEDGED reconcile pass from a STARVED one from inside the
// process. Both look identical from outside today -- no success line, no
// problem line, stale data -- which is why the 29-hour city-store outage
// (bead ci-enyk) could be diagnosed as starvation only by elimination, leaving
// a blocked backing.List as an alternative nobody could exclude (bead ci-dirj).
//
// Two claims are pinned here and they are not the same claim:
//
//  1. A pass in flight is OBSERVABLE while it is in flight. Every existing
//     signal is written after backing.List returns, so a pass that never
//     returns writes nothing anywhere.
//  2. Something OUTSIDE the reconcile goroutine reports the stall. A wedge
//     parks that goroutine inside backing.List, so the obvious cheaper design
//     -- check at the top of the reconcile loop -- is precisely the one that
//     cannot fire in the case it exists for. TestStallWatchdogSpeaksWhileThe
//     ReconcileGoroutineIsParked is the test that would fail if someone moved
//     the check there.
//
// This suite delegates the cadence transition log to
// caching_store_cadence_transition_internal_test.go and the reconcile merge
// itself to the differential suite; it asserts only on stall observability.
//
// Run: go test ./internal/beads/ -run 'TestReconcileInFlight|TestStall'

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blockingListStore parks every full-scan List until the test releases it, and
// signals when it has entered. Distinct from slowListStore (a fixed sleep):
// this one makes the observation window deterministic rather than racing a
// duration, so the test cannot pass by sampling before the pass started.
type blockingListStore struct {
	Store
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newBlockingListStore() *blockingListStore {
	return &blockingListStore{
		Store:   NewMemStore(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

// arm makes subsequent full scans block. Prime also issues a full scan, so
// blocking is opt-in after priming rather than from construction.
func (s *blockingListStore) arm() { s.armed.Store(true) }

func (s *blockingListStore) List(query ListQuery) ([]Bead, error) {
	if query.AllowScan && s.armed.Load() {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.release
	}
	return s.Store.List(query)
}

func primedBlockingCache(t *testing.T) (*CachingStore, *blockingListStore) {
	t.Helper()
	bs := newBlockingListStore()
	cs := NewCachingStoreForTest(bs, nil)
	if err := cs.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	bs.arm()
	return cs, bs
}

// TestReconcileInFlightIsObservableDuringThePass pins claim 1. The assertion
// window is opened by the backing store from inside List, so the pass is
// provably parked when Stats() is read -- a sleep-based version could sample
// after the pass finished and still pass.
func TestReconcileInFlightIsObservableDuringThePass(t *testing.T) {
	cs, bs := primedBlockingCache(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		cs.runReconciliation()
	}()

	select {
	case <-bs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile pass never reached backing.List")
	}

	st := cs.Stats()
	if !st.ReconcileInFlight {
		t.Error("Stats().ReconcileInFlight = false while the pass is parked in backing.List, want true")
	}
	if st.ReconcileStartedAt.IsZero() {
		t.Error("Stats().ReconcileStartedAt is zero while a pass is in flight, want the pass start instant")
	}
	if !st.LastReconcileAt.IsZero() {
		t.Errorf("Stats().LastReconcileAt = %v before any pass completed, want zero", st.LastReconcileAt)
	}

	close(bs.release)
	<-done

	st = cs.Stats()
	if st.ReconcileInFlight {
		t.Error("Stats().ReconcileInFlight = true after the pass returned, want false")
	}
	if !st.ReconcileStartedAt.IsZero() {
		t.Errorf("Stats().ReconcileStartedAt = %v after the pass returned, want zero", st.ReconcileStartedAt)
	}
	if st.LastReconcileAt.IsZero() {
		t.Error("Stats().LastReconcileAt is zero after a successful pass, want the completion instant")
	}
}

// TestStallModesAreMutuallyExclusive pins that the two failure shapes the
// ci-enyk investigation could not separate now decide to different modes from
// one state read, plus the third shape (a reconciler that scanned once and then
// stopped) that is neither. Each case fabricates store state under c.mu
// because the alternative -- reproducing a 29-hour wedge -- is not a test.
func TestStallModesAreMutuallyExclusive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	hourAgo := now.Add(-time.Hour)

	cases := []struct {
		name  string
		setup func(cs *CachingStore)
		want  string
	}{
		{
			// The ci-enyk condition: armed, loop ticking, no full scan ever
			// completed. Held for 29 hours across eight restarts.
			name: "starved reconciler never completed a scan",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = hourAgo
			},
			want: "mode=no-scan-completed",
		},
		{
			// The alternative nobody could exclude: a pass entered
			// backing.List and never came back.
			name: "wedged pass still in flight",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = hourAgo
				cs.reconcileStartedAt = now.Add(-50 * time.Minute)
			},
			want: "mode=pass-in-flight",
		},
		{
			name: "reconciler scanned once then went quiet",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = now.Add(-2 * time.Hour)
				cs.stats.LastReconcileAt = hourAgo
			},
			want: "mode=scan-overdue",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newPrimedCacheForCadenceTest(t)
			cs.mu.Lock()
			defer cs.mu.Unlock()
			tc.setup(cs)

			line, emit := cs.reconcilerStallLogLocked(now)
			if !emit {
				t.Fatalf("no stall line emitted; want %s", tc.want)
			}
			if !strings.Contains(line, tc.want) {
				t.Errorf("line = %q, want it to carry %s", line, tc.want)
			}
			for _, other := range []string{"mode=no-scan-completed", "mode=pass-in-flight", "mode=scan-overdue"} {
				if other != tc.want && strings.Contains(line, other) {
					t.Errorf("line = %q carries %s as well as %s; modes must be exclusive", line, other, tc.want)
				}
			}
		})
	}
}

// TestStallLineCarriesEveryFieldTheOutageNeeded pins the field list from
// ci-dirj gap 1 -- the state that 29 hours of log reported nowhere. Asserting
// the substrings rather than a whole-line golden keeps the test on the contract
// (these facts are present) instead of on the formatting.
func TestStallLineCarriesEveryFieldTheOutageNeeded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	cs := NewCachingStoreForTestWithPrefix(NewMemStore(), "ci", nil)
	if err := cs.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()

	setBeadCountLocked(cs, 1500)
	cs.updateStatsLocked()
	cs.reconcilerArmedAt = now.Add(-time.Hour)
	cs.syncFailures = 3
	cs.stats.SyncFailures = 3
	cs.stats.LastProblemAt = now.Add(-45 * time.Minute)
	cs.markFreshLocked(now.Add(-90 * time.Second))

	line, emit := cs.reconcilerStallLogLocked(now)
	if !emit {
		t.Fatal("no stall line emitted for a store that has never completed a scan")
	}
	// Each value is asserted resolved, not just its key present: "state=" alone
	// would pass on an empty value, which is the failure a field-presence check
	// is most likely to let through.
	for _, want := range []string{
		"rig=ci",             // which store, on a supervisor holding several
		"beads=1500",         // len(c.beads)
		"state=live",         // cacheState
		"cadence=1m0s",       // CurrentReconcileInterval
		"driver=bead-count",  // CadenceDriver
		"last-scan=never",    // LastReconcileAt
		"last-fresh=1m30s",   // LastFreshAt, as an age
		"sync-failures=3",    // SyncFailures
		"last-problem=45m0s", // LastProblemAt, as an age
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line = %q missing %q", line, want)
		}
	}
}

// TestStallWatchdogStaysSilentWhenTheReconcilerIsWorking pins the negative. A
// watchdog that fires on a healthy store trains operators to filter it, which
// costs exactly the signal the 29-hour outage lacked.
func TestStallWatchdogStaysSilentWhenTheReconcilerIsWorking(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name  string
		setup func(cs *CachingStore)
	}{
		{
			// Nothing armed the reconciler: a store the caller drives by hand
			// (every direct-runReconciliation test) has no schedule to miss.
			name: "reconciler never armed",
			setup: func(cs *CachingStore) {
				cs.stats.LastReconcileAt = now.Add(-time.Hour)
			},
		},
		{
			name: "recent successful scan",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = now.Add(-time.Hour)
				cs.stats.LastReconcileAt = now.Add(-time.Second)
			},
		},
		{
			// Freshly armed and inside the first scan's grace: the scan is not
			// due yet, so silence here is correct rather than a missed stall.
			name: "armed within the threshold",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = now.Add(-time.Second)
			},
		},
		{
			// A failing store already logs through the problem path; the
			// watchdog exists for the SILENT cases and must not double-report.
			name: "recent problem report",
			setup: func(cs *CachingStore) {
				cs.reconcilerArmedAt = now.Add(-time.Hour)
				cs.stats.LastProblemAt = now.Add(-time.Second)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newPrimedCacheForCadenceTest(t)
			cs.mu.Lock()
			defer cs.mu.Unlock()
			tc.setup(cs)

			if line, emit := cs.reconcilerStallLogLocked(now); emit {
				t.Errorf("stall line emitted for a healthy store: %q", line)
			}
		})
	}
}

// TestStallLineRateLimitPerModeReportsAModeChangeAtOnce pins that the limiter
// is keyed by mode. A single shared timestamp would hold a store's first
// wedge report for the rest of the window behind an earlier starvation report
// -- suppressing the transition between the two failure shapes, which is the
// most informative event the watchdog can observe.
func TestStallLineRateLimitPerModeReportsAModeChangeAtOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.reconcilerArmedAt = now.Add(-time.Hour)

	if _, emit := cs.reconcilerStallLogLocked(now); !emit {
		t.Fatal("first stall observation was suppressed")
	}
	if line, emit := cs.reconcilerStallLogLocked(now.Add(time.Second)); emit {
		t.Errorf("same mode re-reported inside the window: %q", line)
	}

	cs.reconcileStartedAt = now
	line, emit := cs.reconcilerStallLogLocked(now.Add(2 * time.Second))
	if !emit {
		t.Fatal("mode change to pass-in-flight was suppressed by the previous mode's window")
	}
	if !strings.Contains(line, "mode=pass-in-flight") {
		t.Errorf("line = %q, want mode=pass-in-flight", line)
	}
}

// TestStallWatchdogSpeaksWhileTheReconcileGoroutineIsParked is the load-bearing
// wiring test. It proves the watchdog runs on a goroutine the wedge does not
// take out: the reconcile loop is parked inside backing.List for the whole
// assertion window, so nothing in reconcileLoop could have produced this line.
// Move the check into the loop and this test fails while every other test in
// the file still passes.
//
// It costs one cacheReconcilePollInterval (5 s) of wall clock, because that is
// reconcileLoop's first tick and the loop is what has to enter the blocked
// scan. Driving runReconciliation directly instead would be fast and would
// prove nothing: the claim under test is about which goroutine the wedge parks.
// The store is deliberately NOT primed -- an unprimed store has a scan due
// immediately (nextReconcileDelay returns 0 on a zero lastFreshAt), so the
// loop's first tick is also its first scan.
func TestStallWatchdogSpeaksWhileTheReconcileGoroutineIsParked(t *testing.T) {
	logBuf := captureLog(t)

	bs := newBlockingListStore()
	bs.arm()
	cs := NewCachingStoreForTest(bs, nil)
	// Production periods are 30 s ticks against a 4-cadence (2 min) silence
	// threshold. Only the periods are shortened -- the decision path is the
	// same one production runs.
	cs.SetStallWatchdogForTest(5*time.Millisecond, 20*time.Millisecond)

	cs.StartReconciler(t.Context(), WithStaggerOff(), "stall-watchdog-test")
	// Cleanups run LIFO, so the release registered second runs FIRST, before
	// StopReconciler. That ordering is load-bearing on the FAILURE path: a
	// t.Fatalf below skips the rest of the body, and StopReconciler waits on a
	// loop goroutine still parked in backing.List, so releasing from the body
	// alone would turn a failure into a hang until the go test timeout.
	t.Cleanup(cs.StopReconciler)
	t.Cleanup(func() { close(bs.release) })

	select {
	case <-bs.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile loop never reached backing.List")
	}

	// Wait for the wedge mode specifically. The compressed threshold also trips
	// no-scan-completed during the 5 s before the loop's first scan, which is
	// correct for a just-armed store but is not the claim under test -- waiting
	// on the bare "reconciler stalled" prefix would be satisfied by that line
	// and would pass with the watchdog wired into the reconcile loop.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logBuf.String(), "mode=pass-in-flight") {
		if time.Now().After(deadline) {
			t.Fatalf("no pass-in-flight stall line while the reconcile goroutine was parked in backing.List; output=%q",
				logBuf.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}
