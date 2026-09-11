package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/extmsg"
)

func TestCmdExtMsgBindRejectsInvalidTargetFlags(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		sessionID string
		replace   bool
		wantMsg   string
	}{
		{name: "bind requires a target", wantMsg: "gc extmsg bind: --agent or --session is required"},
		// handoff registers --to, never --agent, so its message must not send
		// the reader to a flag that verb does not have.
		{name: "handoff requires a target", replace: true, wantMsg: "gc extmsg handoff: --to or --session is required"},
		{name: "bind target mutually exclusive", agentName: "myrig/a", sessionID: "sess-1", wantMsg: "gc extmsg bind: --agent and --session are mutually exclusive"},
		{name: "handoff target mutually exclusive", agentName: "myrig/a", sessionID: "sess-1", replace: true, wantMsg: "gc extmsg handoff: --to and --session are mutually exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// These validations short-circuit before the command reaches the
			// city API, so an unconfigured city does not affect the result.
			code := cmdExtMsgBind(extMsgConversationFlags{}, tt.agentName, tt.sessionID, tt.replace, false, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("cmdExtMsgBind code = %d, want 1", code)
			}
			if got := stderr.String(); !strings.Contains(got, tt.wantMsg) {
				t.Fatalf("stderr = %q, want it to contain %q", got, tt.wantMsg)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on validation failure", stdout.String())
			}
		})
	}
}

func TestConversationRefValidatesRequiredFlags(t *testing.T) {
	// scope-id is set so conversationRef does not need to load city config.
	if _, err := (&extMsgConversationFlags{scopeID: "alpha", conversationID: "123"}).conversationRef("/nonexistent"); err == nil || !strings.Contains(err.Error(), "--provider is required") {
		t.Fatalf("conversationRef(no provider) err = %v, want --provider is required", err)
	}
	if _, err := (&extMsgConversationFlags{scopeID: "alpha", provider: "telegram"}).conversationRef("/nonexistent"); err == nil || !strings.Contains(err.Error(), "--conversation-id is required") {
		t.Fatalf("conversationRef(no conversation-id) err = %v, want --conversation-id is required", err)
	}

	ref, err := (&extMsgConversationFlags{
		scopeID:        "alpha",
		provider:       "telegram",
		accountID:      "default",
		conversationID: "7113355",
		kind:           "dm",
	}).conversationRef("/nonexistent")
	if err != nil {
		t.Fatalf("conversationRef(valid): %v", err)
	}
	want := extmsg.ConversationRef{
		ScopeID:        "alpha",
		Provider:       "telegram",
		AccountID:      "default",
		ConversationID: "7113355",
		Kind:           extmsg.ConversationDM,
	}
	if ref != want {
		t.Fatalf("conversationRef = %+v, want %+v", ref, want)
	}
}

func TestConversationRefIfSetIsNilWhenUnset(t *testing.T) {
	got, err := (&extMsgConversationFlags{}).conversationRefIfSet("/nonexistent")
	if err != nil {
		t.Fatalf("conversationRefIfSet(unset): %v", err)
	}
	if got != nil {
		t.Fatalf("conversationRefIfSet(unset) = %+v, want nil so unbind filters by session/agent", got)
	}

	ref, err := (&extMsgConversationFlags{scopeID: "alpha", provider: "telegram", conversationID: "123"}).conversationRefIfSet("/nonexistent")
	if err != nil {
		t.Fatalf("conversationRefIfSet(set): %v", err)
	}
	if ref == nil || ref.Provider != "telegram" || ref.ConversationID != "123" {
		t.Fatalf("conversationRefIfSet(set) = %+v, want a populated ref", ref)
	}
}

func TestPrintExtMsgBindingOutputs(t *testing.T) {
	record := extmsg.SessionBindingRecord{
		ID:                "bind-1",
		Conversation:      extmsg.ConversationRef{Provider: "telegram", ConversationID: "7113355"},
		AgentName:         "myrig/specialist",
		Status:            extmsg.BindingActive,
		BindingGeneration: 3,
	}

	var jsonOut bytes.Buffer
	if code := printExtMsgBinding(&jsonOut, true, record, "handed off"); code != 0 {
		t.Fatalf("printExtMsgBinding(json) code = %d, want 0", code)
	}
	var decoded extmsg.SessionBindingRecord
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
	if decoded.ID != "bind-1" || decoded.AgentName != "myrig/specialist" || decoded.BindingGeneration != 3 {
		t.Fatalf("decoded json = %+v, want the binding record", decoded)
	}

	var humanOut bytes.Buffer
	if code := printExtMsgBinding(&humanOut, false, record, "handed off"); code != 0 {
		t.Fatalf("printExtMsgBinding(human) code = %d, want 0", code)
	}
	human := humanOut.String()
	for _, want := range []string{"handed off", "telegram/7113355", "agent myrig/specialist", "binding bind-1", "generation 3"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output = %q, want it to contain %q", human, want)
		}
	}
}

// TestExtMsgHandoffAcceptsSessionTarget pins that handoff can replace a
// binding with a SESSION, not only an agent.
//
// Before this, replace=true was reachable from exactly one place in the tree:
// gc extmsg handoff --to <agent>. A conversation bound to a session that
// needed moving had no CLI path at all, and the HTTP replace field was the
// only way to do it. That gap is what turned an ordinary reconfigure into a
// 409 with no documented remedy (ci-nlx1rx).
//
// The flag registration is asserted separately from the validation because
// they fail differently: a flag that is declared but never read still passes
// a validation test, and validation logic that is correct but unreachable
// from the command line still passes a unit test on the helper.
func TestExtMsgHandoffAcceptsSessionTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newExtMsgHandoffCmd(&stdout, &stderr)

	sessionFlag := cmd.Flags().Lookup("session")
	if sessionFlag == nil {
		t.Fatal("gc extmsg handoff has no --session flag, so a session-bound conversation cannot be moved")
	}
	if cmd.Flags().Lookup("to") == nil {
		t.Fatal("--to disappeared; handoff's agent path is the one that already had callers")
	}

	// Both targets at once must be refused, and the message must name the
	// flags THIS verb actually has. handoff exposes --to, never --agent, so
	// the shared bind wording would send the reader to a flag that does not
	// exist here.
	code := cmdExtMsgBind(extMsgConversationFlags{}, "myrig/a", "sess-1", true, false, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1 for both targets set", code)
	}
	if got := stderr.String(); !strings.Contains(got, "--to and --session are mutually exclusive") {
		t.Fatalf("stderr = %q, want it to name --to (the flag handoff has)", got)
	}

	stderr.Reset()
	if code := cmdExtMsgBind(extMsgConversationFlags{}, "", "", true, false, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 for no target", code)
	}
	if got := stderr.String(); !strings.Contains(got, "--to or --session is required") {
		t.Fatalf("stderr = %q, want it to name --to (the flag handoff has)", got)
	}
}
