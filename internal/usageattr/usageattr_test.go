// Tests for per-session usage attribution.
//
// Scope: the aggregation itself -- window bounding, the observed/not-observed
// distinction, agent-type derivation from a session name, and the session-to-
// bead join. It delegates fact parsing to internal/usage (ReadFacts has its own
// suite) and the table/JSON rendering to cmd/gc/cmd_usage_test.go.
//
// The suite exists because every number this package produces is an input to a
// before/after comparison across weeks (epic:agent-efficiency criterion 4). A
// silently-zeroed unobserved reading and a genuine zero are indistinguishable
// once they reach that comparison, so most of what follows pins that one
// distinction rather than the arithmetic.
//
// What it cannot represent: a real .gc/usage.jsonl. Facts here are constructed,
// so a change in what the emitter WRITES -- a dropped field, a unit change --
// goes green here. cmd/gc/cmd_usage_test.go reads a planted file end to end,
// and the recorded baseline is the check against live data.
//
// Run: go test ./internal/usageattr/
package usageattr

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/usage"
)

func at(ms int64) int64 { return ms }

func modelFact(session, worker string, ms int64, in, out, cacheRead, cacheCreate int) usage.Fact {
	return usage.Fact{
		RunID: session, SessionID: session, Worker: worker,
		Kind:                usage.KindModel,
		InputTokens:         in,
		OutputTokens:        out,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		At:                  at(ms),
	}
}

func computeFact(session, worker string, ms int64, wall float64) usage.Fact {
	return usage.Fact{
		RunID: session, SessionID: session, Worker: worker,
		Kind:        usage.KindCompute,
		WallSeconds: wall,
		At:          at(ms),
	}
}

// TestWallClockAbsenceIsNotZero is the suite's reason for existing. 1,533 of
// 1,740 sessions in this city's log carry model facts and no compute fact
// (measured 2026-09-10), so a report that renders the missing wall-clock as 0.0
// -- which gc costs does -- states a measurement that was never taken. The
// assertion is on Observed, not on Seconds: a Seconds check alone passes
// whether the field means "measured zero" or "never measured".
func TestWallClockAbsenceIsNotZero(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 1_000, 10, 20, 30, 40),
	}, Options{By: BySession})

	if len(rep.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(rep.Groups))
	}
	g := rep.Groups[0]
	if g.Wall.Observed {
		t.Fatalf("Wall.Observed = true, want false: no compute fact was recorded for this session")
	}
	if !g.Tokens.Observed {
		t.Fatalf("Tokens.Observed = false, want true")
	}
	if rep.Totals.Wall.Observed {
		t.Fatalf("Totals.Wall.Observed = true, want false")
	}
}

// TestMeasuredZeroWallClockStaysObserved is the other half of the same
// distinction, and the one a "did we record anything" implementation fails: a
// compute fact of 0.0 seconds is a reading, not an absence.
func TestMeasuredZeroWallClockStaysObserved(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		computeFact("ci-a", "lab__engineer-ci-a", 1_000, 0),
	}, Options{By: BySession})

	g := rep.Groups[0]
	if !g.Wall.Observed {
		t.Fatalf("Wall.Observed = false, want true: a 0.0s compute fact is a measurement")
	}
	if g.Wall.Seconds != 0 {
		t.Fatalf("Wall.Seconds = %v, want 0", g.Wall.Seconds)
	}
	if g.Tokens.Observed {
		t.Fatalf("Tokens.Observed = true, want false: no model fact was recorded")
	}
}

// TestObservedSpanIsALowerBound pins the span as the interval BETWEEN the first
// and last model fact, never a session duration. Startup before the first
// invocation and teardown after the last are outside it, so the value is a
// floor; the field name and this test are what stop it being read as wall-clock
// once compute facts are absent (which they are, for 88% of sessions).
func TestObservedSpanIsALowerBound(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 10_000, 1, 1, 0, 0),
		modelFact("ci-a", "lab__engineer-ci-a", 25_000, 1, 1, 0, 0),
	}, Options{By: BySession})

	g := rep.Groups[0]
	if !g.Span.Observed {
		t.Fatalf("Span.Observed = false, want true")
	}
	if g.Span.Seconds != 15 {
		t.Fatalf("Span.Seconds = %v, want 15 (25s - 10s)", g.Span.Seconds)
	}
	if g.Span.Facts != 2 {
		t.Fatalf("Span.Facts = %d, want 2", g.Span.Facts)
	}
}

// TestSingleFactSpanIsUnobserved: one fact bounds nothing. Reporting 0 seconds
// would be indistinguishable from a session that really did all its work inside
// one millisecond, and there are 1,533 single-invocation-shaped sessions where
// that difference decides whether a latency figure means anything.
func TestSingleFactSpanIsUnobserved(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 10_000, 1, 1, 0, 0),
	}, Options{By: BySession})

	if rep.Groups[0].Span.Observed {
		t.Fatalf("Span.Observed = true, want false: a single fact bounds no interval")
	}
}

// TestWindowExcludesFactsOutsideBounds pins the window as closed-open on the
// upper end so two adjacent windows over the same log neither drop nor
// double-count a fact at the boundary -- the property criterion 4's
// before/after comparison rests on.
func TestWindowExcludesFactsOutsideBounds(t *testing.T) {
	facts := []usage.Fact{
		modelFact("ci-early", "w-ci-early", 500, 1, 0, 0, 0),
		modelFact("ci-in", "w-ci-in", 1_000, 2, 0, 0, 0),
		modelFact("ci-edge", "w-ci-edge", 2_000, 4, 0, 0, 0),
		modelFact("ci-late", "w-ci-late", 2_500, 8, 0, 0, 0),
	}
	rep := Aggregate(facts, Options{
		By:    BySession,
		Since: time.UnixMilli(1_000),
		Until: time.UnixMilli(2_000),
	})
	if rep.Totals.Tokens.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want 2 (only the fact at the inclusive lower bound)",
			rep.Totals.Tokens.InputTokens)
	}
	if rep.Window.FactsInWindow != 1 {
		t.Fatalf("FactsInWindow = %d, want 1", rep.Window.FactsInWindow)
	}
	if rep.Window.FactsOutsideWindow != 3 {
		t.Fatalf("FactsOutsideWindow = %d, want 3", rep.Window.FactsOutsideWindow)
	}
}

// TestWindowReportsObservedBoundsSeparatelyFromRequested: a requested window
// that reaches past the log's own extent is only partly covered, and a report
// that echoes the request as if it were the coverage overstates it. Both pairs
// are carried so the reader can see the gap.
func TestWindowReportsObservedBoundsSeparatelyFromRequested(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "w-ci-a", 5_000, 1, 0, 0, 0),
		modelFact("ci-b", "w-ci-b", 7_000, 1, 0, 0, 0),
	}, Options{
		By:    BySession,
		Since: time.UnixMilli(0),
		Until: time.UnixMilli(1_000_000),
	})
	if !rep.Window.Observed {
		t.Fatalf("Window.Observed = false, want true")
	}
	if got := rep.Window.FirstFactAt.UnixMilli(); got != 5_000 {
		t.Fatalf("FirstFactAt = %d ms, want 5000", got)
	}
	if got := rep.Window.LastFactAt.UnixMilli(); got != 7_000 {
		t.Fatalf("LastFactAt = %d ms, want 7000", got)
	}
	if rep.Window.Since.UnixMilli() != 0 || rep.Window.Until.UnixMilli() != 1_000_000 {
		t.Fatalf("requested bounds were rewritten to the observed ones")
	}
}

// TestEmptyWindowIsUnobserved: a window containing no fact must not render as a
// row of zeros. The whole point of recording a baseline with its bounds stated
// is that a later comparison can tell "nothing ran" from "nothing was recorded".
func TestEmptyWindowIsUnobserved(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "w-ci-a", 5_000, 1, 0, 0, 0),
	}, Options{
		By:    BySession,
		Since: time.UnixMilli(100_000),
		Until: time.UnixMilli(200_000),
	})
	if rep.Window.Observed {
		t.Fatalf("Window.Observed = true, want false")
	}
	if len(rep.Groups) != 0 {
		t.Fatalf("groups = %d, want 0", len(rep.Groups))
	}
	if rep.Totals.Tokens.Observed || rep.Totals.Wall.Observed {
		t.Fatalf("totals claim an observation inside an empty window")
	}
}

// TestAgentTypeStripsTheSessionSuffixItActuallyCarries pins the derivation on
// the two fields rather than on a session-id-shaped pattern. This is the same
// rule the city console's tokens-by-role panel uses, deliberately: the two
// analytics are meant to be read side by side, and a second, cleverer rule here
// would put the same rows under two different role names.
func TestAgentTypeStripsTheSessionSuffixItActuallyCarries(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 1_000, 1, 0, 0, 0),
		modelFact("ci-b", "mayor", 1_000, 1, 0, 0, 0),
		modelFact("ci-c", "core__control-dispatcher-ci-c", 1_000, 1, 0, 0, 0),
	}, Options{By: ByType})

	got := map[string]bool{}
	for _, g := range rep.Groups {
		got[g.Key] = true
		if g.SuffixUnmatched {
			t.Fatalf("group %q flagged SuffixUnmatched, want matched", g.Key)
		}
	}
	for _, want := range []string{"lab.engineer", "mayor", "core.control-dispatcher"} {
		if !got[want] {
			t.Fatalf("type %q missing from %v", want, got)
		}
	}
}

// TestUnmatchedSessionSuffixIsFlaggedNotRepaired: an adhoc session name carries
// a tag that is not its session id (`toolsmith-codex-adhoc-e1546d8930` ran in
// `ci-wisp-rkib0rl`), so the suffix rule cannot strip it. Trimming to the
// longest configured agent name that prefixes it WOULD recover the rows, and is
// rejected: it merges a session into an agent on the strength of a name, and the
// same shape is how a role that never existed acquires traffic. The row is kept
// verbatim and flagged instead, which is what makes the residue countable.
// Measured 2026-09-10: 6,378 of 83,304 rows are in this state.
func TestUnmatchedSessionSuffixIsFlaggedNotRepaired(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-wisp-rkib0rl", "toolsmith-codex-adhoc-e1546d8930", 1_000, 1, 0, 0, 0),
	}, Options{By: ByType})

	g := rep.Groups[0]
	if g.Key != "toolsmith-codex-adhoc-e1546d8930" {
		t.Fatalf("Key = %q, want the worker name verbatim", g.Key)
	}
	if !g.SuffixUnmatched {
		t.Fatalf("SuffixUnmatched = false, want true")
	}
	if rep.Residue.SuffixUnmatchedSessions != 1 {
		t.Fatalf("Residue.SuffixUnmatchedSessions = %d, want 1", rep.Residue.SuffixUnmatchedSessions)
	}
}

// TestBeadJoinComesFromTheStoreNotTheFact: no production emitter sets Fact.StepID
// -- model usage is attributed at run level and per-step attribution was retired
// with the gc.active_work_bead pointer (internal/worker/invocation_telemetry.go).
// So the only bead link is the reverse one the claim path writes durably onto the
// work bead, and this test pins that direction. If it ever passes off a populated
// StepID, the join has been quietly re-pointed at a field nothing fills.
func TestBeadJoinComesFromTheStoreNotTheFact(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 1_000, 7, 0, 0, 0),
		modelFact("ci-b", "lab__engineer-ci-b", 1_000, 9, 0, 0, 0),
	}, Options{
		By:      ByBead,
		BeadsOf: map[string][]string{"ci-a": {"gs-1"}},
	})

	var withBead, without *Group
	for i := range rep.Groups {
		switch rep.Groups[i].Key {
		case "gs-1":
			withBead = &rep.Groups[i]
		case UnattributedBeadKey:
			without = &rep.Groups[i]
		}
	}
	if withBead == nil {
		t.Fatalf("no group for gs-1: %+v", rep.Groups)
	}
	if withBead.Tokens.InputTokens != 7 {
		t.Fatalf("gs-1 InputTokens = %d, want 7", withBead.Tokens.InputTokens)
	}
	if without == nil {
		t.Fatalf("session ci-b vanished; a session no bead names must surface as %q", UnattributedBeadKey)
	}
	if without.Tokens.InputTokens != 9 {
		t.Fatalf("unattributed InputTokens = %d, want 9", without.Tokens.InputTokens)
	}
	if rep.Residue.SessionsWithNoBead != 1 {
		t.Fatalf("Residue.SessionsWithNoBead = %d, want 1", rep.Residue.SessionsWithNoBead)
	}
}

// TestSharedSessionIsCountedOncePerBeadAndOnceInTotal: 9 of 161 joined sessions
// in this city name two beads (measured 2026-09-10). Splitting a session's
// tokens between them would invent a ratio no record supports, so each bead row
// carries the whole session -- which makes the rows overlap. The totals are
// therefore accumulated over DISTINCT sessions, not by summing the rows, and
// this test is the one that fails if someone "simplifies" the total back into a
// row sum.
func TestSharedSessionIsCountedOncePerBeadAndOnceInTotal(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 1_000, 5, 0, 0, 0),
	}, Options{
		By:      ByBead,
		BeadsOf: map[string][]string{"ci-a": {"gs-1", "gs-2"}},
	})

	if len(rep.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(rep.Groups))
	}
	for _, g := range rep.Groups {
		if g.Tokens.InputTokens != 5 {
			t.Fatalf("group %q InputTokens = %d, want the whole session (5)", g.Key, g.Tokens.InputTokens)
		}
		if !g.SharesSessions {
			t.Fatalf("group %q SharesSessions = false, want true", g.Key)
		}
	}
	if rep.Totals.Tokens.InputTokens != 5 {
		t.Fatalf("Totals.InputTokens = %d, want 5; the rows overlap and must not be summed",
			rep.Totals.Tokens.InputTokens)
	}
	if rep.Residue.SessionsNamingSeveralBeads != 1 {
		t.Fatalf("Residue.SessionsNamingSeveralBeads = %d, want 1", rep.Residue.SessionsNamingSeveralBeads)
	}
}

// TestSessionsCarryTheirAgentTypeUnderEveryGrouping: reading a bead's cost is
// only actionable next to the agent that incurred it, and the bead grouping is
// where a per-bead-per-type comparison (criterion 4) is taken from.
func TestSessionsCarryTheirAgentTypeUnderEveryGrouping(t *testing.T) {
	rep := Aggregate([]usage.Fact{
		modelFact("ci-a", "lab__engineer-ci-a", 1_000, 5, 0, 0, 0),
	}, Options{By: ByBead, BeadsOf: map[string][]string{"ci-a": {"gs-1"}}})

	g := rep.Groups[0]
	if len(g.AgentTypes) != 1 || g.AgentTypes[0] != "lab.engineer" {
		t.Fatalf("AgentTypes = %v, want [lab.engineer]", g.AgentTypes)
	}
	if len(g.Sessions) != 1 || g.Sessions[0] != "ci-a" {
		t.Fatalf("Sessions = %v, want [ci-a]", g.Sessions)
	}
}

// TestTruncationNeverChangesTheTotals: the session grouping has ~1,700 rows in
// this city, so the CLI shows a head. A total recomputed from the shown rows
// would shrink with the display limit and read as a drop in usage.
func TestTruncationNeverChangesTheTotals(t *testing.T) {
	facts := []usage.Fact{
		modelFact("ci-a", "w-ci-a", 1_000, 100, 0, 0, 0),
		modelFact("ci-b", "w-ci-b", 1_000, 10, 0, 0, 0),
		modelFact("ci-c", "w-ci-c", 1_000, 1, 0, 0, 0),
	}
	full := Aggregate(facts, Options{By: BySession})
	cut := Aggregate(facts, Options{By: BySession, Top: 1})

	if cut.Totals.Tokens.InputTokens != full.Totals.Tokens.InputTokens {
		t.Fatalf("truncated total = %d, full total = %d",
			cut.Totals.Tokens.InputTokens, full.Totals.Tokens.InputTokens)
	}
	if len(cut.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(cut.Groups))
	}
	if cut.Groups[0].Key != "ci-a" {
		t.Fatalf("kept %q, want the largest row ci-a", cut.Groups[0].Key)
	}
	if cut.GroupsOmitted != 2 {
		t.Fatalf("GroupsOmitted = %d, want 2", cut.GroupsOmitted)
	}
	if full.GroupsOmitted != 0 {
		t.Fatalf("full.GroupsOmitted = %d, want 0", full.GroupsOmitted)
	}
}

// TestFactsWithNoTimestampAreCountedNotSilentlyBinned: `at` is stamped by the
// emitter, so a fact without one cannot be placed in any window. Dropping it
// silently understates the window; placing it at the epoch corrupts the observed
// bounds. It is excluded and counted.
func TestFactsWithNoTimestampAreCountedNotSilentlyBinned(t *testing.T) {
	undated := modelFact("ci-a", "w-ci-a", 0, 5, 0, 0, 0)
	undated.At = 0
	rep := Aggregate([]usage.Fact{
		undated,
		modelFact("ci-b", "w-ci-b", 1_000, 3, 0, 0, 0),
	}, Options{By: BySession})

	if rep.Totals.Tokens.InputTokens != 3 {
		t.Fatalf("InputTokens = %d, want 3", rep.Totals.Tokens.InputTokens)
	}
	if rep.Window.FactsUndated != 1 {
		t.Fatalf("FactsUndated = %d, want 1", rep.Window.FactsUndated)
	}
}
