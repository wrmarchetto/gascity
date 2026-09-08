package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/cityinit"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/extmsg"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/importsvc"
	"github.com/gastownhall/gascity/internal/mail"
)

// These tests pin the Idempotency-Key wire contract on the S2 create
// endpoints: a repeat POST with the same key + same body replays the first
// response without re-running the create. The helper-level semantics
// (mismatch 422, in-flight 409, unreserve-on-error) are covered by
// idempotency_helper_test.go; here each test proves the endpoint is actually
// wired through withIdempotency.

func postIdempotent(t *testing.T, h http.Handler, url, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newPostRequest(url, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAgentCreateIdempotentReplay(t *testing.T) {
	fs := newFakeMutatorState(t)
	h := newTestCityHandler(t, fs)

	body := `{"name":"coder","provider":"claude"}`
	first := postIdempotent(t, h, cityURL(fs, "/agents"), "agent-create-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(fs, "/agents"), "agent-create-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want first-response body %s", replay.Body.String(), first.Body.String())
	}
	// The create must have run exactly once.
	count := 0
	for _, a := range fs.cfg.Agents {
		if a.Name == "coder" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("agent 'coder' appears %d times in config, want 1 (replay re-ran create)", count)
	}
}

func TestAgentCreateIdempotencyMismatch(t *testing.T) {
	fs := newFakeMutatorState(t)
	h := newTestCityHandler(t, fs)

	first := postIdempotent(t, h, cityURL(fs, "/agents"), "agent-create-1", `{"name":"coder","provider":"claude"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	// Same key, different body → 422, and no second agent is created.
	mismatch := postIdempotent(t, h, cityURL(fs, "/agents"), "agent-create-1", `{"name":"other","provider":"claude"}`)
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: status = %d, want 422; body = %s", mismatch.Code, mismatch.Body.String())
	}
	for _, a := range fs.cfg.Agents {
		if a.Name == "other" {
			t.Fatal("mismatched request created agent 'other'; it must not run the create")
		}
	}
}

func TestProviderCreateIdempotentReplay(t *testing.T) {
	fs := newFakeMutatorState(t)
	h := newTestCityHandler(t, fs)

	// fakeMutatorState.CreateProvider rejects duplicates with ErrAlreadyExists,
	// so a replay that re-ran the create would surface as a 409 here.
	body := `{"name":"ollama","command":"ollama run"}`
	first := postIdempotent(t, h, cityURL(fs, "/providers"), "prov-create-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(fs, "/providers"), "prov-create-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201 (409 means the create re-ran); body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}

func TestRigCreateIdempotentReplay(t *testing.T) {
	fs := newFakeMutatorState(t)
	h := newTestCityHandler(t, fs)

	body := `{"name":"backend","path":"` + t.TempDir() + `"}`
	first := postIdempotent(t, h, cityURL(fs, "/rigs"), "rig-create-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(fs, "/rigs"), "rig-create-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	count := 0
	for _, r := range fs.cfg.Rigs {
		if r.Name == "backend" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("rig 'backend' appears %d times in config, want 1 (replay re-ran create)", count)
	}
}

func TestConvoyCreateIdempotentReplay(t *testing.T) {
	state := newFakeMutatorState(t)
	h := newTestCityHandler(t, state)

	store := state.stores["myrig"]
	item, err := store.Create(beads.Bead{Title: "task-1"})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	body := `{"rig":"myrig","title":"test convoy","items":["` + item.ID + `"]}`
	first := postIdempotent(t, h, cityURL(state, "/convoys"), "convoy-create-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	var firstConvoy beads.Bead
	if err := json.NewDecoder(first.Body).Decode(&firstConvoy); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	replay := postIdempotent(t, h, cityURL(state, "/convoys"), "convoy-create-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	var replayConvoy beads.Bead
	if err := json.NewDecoder(replay.Body).Decode(&replayConvoy); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayConvoy.ID != firstConvoy.ID {
		t.Fatalf("replay returned convoy %s, want the first convoy %s", replayConvoy.ID, firstConvoy.ID)
	}
	// Exactly one convoy bead must exist.
	convoys, err := store.List(beads.ListQuery{Type: "convoy", IncludeClosed: true})
	if err != nil {
		t.Fatalf("list beads: %v", err)
	}
	if len(convoys) != 1 {
		t.Fatalf("store holds %d convoy beads, want 1 (replay re-ran create)", len(convoys))
	}
}

func TestMailReplyIdempotentReplay(t *testing.T) {
	state := newFakeState(t)
	mp := state.cityMailProv
	msg, _ := mp.Send("mayor", "worker", "Initial", "content")
	h := newTestCityHandler(t, state)

	body := `{"from":"worker","subject":"Re: Initial","body":"Done!"}`
	first := postIdempotent(t, h, cityURL(state, "/mail/")+msg.ID+"/reply", "mail-reply-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first reply: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(state, "/mail/")+msg.ID+"/reply", "mail-reply-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	// A re-run would mint a NEW message ID; the replay must return the first one.
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	// The MailReplied event must have fired exactly once.
	ep := state.eventProv.(*events.Fake)
	evts, err := ep.List(events.Filter{Type: events.MailReplied})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("MailReplied fired %d times, want 1 (replay re-ran the reply)", len(evts))
	}
}

func TestMailReplySameKeyDifferentMessagesIndependent(t *testing.T) {
	state := newFakeState(t)
	mp := state.cityMailProv
	m1, _ := mp.Send("mayor", "worker", "One", "first")
	m2, _ := mp.Send("mayor", "worker", "Two", "second")
	h := newTestCityHandler(t, state)

	// Same key + same body against two DIFFERENT messages must be two
	// independent creates — the message ID participates in the cache scope.
	// A regression to a constant cache path would replay m1's reply for m2.
	body := `{"from":"worker","subject":"Re","body":"ack"}`
	first := postIdempotent(t, h, cityURL(state, "/mail/")+m1.ID+"/reply", "mail-reply-x", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("reply to m1: status = %d; body = %s", first.Code, first.Body.String())
	}
	second := postIdempotent(t, h, cityURL(state, "/mail/")+m2.ID+"/reply", "mail-reply-x", body)
	if second.Code != http.StatusCreated {
		t.Fatalf("reply to m2: status = %d; body = %s", second.Code, second.Body.String())
	}
	var r1, r2 mail.Message
	json.NewDecoder(first.Body).Decode(&r1)  //nolint:errcheck
	json.NewDecoder(second.Body).Decode(&r2) //nolint:errcheck
	if r1.ID == r2.ID {
		t.Fatalf("reply to m2 replayed m1's reply (%s) — the message ID must participate in the cache scope", r1.ID)
	}
}

func TestMailReplyReplayAfterOriginalDeleted(t *testing.T) {
	state := newFakeState(t)
	mp := state.cityMailProv
	msg, _ := mp.Send("mayor", "worker", "Initial", "content")
	h := newTestCityHandler(t, state)

	body := `{"from":"worker","subject":"Re: Initial","body":"Done!"}`
	first := postIdempotent(t, h, cityURL(state, "/mail/")+msg.ID+"/reply", "mail-reply-del", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first reply: status = %d; body = %s", first.Code, first.Body.String())
	}

	// Delete the original message. The replay must still return the cached
	// reply — the provider lookup lives inside the (skipped) closure, so the
	// vanished original cannot turn a legitimate replay into a 404.
	if err := mp.Delete(msg.ID); err != nil {
		t.Fatalf("delete original: %v", err)
	}
	replay := postIdempotent(t, h, cityURL(state, "/mail/")+msg.ID+"/reply", "mail-reply-del", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay after delete: status = %d, want 201 (404 means the lookup ran outside the closure); body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}

func TestEventEmitIdempotentReplay(t *testing.T) {
	state := newFakeState(t)
	h := newTestCityHandler(t, state)

	body := `{"type":"deploy.completed","actor":"ci","subject":"myapp","message":"v2.3.1"}`
	first := postIdempotent(t, h, cityURL(state, "/events"), "emit-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first emit: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(state, "/events"), "emit-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	// The event must have been appended exactly once.
	ep := state.eventProv.(*events.Fake)
	evts, err := ep.List(events.Filter{Type: "deploy.completed"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("event appended %d times, want 1 (replay re-ran the emit)", len(evts))
	}
}

func TestExtMsgAdapterRegisterIdempotentReplay(t *testing.T) {
	state := newFakeState(t)
	state.adapterReg = extmsg.NewAdapterRegistry()
	h := newTestCityHandler(t, state)

	body := `{"provider":"slack","account_id":"T123","callback_url":"http://127.0.0.1:9/cb"}`
	first := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-reg-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first register: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-reg-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	// The ExtMsgAdapterAdded event must have fired exactly once.
	ep := state.eventProv.(*events.Fake)
	evts, err := ep.List(events.Filter{Type: events.ExtMsgAdapterAdded})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("ExtMsgAdapterAdded fired %d times, want 1 (replay re-ran the register)", len(evts))
	}
}

// TestExtMsgAdapterRegisterReplayStillRegistersTheAdapter pins that a replayed
// register leaves the adapter IN the registry, not merely reports that it is.
//
// withIdempotency replays a completed entry without calling create(), and
// create() is where reg.Register lives. Registration is a lease in practice --
// contrib/openclaw-bridge re-registers every 30 s -- so the sequence below is
// the ordinary reconnect path, not an exotic one: register, unregister,
// register again inside the 30 minute cache TTL. Before this fix the replay
// answered 201 {"status":"registered"} over an EMPTY registry, and every
// outbound publish afterwards failed "no adapter for /" against a caller that
// had been told it was registered.
//
// The event assertion is the other half and must not be dropped. The reason
// the endpoint is idempotent at all is to suppress a duplicate
// ExtMsgAdapterAdded on retry, so a fix that simply stopped replaying would
// pass the registry assertion and undo the thing the endpoint wanted.
//
// Not folded into TestExtMsgAdapterRegisterIdempotentReplay: that test cannot
// see this defect at all, because it never removes the adapter between the two
// posts and so cannot tell a registry the replay refreshed from one it left
// alone.
func TestExtMsgAdapterRegisterReplayStillRegistersTheAdapter(t *testing.T) {
	state := newFakeState(t)
	state.adapterReg = extmsg.NewAdapterRegistry()
	h := newTestCityHandler(t, state)

	body := `{"provider":"slack","account_id":"T123","callback_url":"http://127.0.0.1:9/cb"}`
	key := extmsg.AdapterKey{Provider: "slack", AccountID: "T123"}

	first := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-release-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first register: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	if state.adapterReg.Lookup(key) == nil {
		t.Fatalf("first register left no adapter in the registry")
	}

	del := newDeleteRequestWithBody(cityURL(state, "/extmsg/adapters"),
		`{"provider":"slack","account_id":"T123"}`)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusOK {
		t.Fatalf("unregister: status = %d, want 200; body = %s", delRec.Code, delRec.Body.String())
	}
	if state.adapterReg.Lookup(key) != nil {
		t.Fatalf("unregister left the adapter in the registry, so the replay below proves nothing")
	}

	replay := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-release-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	if state.adapterReg.Lookup(key) == nil {
		t.Fatalf("replay answered %q but registered nothing; every publish would fail \"no adapter\"", replay.Body.String())
	}

	ep := state.eventProv.(*events.Fake)
	evts, err := ep.List(events.Filter{Type: events.ExtMsgAdapterAdded})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("ExtMsgAdapterAdded fired %d times, want 1 (the replay re-emitted)", len(evts))
	}
}

// TestExtMsgAdapterRegisterMismatchRegistersNothing pins the ordering the
// reconcile hook exists to preserve.
//
// The cheap way to make a replay re-register is to hoist reg.Register out of
// create() and run it before withIdempotency. That reads identically for every
// case this suite otherwise covers, and it moves the registration IN FRONT of
// the same-key/different-body guard: the second post below would register a
// second adapter and then be rejected 422, leaving a registration the caller
// was told did not happen. Running the effect through the helper's replay
// branch keeps it behind that guard.
//
// The assertion is on the SECOND body's key, not on the first. The first
// adapter must survive -- rejecting a mismatched retry says nothing about the
// registration that succeeded -- so a test that asserted an empty registry
// would pass against a handler that had torn down the wrong one.
func TestExtMsgAdapterRegisterMismatchRegistersNothing(t *testing.T) {
	state := newFakeState(t)
	state.adapterReg = extmsg.NewAdapterRegistry()
	h := newTestCityHandler(t, state)

	first := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-mismatch-1",
		`{"provider":"slack","account_id":"T123","callback_url":"http://127.0.0.1:9/cb"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first register: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	second := postIdempotent(t, h, cityURL(state, "/extmsg/adapters"), "adapter-mismatch-1",
		`{"provider":"slack","account_id":"T999","callback_url":"http://127.0.0.1:9/cb"}`)
	if second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched retry: status = %d, want 422; body = %s", second.Code, second.Body.String())
	}
	if state.adapterReg.Lookup(extmsg.AdapterKey{Provider: "slack", AccountID: "T999"}) != nil {
		t.Fatalf("a retry rejected 422 still registered its adapter")
	}
	if state.adapterReg.Lookup(extmsg.AdapterKey{Provider: "slack", AccountID: "T123"}) == nil {
		t.Fatalf("the rejected retry tore down the registration that succeeded")
	}
}

// newDeleteRequestWithBody is the DELETE counterpart of newPostRequest for the
// extmsg endpoints, whose unregister takes its key in a body rather than in
// the path. newDeleteRequest sends none.
func newDeleteRequestWithBody(url, body string) *http.Request {
	req := httptest.NewRequest("DELETE", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GC-Request", "true")
	return req
}

// countingInitializer wraps fakeInitializer to count Scaffold invocations for
// the supervisor city-create replay test.
type countingInitializer struct {
	*fakeInitializer
	scaffoldCalls int
}

func (c *countingInitializer) Scaffold(ctx context.Context, req cityinit.InitRequest) (*cityinit.InitResult, error) {
	c.scaffoldCalls++
	return c.fakeInitializer.Scaffold(ctx, req)
}

func TestSupervisorCityCreateIdempotentReplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GC_HOME", filepath.Join(home, ".gc"))
	cityPath := filepath.Join(home, "mc-city")
	init := &countingInitializer{fakeInitializer: &fakeInitializer{
		scaffoldResult: &cityinit.InitResult{CityName: "mc-city", CityPath: cityPath, ProviderUsed: "codex"},
	}}
	sm := newTestSupervisorMuxWithInitializer(t, init)

	body := `{"dir":"mc-city","provider":"codex"}`
	post := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v0/city", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GC-Request", "test")
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		sm.ServeHTTP(rec, req)
		return rec
	}

	first := post("city-create-1")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first create: status = %d, want 202; body = %s", first.Code, first.Body.String())
	}

	// A re-run would either 409 on the still-pending request ID or mint a new
	// request_id; the replay must return the ORIGINAL request_id + cursor.
	replay := post("city-create-1")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay: status = %d, want 202 (409 means the create re-ran); body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want the first-response body %s (original request_id)", replay.Body.String(), first.Body.String())
	}
	if init.scaffoldCalls != 1 {
		t.Fatalf("Scaffold ran %d times, want 1 (replay re-ran the create)", init.scaffoldCalls)
	}
}

func TestPackAddIdempotentReplay(t *testing.T) {
	calls := 0
	orig := packAddImport
	packAddImport = func(_ fsys.FS, _, source, _, version string) (*importsvc.AddResult, error) {
		calls++
		return &importsvc.AddResult{Name: "review", Source: source, Version: version, GitBacked: true}, nil
	}
	defer func() { packAddImport = orig }()

	fs := newFakeMutatorState(t)
	h := newTestCityHandler(t, fs)

	body := `{"source":"https://github.com/org/repo/tree/main/packs/review"}`
	first := postIdempotent(t, h, cityURL(fs, "/packs"), "pack-add-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first add: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}

	replay := postIdempotent(t, h, cityURL(fs, "/packs"), "pack-add-1", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status = %d, want 201; body = %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	if calls != 1 {
		t.Fatalf("packAddImport ran %d times, want 1 (replay re-ran the import)", calls)
	}
}
