// Cooldown scheduling across many patrol ticks, not one predicate call.
//
// WHY THIS SUITE EXISTS SEPARATELY from the table-driven cases in
// triggers_test.go. The defect it pins (ci-tv57qh) is a FEEDBACK LOOP: each
// dispatch records a last-run timestamp taken after the dispatcher's own
// write latency, and that offset then sets the next deadline, so the phase
// error is re-established on every run instead of decaying. A single
// checkCooldown call cannot represent that -- it has no previous run of its
// own making -- and the table cases in triggers_test.go all pass on the
// defective code. Only driving the tick loop, with the recorded last-run fed
// back the way the dispatcher feeds it back, goes red.
//
// The loop here mirrors cmd/gc/order_dispatch.go: the remembered last-run is
// the tracking bead's CreatedAt, written after this order's own gates and
// Dolt reads, and `now` is the instant the order is EVALUATED. The gap
// between the two is dispatchWriteLatency below.
//
// THAT SECOND CLAUSE CHANGED, and with it what the constant means. `now` used
// to be the tick anchor, frozen before the ring walk, so the gap carried the
// cost of every order dispatched ahead of this one as well as its own write
// -- and on a ring that had grown to 41 enabled cooldown orders that sum
// crossed the 5s allowance a 30s order gets, costing the three fastest orders
// about a quarter of their cycles (ci-l2n4i6). The dispatcher now differences
// against the wall clock at the walk position, which cancels the shared
// prefix; the residual this suite models is the write this order does itself.
// dispatchWriteLatency was measured on the old basis and is therefore a
// conservative upper bound on the new one, which is the safe direction for a
// suite whose whole job is to prove the allowance covers it. It is NOT a
// current measurement of the residual and must not be cited as one.
//
// The cmd/gc-side case is TestCooldownDeadlineIsNotChargedTheRingWalkAheadOfIt,
// which drives the real ring walk; this suite structurally cannot, because it
// hands checkCooldown a `now` and a lastRun directly and so has no walk to
// charge.
//
// WHAT THIS SUITE CANNOT REPRESENT, so it does not pretend to: the poke ticks
// that arm from event traffic. Live, they land between patrol ticks and let a
// starved order fire early, which is why the measured spacing for a 30s order
// has a 31-58s tail under a 60s mode rather than a clean 60s. Modeling them
// would flatter the defect, because they partially mask it. The loop is
// patrol-only, which is the cadence a clock-driven schedule must be met by.
//
// Run: go test ./internal/orders/ -run Cooldown
package orders

import (
	"testing"
	"time"
)

// livePatrolTick is the measured patrol period of the city this defect was
// found on: 193 dispatch clusters over 90 minutes, inter-cluster gap p50 29s,
// p75 30s, p90 32s (ci-tv57qh). It is NOT read from
// config.DaemonConfig.PatrolIntervalDuration() on purpose -- a test that
// recomputes its grid from the same default the dispatcher reads cannot catch
// that default changing underneath the schedule.
const livePatrolTick = 30 * time.Second

// dispatchWriteLatency is the delay between a tick's `now` and the CreatedAt
// of the tracking bead that tick writes. This is the p90 for the LAST order
// dispatched in a tick, off the controller trace (site_code orders.dispatch,
// n=214 ticks, 2026-09-08): position 0 p50 0.83s / p90 2.38s rising to
// position 7 p50 2.89s / p90 3.62s.
//
// THE P90 AND NOT THE MEAN, deliberately. An earlier draft of this suite used
// 1.06s, an aggregate computed as each dispatch's offset from its own tick's
// first write -- which fixes the first dispatch of every tick at zero by
// construction and so understates the gap. Sizing the allowance against that
// mean left the three 30s orders, the ones already worst hit, relapsing
// whenever they landed late in a tick's candidate order. A suite pinned to a
// mean cannot see a tail, and the tail is the whole failure mode.
//
// It must also be NONZERO for this suite to have any power. At exactly zero
// the defective code passes every case below, because every configured
// interval in the city is an exact multiple of the tick and elapsed lands
// exactly on the interval. Zero is the one value that hides the bug, which is
// why the pre-existing table cases in triggers_test.go -- all of which
// construct lastRun as now minus a round number -- went green over it for as
// long as it existed.
const dispatchWriteLatency = 3620 * time.Millisecond

// runPatrolLoop drives count patrol ticks at period tick and returns the
// times at which the order was dispatched.
//
// lastRun is fed back as (evaluation instant + latency), which is what the
// dispatcher durably records. tick is passed through TriggerOptions as the
// grid period, leaving the slack sizing where the implementation owns it.
//
// The evaluation instant is the tick here, with no walk offset, and the
// absence is deliberate rather than an omission: a constant offset added to
// every tick cancels out of the difference and would model nothing, and a
// VARYING one is the dispatcher-side concern that
// TestCooldownDeadlineIsNotChargedTheRingWalkAheadOfIt owns.
func runPatrolLoop(a Order, start time.Time, tick, latency time.Duration, count int) []time.Time {
	var lastRun time.Time
	var fires []time.Time
	lastRunFn := func(string) (time.Time, error) { return lastRun, nil }
	for i := 0; i < count; i++ {
		now := start.Add(time.Duration(i) * tick)
		res := CheckTriggerWithOptions(a, now, lastRunFn, nil, nil, TriggerOptions{PatrolInterval: tick})
		if res.Due {
			fires = append(fires, now)
			lastRun = now.Add(latency)
		}
	}
	return fires
}

// achievedPeriod reports the mean spacing between dispatches, or zero when
// there were fewer than two. The first fire is the "never run" case and
// carries no spacing information, so it is excluded by construction.
func achievedPeriod(fires []time.Time) time.Duration {
	if len(fires) < 2 {
		return 0
	}
	span := fires[len(fires)-1].Sub(fires[0])
	return span / time.Duration(len(fires)-1)
}

// A cooldown order whose interval is an exact multiple of the patrol tick must
// be dispatched on the tick its interval elapses, not the tick after.
//
// This is the whole defect. Every one of the 37 enabled cooldown orders in the
// city configures an interval that is an exact multiple of 30s, and the
// dispatcher's own write latency is folded into the next deadline, so at the
// tick where the interval is due elapsed reads (interval - latency) and falls
// short by a hair. The order then waits a full further tick. Effective period
// is interval + one tick for EVERY order, which is why the measured shortfall
// was uniform rather than concentrated in whichever orders a per-tick budget
// starved: 30s orders delivered at 0.51 of nominal, 300s at 0.90, and
// sum(60/(interval+30)) predicted 8.15 dispatches/min against 8.229 measured.
//
// The expectations are re-derived from interval and tick rather than written
// as the literals the live city happens to produce. A literal 60s here would
// agree with a fix that made the period "one tick" instead of "the interval",
// which for the 30s orders is the same number.
func TestCooldownHonorsItsIntervalDespiteDispatchWriteLatency(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval string
		want     time.Duration
	}{
		{"one tick", "30s", 30 * time.Second},
		{"two ticks", "60s", 60 * time.Second},
		{"four ticks", "120s", 120 * time.Second},
		{"ten ticks", "300s", 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Order{Name: "sweep", Trigger: "cooldown", Interval: tc.interval}
			// Enough ticks to see at least ten dispatches at the
			// configured interval, so a one-tick error is unambiguous
			// in the mean rather than an artifact of the last cycle.
			ticks := 10*int(tc.want/livePatrolTick) + 2
			start := time.Date(2026, 9, 8, 20, 23, 17, 0, time.UTC)
			fires := runPatrolLoop(a, start, livePatrolTick, dispatchWriteLatency, ticks)
			got := achievedPeriod(fires)
			if got != tc.want {
				t.Errorf("achieved period = %v over %d dispatches, want %v (interval %s on a %v tick); a %v error is the one-tick phase lock",
					got, len(fires), tc.want, tc.interval, livePatrolTick, got-tc.want)
			}
		})
	}
}

// Slack must never let the patrol grid dispatch an order EARLY.
//
// The fix admits an order whose remaining time is under a slack allowance, and
// the failure mode that buys is an order running faster than configured --
// which on the three 30s orders would double the load they put on Dolt. The
// guarantee that rules it out is slack < tick: on a patrol grid of period P an
// interval of k*P can only be met at tick k or later, never k-1, so long as
// the slack cannot span a whole tick.
//
// Asserted against a latency of ZERO, which is the adversarial case for this
// direction: with no write latency the deadline lands exactly on a tick, so
// any slack at all is pure margin, and a slack of a full tick or more would
// fire one tick early here while the test above still passed.
func TestCooldownSlackNeverDispatchesEarly(t *testing.T) {
	for _, interval := range []string{"30s", "60s", "120s", "300s", "900s"} {
		t.Run(interval, func(t *testing.T) {
			want, err := time.ParseDuration(interval)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", interval, err)
			}
			slack := defaultCooldownSlack(livePatrolTick, want)
			if slack >= livePatrolTick {
				t.Fatalf("slack %v >= tick %v: a slack that spans a whole tick can dispatch on the previous tick", slack, livePatrolTick)
			}
			a := Order{Name: "sweep", Trigger: "cooldown", Interval: interval}
			ticks := 10*int(want/livePatrolTick) + 2
			start := time.Date(2026, 9, 8, 20, 23, 17, 0, time.UTC)
			fires := runPatrolLoop(a, start, livePatrolTick, 0, ticks)
			if got := achievedPeriod(fires); got < want {
				t.Errorf("achieved period = %v over %d dispatches, want no faster than %v; slack %v dispatched the order early",
					got, len(fires), want, slack)
			}
		})
	}
}

// An order whose interval is NOT a multiple of the tick keeps paying the
// grid's own rounding, and the slack must not be read as a fix for that.
//
// The city has no such order today -- all 37 enabled cooldown intervals are
// multiples of 30s -- and that absence is why the defect above presented as a
// uniform one-tick delay rather than as a spread. This case is here so the
// distinction survives the first order authored at, say, 45s: a 45s interval
// on a 30s grid can only be served at 60s, and no slack sized under a tick
// changes that. If a future editor widens slack until this test goes red, they
// have made the slack able to dispatch early, which
// TestCooldownSlackNeverDispatchesEarly then also catches.
func TestCooldownOffGridIntervalStillRoundsUpToTheTick(t *testing.T) {
	a := Order{Name: "odd", Trigger: "cooldown", Interval: "45s"}
	want := 45 * time.Second
	slack := defaultCooldownSlack(livePatrolTick, want)
	start := time.Date(2026, 9, 8, 20, 23, 17, 0, time.UTC)
	fires := runPatrolLoop(a, start, livePatrolTick, dispatchWriteLatency, 42)
	got := achievedPeriod(fires)
	// ceil(45s / 30s) * 30s
	wantRounded := 60 * time.Second
	if got != wantRounded {
		t.Errorf("achieved period = %v over %d dispatches, want %v -- a 45s interval on a %v grid rounds up, and slack %v must not pretend otherwise",
			got, len(fires), wantRounded, livePatrolTick, slack)
	}
}

// The slack allowance is bounded in both directions by construction.
//
// Pinned as a property rather than a table of outputs because the two bounds
// are what make the fix safe, and a table would go green over a formula that
// happened to hit the same numbers for the intervals the city uses today while
// violating a bound for one it does not.
func TestDefaultCooldownSlackStaysInsideItsTwoBounds(t *testing.T) {
	for _, tick := range []time.Duration{5 * time.Second, 10 * time.Second, livePatrolTick, 2 * time.Minute} {
		for _, interval := range []time.Duration{
			10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute,
			15 * time.Minute, 30 * time.Minute, 90 * time.Minute, 6 * time.Hour,
		} {
			slack := defaultCooldownSlack(tick, interval)
			if slack < 0 {
				t.Errorf("tick %v interval %v: slack %v is negative", tick, interval, slack)
			}
			// Bound 1, the safety bound: under a whole tick, so the
			// patrol grid can never serve an interval a tick early.
			if slack >= tick {
				t.Errorf("tick %v interval %v: slack %v is not under one tick", tick, interval, slack)
			}
			// Bound 2, the rate bound: a poke tick landing inside the
			// slack window fires the order early by at most this share
			// of its interval, so the long-run rate cannot exceed
			// 1/(interval-slack).
			if slack*int64AsDuration(cooldownSlackIntervalDivisor) > interval {
				t.Errorf("tick %v interval %v: slack %v exceeds interval/%d",
					tick, interval, slack, cooldownSlackIntervalDivisor)
			}
		}
	}
}

func int64AsDuration(n int) time.Duration { return time.Duration(n) }

// A latency past the allowance still costs a whole tick, and that residual is
// pinned here rather than left for the next reader to discover.
//
// WHY PIN A KNOWN-BAD CASE instead of widening the allowance until it passes.
// The allowance cannot exceed one tick without letting the patrol grid serve
// an interval early (TestCooldownSlackNeverDispatchesEarly), so no value of
// cooldownSlackIntervalDivisor covers a 16s stall -- and the two orders most
// exposed to one are dolt-health and beads-health, whose own gate reads and
// tracking-bead write go against the store whose health they report, behind
// two serial gates at orderGateTimeout=8s each. Under Dolt degradation they
// will still slip a tick. Removing that needs the cooldown clock to stop
// being a timestamp written after the work, which is a change to the bead
// write path (cmd/gc/order_dispatch.go:781) and is not attempted here.
//
// If a future editor moves the cooldown clock to the tick's own `now`, THIS
// TEST IS THE ONE THAT SHOULD GO RED -- it is the assertion carrying that
// expiry, so the change cannot land silently while the comment above it
// rots into a false statement.
func TestCooldownStillSlipsATickWhenLatencyExceedsTheAllowance(t *testing.T) {
	a := Order{Name: "dolt-health", Trigger: "cooldown", Interval: "30s"}
	interval := 30 * time.Second
	// A gate stall, not ordinary dispatch work: past the allowance by
	// construction, so the assertion below cannot be satisfied by a
	// rounding coincidence.
	stalled := defaultCooldownSlack(livePatrolTick, interval) + time.Second
	start := time.Date(2026, 9, 8, 20, 23, 17, 0, time.UTC)
	fires := runPatrolLoop(a, start, livePatrolTick, stalled, 42)
	got := achievedPeriod(fires)
	want := interval + livePatrolTick
	if got != want {
		t.Errorf("achieved period = %v at a %v write latency, want %v; the residual one-tick slip past the allowance is expected and pinned -- if this now reads %v the cooldown clock stopped carrying the write latency and the comments naming that residual are stale",
			got, stalled, want, interval)
	}
}

// The allowance tracks the CONFIGURED patrol grid, not the 30s this city runs.
//
// patrol_interval is operator-configurable
// (config.DaemonConfig.PatrolIntervalDuration), and every other case in this
// suite drives the 30s grid the defect was measured on -- so on its own the
// suite would go green over a fix that happened to work only at 30s. Driven
// here at 10s, where the same write latency is a LARGER share of the tick.
//
// A 10s INTERVAL ON THIS 10s GRID IS ABSENT, and the absence is the point.
// There the allowance is interval/6 = 1.67s, under the 3.62s write latency,
// so such an order would still slip a tick and the fix does not help it. The
// uncovered region is interval == tick on any grid faster than about 22s,
// which is where interval/6 drops below the latency. This city does not enter
// it -- patrol is 30s and the fastest interval is 30s, giving 5s -- and it is
// left uncovered rather than bought with a wider allowance because widening
// past tick/2 is what lets the grid dispatch early
// (TestCooldownSlackNeverDispatchesEarly). The Fatalf guard below is what
// stops a future editor re-adding the case and reading a vacuous pass as
// coverage.
func TestCooldownHonorsItsIntervalOnANonDefaultPatrolGrid(t *testing.T) {
	const tick = 10 * time.Second
	for _, tc := range []struct {
		interval string
		want     time.Duration
	}{
		{"30s", 30 * time.Second},
		{"120s", 120 * time.Second},
	} {
		t.Run(tc.interval, func(t *testing.T) {
			// The allowance is min(tick/2, interval/6) = 5s at this
			// grid for every interval here, so a latency at the 30s
			// grid's p90 is still covered and the case is a real
			// test of the sizing rather than of a wider margin.
			if slack := defaultCooldownSlack(tick, tc.want); slack <= dispatchWriteLatency {
				t.Fatalf("slack %v does not cover the %v write latency; this case cannot distinguish the fix from the defect", slack, dispatchWriteLatency)
			}
			a := Order{Name: "sweep", Trigger: "cooldown", Interval: tc.interval}
			ticks := 10*int(tc.want/tick) + 2
			start := time.Date(2026, 9, 8, 20, 23, 17, 0, time.UTC)
			fires := runPatrolLoop(a, start, tick, dispatchWriteLatency, ticks)
			if got := achievedPeriod(fires); got != tc.want {
				t.Errorf("achieved period = %v over %d dispatches, want %v (interval %s on a %v tick)",
					got, len(fires), tc.want, tc.interval, tick)
			}
		})
	}
}
