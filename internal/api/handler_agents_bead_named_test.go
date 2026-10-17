package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

type beadNamedAgentFixture struct {
	state                            *fakeState
	srv                              *Server
	h                                http.Handler
	dispatcher, engineer1, engineer2 string
}

// newBeadNamedAgentFixture seeds a max=1/min=0 singleton and two pool slots,
// each running under a "<template>-<beadID>" session the identity cannot
// derive. engineer-2 carries runtime suspended meta.
func newBeadNamedAgentFixture(t *testing.T) beadNamedAgentFixture {
	t.Helper()
	state := newFakeState(t)
	state.cfg.NamedSessions = nil
	state.cfg.Agents = []config.Agent{
		{Name: "dispatcher", Provider: "test-agent", MaxActiveSessions: intPtr(1), MinActiveSessions: intPtr(0)},
		{Name: "engineer", Dir: "myrig", Provider: "test-agent", MaxActiveSessions: intPtr(2)},
	}
	store := beads.NewMemStore()
	state.cityBeadStore = store

	f := beadNamedAgentFixture{state: state}
	f.dispatcher = seedBeadNamedSession(t, store, "dispatcher", map[string]string{
		"state":      "awake",
		"template":   "dispatcher",
		"agent_name": "dispatcher",
	})
	f.engineer1 = seedBeadNamedSession(t, store, "engineer", map[string]string{
		"state":      "awake",
		"template":   "myrig/engineer",
		"agent_name": "myrig/engineer-1",
		"pool_slot":  "1",
	})
	f.engineer2 = seedBeadNamedSession(t, store, "engineer", map[string]string{
		"state":      string(session.StateSuspended),
		"template":   "myrig/engineer",
		"agent_name": "myrig/engineer-2",
		"pool_slot":  "2",
	})
	for _, name := range []string{f.dispatcher, f.engineer1, f.engineer2} {
		if err := state.sp.Start(context.Background(), name, runtime.Config{}); err != nil {
			t.Fatalf("Start %s: %v", name, err)
		}
	}
	if err := state.sp.SetMeta(f.engineer2, "suspended", "true"); err != nil {
		t.Fatalf("SetMeta suspended: %v", err)
	}
	f.srv = New(state)
	f.h = newTestCityHandlerWith(t, state, f.srv)
	return f
}

func (f beadNamedAgentFixture) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return serveOK(t, f.h, req)
}

func serveOK(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d, want 200: %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
	}
	return rec
}

type beadNamedAgentWant struct {
	session            string
	running, suspended bool
}

func checkBeadNamedAgent(t *testing.T, route string, got agentResponse, w beadNamedAgentWant) {
	t.Helper()
	sessName := ""
	if got.Session != nil {
		sessName = got.Session.Name
	}
	if got.Running != w.running || got.Suspended != w.suspended || sessName != w.session {
		t.Errorf("%s %s: running=%v suspended=%v session=%q, want running=%v suspended=%v session=%q",
			route, got.Name, got.Running, got.Suspended, sessName, w.running, w.suspended, w.session)
	}
}

func TestAgentListAndGetResolveBeadNamedSessions(t *testing.T) {
	f := newBeadNamedAgentFixture(t)
	want := map[string]beadNamedAgentWant{
		"dispatcher":       {f.dispatcher, true, false},
		"myrig/engineer-1": {f.engineer1, true, false},
		"myrig/engineer-2": {f.engineer2, true, true},
	}

	rec := f.do(t, httptest.NewRequest("GET", cityURL(f.state, "/agents"), nil))
	var list struct {
		Items []agentResponse `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode /agents: %v", err)
	}
	if len(list.Items) != len(want) {
		t.Fatalf("/agents items = %+v, want %d rows", list.Items, len(want))
	}
	for _, got := range list.Items {
		w, ok := want[got.Name]
		if !ok {
			t.Fatalf("/agents: unexpected row %q", got.Name)
		}
		checkBeadNamedAgent(t, "list", got, w)
	}

	for name, w := range want {
		rec := f.do(t, httptest.NewRequest("GET", cityURL(f.state, "/agent/"+name), nil))
		var got agentResponse
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decode /agent/%s: %v", name, err)
		}
		checkBeadNamedAgent(t, "get", got, w)
	}
}

func TestAgentOutputPeeksBeadNamedSession(t *testing.T) {
	f := newBeadNamedAgentFixture(t)
	text := "peek from " + f.dispatcher
	f.state.sp.SetPeekOutput(f.dispatcher, text)

	routes := map[string]*httptest.ResponseRecorder{
		"city":   f.do(t, httptest.NewRequest("GET", cityURL(f.state, "/agent/dispatcher/output"), nil)),
		"legacy": serveOK(t, f.srv, httptest.NewRequest("GET", "/v0/agent/dispatcher/output", nil)),
	}
	for route, rec := range routes {
		var resp agentOutputResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("%s: decode: %v", route, err)
		}
		if len(resp.Turns) != 1 || resp.Turns[0].Text != text {
			t.Errorf("%s: turns = %+v, want one turn %q", route, resp.Turns, text)
		}
	}
}

func TestRigRestartStopsBeadNamedSessions(t *testing.T) {
	f := newBeadNamedAgentFixture(t)

	f.do(t, newPostRequest(cityURL(f.state, "/rig/myrig/restart"), nil))

	for _, name := range []string{f.engineer1, f.engineer2} {
		if f.state.sp.IsRunning(name) {
			t.Errorf("rig restart left %s running", name)
		}
	}
	if !f.state.sp.IsRunning(f.dispatcher) {
		t.Errorf("rig restart stopped %s, which is outside myrig", f.dispatcher)
	}
}
