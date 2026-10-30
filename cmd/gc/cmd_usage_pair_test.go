package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

func usagePairTestSession(id, slot, account string, created time.Time, usage worker.FirstInvocationUsage) usagePairSession {
	return usagePairSession{
		ID:              id,
		Slot:            slot,
		TranscriptRoot:  account,
		CreatedAt:       created,
		FirstInvocation: usage,
	}
}

// TestUsagePairJSONReportMarksSuccessfulReportingWithoutACacheVerdict also
// holds the hand-written schema to the keys a session row marshals, both ways.
func TestUsagePairJSONReportMarksSuccessfulReportingWithoutACacheVerdict(t *testing.T) {
	raw, err := json.Marshal(usagePairJSONReport{
		SchemaVersion: "1", OK: true,
		Sessions: []usagePairReading{{}},
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if got["ok"] != true {
		t.Fatalf("ok = %#v, want command-success true", got["ok"])
	}
	if _, exists := got["cache_verdict"]; exists {
		t.Fatalf("report must not make a cache verdict: %s", raw)
	}

	var marshaled []string
	for key := range got["sessions"].([]any)[0].(map[string]any) {
		marshaled = append(marshaled, key)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "schemas", "usage", "pair", "result.schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema struct {
		Properties struct {
			Sessions struct {
				Items struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"sessions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	var declared []string
	for key := range schema.Properties.Sessions.Items.Properties {
		declared = append(declared, key)
	}
	required := append([]string(nil), schema.Properties.Sessions.Items.Required...)
	for _, keys := range [][]string{marshaled, declared, required} {
		sort.Strings(keys)
	}
	if strings.Join(marshaled, ",") != strings.Join(declared, ",") ||
		strings.Join(marshaled, ",") != strings.Join(required, ",") {
		t.Fatalf("session row keys %v, schema properties %v, required %v: all three must match",
			marshaled, declared, required)
	}
}

func TestUsagePairStableSlotNormalizesAdHocTemplateInstances(t *testing.T) {
	info := session.Info{
		AgentName: "measurement-adhoc-fc3a2d2f46",
		Template:  "measurement",
	}
	if got := usagePairStableSlot(info); got != "measurement" {
		t.Fatalf("stable slot = %q, want template for ad-hoc instance", got)
	}
}

// TestBuildUsagePairReportRefusesAPairItCannotVouchFor: the read is a second
// consecutive spawn of one slot on one account, so any pair it cannot order or
// prove consecutive is refused rather than reported.
func TestBuildUsagePairReportRefusesAPairItCannotVouchFor(t *testing.T) {
	const slot, root = "gascity/lab.engineer-1", "/accounts/0"
	t0 := time.Date(2026, 10, 29, 21, 33, 5, 0, time.UTC)
	t1 := t0.Add(7 * time.Minute)
	earlier := usagePairTestSession("ci-first", slot, root, t0, worker.FirstInvocationUsage{})
	later := usagePairTestSession("ci-second", slot, root, t1, worker.FirstInvocationUsage{})
	for _, tc := range []struct {
		name  string
		a, b  usagePairSession
		peers []usagePairPeer
		want  string
	}{
		{"the same session twice", earlier, earlier, nil, "one session twice"},
		{
			"mismatched stable slot", earlier,
			usagePairTestSession("ci-second", "gascity/lab.engineer-2", root, t1, worker.FirstInvocationUsage{}),
			nil, "stable slot",
		},
		{
			"mismatched transcript root", earlier,
			usagePairTestSession("ci-second", slot, "/accounts/1", t1, worker.FirstInvocationUsage{}),
			nil, "transcript-root account",
		},
		{
			"no creation time", earlier,
			usagePairTestSession("ci-second", slot, root, time.Time{}, worker.FirstInvocationUsage{}),
			nil, "cannot be ordered",
		},
		{
			"one creation time", earlier,
			usagePairTestSession("ci-second", slot, root, t0, worker.FirstInvocationUsage{}),
			nil, "cannot be ordered",
		},
		{
			"a spawn between them on the pair's root", later, earlier,
			[]usagePairPeer{{ID: "ci-mid", Slot: slot, CreatedAt: t0.Add(3 * time.Minute), TranscriptRoot: root}},
			"ci-mid",
		},
		{
			"a spawn between them on an unknown root", earlier, later,
			[]usagePairPeer{{ID: "ci-mid", Slot: slot, CreatedAt: t0.Add(3 * time.Minute)}},
			"ci-mid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildUsagePairReport(tc.a, tc.b, tc.peers)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestUsagePairSlotPeersListsTheSlotsSpawnsBetweenThePair(t *testing.T) {
	const slot = "gascity/lab.engineer-1"
	t0 := time.Date(2026, 10, 29, 21, 33, 5, 0, time.UTC)
	t1 := t0.Add(7 * time.Minute)
	seed := func(id, agent string, at time.Time) beads.Bead {
		return beads.Bead{
			ID: id, Type: sessionBeadType, Labels: []string{session.LabelSession},
			CreatedAt: at, Metadata: map[string]string{"agent_name": agent},
		}
	}
	// The spawn between is closed, as it almost always is by the time a pair
	// is read.
	mid := seed("ci-mid", slot, t0.Add(3*time.Minute))
	mid.Status = "closed"
	front := sessionFrontDoor(beads.NewMemStoreFrom(1, []beads.Bead{
		seed("ci-first", slot, t0),
		seed("ci-second", slot, t1),
		mid,
		seed("ci-other-slot", "gascity/lab.engineer-2", t0.Add(3*time.Minute)),
		seed("ci-before", slot, t0.Add(-time.Minute)),
		seed("ci-after", slot, t1.Add(time.Minute)),
	}, nil))
	first := usagePairTestSession("ci-first", slot, "/accounts/0", t0, worker.FirstInvocationUsage{})
	second := usagePairTestSession("ci-second", slot, "/accounts/0", t1, worker.FirstInvocationUsage{})

	peers, err := usagePairSlotPeers(t.TempDir(), &config.City{}, front, nil, second, first)
	if err != nil {
		t.Fatalf("usagePairSlotPeers: %v", err)
	}
	if len(peers) != 1 || peers[0].ID != "ci-mid" || !peers[0].CreatedAt.Equal(t0.Add(3*time.Minute)) ||
		peers[0].TranscriptRoot != "" {
		t.Fatalf("peers = %+v, want ci-mid alone, with no transcript root to place it under", peers)
	}
}

func TestUsagePairTranscriptRootUsesMostSpecificConfiguredRoot(t *testing.T) {
	got := usagePairTranscriptRoot(
		[]string{"/accounts", "/accounts/0/transcripts", "/elsewhere"},
		"/accounts/0/transcripts/project/session.jsonl",
	)
	if got != "/accounts/0/transcripts" {
		t.Fatalf("transcript root = %q, want most-specific account root", got)
	}
}

// TestBuildUsagePairReportPreservesAllFirstInvocationInputSides is given the
// pair later-first, with peers that do not break consecutiveness.
func TestBuildUsagePairReportPreservesAllFirstInvocationInputSides(t *testing.T) {
	const slot, root = "gascity/lab.engineer-1", "/accounts/0"
	t0 := time.Date(2026, 10, 29, 21, 33, 5, 0, time.UTC)
	t1 := t0.Add(7 * time.Minute)
	first := usagePairTestSession("ci-first", slot, root, t0, worker.FirstInvocationUsage{InputTokens: 3, CacheReadTokens: 100, CacheCreationTokens: 200})
	second := usagePairTestSession("ci-second", slot, root, t1, worker.FirstInvocationUsage{InputTokens: 4, CacheReadTokens: 300, CacheCreationTokens: 500})
	peers := []usagePairPeer{
		{ID: "ci-first", Slot: slot, CreatedAt: t0, TranscriptRoot: root},
		{ID: "ci-second", Slot: slot, CreatedAt: t1, TranscriptRoot: root},
		{ID: "ci-other-account", Slot: slot, CreatedAt: t0.Add(time.Minute), TranscriptRoot: "/accounts/1"},
		{ID: "ci-other-slot", Slot: "gascity/lab.engineer-2", CreatedAt: t0.Add(time.Minute), TranscriptRoot: root},
		{ID: "ci-before", Slot: slot, CreatedAt: t0.Add(-time.Minute), TranscriptRoot: root},
		{ID: "ci-after", Slot: slot, CreatedAt: t1.Add(time.Minute), TranscriptRoot: root},
	}
	report, err := buildUsagePairReport(second, first, peers)
	if err != nil {
		t.Fatalf("buildUsagePairReport: %v", err)
	}
	if report.Slot != slot || report.TranscriptRoot != root {
		t.Fatalf("pair identity = %+v", report)
	}
	if report.Sessions[0].SessionID != "ci-first" || !report.Sessions[0].CreatedAt.Equal(t0) ||
		report.Sessions[1].SessionID != "ci-second" || !report.Sessions[1].CreatedAt.Equal(t1) {
		t.Fatalf("sessions = %+v, want ci-first then ci-second with their creation times", report.Sessions)
	}
	if got := report.Sessions[1].CacheCreationTokens; got != 500 {
		t.Fatalf("second cache creation = %d, want 500", got)
	}
	if got := report.Sessions[1].CacheReadTokens; got != 300 {
		t.Fatalf("second cache read = %d, want 300", got)
	}
	if got := report.Sessions[1].UncachedTokens; got != 4 {
		t.Fatalf("second uncached = %d, want 4", got)
	}
}
