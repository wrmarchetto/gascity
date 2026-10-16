package api

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// seedBeadNamedSession creates an open session bead whose runtime
// session_name is "<base>-<beadID>", the shape PoolSessionName produces, and
// returns that name.
func seedBeadNamedSession(t *testing.T, store beads.Store, base string, meta map[string]string) string {
	t.Helper()
	meta["session_origin"] = "ephemeral"
	meta["pool_managed"] = "true"
	b, err := store.Create(beads.Bead{
		Type:     session.BeadType,
		Status:   "open",
		Labels:   []string{session.LabelSession},
		Metadata: meta,
	})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	name := base + "-" + b.ID
	if err := store.SetMetadata(b.ID, "session_name", name); err != nil {
		t.Fatalf("SetMetadata session_name: %v", err)
	}
	return name
}

func TestHandleStatusResolvesBeadNamedSessionsThroughSnapshot(t *testing.T) {
	state := newFakeState(t)
	state.cfg.NamedSessions = nil
	state.cfg.Agents = []config.Agent{
		{Name: "dispatcher", Provider: "test-agent", MaxActiveSessions: intPtr(1), MinActiveSessions: intPtr(0)},
		{Name: "engineer", Dir: "myrig", Provider: "test-agent", MaxActiveSessions: intPtr(2)},
	}
	store := beads.NewMemStore()
	state.cityBeadStore = store

	dispatcher := seedBeadNamedSession(t, store, "dispatcher", map[string]string{
		"state":      "awake",
		"template":   "dispatcher",
		"agent_name": "dispatcher",
	})
	engineer1 := seedBeadNamedSession(t, store, "engineer", map[string]string{
		"state":      "awake",
		"template":   "myrig/engineer",
		"agent_name": "myrig/engineer-1",
		"pool_slot":  "1",
	})
	engineer2 := seedBeadNamedSession(t, store, "engineer", map[string]string{
		"state":      string(session.StateSuspended),
		"template":   "myrig/engineer",
		"agent_name": "myrig/engineer-2",
		"pool_slot":  "2",
	})
	for _, name := range []string{dispatcher, engineer1} {
		if err := state.sp.Start(context.Background(), name, runtime.Config{}); err != nil {
			t.Fatalf("Start %s: %v", name, err)
		}
	}

	resp := getStatus(t, state)

	want := map[string]struct {
		session            string
		running, suspended bool
	}{
		"dispatcher":       {dispatcher, true, false},
		"myrig/engineer-1": {engineer1, true, false},
		"myrig/engineer-2": {engineer2, false, true},
	}
	if len(resp.AgentDetails) != len(want) {
		t.Fatalf("AgentDetails = %+v, want %d rows", resp.AgentDetails, len(want))
	}
	for _, d := range resp.AgentDetails {
		w, ok := want[d.QualifiedName]
		if !ok {
			t.Fatalf("unexpected agent row %q", d.QualifiedName)
		}
		if d.SessionName != w.session || d.Running != w.running || d.Suspended != w.suspended {
			t.Errorf("%s: session=%q running=%v suspended=%v, want session=%q running=%v suspended=%v",
				d.QualifiedName, d.SessionName, d.Running, d.Suspended, w.session, w.running, w.suspended)
		}
	}
	if resp.Agents.Running != 2 || resp.Agents.Suspended != 1 {
		t.Errorf("Agents = %+v, want Running=2 Suspended=1", resp.Agents)
	}
}
