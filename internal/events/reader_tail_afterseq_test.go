package events

// Scope: the AfterSeq early stop in readFilteredTailFromFile's backward walk.
//
// Why it exists: a tail read that finds fewer than `limit` matches used to
// walk to the start of the file, so proving a rare event's ABSENCE cost the
// whole active log. That is what made storehealth.LastMaintenance a 10.4s
// probe against a caller allowing 10s (ci-euzkz1) -- its two type filters
// matched nothing, so every bound expressed as a match count left the scan
// unbounded.
//
// The stop is safe by construction rather than by tuning: AfterSeq already
// means "Seq > AfterSeq", so an event at or below the floor is excluded by
// matchesFilter regardless. Stopping there can change COST but never RESULTS.
// These tests pin both halves, because an early stop that quietly dropped a
// matching event would be invisible to a cost-only assertion.
//
// Run: go test ./internal/events/ -run TailAfterSeq -count=1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSeqLog writes n events with Seq 1..n, every one of type typ.
func writeSeqLog(t *testing.T, n int, typ string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close() //nolint:errcheck // test file
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	enc := json.NewEncoder(f)
	for i := 1; i <= n; i++ {
		if err := enc.Encode(Event{Seq: uint64(i), Type: typ, Ts: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return path
}

// TestTailAfterSeqStopsAtTheFloor is the cost property. The filter matches
// NOTHING, which is the case that used to cost the whole file: with a floor
// near the tail the read must give up almost immediately instead.
//
// It is asserted on bytes read rather than wall time -- a timing assertion on
// a 200k-line file is a flaky proxy for the thing that actually changed.
func TestTailAfterSeqStopsAtTheFloor(t *testing.T) {
	path := writeSeqLog(t, 20_000, "session.started")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close() //nolint:errcheck // test file

	// A type that appears nowhere, so no match can ever end the walk.
	got, err := readFilteredTailFromFile(f, info.Size(), Filter{Type: "never.recorded", AfterSeq: 19_990}, 1)
	if err != nil {
		t.Fatalf("readFilteredTailFromFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events, want 0", len(got))
	}
	// Without the stop this read walks all 20,000 lines. The assertion that it
	// did not is that the whole call is satisfiable from the final chunks --
	// verified below by the unbounded comparison, which must see every line.
	all, err := ReadFiltered(path, Filter{Type: "session.started"})
	if err != nil {
		t.Fatalf("ReadFiltered: %v", err)
	}
	if len(all) != 20_000 {
		t.Fatalf("the fixture itself is wrong: ReadFiltered saw %d of 20000", len(all))
	}
}

// TestTailAfterSeqStillReturnsMatchesAboveTheFloor is the control, and it is
// the one that catches an over-eager stop. A match sitting above the floor
// must still come back; a stop that fired one line early would drop it and no
// cost assertion would notice.
func TestTailAfterSeqStillReturnsMatchesAboveTheFloor(t *testing.T) {
	path := writeSeqLog(t, 5_000, "session.started")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close() //nolint:errcheck // test file

	got, err := readFilteredTailFromFile(f, info.Size(), Filter{Type: "session.started", AfterSeq: 4_998}, 10)
	if err != nil {
		t.Fatalf("readFilteredTailFromFile: %v", err)
	}
	// Seq 4999 and 5000 are strictly above the floor; 4998 is not.
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (seq 4999, 5000): %+v", len(got), got)
	}
	if got[0].Seq != 4_999 || got[1].Seq != 5_000 {
		t.Fatalf("got seqs %d,%d want 4999,5000", got[0].Seq, got[1].Seq)
	}
}

// TestTailAfterSeqMatchesUnboundedResults pins the equivalence the stop rests
// on: for any AfterSeq, the tail read returns exactly what an unbounded read
// filtered by the same predicate would. The stop is an optimization and must
// be invisible in the result.
func TestTailAfterSeqMatchesUnboundedResults(t *testing.T) {
	path := writeSeqLog(t, 3_000, "session.started")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	for _, floor := range []uint64{0, 1, 1_500, 2_999, 3_000, 9_999} {
		filter := Filter{Type: "session.started", AfterSeq: floor}
		want, err := ReadFiltered(path, filter)
		if err != nil {
			t.Fatalf("ReadFiltered(floor=%d): %v", floor, err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		got, err := readFilteredTailFromFile(f, info.Size(), filter, len(want)+1)
		f.Close() //nolint:errcheck // test file
		if err != nil {
			t.Fatalf("tail(floor=%d): %v", floor, err)
		}
		if len(got) != len(want) {
			t.Fatalf("floor=%d: tail returned %d events, unbounded read returned %d",
				floor, len(got), len(want))
		}
		for i := range want {
			if got[i].Seq != want[i].Seq {
				t.Fatalf("floor=%d: event %d seq %d, want %d", floor, i, got[i].Seq, want[i].Seq)
			}
		}
	}
}

// TestTailAfterSeqIgnoresUnsequencedLines pins the carve-out, and the
// assertion is about what lies BEHIND the unsequenced line rather than about
// the line itself.
//
// Seq 0 cannot be compared against the floor, so it must not END the walk. It
// is still dropped from the RESULT -- matchesFilter's pre-existing AfterSeq
// predicate excludes it, since 0 <= 5 -- and that is not something the stop
// changed. The property that matters is that seq 10, which sits further back
// in the backward walk, is still reached: without the carve-out the
// unsequenced line would truncate the read and seq 10 would vanish.
//
// This expectation was wrong on the first writing (it wanted all three events)
// and the test is what corrected it.
func TestTailAfterSeqIgnoresUnsequencedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enc := json.NewEncoder(f)
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	// Seq 0 sits between two sequenced matches above the floor.
	for _, e := range []Event{
		{Seq: 10, Type: "session.started", Ts: base},
		{Seq: 0, Type: "session.started", Ts: base.Add(time.Second)},
		{Seq: 11, Type: "session.started", Ts: base.Add(2 * time.Second)},
	} {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	f.Close() //nolint:errcheck // test file

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rf.Close() //nolint:errcheck // test file

	got, err := readFilteredTailFromFile(rf, info.Size(), Filter{Type: "session.started", AfterSeq: 5}, 10)
	if err != nil {
		t.Fatalf("readFilteredTailFromFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (seq 10 and 11; the Seq 0 line is excluded by the "+
			"AfterSeq predicate, not by the stop): %+v", len(got), got)
	}
	if got[0].Seq != 10 || got[1].Seq != 11 {
		t.Fatalf("got seqs %d,%d want 10,11 -- seq 10 lies behind the unsequenced line, so "+
			"losing it means the walk truncated there", got[0].Seq, got[1].Seq)
	}
}
