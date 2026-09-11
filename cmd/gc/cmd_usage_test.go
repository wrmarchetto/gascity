// Tests for the gc usage projection.
//
// Scope: rendering and the end-to-end read of a planted .gc/usage.jsonl. The
// aggregation itself is pinned in internal/usageattr; what is checked here is
// that the observed/not-observed distinction the aggregation carries SURVIVES
// into what a reader actually sees. A report that models absence correctly and
// then prints it as 0.0 has published the same fiction.
//
// Delegated elsewhere: fact parsing and de-duplication (internal/usage), the
// grouping and window arithmetic (internal/usageattr), and the bead-store scope
// resolution (shared with gc bd).
//
// What it cannot represent: a live store. BeadsOf is injected, so a change in
// how the claim path stamps gc.session_id goes green here and is caught only by
// running the command against the real city -- which the recorded baseline is.
//
// Run: go test ./cmd/gc/ -run TestUsage
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/usage"
	"github.com/gastownhall/gascity/internal/usageattr"
)

func usageTestReport(t *testing.T, facts []usage.Fact, opts usageattr.Options) usageattr.Report {
	t.Helper()
	return usageattr.Aggregate(facts, opts)
}

// TestUsageTextRendersAbsenceAsADashNotZero is the reason this layer has its own
// test. gc costs prints 0.0 wall-seconds for the 1,533 sessions that never
// recorded a compute fact; a reader cannot tell that from a session that ran
// instantly. The dash is the whole point of the column.
func TestUsageTextRendersAbsenceAsADashNotZero(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "lab__engineer-ci-a",
		Kind: usage.KindModel, InputTokens: 11, OutputTokens: 22,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{By: usageattr.BySession})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{UsagePath: "/c/.gc/usage.jsonl", BeadScope: "rig/gascity"})
	out := buf.String()

	line := usageLineContaining(t, out, "ci-a")
	if !strings.Contains(line, "-") {
		t.Fatalf("session row shows no absence marker for wall-clock:\n%s", line)
	}
	if strings.Contains(line, "0.0") {
		t.Fatalf("session row renders unobserved wall-clock as a zero reading:\n%s", line)
	}
}

// TestUsageTextRendersAbsentCountsAsADashToo covers the INTEGER columns, which
// the float test above cannot reach: a session with compute facts and no model
// facts has no invocation or token reading at all. core.control-dispatcher is
// exactly that shape in this city (3 sessions, wall-clock only), and printing 0
// invocations for it would say the agent ran and did nothing.
//
// Added after a mutation sweep: flipping usageInt's absent branch to "0" left
// the whole suite green, because every row in the other tests still carried a
// dash from the wall-clock column.
func TestUsageTextRendersAbsentCountsAsADashToo(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "core__control-dispatcher-ci-a",
		Kind: usage.KindCompute, WallSeconds: 12.5,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{By: usageattr.BySession})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{})
	line := usageLineContaining(t, buf.String(), "ci-a")

	fields := strings.Fields(line)
	// GROUP SESSIONS INVOCATIONS IN OUT CACHE_R CACHE_C WALL_S SPAN_S
	if len(fields) < 9 {
		t.Fatalf("row has %d fields, want at least 9:\n%s", len(fields), line)
	}
	for i, name := range []string{"INVOCATIONS", "IN", "OUT", "CACHE_R", "CACHE_C"} {
		if got := fields[2+i]; got != usageAbsent {
			t.Fatalf("%s = %q, want %q: no model fact was recorded for this session",
				name, got, usageAbsent)
		}
	}
	if fields[7] != "12.5" {
		t.Fatalf("WALL_S = %q, want 12.5", fields[7])
	}
}

// TestUsageTextStatesBothWindowPairs: a requested window wider than the log is
// only partly covered. The header has to carry the request AND the coverage, or
// a baseline recorded from this output cannot be compared to anything later --
// the comparison would not know what it was actually taken over.
func TestUsageTextStatesBothWindowPairs(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "w-ci-a",
		Kind: usage.KindModel, InputTokens: 1,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{
		By:    usageattr.BySession,
		Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{UsagePath: "/c/.gc/usage.jsonl", BeadScope: "rig/gascity"})
	out := buf.String()

	for _, want := range []string{"2026-09-01", "2026-10-01", "2026-09-10", "requested", "observed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("header is missing %q:\n%s", want, out)
		}
	}
}

// TestUsageTextSaysWhenNothingWasObserved: an empty window must not render as a
// table of zeros with a total of zero. "Nothing ran" and "nothing was recorded"
// are different findings and only one of them is about the fleet.
func TestUsageTextSaysWhenNothingWasObserved(t *testing.T) {
	rep := usageTestReport(t, nil, usageattr.Options{
		By:    usageattr.BySession,
		Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{UsagePath: "/c/.gc/usage.jsonl", BeadScope: "rig/gascity"})
	out := buf.String()

	if !strings.Contains(out, "no usage facts") {
		t.Fatalf("empty window does not say so:\n%s", out)
	}
	if strings.Contains(out, "TOTAL") {
		t.Fatalf("empty window still printed a totals row:\n%s", out)
	}
}

// TestUsageJSONEncodesAbsenceAsNull: a consumer that reads the JSON and sums
// wall_seconds must not silently add zeros for sessions that never reported. A
// null forces the question; a 0 answers it wrongly.
func TestUsageJSONEncodesAbsenceAsNull(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "lab__engineer-ci-a",
		Kind: usage.KindModel, InputTokens: 11,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{By: usageattr.BySession})

	raw, err := json.Marshal(usageJSONView(rep, usageSource{UsagePath: "/c/.gc/usage.jsonl", BeadScope: "rig/gascity"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Groups []struct {
			Key         string   `json:"key"`
			WallSeconds *float64 `json:"wall_seconds"`
			InputTokens *int     `json:"input_tokens"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(decoded.Groups))
	}
	if decoded.Groups[0].WallSeconds != nil {
		t.Fatalf("wall_seconds = %v, want null: no compute fact was recorded", *decoded.Groups[0].WallSeconds)
	}
	if decoded.Groups[0].InputTokens == nil || *decoded.Groups[0].InputTokens != 11 {
		t.Fatalf("input_tokens = %v, want 11", decoded.Groups[0].InputTokens)
	}
}

// TestUsageJSONKeepsAMeasuredZeroAsZero is the negative control for the test
// above: if absence is null, a real zero has to stay 0, or the encoding has just
// moved the ambiguity rather than removed it.
func TestUsageJSONKeepsAMeasuredZeroAsZero(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "lab__engineer-ci-a",
		Kind: usage.KindCompute, WallSeconds: 0,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{By: usageattr.BySession})

	raw, err := json.Marshal(usageJSONView(rep, usageSource{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Groups []struct {
			WallSeconds *float64 `json:"wall_seconds"`
			InputTokens *int     `json:"input_tokens"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Groups[0].WallSeconds == nil {
		t.Fatalf("wall_seconds = null, want 0: a 0.0s compute fact is a measurement")
	}
	if *decoded.Groups[0].WallSeconds != 0 {
		t.Fatalf("wall_seconds = %v, want 0", *decoded.Groups[0].WallSeconds)
	}
	if decoded.Groups[0].InputTokens != nil {
		t.Fatalf("input_tokens = %v, want null: no model fact was recorded", *decoded.Groups[0].InputTokens)
	}
}

// TestUsageTextAnnouncesTruncation: the session grouping has ~1,700 rows here,
// so the table is a head. A head that does not say it is one reads as the whole
// fleet, and the rows below the cut are exactly the long tail of small agents an
// efficiency review is looking for.
func TestUsageTextAnnouncesTruncation(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	facts := []usage.Fact{
		{RunID: "ci-a", SessionID: "ci-a", Worker: "w-ci-a", Kind: usage.KindModel, InputTokens: 100, At: base},
		{RunID: "ci-b", SessionID: "ci-b", Worker: "w-ci-b", Kind: usage.KindModel, InputTokens: 10, At: base},
	}
	rep := usageTestReport(t, facts, usageattr.Options{By: usageattr.BySession, Top: 1})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{})
	out := buf.String()

	if !strings.Contains(out, "1 more") {
		t.Fatalf("truncation not announced:\n%s", out)
	}
	total := usageLineContaining(t, out, "TOTAL")
	if !strings.Contains(total, "110") {
		t.Fatalf("TOTAL covers only the shown rows, want the full 110:\n%s", total)
	}
}

// TestUsageTextNamesTheBeadScopeItConsulted: "(no bead recorded)" is only
// readable next to WHICH store was asked. A multi-rig city holds most sessions'
// beads in another rig's store, so the same row means "not this rig's work" far
// more often than it means "unattributed".
func TestUsageTextNamesTheBeadScopeItConsulted(t *testing.T) {
	rep := usageTestReport(t, []usage.Fact{{
		RunID: "ci-a", SessionID: "ci-a", Worker: "lab__engineer-ci-a",
		Kind: usage.KindModel, InputTokens: 1,
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}}, usageattr.Options{By: usageattr.ByBead})

	var buf bytes.Buffer
	renderUsageText(&buf, rep, usageSource{BeadScope: "rig/gascity"})
	out := buf.String()

	if !strings.Contains(out, "rig/gascity") {
		t.Fatalf("bead scope not named:\n%s", out)
	}
	if !strings.Contains(out, usageattr.UnattributedBeadKey) {
		t.Fatalf("unattributed session not shown:\n%s", out)
	}
}

// TestUsageReadsThePlantedLogEndToEnd drives the file read, so a change in what
// the emitter writes -- a renamed field, a unit change -- shows up here rather
// than only in a hand-built Fact. The salt is the process start time so a stale
// file left by an earlier run cannot satisfy it.
func TestUsageReadsThePlantedLogEndToEnd(t *testing.T) {
	dir := t.TempDir()
	gcDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(gcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	salt := time.Now().UnixMilli()
	// Two facts in one session and a third in another, so the read exercises
	// per-session accumulation rather than a single pass-through. A sweep found
	// the one-fact version green against an aggregation that never reused an
	// account at all.
	first := int(salt%9000) + 1000
	second := int(salt%700) + 300
	other := int(salt%400) + 100
	lines := `{"run_id":"ci-salt","session_id":"ci-salt","worker":"lab__engineer-ci-salt",` +
		`"kind":"model","input_tokens":` + strconv.Itoa(first) +
		`,"at":` + strconv.FormatInt(salt, 10) + `,"idempotency_key":"k1"}` + "\n" +
		`{"run_id":"ci-salt","session_id":"ci-salt","worker":"lab__engineer-ci-salt",` +
		`"kind":"model","input_tokens":` + strconv.Itoa(second) +
		`,"at":` + strconv.FormatInt(salt+5000, 10) + `,"idempotency_key":"k2"}` + "\n" +
		`{"run_id":"ci-other","session_id":"ci-other","worker":"lab__pm-ci-other",` +
		`"kind":"model","input_tokens":` + strconv.Itoa(other) +
		`,"at":` + strconv.FormatInt(salt, 10) + `,"idempotency_key":"k3"}` + "\n"
	path := filepath.Join(gcDir, "usage.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	facts, warnings, err := usage.ReadFacts(path)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	rep := usageattr.Aggregate(facts, usageattr.Options{By: usageattr.ByType})
	byKey := map[string]usageattr.Group{}
	for _, g := range rep.Groups {
		byKey[g.Key] = g
	}
	eng, ok := byKey["lab.engineer"]
	if !ok {
		t.Fatalf("groups = %+v, want a lab.engineer row", rep.Groups)
	}
	if eng.Tokens.InputTokens != first+second {
		t.Fatalf("lab.engineer InputTokens = %d, want the salted %d (both facts of one session)",
			eng.Tokens.InputTokens, first+second)
	}
	if len(eng.Sessions) != 1 {
		t.Fatalf("lab.engineer Sessions = %v, want the two facts folded into one session", eng.Sessions)
	}
	if !eng.Span.Observed || eng.Span.Seconds != 5 {
		t.Fatalf("lab.engineer Span = %+v, want an observed 5s span between the two facts", eng.Span)
	}
	pm, ok := byKey["lab.pm"]
	if !ok {
		t.Fatalf("groups = %+v, want a lab.pm row", rep.Groups)
	}
	if pm.Tokens.InputTokens != other {
		t.Fatalf("lab.pm InputTokens = %d, want the salted %d", pm.Tokens.InputTokens, other)
	}
	if rep.Sessions != 2 {
		t.Fatalf("Sessions = %d, want 2", rep.Sessions)
	}
}

func usageLineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", needle, out)
	return ""
}
