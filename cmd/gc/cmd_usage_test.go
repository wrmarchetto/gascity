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
	line := `{"run_id":"ci-salt","session_id":"ci-salt","worker":"lab__engineer-ci-salt",` +
		`"kind":"model","input_tokens":` + strconv.Itoa(int(salt%9000)+1000) +
		`,"at":` + strconv.FormatInt(salt, 10) + `}` + "\n"
	path := filepath.Join(gcDir, "usage.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
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
	if len(rep.Groups) != 1 || rep.Groups[0].Key != "lab.engineer" {
		t.Fatalf("groups = %+v, want one lab.engineer row", rep.Groups)
	}
	want := int(salt%9000) + 1000
	if rep.Groups[0].Tokens.InputTokens != want {
		t.Fatalf("InputTokens = %d, want the salted %d", rep.Groups[0].Tokens.InputTokens, want)
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
