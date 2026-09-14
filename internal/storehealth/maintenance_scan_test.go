package storehealth

// Scope: the cost of the store-maintenance probe, as distinct from its result.
// storehealth_test.go already pins WHAT LastMaintenance returns; this file
// pins what it has to READ to return it.
//
// Why this suite exists: ci-euzkz1. LastMaintenance asked the event provider
// for `Filter{Type: ...}` with no Since, no AfterSeq and no Limit, twice per
// call. Type is not a prunable dimension for the archive reader, so every
// gzipped archive plus the active log was decoded in full, both times. On the
// city that found it that was ~1.3 million events and ~1.44 GB of JSON per
// `gc status --json`, costing 10.4-12.7s of CPU against a caller that allows
// 10s -- so the dashboard health panel returned 503, and the whole scan
// matched ZERO events because store maintenance had never run there.
//
// The cost is the defect, so the cost is what these tests measure. A test that
// only checked the returned timestamp passes today at any history size, which
// is exactly how this shipped.
//
// THE STAND-IN COUNTS WHAT WAS CONSUMED, NOT WHAT IT HELD. A provider that
// merely holds 100x the events and is asked for all of them satisfies a
// presence check trivially -- the read would look identical whether or not it
// was bounded. countingProvider records how many events each read had to walk,
// and refuses every method the probe is not supposed to call.
//
// THE ZERO-MATCH CASE IS THE PRIMARY ONE and it is not an edge case: it is the
// state of the city that surfaced this. It also cannot be fixed by a result
// cap alone -- proving no maintenance event exists requires reading everything
// unless the read is bounded by something other than a match count. A fix that
// only short-circuits once a match is found would leave this defect live.
//
// Run: go test ./internal/storehealth/ -run MaintenanceScan -count=1

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

// countingProvider is an events.Provider that reports how much history a read
// walked. Reads honor Since, BeforeSeq, Type and Limit the way the real reader
// does, so a bounded filter genuinely costs less here rather than only looking
// different.
//
// Every method the maintenance probe has no business calling panics rather
// than returning a zero value. A stand-in that answers everything with success
// hands a pass to whatever the suite forgot to script.
type countingProvider struct {
	events []events.Event
	walked int
}

// newCountingProvider builds n events spaced one minute apart ending at now,
// none of them maintenance events. That is the shape of the real city: a large
// history in which the probe's two types never appear.
func newCountingProvider(n int, now time.Time) *countingProvider {
	out := make([]events.Event, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, events.Event{
			Seq:  uint64(i + 1),
			Type: "session.started",
			Ts:   now.Add(-time.Duration(n-i) * time.Minute),
		})
	}
	return &countingProvider{events: out}
}

// withMaintenanceAt inserts a maintenance event at position idx so the result
// tests have something to find.
func (c *countingProvider) withMaintenanceAt(idx int, typ string, ts time.Time) {
	c.events[idx].Type = typ
	c.events[idx].Ts = ts
}

func (c *countingProvider) matches(e events.Event, f events.Filter) bool {
	if f.Type != "" && e.Type != f.Type {
		return false
	}
	if !f.Since.IsZero() && e.Ts.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.Ts.After(f.Until) {
		return false
	}
	if f.AfterSeq > 0 && e.Seq <= f.AfterSeq {
		return false
	}
	if f.BeforeSeq > 0 && e.Seq >= f.BeforeSeq {
		return false
	}
	return true
}

// List walks oldest-first and stops early once Limit matches are found, which
// is what the real reader does -- and is why a Limit alone cannot bound the
// zero-match case.
func (c *countingProvider) List(filter events.Filter) ([]events.Event, error) {
	var out []events.Event
	for _, e := range c.events {
		c.walked++
		if !c.matches(e, filter) {
			continue
		}
		out = append(out, e)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

// ListTail walks newest-first and stops at limit matches. It also stops as
// soon as it passes below Since, which is the property that bounds the
// zero-match case: without it, proving absence still costs the whole log.
func (c *countingProvider) ListTail(filter events.Filter, limit int) ([]events.Event, error) {
	var out []events.Event
	for i := len(c.events) - 1; i >= 0; i-- {
		e := c.events[i]
		c.walked++
		// Seq is monotonic, so the first event at or below the floor ends the
		// walk. This mirrors readFilteredTailFromFile's belowFloor stop; without
		// it the stand-in would keep walking and the cost tests would report a
		// bound the real reader does not have.
		if filter.AfterSeq > 0 && e.Seq > 0 && e.Seq <= filter.AfterSeq {
			break
		}
		if !filter.Since.IsZero() && e.Ts.Before(filter.Since) {
			break
		}
		if !c.matches(e, filter) {
			continue
		}
		out = append([]events.Event{e}, out...)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (c *countingProvider) Record(events.Event) {
	panic("countingProvider: the maintenance probe must not record events")
}

// LatestSeq is scripted because the bounded probe legitimately asks for the
// head of the log to compute its floor. It is counted as one walk so a probe
// that called it in a loop would show up in the cost.
func (c *countingProvider) LatestSeq() (uint64, error) {
	c.walked++
	if len(c.events) == 0 {
		return 0, nil
	}
	return c.events[len(c.events)-1].Seq, nil
}

func (c *countingProvider) Watch(context.Context, uint64) (events.Watcher, error) {
	panic("countingProvider: Watch was not scripted for this probe")
}

func (c *countingProvider) Close() error {
	panic("countingProvider: the maintenance probe must not close the provider")
}

// maintenanceScanCost returns how many events the bounded probe had to walk
// over a history of n events containing no maintenance event at all, with the
// scan bound set to maxEvents.
func maintenanceScanCost(t *testing.T, n int, maxEvents uint64) int {
	t.Helper()
	ep := newCountingProvider(n, time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	ts, status := lastMaintenanceWithin(ep, maxEvents)
	if !ts.IsZero() || status != "" {
		t.Fatalf("history with no maintenance event reported (%v, %q), want (zero, \"\")", ts, status)
	}
	return ep.walked
}

// TestMaintenanceScanOlderHistoryIsFree is the invariant the defect violated,
// stated so that it does not depend on how fast a city produces events.
//
// Both histories carry the same recent window; the second simply has ten times
// as much OLD history behind it. The probe's cost must not notice. An earlier
// version of this test compared total history sizes, which conflates "reads
// too much" with "this city is busy" -- the bound is about age, not volume.
func TestMaintenanceScanOlderHistoryIsFree(t *testing.T) {
	const bound = 1_000

	recent := maintenanceScanCost(t, 5_000, bound)
	ancient := maintenanceScanCost(t, 50_000, bound)

	if ancient > recent*2 {
		t.Fatalf("the maintenance probe pays for old history: walked %d events over a "+
			"5k log and %d over a 50k log with the same %d-event bound. Ten times the "+
			"history must not cost more; see ci-euzkz1, where it cost 10.4s of a 10s "+
			"budget.", recent, ancient, bound)
	}
}

// TestMaintenanceScanIsBoundedInAbsoluteTerms pins the stronger property the
// comparison cannot express: two reads that both scale would pass a ratio
// check if the constant were tuned, and a caller with a 10s budget needs an
// absolute ceiling rather than a favorable ratio.
//
// The ceiling allows one pass per event type over the bound, plus slack for
// the head-seq lookup. A probe that reads the whole 50k log cannot fit.
func TestMaintenanceScanIsBoundedInAbsoluteTerms(t *testing.T) {
	const bound = 1_000
	const ceiling = 2*bound + 10

	if walked := maintenanceScanCost(t, 50_000, bound); walked > ceiling {
		t.Fatalf("the maintenance probe walked %d events of a 50k-event history under "+
			"a %d-event bound; it must stay under %d. Proving no maintenance event "+
			"exists must not cost the whole log.", walked, bound, ceiling)
	}
}

// TestMaintenanceScanStillFindsARecentEvent is the control. Without it the two
// cost tests above are satisfiable by a probe that reads nothing and always
// reports "never", which would be cheap, bounded, and wrong.
func TestMaintenanceScanStillFindsARecentEvent(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	ep := newCountingProvider(50_000, now)
	want := now.Add(-30 * time.Minute)
	ep.withMaintenanceAt(len(ep.events)-30, events.StoreMaintenanceDone, want)

	ts, status := lastMaintenanceWithin(ep, 1_000)
	if !ts.Equal(want) {
		t.Fatalf("ts = %v, want %v (a recent maintenance event must still be found)", ts, want)
	}
	if status != "success" {
		t.Fatalf("status = %q, want success", status)
	}
}

// TestMaintenanceScanPrefersTheLatestOfTwoRecentEvents pins that bounding the
// read did not break the "latest wins" rule across the two event types. This
// is what catches a bounded read that stops at the first match it meets rather
// than the newest.
func TestMaintenanceScanPrefersTheLatestOfTwoRecentEvents(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	ep := newCountingProvider(50_000, now)
	older := now.Add(-40 * time.Minute)
	newer := now.Add(-20 * time.Minute)
	ep.withMaintenanceAt(len(ep.events)-40, events.StoreMaintenanceDone, older)
	ep.withMaintenanceAt(len(ep.events)-20, events.StoreMaintenanceFailed, newer)

	ts, status := lastMaintenanceWithin(ep, 1_000)
	if !ts.Equal(newer) || status != "failed" {
		t.Fatalf("lastMaintenanceWithin = (%v, %q), want (%v, failed)", ts, status, newer)
	}
}

// TestMaintenanceScanReportsAnOutOfWindowEventAsAbsent pins the COST of the
// bound, so it is a decision on the record rather than a surprise. A
// maintenance event older than the window reads as "never".
//
// That narrowing is only ever toward "unknown": the bound cannot report a
// stale success as current, which is the direction that would actually
// mislead. Both zero and a very old timestamp mean the same thing to the
// caller, which omits the field entirely on a zero.
func TestMaintenanceScanReportsAnOutOfWindowEventAsAbsent(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	ep := newCountingProvider(50_000, now)
	ep.withMaintenanceAt(10, events.StoreMaintenanceDone, now.Add(-40_000*time.Minute))

	ts, status := lastMaintenanceWithin(ep, 1_000)
	if !ts.IsZero() || status != "" {
		t.Fatalf("an event far outside the bound reported (%v, %q); the bound is "+
			"documented to narrow toward absent, so this is the behavior to change "+
			"deliberately if it is ever wrong", ts, status)
	}
}

// TestMaintenanceScanUnboundedStillWorks pins the escape hatch: a provider
// whose head seq is unavailable, or a bound of zero, reads everything rather
// than reading nothing. Failing open on the head-seq lookup is deliberate --
// guessing a floor would silently hide events.
func TestMaintenanceScanUnboundedStillWorks(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	ep := newCountingProvider(5_000, now)
	want := now.Add(-4_000 * time.Minute)
	ep.withMaintenanceAt(10, events.StoreMaintenanceDone, want)

	ts, status := lastMaintenanceWithin(ep, 0)
	if !ts.Equal(want) || status != "success" {
		t.Fatalf("unbounded read = (%v, %q), want (%v, success)", ts, status, want)
	}
}
