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
		now:                    func() time.Time { return now },
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

func TestSelectOrPlanPoolSessionBeadRetriesAfterBackoffRelativeToCurrentCycle(t *testing.T) {
	createdAt := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
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
			poolCreateFailureRetryAfterMetadata: createdAt.Add(time.Minute).Format(time.RFC3339),
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
		beaconTime:             createdAt,
		now:                    func() time.Time { return createdAt.Add(time.Minute) },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
	_, _, plan, err := selectOrPlanPoolSessionBead(bp, &agent, "worker", nil, SessionRequest{WorkBeadID: "work-1"}, map[string]bool{}, map[int]bool{})
	if err != nil {
		t.Fatalf("selectOrPlanPoolSessionBead error = %v, want retry after the backoff expires", err)
	}
	if plan == nil {
		t.Fatal("selectOrPlanPoolSessionBead returned no plan after the backoff expired")
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

// TestCountOnlyBrakeScopeIsTheLowestFreeSlotNotThePool pins how far a
// count-only claim_no_work brake reaches, because nothing in the code that
// produces that reach states it.
//
// A custom scale_check reports a bare count, so the create request it raises
// carries no work bead and its retry ledger row is keyed by the empty trigger.
// What that key names is the SLOT instance ("worker-1"), not the pool. The
// pool-wide behavior is emergent: selectOrPlanPoolSessionBead releases the
// slot it reserved when it refuses (`delete(usedSlots, slot)` in
// build_desired_state.go), so the next count-only request re-picks the same
// lowest free slot and meets the same row. A future change to slot allocation
// would silently widen or narrow the brake, and only this test would notice.
//
// The cold-pool case issues TWO sequential requests against one usedSlots map
// rather than asserting a single refusal. One request proves only that slot
// 1 is braked; it is the second, which never reaches the unbraked slot 2,
// that shows a cold pool's whole new tier stopped by one drain. Asserting a
// lone refusal would pass identically if the slot were retained.
//
// The warm and triggered cases are what make this a measurement rather than a
// restatement of the first. A suite holding only the cold case would pass just
// as well if the key were the pool template -- the reading the former comment
// on poolCreateFailureBackoffActive invited -- because the two designs differ
// ONLY once a live session occupies the low slot.
//
// Absent on purpose: no case drives slot 2's own ledger row. Whether a brake
// recorded against slot 2 blocks slot 1 is the same question with the operands
// swapped, and the lowest-free-slot rule already answers it.
//
// Run: go test ./cmd/gc/ -run TestCountOnlyBrakeScopeIsTheLowestFreeSlot
func TestCountOnlyBrakeScopeIsTheLowestFreeSlotNotThePool(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	store := beads.NewMemStore()
	agent := config.Agent{Name: "worker", MaxActiveSessions: intPtr(2)}
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{agent}}

	// The row a count-only no-work drain leaves behind: closed, ephemeral,
	// empty trigger, recorded against slot 1's instance name.
	if _, err := store.Create(beads.Bead{
		Title:  "worker-1",
		Type:   sessionBeadType,
		Status: "closed",
		Metadata: map[string]string{
			"agent_name":                        "worker-1",
			"template":                          "worker",
			"session_origin":                    "ephemeral",
			"gc.trigger_bead_id":                "",
			poolCreateFailureClassMetadataKey:   poolCreateFailureClassClaimNoWork,
			poolCreateFailureRetryAfterMetadata: now.Add(time.Minute).Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("create count-only failure history: %v", err)
	}

	bp := &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              store,
		sessionBeads:           newSessionBeadSnapshotFromInfos(nil),
		beaconTime:             now,
		now:                    func() time.Time { return now },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}

	tests := []struct {
		name         string
		request      SessionRequest
		requests     int // sequential creates sharing one usedSlots map
		usedSlots    map[int]bool
		wantBackoff  bool
		wantInstance string
	}{
		{
			// Cold pool, both slots free. Slot 2 carries no ledger row of its
			// own and is still never reached, because each refusal hands slot 1
			// back to the allocator.
			name:        "cold pool: one slot's drain stops every count-only create",
			request:     SessionRequest{},
			requests:    2,
			usedSlots:   map[int]bool{},
			wantBackoff: true,
		},
		{
			// Slot 1 held by a live session, so the allocator reserves slot 2,
			// whose instance name misses the row. A warm pool still grows.
			name:         "warm pool: a free slot above the braked one still fills",
			request:      SessionRequest{},
			requests:     1,
			usedSlots:    map[int]bool{1: true},
			wantInstance: "worker-2",
		},
		{
			// The escape hatch: work arriving with a bead id is matched on that
			// id, misses the empty-trigger row, and proceeds. A pool whose
			// demand is ONLY ever count-only -- every rig lab.engineer-codex
			// pool in this city, measured 2026-09-12 -- never takes this path,
			// so for those pools the cold case above is the whole story.
			name:         "concrete triggered work is not blocked by the count-only row",
			request:      SessionRequest{WorkBeadID: "work-9"},
			requests:     1,
			usedSlots:    map[int]bool{},
			wantInstance: "worker-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var plan *poolSessionCreatePlan
			var err error
			for i := 0; i < tt.requests; i++ {
				_, _, plan, err = selectOrPlanPoolSessionBead(
					bp, &agent, "worker", nil, tt.request, map[string]bool{}, tt.usedSlots)
			}
			if tt.wantBackoff {
				if !errors.Is(err, errPoolSessionCreateBackoff) {
					t.Fatalf("request %d err = %v, want failed-create backoff", tt.requests, err)
				}
				if plan != nil {
					t.Fatalf("request %d planned %q while the brake is active; the released slot let the pool grow past its brake", tt.requests, plan.qualifiedInstance)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want a create plan", err)
			}
			if plan == nil {
				t.Fatal("no create plan; the count-only brake reached further than its slot")
			}
			if plan.qualifiedInstance != tt.wantInstance {
				t.Fatalf("planned instance = %q, want %q", plan.qualifiedInstance, tt.wantInstance)
			}
		})
	}
}
