package beads

// internal/beads/caching_store_reconcile_stall.go
//
// The reconciler's stall watchdog: the one thing in this package that reports a
// reconciler which has stopped reporting on itself.
//
// Every other signal the reconciler emits is written after backing.List
// returns -- the success line, the problem line, the latency window, the bd
// trace record. A pass that never returns therefore writes nothing anywhere,
// and neither does a loop whose next scan is never due. Those two failures are
// indistinguishable from outside the process: no success line, no problem line,
// stale data. The city store held the second of them for 29 hours across eight
// restarts (bead ci-enyk) and the first could only be excluded by argument,
// because no instrument separated them (bead ci-dirj).
//
// WHY THIS IS A SEPARATE GOROUTINE. The obvious cheaper design is a check at
// the top of reconcileLoop, which already wakes every 5 s. It cannot work:
// reconcileLoop calls runReconciliation inline, so a backing.List that blocks
// forever parks the loop goroutine itself. The watchdog must not share a
// goroutine with the thing it watches. It also must not touch the backing
// store -- it reads only cached state under c.mu, so the wedge it reports
// cannot spread to it.
//
// EDITING CONSTRAINT: nothing here may call into c.backing, and nothing here
// may change reconciler behavior. It reports; it does not restart, reschedule,
// or cancel. A watchdog that acted would be framework intelligence, and the
// action it would take (drop the pass, spawn another) is unsafe while the
// abandoned pass may still be holding a bd subprocess.
//
// Verified by caching_store_reconcile_stall_internal_test.go, whose
// TestStallWatchdogSpeaksWhileTheReconcileGoroutineIsParked is what fails if
// the check is ever moved back into reconcileLoop.

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	// cacheReconcileStallFactor is how many effective cadence intervals of
	// total reconciler silence trip the watchdog.
	//
	// Sized against the period rather than picked: the earliest a healthy store
	// can produce its first success line is armedAt + max(stagger, interval) +
	// scan duration, and stagger is bounded by cacheReconcileIntervalSmall. At
	// SMALL that earliest instant is ~60 s, so a factor of 4 (120 s) leaves
	// ~60 s of headroom for the scan itself. A scan exceeding that at SMALL
	// cadence is itself worth a line, and reports as pass-in-flight, which is
	// accurate rather than a false positive.
	cacheReconcileStallFactor = 4

	// cacheReconcileWatchdogInterval is how often the watchdog samples. Slower
	// than cacheReconcilePollInterval on purpose: the check can only produce a
	// line once per cacheReconcileStallLogWindow, so sampling at the reconcile
	// loop's rate would be five wakeups wasted for every one that could speak.
	// Granularity stays well inside the smallest threshold (120 s).
	cacheReconcileWatchdogInterval = cacheReconcileIntervalSmall

	// cacheReconcileStallLogWindow rate-limits the stall line per mode. Wider
	// than cacheProblemLogWindow because a stall persists for as long as the
	// condition does -- at one line per minute a 29-hour outage would add
	// ~1700 lines. Five minutes keeps any log tail longer than that landing on
	// one while leaving the rest of the log readable.
	cacheReconcileStallLogWindow = 5 * time.Minute
)

// SetStallWatchdogForTest overrides the stall watchdog's sample period and its
// silence threshold so a test can observe the real goroutine inside a test
// timeout instead of the production 30 s / 4-cadence periods. Only the periods
// change -- the decision path is the one production runs. Call before
// StartReconciler. Test-only; production never mutates these.
func (c *CachingStore) SetStallWatchdogForTest(interval, threshold time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stallWatchdogInterval = interval
	c.stallThresholdOverride = threshold
}

// reconcilerStallWatchdog samples reconciler state until ctx is canceled and
// logs whenever the reconciler has gone silent past its threshold. Owned by
// StartReconciler; runs on its own goroutine for the reason in the file header.
func (c *CachingStore) reconcilerStallWatchdog(ctx context.Context) {
	c.mu.RLock()
	interval := c.stallWatchdogInterval
	c.mu.RUnlock()
	if interval <= 0 {
		interval = cacheReconcileWatchdogInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// The write lock is needed for the rate limiter's bookkeeping, not for
		// the read. It is held across in-memory comparisons and a Sprintf only;
		// a wedged pass holds no lock while parked in backing.List, so this
		// cannot be blocked by the condition it reports.
		c.mu.Lock()
		line, emit := c.reconcilerStallLogLocked(time.Now())
		c.mu.Unlock()
		if emit {
			log.Print(line)
		}
	}
}

// reconcilerStallLogLocked returns (line, true) when the reconciler has
// produced no operator-visible evidence for longer than its stall threshold,
// or ("", false) otherwise. Caller must hold c.mu (write lock).
//
// "Evidence" is the later of LastReconcileAt and LastProblemAt, falling back to
// reconcilerArmedAt before either exists. Anchoring on lastFreshAt instead
// would be wrong for the same reason nextReconcileDelay may not use it:
// markFreshLocked runs on every write, event apply and cache-absorbing read, so
// on a busy store it advances whether or not the reconciler is alive -- which
// is exactly the blindness that let the ci-enyk starvation hide.
//
// Suppressing while syncFailures > 0 on the grounds that the problem path
// already logs was rejected: a pass that wedges after a failure leaves
// syncFailures non-zero and LastProblemAt frozen forever, which is the precise
// case this exists to catch. The consequence is that a store in deep
// sync-failure backoff (up to cacheReconcileMaxBackoff, wider than the
// threshold at every tier) trips the watchdog once per backoff cycle; the line
// carries sync-failures and last-problem so the cause is on its face.
func (c *CachingStore) reconcilerStallLogLocked(now time.Time) (string, bool) {
	// A store nobody armed has no schedule to miss. Every test that drives
	// runReconciliation by hand, and every non-reconciling cache, lands here.
	if c.reconcilerArmedAt.IsZero() {
		return "", false
	}

	quietSince := c.stats.LastReconcileAt
	if c.stats.LastProblemAt.After(quietSince) {
		quietSince = c.stats.LastProblemAt
	}
	if quietSince.IsZero() {
		quietSince = c.reconcilerArmedAt
	}
	quiet := now.Sub(quietSince)
	if quiet <= c.stallThresholdLocked() {
		return "", false
	}

	// The three modes are exclusive and ordered by what they rule out. An
	// in-flight pass is checked first because it explains the silence outright;
	// a never-completed scan is the ci-enyk shape; scan-overdue is a reconciler
	// that worked once and then stopped, which is neither of the two the
	// investigation was choosing between.
	mode := "scan-overdue"
	inFlight := "no"
	switch {
	case !c.reconcileStartedAt.IsZero():
		mode = "pass-in-flight"
		inFlight = cacheAgeLabel(c.reconcileStartedAt, now)
	case c.stats.LastReconcileAt.IsZero():
		mode = "no-scan-completed"
	}

	rig := c.idPrefix
	if rig == "" {
		rig = "(no-prefix)"
	}
	driver := c.stats.CadenceDriver
	if driver == "" {
		driver = "default"
	}
	msg := fmt.Sprintf(
		"beads cache: reconciler stalled rig=%s mode=%s quiet=%s in-flight=%s beads=%d "+
			"state=%s cadence=%s driver=%s last-scan=%s last-fresh=%s last-problem=%s sync-failures=%d",
		rig, mode, quiet.Round(time.Second), inFlight, len(c.beads),
		cacheStateLabel(c.state), c.stats.CurrentReconcileInterval, driver,
		cacheAgeLabel(c.stats.LastReconcileAt, now),
		cacheAgeLabel(c.stats.LastFreshAt, now),
		cacheAgeLabel(c.stats.LastProblemAt, now),
		c.stats.SyncFailures,
	)

	if c.stallLog == nil {
		c.stallLog = make(map[string]cacheProblemLogState)
	}
	// Keyed by mode, not by store: a starved reconciler that then wedges must
	// report the new mode immediately rather than waiting out the previous
	// mode's window, because that transition is the most informative thing the
	// watchdog can observe.
	return rateLimitLogLocked(c.stallLog, mode, msg, now, cacheReconcileStallLogWindow)
}

// stallThresholdLocked returns the silence duration that trips the watchdog.
// Caller must hold c.mu.
//
// Derived from the CURRENT cadence, not the cadence in force when the last scan
// ran, which has one narrow consequence worth knowing: a tier demotion (a mass
// close dropping a store from LARGE to SMALL) shrinks the threshold under a gap
// that was legitimate at the old tier, so one scan-overdue line can appear for
// the ~5 s until the loop's next poll scans. Recording the per-scan cadence to
// avoid that was rejected -- it buys accuracy in a self-correcting, once-per-
// demotion case at the cost of a second timestamp to keep consistent.
func (c *CachingStore) stallThresholdLocked() time.Duration {
	if c.stallThresholdOverride > 0 {
		return c.stallThresholdOverride
	}
	return cacheReconcileStallFactor * c.adaptiveIntervalLocked()
}

// cacheAgeLabel renders how long ago at was, or "never" for a zero instant.
// Ages rather than timestamps because the reader's question during an outage is
// how long this has been true, and an age makes the stall self-evident without
// subtracting the log's own timestamp. Negative ages clamp to zero so a clock
// step backwards cannot print a future age.
func cacheAgeLabel(at, now time.Time) string {
	if at.IsZero() {
		return "never"
	}
	d := now.Sub(at)
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}
