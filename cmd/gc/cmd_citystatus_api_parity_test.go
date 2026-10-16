package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

const charCityBeadNamedPools = "[workspace]\nname = \"" + charCityName + "\"\nprefix = \"gc\"\n" + `
[[agent]]
name = "dispatcher"
max_active_sessions = 1
min_active_sessions = 0

[[agent]]
name = "engineer"
max_active_sessions = 2
`

// The API and local routes must report the same per-agent running and
// suspended state for sessions whose tmux name is "<template>-<beadID>".
func TestCityStatusAPIAndLocalRoutesAgreeOnBeadNamedSessions(t *testing.T) {
	var live []string
	h := newCharCity(t, charCityBeadNamedPools, func(t *testing.T, store beads.Store) {
		seed := func(template, agentName, slot, state string) string {
			meta := map[string]string{
				"template":       template,
				"agent_name":     agentName,
				"state":          state,
				"session_origin": "ephemeral",
				"pool_managed":   "true",
			}
			if slot != "" {
				meta["pool_slot"] = slot
			}
			b, err := store.Create(beads.Bead{Type: session.BeadType, Labels: []string{session.LabelSession}, Metadata: meta})
			if err != nil {
				t.Fatalf("create session bead: %v", err)
			}
			name := PoolSessionName(template, b.ID)
			if err := store.SetMetadata(b.ID, "session_name", name); err != nil {
				t.Fatalf("set session_name: %v", err)
			}
			return name
		}
		live = append(live, seed("dispatcher", "dispatcher", "", "awake"))
		live = append(live, seed("engineer", "engineer", "1", "awake"))
		seed("engineer", "engineer", "2", string(session.StateSuspended))
	})
	sp := h.cs.SessionProvider()
	for _, name := range live {
		if err := sp.Start(context.Background(), name, runtime.Config{}); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
	}
	type row struct{ Running, Suspended bool }
	render := func(c *api.Client, nilReason, wantRoute string) map[string]row {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := routeCityStatus(h.cityPath, h.cs.Config(), sp, newFakeDrainOps(), c, nilReason, true, &stdout, &stderr); code != 0 {
			t.Fatalf("routeCityStatus exit %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "cmd=status route="+wantRoute) {
			t.Fatalf("stderr does not show route=%s:\n%s", wantRoute, stderr.String())
		}
		var out StatusJSON
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("decode status json: %v; out=%s", err, stdout.String())
		}
		rows := make(map[string]row, len(out.Agents))
		for _, a := range out.Agents {
			rows[a.QualifiedName] = row{a.Running, a.Suspended}
		}
		return rows
	}
	lanes := map[string]charLane{}
	for _, lane := range h.lanes(t) {
		lanes[lane.name] = lane
	}
	viaAPI := render(lanes["alive"].client, "", "api")
	local := render(nil, lanes["serverless"].nilReason, "fallback")

	want := map[string]row{
		"dispatcher": {Running: true},
		"engineer-1": {Running: true},
		"engineer-2": {Suspended: true},
	}
	for name, w := range want {
		if got := local[name]; got != w {
			t.Errorf("local %s = %+v, want %+v", name, got, w)
		}
		if got := viaAPI[name]; got != w {
			t.Errorf("api %s = %+v, want %+v", name, got, w)
		}
	}
}
