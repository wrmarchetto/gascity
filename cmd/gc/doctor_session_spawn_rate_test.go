package main

// Scope: the doctor gate that trips when one agent is spawning sessions far
// faster than work could possibly justify -- the spawn-loop detector.
//
// Why this suite exists: every symptom of the loop it watches for is
// invisible. The bead never moves, so `gc bd list` looks healthy; the session
// is running, so `gc status` says running; the close reason is the same
// canonical string a normal drain produces, so the session bead does not
// read as anomalous. 140 sessions burned across two days before anyone
// noticed, each a full model launch on a rotated pool account. The only
// signal that would have shown it in seconds is the one nothing counted:
// session CREATES per minute per agent.
//
// The gate is deliberately CAUSE-AGNOSTIC. The loop this shipped for was
// caused by unread mail creating demand the claim refuses, but the same
// shape was independently observed from a wedged bead
// (open + started_at + no assignee), and a third agent's 110 cycles were
// never diagnosed at all. A detector keyed to any one of those causes would
// have missed the other two.
//
// Delegated elsewhere: whether mail specifically can create that demand is
// pinned in internal/config/workquery_message_displacement_test.go and
// cmd/gc/hook_message_demand_test.go. This suite covers only detection of
// the resulting burn, whatever produced it.
//
// Run: go test ./cmd/gc/ -run SessionSpawnRate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// spawnFixture builds n session beads for agent, spaced `spacing` apart and
// ending `endingAgo` before now. Session beads are identified by type and
// carry the agent in the agent:<name> label, which is how the runtime stamps
// them.
func spawnFixture(agent string, n int, spacing, endingAgo time.Duration, now time.Time) []beads.Bead {
	out := make([]beads.Bead, 0, n)
	for i := 0; i < n; i++ {
		created := now.Add(-endingAgo).Add(-time.Duration(n-1-i) * spacing)
		out = append(out, beads.Bead{
			ID:        fmt.Sprintf("s-%s-%d", agent, i),
			Title:     agent,
			Type:      "session",
			Status:    "closed",
			Labels:    []string{"agent:" + agent, "gc:session"},
			CreatedAt: created,
		})
	}
	return out
}

func newSpawnRateCheckAt(store beads.Store, now time.Time) *sessionSpawnRateCheck {
	c := newSessionSpawnRateCheck(&config.City{}, "/city",
		func(string) (beads.Store, error) { return store, nil })
	c.now = func() time.Time { return now }
	return c
}

// TestSessionSpawnRateTripsOnStorm pins the observed storm: a session every
// ~13 seconds, sustained. Measured live at 4-5 creates/min on three separate
// agents. It must fail BLOCKING -- `gc doctor` derives its exit code from
// BlockingFailed alone, so an advisory result would print the same line and
// still exit 0, which is exactly the quiet-burn this gate exists to end.
func TestSessionSpawnRateTripsOnStorm(t *testing.T) {
	now := time.Now()
	store := beads.NewMemStoreFrom(0, spawnFixture("bench-engineer", 40, 13*time.Second, 0, now), nil)

	res := newSpawnRateCheckAt(store, now).Run(&doctor.CheckContext{})

	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError on a spawn storm: %#v", res.Status, res)
	}
	if res.Severity != doctor.SeverityBlocking {
		t.Fatalf("Severity = %v, want SeverityBlocking", res.Severity)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "bench-engineer") {
		t.Errorf("Details must name the storming agent:\n%s", strings.Join(res.Details, "\n"))
	}
}

// TestSessionSpawnRateIgnoresHealthyWork pins the shape that must NOT trip:
// an agent doing real work, whose sessions live minutes to tens of minutes.
// bd.dog-1 was the live control for this -- median lifetime 236s, it does
// work and exits, and the reconciler is not killing it.
func TestSessionSpawnRateIgnoresHealthyWork(t *testing.T) {
	now := time.Now()
	store := beads.NewMemStoreFrom(0, spawnFixture("toolsmith", 3, 8*time.Minute, 0, now), nil)

	res := newSpawnRateCheckAt(store, now).Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK for normal work cadence: %#v", res.Status, res)
	}
}

// TestSessionSpawnRateIsPerAgentNotFleetWide pins the grain. `gc start`
// spawns one session for every configured agent at once, which is a
// legitimate fleet-wide burst and must not trip. Counting fleet-wide instead
// of per-agent would fail the gate on every city start -- and a gate that
// fires on startup gets muted, which is worth less than no gate.
func TestSessionSpawnRateIsPerAgentNotFleetWide(t *testing.T) {
	now := time.Now()
	var all []beads.Bead
	for i := 0; i < 30; i++ {
		all = append(all, spawnFixture(fmt.Sprintf("agent-%d", i), 1, time.Second, 0, now)...)
	}
	store := beads.NewMemStoreFrom(0, all, nil)

	res := newSpawnRateCheckAt(store, now).Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("30 agents starting at once tripped the gate; grain must be per-agent: %#v", res)
	}
}

// TestSessionSpawnRateOnlyCountsInsideTheWindow pins that the gate reports the
// CURRENT state, not the historical damage. 140 sessions were already burned
// before this check existed; if history counted, the gate would fail forever
// on damage that is over, and a gate that cannot return to green gets muted.
func TestSessionSpawnRateOnlyCountsInsideTheWindow(t *testing.T) {
	now := time.Now()
	// The same storm, but it ended well outside the window.
	store := beads.NewMemStoreFrom(0,
		spawnFixture("bench-engineer", 40, 13*time.Second, 6*time.Hour, now), nil)

	res := newSpawnRateCheckAt(store, now).Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("a storm that ended 6h ago still trips the gate; it can never return to green: %#v", res)
	}
}

// TestSessionSpawnRateStoreErrorDoesNotGate pins that an unreachable store is
// reported as unknown rather than blocking. A store read failure means the
// rate is UNKNOWN, and failing closed there would wedge `gc doctor` whenever
// Dolt is briefly away.
func TestSessionSpawnRateStoreErrorDoesNotGate(t *testing.T) {
	check := newSessionSpawnRateCheck(&config.City{}, "/city",
		func(string) (beads.Store, error) { return nil, fmt.Errorf("store unreachable") })

	res := check.Run(&doctor.CheckContext{})
	if res.Status == doctor.StatusError && res.Severity == doctor.SeverityBlocking {
		t.Fatalf("unreachable store must not block: %#v", res)
	}
	if check.CanFix() {
		t.Error("CanFix = true; there is no safe mechanical remedy -- suspending an agent is an operator decision")
	}
}
