package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

const (
	testTriggerBeadIDKey       = "gc.trigger_bead_id"
	testTriggerBeadStoreRefKey = "gc.trigger_bead_store_ref"
)

func idleClaimTestCfg() *config.City {
	return &config.City{Agents: []config.Agent{{
		Name:  "agent-a",
		Nudge: "Run gc hook --claim --json now; if it returns work, execute the claimed formula immediately.",
	}}}
}

func idleClaimPoolSession() beads.Bead {
	return beads.Bead{
		ID:     "session-bead-a",
		Status: "open",
		Type:   "session",
		Metadata: map[string]string{
			"session_name":       "session-a",
			"pool_managed":       "true",
			"template":           "agent-a",
			testTriggerBeadIDKey: "work-a",
		},
	}
}

//nolint:unparam // sessionName is always "session-a" today; kept as a param so new cases can vary it.
func runningIdleClaimFake(t *testing.T, sessionName string) *runtime.Fake {
	t.Helper()
	sp := runtime.NewFake()
	if err := sp.Start(context.Background(), sessionName, runtime.Config{}); err != nil {
		t.Fatalf("fake start: %v", err)
	}
	return sp
}

func mustGetTestBead(t *testing.T, store beads.Store, id string) beads.Bead {
	t.Helper()
	b, err := store.Get(id)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", id, err)
	}
	return b
}

func TestNudgeStalledPoolClaims_NudgesAfterGrace(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("first tick Nudge calls = %d, want 0 inside grace", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeTriggerKey]; got != "work-a" {
		t.Fatalf("idle claim marker trigger = %q, want work-a", got)
	}

	clk.Advance(idleClaimNudgeGrace + time.Second)
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 1 {
		t.Fatalf("Nudge calls = %d, want 1 after grace", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeCountKey]; got != "1" {
		t.Fatalf("idle claim attempt count = %q, want 1", got)
	}
	if got := session.Metadata[idleClaimNudgeAtKey]; got != clk.Now().UTC().Format(time.RFC3339) {
		t.Fatalf("idle claim last nudge at = %q, want %q", got, clk.Now().UTC().Format(time.RFC3339))
	}
}

// Two stores can hold beads with the same ID, so the backstop must resolve the
// slot's trigger through the store ref it was bound to. Here the rig-scoped
// copy is still open (nudge-worthy) while the city-scoped copy of the same ID
// is closed; keying on ID alone would read the wrong bead and stay silent.
func TestNudgeStalledPoolClaims_MatchesTriggerStoreRefForDuplicateIDs(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPoolSession()
	session.Metadata[testTriggerBeadStoreRefKey] = "rig:fixture"
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = "0"
	session.Metadata[idleClaimNudgeAtKey] = base.Format(time.RFC3339)
	work := []beads.Bead{
		{ID: "work-a", Status: "open"},
		{ID: "work-a", Status: "closed"},
	}
	storeRefs := []string{"rig:fixture", "city:test-city"}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(idleClaimNudgeGrace + time.Second)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, storeRefs, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 1 {
		t.Fatalf("Nudge calls = %d, want 1 for the open rig-scoped trigger", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeCountKey]; got != "1" {
		t.Fatalf("idle claim attempt count = %q, want 1", got)
	}
}

func TestNudgeStalledPoolClaims_NeverTouchesWorkingSlot(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = "1"
	session.Metadata[idleClaimNudgeAtKey] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	work := []beads.Bead{{ID: "work-a", Status: "in_progress", Assignee: "session-a"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("working slot Nudge calls = %d, want 0", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeTriggerKey]; got != "" {
		t.Fatalf("idle claim marker trigger = %q, want cleared", got)
	}
	if got := session.Metadata[idleClaimNudgeCountKey]; got != "" {
		t.Fatalf("idle claim marker count = %q, want cleared", got)
	}
	if got := session.Metadata[idleClaimNudgeAtKey]; got != "" {
		t.Fatalf("idle claim marker at = %q, want cleared", got)
	}
}

func TestNudgeStalledPoolClaims_GivesUpAtCap(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPoolSession()
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = strconv.Itoa(idleClaimNudgeMaxAttempts)
	session.Metadata[idleClaimNudgeAtKey] = base.Format(time.RFC3339)
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(time.Hour)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("Nudge calls past cap = %d, want 0", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeCountKey]; got != strconv.Itoa(idleClaimNudgeMaxAttempts) {
		t.Fatalf("idle claim attempt count = %q, want cap preserved", got)
	}
}

// The attempt is reserved on the session bead BEFORE delivery, so a nudge the
// provider fails to deliver still consumes one of the bounded attempts. That is
// what stops a slot whose provider is wedged from being re-nudged on every tick
// forever; the cost is that transient delivery failures burn the cap. The
// failing-provider fixture is continuationFailingNudgeProvider
// (continuation_nudge_test.go), shared across both backstop lanes.
func TestNudgeStalledPoolClaims_DeliveryFailureConsumesAttempt(t *testing.T) {
	sp := &continuationFailingNudgeProvider{Provider: runningIdleClaimFake(t, "session-a")}
	cfg := idleClaimTestCfg()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPoolSession()
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = "0"
	session.Metadata[idleClaimNudgeAtKey] = base.Format(time.RFC3339)
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(idleClaimNudgeGrace + time.Second)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if sp.nudgeCalls != 1 {
		t.Fatalf("delivery calls = %d, want 1 failed attempt", sp.nudgeCalls)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeCountKey]; got != "1" {
		t.Fatalf("persisted attempt count = %q, want 1 despite delivery failure", got)
	}
	if got := session.Metadata[idleClaimNudgeAtKey]; got != clk.Now().UTC().Format(time.RFC3339) {
		t.Fatalf("persisted attempt time = %q, want %q", got, clk.Now().UTC().Format(time.RFC3339))
	}

	// The reservation paces the next retry exactly as a delivered nudge would:
	// nothing more is attempted until the backoff elapses.
	clk.Advance(idleClaimNudgeBackoff - time.Second)
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if sp.nudgeCalls != 1 {
		t.Fatalf("inside-backoff delivery calls = %d, want unchanged 1", sp.nudgeCalls)
	}

	for want := 2; want <= idleClaimNudgeMaxAttempts; want++ {
		session = mustGetTestBead(t, store, session.ID)
		clk.Advance(idleClaimNudgeBackoff + time.Second)
		nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
		if sp.nudgeCalls != want {
			t.Fatalf("attempt %d delivery calls = %d, want %d", want, sp.nudgeCalls, want)
		}
	}

	// Every attempt failed, so exhausted() is reached without the trigger ever
	// being claimed: the lane stops attempting and leaves the cap in place.
	session = mustGetTestBead(t, store, session.ID)
	clk.Advance(time.Hour)
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if sp.nudgeCalls != idleClaimNudgeMaxAttempts {
		t.Fatalf("past-cap delivery calls = %d, want %d", sp.nudgeCalls, idleClaimNudgeMaxAttempts)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeCountKey]; got != strconv.Itoa(idleClaimNudgeMaxAttempts) {
		t.Fatalf("persisted attempt count = %q, want cap %d preserved", got, idleClaimNudgeMaxAttempts)
	}
}

func TestNudgeStalledPoolClaims_SkipsNonPool(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, "pool_managed")
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	clk.Advance(time.Hour)
	session = mustGetTestBead(t, store, session.ID)
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Fatalf("non-pool Nudge calls = %d, want 0", got)
	}
	session = mustGetTestBead(t, store, session.ID)
	if got := session.Metadata[idleClaimNudgeTriggerKey]; got != "" {
		t.Fatalf("non-pool marker trigger = %q, want empty", got)
	}
}

// TestNudgeStalledPoolClaims_ReportsMissingNudgeText pins the ci-a0tquz
// observability contract: an agent whose config carries no nudge text has NO
// working claim backstop, and until this line existed that was indistinguishable
// from a healthy one at every surface an operator has. The mayor cleared the
// same wedge by hand twelve times in one morning without suspecting a rescue
// path existed, because the skip returned silently.
//
// The assertions are split deliberately. Naming the SESSION is not enough --
// the remedy is an edit to the agent's config, so the line has to name the
// agent template a reader would go and edit, and say what to add. And the line
// has to be BOUNDED: this branch is reached on every patrol tick (30s by
// default) for as long as the misconfiguration lasts, so an unbounded line is a
// log flood rather than a signal. Bounding it on the backstop's own attempt
// budget is what the production change buys, and the second half of this test
// is the only thing that proves the budget is actually consumed.
func TestNudgeStalledPoolClaims_ReportsMissingNudgeText(t *testing.T) {
	sp := runningIdleClaimFake(t, "session-a")
	cfg := idleClaimTestCfg()
	cfg.Agents[0].Nudge = "" // the whole subject: a configured agent with no nudge text
	session := idleClaimPoolSession()
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var out bytes.Buffer

	// Grace tick: still silent, because nothing is wrong yet -- a normal claim
	// usually lands inside the window and the backstop would never have fired.
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	if out.Len() != 0 {
		t.Fatalf("grace tick logged %q, want silence — reporting before the backstop would have acted turns every healthy claim into a warning", out.String())
	}

	clk.Advance(idleClaimNudgeGrace + time.Second)
	session = mustGetTestBead(t, store, session.ID)
	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)

	line := out.String()
	for _, want := range []string{"session-a", "work-a", "agent-a", "nudge"} {
		if !strings.Contains(line, want) {
			t.Errorf("log = %q, missing %q — the line has to name the session, the work it is stuck on, the agent to edit, and the field to set", line, want)
		}
	}
	if got := sp.CountCalls("Nudge", "session-a"); got != 0 {
		t.Errorf("Nudge calls = %d, want 0 — there is no text to deliver", got)
	}

	// Bounded: the attempt budget must be consumed, or this line repeats every
	// patrol tick forever. Drive past the cap and require the log to stop.
	for i := 0; i < idleClaimNudgeMaxAttempts+2; i++ {
		clk.Advance(idleClaimNudgeBackoff + time.Second)
		session = mustGetTestBead(t, store, session.ID)
		nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	}
	if got := strings.Count(out.String(), "no nudge text"); got > idleClaimNudgeMaxAttempts {
		t.Errorf("reported %d times, want at most %d — an unbounded line on a 30s patrol tick is a flood, not a signal", got, idleClaimNudgeMaxAttempts)
	}
	if got := strings.Count(out.String(), "no nudge text"); got == 0 {
		t.Errorf("reported 0 times, want at least 1")
	}
}

// The delivery-failure line must name the trigger bead and the attempt number,
// because the operator reads it during the stall and both facts decide what to
// do next. The sibling lines already carry them -- the success line and the
// missing-nudge-text line both end "(attempt N/M)" and both name the bead -- so
// this pins the one branch that dropped them.
//
// WHY THIS IS THE BRANCH THAT MATTERS. It is the only one that precedes
// permanent silence: at the cap the lane stops attempting and logs nothing ever
// again (exhausted() is a deliberate no-op). So the last of these lines is the
// final word on that slot, and without the counter it is indistinguishable from
// the first -- a reader cannot tell a slot that will retry from one that has
// just been abandoned. Measured on 2026-09-06 (ci-mdfcgs): governor-ci-xbhzyp
// emitted three of these, then held its pool slot for 1h35m with nothing
// further logged, and the operator had to count the lines by hand to discover
// the budget was spent.
//
// Asserting on the rendered line rather than on a helper's return value is
// deliberate: the defect was that the format string omitted two arguments the
// call site already had in scope, which no test of a helper can catch.
func TestNudgeStalledPoolClaims_DeliveryFailureLineNamesBeadAndAttempt(t *testing.T) {
	sp := &continuationFailingNudgeProvider{Provider: runningIdleClaimFake(t, "session-a")}
	cfg := idleClaimTestCfg()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	session := idleClaimPoolSession()
	// Seeded past the observe-before-first-nudge grace, matching
	// TestNudgeStalledPoolClaims_DeliveryFailureConsumesAttempt: without these
	// the first tick only records the observation and this suite would assert
	// against an empty buffer for a reason that has nothing to do with the
	// format string under test.
	session.Metadata[idleClaimNudgeTriggerKey] = "work-a"
	session.Metadata[idleClaimNudgeCountKey] = "0"
	session.Metadata[idleClaimNudgeAtKey] = base.Format(time.RFC3339)
	work := []beads.Bead{{ID: "work-a", Status: "open"}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{session}, nil)
	clk := &clock.Fake{Time: base.Add(idleClaimNudgeGrace + time.Second)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)

	line := out.String()
	if !strings.Contains(line, "session-a") {
		t.Errorf("log = %q, missing the session name", line)
	}
	if !strings.Contains(line, "work-a") {
		t.Errorf("log = %q, missing the trigger bead -- the reader cannot tell which work is stalled", line)
	}
	if want := "(attempt 1/" + strconv.Itoa(idleClaimNudgeMaxAttempts) + ")"; !strings.Contains(line, want) {
		t.Errorf("log = %q, missing %q -- without the counter the last line before permanent silence reads like the first", line, want)
	}

	// Drive to the cap and require the final line to say so. This is the line
	// that is followed by nothing, so it is the one that has to be unambiguous.
	for want := 2; want <= idleClaimNudgeMaxAttempts; want++ {
		session = mustGetTestBead(t, store, session.ID)
		clk.Advance(idleClaimNudgeBackoff + time.Second)
		nudgeStalledPoolClaims(sp, cfg, store, []beads.Bead{session}, work, nil, clk.Now(), &out)
	}
	last := strconv.Itoa(idleClaimNudgeMaxAttempts) + "/" + strconv.Itoa(idleClaimNudgeMaxAttempts)
	if !strings.Contains(out.String(), "(attempt "+last+")") {
		t.Errorf("log = %q, missing the terminal (attempt %s) -- nothing else ever reports that this slot was abandoned", out.String(), last)
	}
}
