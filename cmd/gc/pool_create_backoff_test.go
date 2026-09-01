package main

import (
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func TestPoolCreateFailureBackoffBlocksOnlySameAgentAndTrigger(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	store := beads.NewMemStore()
	front := sessionFrontDoor(store)

	failed, err := store.Create(beads.Bead{
		Title:  "worker-1",
		Type:   sessionBeadType,
		Status: "closed",
		Metadata: map[string]string{
			"agent_name":                        "worker-1",
			"template":                          "worker",
			"session_origin":                    "ephemeral",
			"gc.trigger_bead_id":                "work-1",
			poolCreateFailureClassMetadataKey:   poolCreateFailureClassAborted,
			poolCreateFailureRetryAfterMetadata: now.Add(time.Minute).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("create failed session history: %v", err)
	}
	if failed.ID == "" {
		t.Fatal("failed session history did not receive an ID")
	}

	tests := []struct {
		name, agent, trigger string
		want                 bool
	}{
		{name: "same agent and trigger is throttled", agent: "worker-1", trigger: "work-1", want: true},
		{name: "different trigger proceeds", agent: "worker-1", trigger: "work-2", want: false},
		{name: "different agent proceeds", agent: "worker-2", trigger: "work-1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := poolCreateFailureBackoffActive(front, "worker", tt.agent, tt.trigger, now)
			if err != nil {
				t.Fatalf("read failed-create backoff: %v", err)
			}
			if got != tt.want {
				t.Fatalf("poolCreateFailureBackoffActive(%q, %q) = %v, want %v", tt.agent, tt.trigger, got, tt.want)
			}
		})
	}
}

func TestPoolCreateFailureBackoffExpiresAtRetryAfterAndCapsGrowth(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	store := beads.NewMemStore()
	front := sessionFrontDoor(store)
	_, err := store.Create(beads.Bead{
		Title:  "worker-1",
		Type:   sessionBeadType,
		Status: "closed",
		Metadata: map[string]string{
			"agent_name":                        "worker-1",
			"template":                          "worker",
			"session_origin":                    "ephemeral",
			"gc.trigger_bead_id":                "work-1",
			poolCreateFailureClassMetadataKey:   poolCreateFailureClassAborted,
			poolCreateFailureRetryAfterMetadata: now.Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("create failed session history: %v", err)
	}
	active, err := poolCreateFailureBackoffActive(front, "worker", "worker-1", "work-1", now)
	if err != nil {
		t.Fatalf("read failed-create backoff: %v", err)
	}
	if active {
		t.Fatal("retry remained blocked at its exact retry_after boundary")
	}
	if got := poolCreateFailureBackoffDelay(100); got != poolCreateFailureBackoffCeiling {
		t.Fatalf("backoff delay = %s, want ceiling %s", got, poolCreateFailureBackoffCeiling)
	}
}

func TestRecordPoolCreateFailureBackoffPersistsClassCauseAndGrowingDelay(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	store := beads.NewMemStore()
	front := sessionFrontDoor(store)

	create := func(t *testing.T, id string) sessionpkg.Info {
		t.Helper()
		row, err := store.Create(beads.Bead{
			ID:     id,
			Title:  id,
			Type:   sessionBeadType,
			Status: "open",
			Metadata: map[string]string{
				"agent_name":         "worker-1",
				"template":           "worker",
				"session_origin":     "ephemeral",
				"pool_managed":       "true",
				"gc.trigger_bead_id": "work-1",
			},
		})
		if err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
		info, err := front.Get(row.ID)
		if err != nil {
			t.Fatalf("load session %s: %v", row.ID, err)
		}
		return info
	}

	first := create(t, "first")
	if err := recordPoolCreateFailureBackoff(first, front, now, errors.New("account binding aborted")); err != nil {
		t.Fatalf("record first failure: %v", err)
	}
	second := create(t, "second")
	if err := recordPoolCreateFailureBackoff(second, front, now.Add(poolCreateFailureBackoffBase), errors.New("account binding aborted")); err != nil {
		t.Fatalf("record second failure: %v", err)
	}

	got, err := store.Get(second.ID)
	if err != nil {
		t.Fatalf("load second failure: %v", err)
	}
	if got.Metadata[poolCreateFailureClassMetadataKey] != poolCreateFailureClassAborted {
		t.Fatalf("failure class = %q, want %q", got.Metadata[poolCreateFailureClassMetadataKey], poolCreateFailureClassAborted)
	}
	if got.Metadata[poolCreateFailureErrorMetadataKey] != "account binding aborted" {
		t.Fatalf("failure cause = %q, want preserved provider cause", got.Metadata[poolCreateFailureErrorMetadataKey])
	}
	if got.Metadata[poolCreateFailureAttemptsMetadataKey] != "2" {
		t.Fatalf("failure attempts = %q, want 2", got.Metadata[poolCreateFailureAttemptsMetadataKey])
	}
	wantRetryAfter := now.Add(poolCreateFailureBackoffBase + 2*poolCreateFailureBackoffBase).Format(time.RFC3339)
	if got.Metadata[poolCreateFailureRetryAfterMetadata] != wantRetryAfter {
		t.Fatalf("retry after = %q, want %q", got.Metadata[poolCreateFailureRetryAfterMetadata], wantRetryAfter)
	}
}

func TestSelectOrPlanPoolSessionBeadBacksOffMatchingFailedCreate(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	store := beads.NewMemStore()
	agent := config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{agent}}

	_, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Status: "closed",
		Metadata: map[string]string{
			"agent_name":                        "worker",
			"template":                          "worker",
			"session_origin":                    "ephemeral",
			"gc.trigger_bead_id":                "work-1",
			poolCreateFailureClassMetadataKey:   poolCreateFailureClassAborted,
			poolCreateFailureRetryAfterMetadata: now.Add(time.Minute).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("create failed session history: %v", err)
	}

	bp := &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              store,
		sessionBeads:           newSessionBeadSnapshotFromInfos(nil),
		beaconTime:             now,
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
	_, _, plan, err := selectOrPlanPoolSessionBead(bp, &agent, "worker", nil, SessionRequest{WorkBeadID: "work-1"}, map[string]bool{}, map[int]bool{})
	if !errors.Is(err, errPoolSessionCreateBackoff) {
		t.Fatalf("selectOrPlanPoolSessionBead error = %v, want failed-create backoff", err)
	}
	if plan != nil {
		t.Fatalf("selectOrPlanPoolSessionBead returned plan %#v while backoff is active", plan)
	}
}

func TestCommitStartResultRollbackPersistsPoolCreateFailureBackoff(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	store := beads.NewMemStore()
	front := sessionFrontDoor(store)
	row, err := store.Create(beads.Bead{
		Title:  "worker-1",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"agent_name":           "worker-1",
			"template":             "worker",
			"session_name":         "worker-1",
			"session_origin":       "ephemeral",
			"pool_managed":         "true",
			"pending_create_claim": "true",
			"gc.trigger_bead_id":   "work-1",
		},
	})
	if err != nil {
		t.Fatalf("create pending session: %v", err)
	}
	info, err := front.Get(row.ID)
	if err != nil {
		t.Fatalf("load pending session: %v", err)
	}
	result := startResult{
		prepared: preparedStart{candidate: startCandidate{
			info: info,
			tp:   TemplateParams{SessionName: "worker-1", TemplateName: "worker"},
		}},
		err:             errors.New("account binding aborted"),
		outcome:         TraceOutcomeProviderError,
		started:         now,
		finished:        now,
		rollbackPending: true,
	}
	if commitStartResult(result, front, clk, events.Discard, 0, ioDiscard{}, ioDiscard{}) {
		t.Fatal("failed pending create counted as a committed start")
	}

	got, err := store.Get(row.ID)
	if err != nil {
		t.Fatalf("load failed session: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("failed session status = %q, want closed", got.Status)
	}
	if got.Metadata[poolCreateFailureClassMetadataKey] != poolCreateFailureClassAborted {
		t.Fatalf("failure class = %q, want %q", got.Metadata[poolCreateFailureClassMetadataKey], poolCreateFailureClassAborted)
	}
	if got.Metadata[poolCreateFailureErrorMetadataKey] != "account binding aborted" {
		t.Fatalf("failure cause = %q, want original startup error", got.Metadata[poolCreateFailureErrorMetadataKey])
	}
	active, err := poolCreateFailureBackoffActive(front, "worker", "worker-1", "work-1", now)
	if err != nil {
		t.Fatalf("read failed-create backoff: %v", err)
	}
	if !active {
		t.Fatal("fresh pool create was not throttled by its closed failed-create ledger row")
	}
}
