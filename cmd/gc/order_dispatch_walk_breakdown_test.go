package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// Scope: the ring walk's per-order cost breakdown, the diagnostic that says
// WHICH order is spending the orders.dispatch phase.
//
// Why the suite exists. The phase measured 7.4-7.6s p50 on 2026-09-14 against
// a 5s allowance for a 30s order, so the three 30s orders deliver ~1.6/min
// against 2.00 demanded (city bead ci-hefnfv). Two leads were on the table and
// neither could be settled from the data available: the phase was recorded as
// ONE duration for all 42 orders, so localizing it meant either an operator
// experiment that disables four orders for an hour, or subtraction. This
// breakdown replaces both with a direct reading.
//
// What it delegates elsewhere: the cooldown-clock correction and fire rates
// are TestCooldownDeadlineIsNotChargedTheRingWalkAheadOfIt's, in
// order_dispatch_test.go, which drives the same injected clock. Nothing here
// asserts a dispatch decision -- the breakdown is a diagnostic and must not
// change what fires.
//
// What this suite CANNOT represent: the cost of the LAST order evaluated in a
// tick. The breakdown is derived from offsets the walk already reads, and
// taking one further clock read to close that gap would both be charged to the
// order it measures and perturb the injected clock this suite and its sibling
// drive. The blind spot is asserted here rather than hidden, so nobody reads a
// missing row as a cheap order.
//
// Run: go test ./cmd/gc/ -run RingWalkBreakdown

// jumpClock advances a fixed step per call, except on one call index where it
// advances by jump instead. That makes exactly one ring position expensive,
// which a uniform-step clock cannot express -- and a uniform clock would leave
// the "names the slow order" assertion satisfiable by naming any order at all.
type jumpClock struct {
	mu       sync.Mutex
	tick     time.Time
	step     time.Duration
	jump     time.Duration
	jumpCall int
	calls    int
	last     time.Time
}

func (c *jumpClock) startTick(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick = at
	c.calls = 0
	c.last = at
}

func (c *jumpClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.last
	if c.calls > 0 {
		if c.calls == c.jumpCall {
			at = c.last.Add(c.jump)
		} else {
			at = c.last.Add(c.step)
		}
	}
	c.calls++
	c.last = at
	return at
}

func (c *jumpClock) stamp() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// stderr is collected through cmd_supervisor_test.go's lockedBuffer: the walk
// and the dispatchOne goroutines it launches write concurrently.

// runRingWalkForBreakdown drives one ring walk over `fillers` cheap orders
// whose per-order cost is `step`, with the order at ring position `jumpAt`
// costing `jump` instead, and returns what the walk wrote to stderr.
func runRingWalkForBreakdown(t *testing.T, fillers int, step, jump time.Duration, jumpAt int) string {
	t.Helper()
	clk := &jumpClock{step: step, jump: jump, jumpCall: jumpAt}
	store := beads.NewMemStore()
	store.Clock = clk.stamp

	var aa []orders.Order
	for i := 0; i < fillers; i++ {
		// 6h so each fires once as "never run" and then stays quiet: these
		// exist to cost walk time, not to compete for dispatches.
		aa = append(aa, orders.Order{
			Name: fmt.Sprintf("filler-%02d", i), Trigger: "cooldown",
			Interval: "6h", Exec: "true", NoWorkGate: true,
		})
	}
	ad := buildOrderDispatcherFromListExec(aa, store, nil, func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("ok\n"), nil
	}, nil)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	m := ad.(*memoryOrderDispatcher)
	stderr := &lockedBuffer{}
	m.stderr = stderr
	m.nowFn = clk.now
	// No per-tick budget: this suite measures the whole walk, and a binding
	// budget would rotate the ring and cut it short.
	m.maxDispatchesPerTick = 0

	tickAt := time.Date(2026, 9, 14, 14, 0, 0, 0, time.UTC)
	clk.startTick(tickAt)
	m.dispatch(context.Background(), t.TempDir(), tickAt)
	m.drain(context.Background())
	return stderr.String()
}

// TestRingWalkBreakdownNamesTheExpensiveOrder is the whole point: with one
// ring position costing far more than the rest, the breakdown must say which.
//
// The expensive position is deliberately mid-ring, not last: the last order
// evaluated is the documented blind spot -- see the sibling test -- so a jump
// placed there would be unmeasurable and this arm would pass over a
// breakdown that measured nothing at all.
func TestRingWalkBreakdownNamesTheExpensiveOrder(t *testing.T) {
	const fillers = 12
	// 11 cheap orders at 50ms plus one at 6s puts walkToLast at 6.55s, over
	// the 5s threshold on the slow order's back alone -- the cheap orders
	// contribute 0.55s and could not reach it by themselves. So this arm
	// fails both for a breakdown that never fires and for one that fires on a
	// total the slow order did not cause.
	got := runRingWalkForBreakdown(t, fillers, 50*time.Millisecond, 6*time.Second, 4)
	if !strings.Contains(got, "orderWalk:") {
		t.Fatalf("no orderWalk breakdown emitted; stderr:\n%s", got)
	}
	// filler-02, and the indexing is worth deriving rather than observing.
	// dispatch reads the clock at exactly two sites (order_dispatch.go: the
	// tickWall anchor, then once per loop iteration), so the anchor is call 0
	// and the order at ring index i reads call i+1. jumpCall 4 therefore lands
	// on ring index 3's read. An order's cost is the DIFFERENCE between its
	// own offset and its successor's, so a jump appearing in index 3's read
	// was spent evaluating index 2 -- filler-02, not filler-03.
	//
	// Asserted on the "slowest <name>=" position rather than on a bare
	// substring: every filler name appears in the line, so a substring match
	// would pass over an off-by-one that ranked the wrong order first.
	if !strings.Contains(got, "slowest filler-02=") {
		t.Errorf("orderWalk breakdown does not rank filler-02 slowest; stderr:\n%s", got)
	}
}

// TestRingWalkBreakdownStaysSilentOnACheapWalk pins the threshold. Without
// this arm the breakdown could print every tick for all 42 orders and the
// naming assertion above would still pass, which trades one unreadable
// diagnostic for another -- the failure ci-hefnfv's parent bead already paid
// for once.
func TestRingWalkBreakdownStaysSilentOnACheapWalk(t *testing.T) {
	got := runRingWalkForBreakdown(t, 12, 10*time.Millisecond, 10*time.Millisecond, 4)
	if strings.Contains(got, "orderWalk:") {
		t.Errorf("orderWalk breakdown printed for a walk well under the allowance; stderr:\n%s", got)
	}
}

// TestRingWalkBreakdownDeclaresTheLastOrderBlindSpot pins the honesty of the
// row set rather than its contents. The breakdown cannot measure the last
// order evaluated, and a reader scanning for a missing name must be told that
// rather than concluding the order was cheap.
//
// Asserted on the emitted text, not on a comment, because prose expires
// silently: every step that could notice a stale note declares the file
// untouched.
func TestRingWalkBreakdownDeclaresTheLastOrderBlindSpot(t *testing.T) {
	got := runRingWalkForBreakdown(t, 12, 50*time.Millisecond, 6*time.Second, 4)
	if !strings.Contains(got, "orderWalk:") {
		t.Fatalf("no orderWalk breakdown emitted; stderr:\n%s", got)
	}
	if !strings.Contains(got, "lastUnmeasured=") {
		t.Errorf("orderWalk breakdown does not declare which order it could not measure; stderr:\n%s", got)
	}
}
