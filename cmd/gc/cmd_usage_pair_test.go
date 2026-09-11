package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
)

func usagePairTestSession(id, slot, account string, usage sessionlog.TailUsage) usagePairSession {
	return usagePairSession{
		ID:              id,
		Slot:            slot,
		TranscriptRoot:  account,
		FirstInvocation: usage,
	}
}

func TestUsagePairJSONReportMarksSuccessfulReportingWithoutACacheVerdict(t *testing.T) {
	raw, err := json.Marshal(usagePairJSONReport{SchemaVersion: "1", OK: true})
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

func TestBuildUsagePairReportRefusesMismatchedSlot(t *testing.T) {
	_, err := buildUsagePairReport(
		usagePairTestSession("ci-first", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{}),
		usagePairTestSession("ci-second", "gascity/lab.engineer-2", "/accounts/0", sessionlog.TailUsage{}),
	)
	if err == nil || !strings.Contains(err.Error(), "stable slot") {
		t.Fatalf("mismatched slots error = %v, want loud stable-slot refusal", err)
	}
}

func TestBuildUsagePairReportRefusesMismatchedTranscriptRoot(t *testing.T) {
	_, err := buildUsagePairReport(
		usagePairTestSession("ci-first", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{}),
		usagePairTestSession("ci-second", "gascity/lab.engineer-1", "/accounts/1", sessionlog.TailUsage{}),
	)
	if err == nil || !strings.Contains(err.Error(), "transcript-root account") {
		t.Fatalf("mismatched accounts error = %v, want loud transcript-root-account refusal", err)
	}
}

func TestBuildUsagePairReportRefusesTheSameSessionTwice(t *testing.T) {
	_, err := buildUsagePairReport(
		usagePairTestSession("ci-one", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{}),
		usagePairTestSession("ci-one", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{}),
	)
	if err == nil || !strings.Contains(err.Error(), "one session twice") {
		t.Fatalf("duplicate-session error = %v, want loud refusal", err)
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

func TestBuildUsagePairReportPreservesAllFirstInvocationInputSides(t *testing.T) {
	report, err := buildUsagePairReport(
		usagePairTestSession("ci-first", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{InputTokens: 3, CacheReadTokens: 100, CacheCreationTokens: 200}),
		usagePairTestSession("ci-second", "gascity/lab.engineer-1", "/accounts/0", sessionlog.TailUsage{InputTokens: 4, CacheReadTokens: 300, CacheCreationTokens: 500}),
	)
	if err != nil {
		t.Fatalf("buildUsagePairReport: %v", err)
	}
	if report.Slot != "gascity/lab.engineer-1" || report.TranscriptRoot != "/accounts/0" {
		t.Fatalf("pair identity = %+v", report)
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
