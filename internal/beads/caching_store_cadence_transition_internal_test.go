package beads

// Scope: the cadence TRANSITION log, as distinct from the cadence VALUE that
// caching_store_cadence_internal_test.go already pins. These tests exist
// because the value being right is not the same claim as the move being
// reported: the transition detector used to live in recomputeCadenceLocked
// while updateCadenceStatsLocked wrote the value, so any stats refresh
// between two reconciles advanced CurrentReconcileInterval first and left the
// detector comparing a tier against itself. A store demonstrably swinging
// 30s<->120s therefore produced no transition line at all (bead ci-dirj, gap
// 2). Every test here drives the helpers under c.mu, mirroring the production
// call sites.
//
// Run: go test ./internal/beads/ -run TestCadenceTransition

import (
	"strings"
	"testing"
	"time"
)

// TestCadenceTransitionLogsWhenStatsRefreshObservesMoveFirst pins that a tier
// move is reported even when a non-reconcile stats refresh observes it before
// the reconcile pass does. This is the exact shape that swallowed the city
// store's transitions: updateStatsLocked runs on every mutation, so on any
// store busier than its own cadence it -- not the reconciler -- is what first
// sees the new bead count.
func TestCadenceTransitionLogsWhenStatsRefreshObservesMoveFirst(t *testing.T) {
	logBuf := captureLog(t)

	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.updateStatsLocked()
	if cs.stats.CurrentReconcileInterval != cacheReconcileIntervalSmall {
		t.Fatalf("setup: interval = %v, want SMALL (%v)",
			cs.stats.CurrentReconcileInterval, cacheReconcileIntervalSmall)
	}

	for i := 0; i < 1500; i++ {
		id := "x" + intToString(i)
		cs.beads[id] = Bead{ID: id, Status: "open"}
	}

	// A mutation refreshes stats between reconciles, then the next reconcile
	// pass recomputes cadence. Both orders must report the move once.
	cs.updateStatsLocked()
	cs.recomputeCadenceLocked()

	out := logBuf.String()
	if n := strings.Count(out, "cadence promoted small→medium"); n != 1 {
		t.Errorf("promote log emitted %d time(s), want exactly 1; output=%q", n, out)
	}
	if !strings.Contains(out, "driver=bead-count") {
		t.Errorf("promote log missing driver=bead-count; output=%q", out)
	}
	if !strings.Contains(out, "beads=1500") {
		t.Errorf("promote log missing beads=1500; output=%q", out)
	}
}

// TestCadenceTransitionLogsEveryTierPair pins that all six ordered tier pairs
// report, not just small<->medium. The old detector was a switch with exactly
// two arms, so a store crossing the 5000-bead LARGE threshold moved its
// cadence silently -- the same blindness as the gap above, from a different
// cause, and invisible in a test that only ever drives latency.
func TestCadenceTransitionLogsEveryTierPair(t *testing.T) {
	pairs := []struct {
		name      string
		fromBeads int
		toBeads   int
		want      string
	}{
		{"small to medium", 0, 1500, "cadence promoted small→medium"},
		{"small to large", 0, 5500, "cadence promoted small→large"},
		{"medium to large", 1500, 5500, "cadence promoted medium→large"},
		{"large to medium", 5500, 1500, "cadence demoted large→medium"},
		{"large to small", 5500, 0, "cadence demoted large→small"},
		{"medium to small", 1500, 0, "cadence demoted medium→small"},
	}
	for _, tc := range pairs {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := captureLog(t)

			cs := newPrimedCacheForCadenceTest(t)
			cs.mu.Lock()
			defer cs.mu.Unlock()

			setBeadCountLocked(cs, tc.fromBeads)
			cs.recomputeCadenceLocked()
			setBeadCountLocked(cs, tc.toBeads)
			cs.recomputeCadenceLocked()

			if out := logBuf.String(); !strings.Contains(out, tc.want) {
				t.Errorf("missing %q; output=%q", tc.want, out)
			}
		})
	}
}

// TestCadenceTransitionOscillationIsRateLimitedNotSilenced pins the bound on
// the cost of reporting from the value writer. beadCountCadence has no
// hysteresis, so a store hovering on a threshold crosses it on every
// create/close pair, and updateStatsLocked runs per mutation -- unbounded
// logging if the line were unlimited. The limiter must cap the rate AND
// surface the suppressed count, because an oscillating tier is itself the
// finding a reader needs; silencing the repeats outright would hide it.
func TestCadenceTransitionOscillationIsRateLimitedNotSilenced(t *testing.T) {
	logBuf := captureLog(t)

	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	const crossings = 6
	for i := 0; i < crossings; i++ {
		setBeadCountLocked(cs, 1500)
		cs.updateStatsLocked()
		setBeadCountLocked(cs, 0)
		cs.updateStatsLocked()
	}

	out := logBuf.String()
	promotes := strings.Count(out, "cadence promoted small→medium")
	demotes := strings.Count(out, "cadence demoted medium→small")
	if promotes != 1 {
		t.Errorf("promote log emitted %d time(s) across %d crossings, want exactly 1 (rate limited); output=%q",
			promotes, crossings, out)
	}
	if demotes != 1 {
		t.Errorf("demote log emitted %d time(s) across %d crossings, want exactly 1 (rate limited); output=%q",
			demotes, crossings, out)
	}

	// Advance past the window and prove the limiter reopens, carrying the
	// count of what it swallowed. A limiter that never reopens would pass the
	// two assertions above while reporting nothing for the rest of the
	// process lifetime.
	for key, state := range cs.cadenceLog {
		state.lastAt = state.lastAt.Add(-2 * cacheCadenceLogWindow)
		cs.cadenceLog[key] = state
	}
	setBeadCountLocked(cs, 1500)
	cs.updateStatsLocked()

	out = logBuf.String()
	if n := strings.Count(out, "cadence promoted small→medium"); n != 2 {
		t.Errorf("promote log emitted %d time(s) after the window elapsed, want 2; output=%q", n, out)
	}
	// The exact count, not just the word: 6 crossings emit the first promote and
	// swallow 5. Asserting on "suppressed" alone would pass with a counter stuck
	// at any value, which is the thing that makes the suffix worth having.
	if !strings.Contains(out, "(suppressed 5 duplicate logs)") {
		t.Errorf("reopened promote log does not report 5 suppressed repeats; output=%q", out)
	}
}

// TestCadenceTierNameFallsBackToDuration pins the behavior of the tier namer
// on a duration that is not one of the three configured tiers. There is no
// fourth tier today and none is planned; the arm exists so that adding one
// degrades the log to a raw duration instead of mislabeling it as a tier it
// is not.
func TestCadenceTierNameFallsBackToDuration(t *testing.T) {
	if got := cadenceTierName(cacheReconcileIntervalSmall); got != "small" {
		t.Errorf("cadenceTierName(SMALL) = %q, want small", got)
	}
	if got := cadenceTierName(cacheReconcileIntervalMedium); got != "medium" {
		t.Errorf("cadenceTierName(MEDIUM) = %q, want medium", got)
	}
	if got := cadenceTierName(cacheReconcileIntervalLarge); got != "large" {
		t.Errorf("cadenceTierName(LARGE) = %q, want large", got)
	}
	if got := cadenceTierName(7 * time.Second); got != "7s" {
		t.Errorf("cadenceTierName(7s) = %q, want the raw duration 7s", got)
	}
}

// setBeadCountLocked resizes the cached bead map to exactly n synthetic rows.
// Internal test territory: production traffic populates via Prime/reconcile,
// but cadence depends only on len(c.beads), so synthesizing the count is the
// cheapest faithful driver. Caller must hold c.mu.
func setBeadCountLocked(cs *CachingStore, n int) {
	for id := range cs.beads {
		delete(cs.beads, id)
	}
	for i := 0; i < n; i++ {
		id := "x" + intToString(i)
		cs.beads[id] = Bead{ID: id, Status: "open"}
	}
}
