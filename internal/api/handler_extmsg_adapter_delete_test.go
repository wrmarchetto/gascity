package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/extmsg"
)

// newExtMsgAdapterDeleteFixture mirrors newExtMsgAgentBindingFixture but
// configures a SECOND named-session agent. The handoff half of
// TestExtMsgReconnectRebindsSameAgentWithoutReplace needs a second legitimate
// bind target, and the shared fixture has exactly one -- binding an
// unconfigured name is rejected at 400 before the conflict path is reached,
// so it would prove nothing about the conflict.
func newExtMsgAdapterDeleteFixture(t *testing.T) (*fakeState, *Server, *extmsg.Services, extmsg.ConversationRef) {
	t.Helper()
	fs := newSessionFakeState(t)
	fs.cfg.Agents = append(fs.cfg.Agents,
		config.Agent{Name: "relief", Dir: "myrig", Provider: "test-agent", MaxActiveSessions: intPtr(1)})
	fs.cfg.NamedSessions = append(fs.cfg.NamedSessions,
		config.NamedSession{Template: "relief", Dir: "myrig"})
	srv := New(fs)
	t.Cleanup(srv.waitForBackground)
	services := extmsg.NewServices(fs.cityBeadStore)
	fs.extmsgSvc = &services
	registry := extmsg.NewAdapterRegistry()
	registry.Register(extmsg.AdapterKey{Provider: "discord", AccountID: "acct-1"}, &testExtMsgAdapter{})
	fs.adapterReg = registry
	ref := extmsg.ConversationRef{
		ScopeID:        "guild-1",
		Provider:       "discord",
		AccountID:      "acct-1",
		ConversationID: "thread-adapter-delete",
		Kind:           extmsg.ConversationThread,
	}
	return fs, srv, &services, ref
}

// newExtMsgDeleteRequest builds a DELETE carrying a JSON body. The shared
// newDeleteRequest helper sends no body, and DELETE /extmsg/adapters
// identifies its target in one -- the adapter key is {provider, account_id},
// not a path segment.
func newExtMsgDeleteRequest(t *testing.T, url string, body map[string]any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal(body): %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, url, bytes.NewReader(raw))
	req.Header.Set("X-GC-Request", "true")
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestExtMsgAdapterDeleteLeavesConversationStateIntact pins the contract that
// DELETE /extmsg/adapters removes a TRANSPORT, not a CONVERSATION: the
// binding, its transcript membership and the conversation's transcript state
// all survive it.
//
// This is the reconnect contract, and it is load-bearing rather than
// incidental. The registry is in-memory and does not survive a controller
// restart (internal/extmsg/adapter_registry.go:11-18), so out-of-process
// adapters re-register routinely; a DELETE that reaped beads would make an
// explicit disconnect destructive while the crash path is not, and would
// silently discard a conversation's sequence numbering. The API already has a
// separate verb for ending a binding -- POST /extmsg/unbind -- so nothing is
// unreachable under this reading.
//
// The test drives the real HTTP handler rather than calling
// AdapterRegistry.Unregister directly. At the registry level the assertion is
// a tautology (AdapterRegistry holds no beads.Store and is structurally
// incapable of touching a row); through the handler it goes red the moment
// someone wires reaping into the DELETE path, which is the regression it
// exists to catch.
func TestExtMsgAdapterDeleteLeavesConversationStateIntact(t *testing.T) {
	fs, srv, services, ref := newExtMsgAdapterDeleteFixture(t)
	ctx := context.Background()
	caller := extmsg.Caller{Kind: extmsg.CallerController, ID: "test"}

	rec := postExtMsg(t, fs, srv, "/extmsg/bind", map[string]any{
		"conversation": conversationBody(ref),
		"agent_name":   "myrig/worker",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bind: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Advance the transcript past its initial sequence so a reset would be
	// visible. A state row created fresh starts at next_sequence=1
	// (transcript_service.go:754); asserting only that A row exists would go
	// green over a delete-and-recreate.
	if _, err := services.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
		Caller:            caller,
		Conversation:      ref,
		Kind:              extmsg.TranscriptMessageInbound,
		Provenance:        extmsg.TranscriptProvenanceLive,
		ProviderMessageID: "m-1",
		Text:              "hello",
		CreatedAt:         time.Date(2026, time.September, 8, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before, err := services.Transcript.State(ctx, caller, ref)
	if err != nil || before == nil {
		t.Fatalf("State before delete: %+v, %v", before, err)
	}
	if before.NextSequence < 2 {
		t.Fatalf("fixture did not advance the sequence: next_sequence = %d", before.NextSequence)
	}

	req := newExtMsgDeleteRequest(t, cityURL(fs, "/extmsg/adapters"), map[string]any{
		"provider":   ref.Provider,
		"account_id": ref.AccountID,
	})
	del := httptest.NewRecorder()
	srv.ServeHTTP(del, req)
	if del.Code != http.StatusOK {
		t.Fatalf("delete adapter: status = %d, body = %s", del.Code, del.Body.String())
	}
	if got := fs.adapterReg.Lookup(extmsg.AdapterKey{Provider: ref.Provider, AccountID: ref.AccountID}); got != nil {
		t.Fatal("adapter still registered after DELETE, so the rest of this test proves nothing")
	}

	binding, err := services.Bindings.ResolveByConversation(ctx, ref)
	if err != nil {
		t.Fatalf("ResolveByConversation: %v", err)
	}
	if binding == nil {
		t.Fatal("DELETE /extmsg/adapters closed the binding; a reconnect would now land unbound")
	}
	if binding.AgentName != "myrig/worker" {
		t.Fatalf("binding target changed across DELETE: %+v", binding)
	}

	memberships, err := services.Transcript.ListMemberships(ctx, caller, ref)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(memberships) == 0 {
		t.Fatal("DELETE /extmsg/adapters closed the transcript membership")
	}

	after, err := services.Transcript.State(ctx, caller, ref)
	if err != nil {
		t.Fatalf("State after delete: %v", err)
	}
	if after == nil {
		t.Fatal("DELETE /extmsg/adapters closed the transcript state row")
	}
	if after.NextSequence != before.NextSequence {
		t.Fatalf("transcript sequence moved across DELETE: before = %d, after = %d -- a re-bind would now reissue numbers already used",
			before.NextSequence, after.NextSequence)
	}
}

// TestExtMsgReconnectRebindsSameAgentWithoutReplace is the assertion ci-e7xsbk
// asked for, and it passes for a reason the bead did not predict. The bead
// expected the surviving binding to refuse the next bind, so it asked for a
// test that a fresh Bind succeeds without replace=true. It does -- but not
// because anything was reaped: binding_service.go:145 conflicts ONLY when the
// new target differs from the active one, so re-binding the SAME agent takes
// rebindActiveLocked and succeeds. The stranded binding never blocked the
// reconnect idiom it was filed against.
//
// The second half pins the case that DOES conflict, so the boundary is
// recorded rather than left to be rediscovered: a DIFFERENT agent needs
// replace=true. Without it the suite would read as "binding never
// conflicts", which is false.
//
// Note what this test can and cannot speak for. replace=true is exercised
// here over HTTP, where it works. It is NOT reachable from the callers that
// need it -- contrib/openclaw-bridge never sends it and gc extmsg bind has
// no --replace -- so a green run here must not be read as "reconfiguring a
// bridge onto another agent works". That gap is caller-side and tracked
// separately.
func TestExtMsgReconnectRebindsSameAgentWithoutReplace(t *testing.T) {
	fs, srv, services, ref := newExtMsgAdapterDeleteFixture(t)

	if rec := postExtMsg(t, fs, srv, "/extmsg/bind", map[string]any{
		"conversation": conversationBody(ref),
		"agent_name":   "myrig/worker",
	}); rec.Code != http.StatusOK {
		t.Fatalf("initial bind: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req := newExtMsgDeleteRequest(t, cityURL(fs, "/extmsg/adapters"), map[string]any{
		"provider":   ref.Provider,
		"account_id": ref.AccountID,
	})
	del := httptest.NewRecorder()
	srv.ServeHTTP(del, req)
	if del.Code != http.StatusOK {
		t.Fatalf("delete adapter: status = %d, body = %s", del.Code, del.Body.String())
	}

	// Reconnect: the out-of-process adapter comes back and re-registers.
	if rec := postExtMsg(t, fs, srv, "/extmsg/adapters", map[string]any{
		"provider":   ref.Provider,
		"account_id": ref.AccountID,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("re-register adapter: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if rec := postExtMsg(t, fs, srv, "/extmsg/bind", map[string]any{
		"conversation": conversationBody(ref),
		"agent_name":   "myrig/worker",
	}); rec.Code != http.StatusOK {
		t.Fatalf("re-bind of the same agent was refused: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	conflict := postExtMsg(t, fs, srv, "/extmsg/bind", map[string]any{
		"conversation": conversationBody(ref),
		"agent_name":   "myrig/relief",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("binding a different agent without replace: status = %d, want 409; body = %s",
			conflict.Code, conflict.Body.String())
	}
	if !strings.Contains(strings.ToLower(conflict.Body.String()), "already bound") {
		t.Fatalf("conflict body does not name the cause: %s", conflict.Body.String())
	}

	if rec := postExtMsg(t, fs, srv, "/extmsg/bind", map[string]any{
		"conversation": conversationBody(ref),
		"agent_name":   "myrig/relief",
		"replace":      true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("handoff with replace=true was refused: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := services.Bindings.ResolveByConversation(context.Background(), ref)
	if err != nil || got == nil || got.AgentName != "myrig/relief" {
		t.Fatalf("handoff did not take: %+v, %v", got, err)
	}
}
