package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Scope: the Claude Code hook wire, and specifically the one event whose
// working shape contradicts the documented one. The suite exists because the
// failure it guards against is invisible from the outside -- a PostToolUse
// hook emitting hookSpecificOutput.additionalContext exits 0, the harness
// reports success, and the model is never shown a word of it. Nothing else in
// this tree would go red.
//
// WHAT THIS SUITE CANNOT REPRESENT, and it is the important half: whether
// Claude Code still honors the shape. These are assertions about the bytes gc
// prints, not about what a model receives. The contract behind them was
// established by driving real one-shot sessions against Claude Code 2.1.267
// and grepping the stream-json transcript for a per-run salt, with the
// tool_result as the positive control -- see the table in
// hook_output_claude.go. A version bump can invalidate it with this suite
// still green, so that experiment, not this file, is what to re-run.
//
// Run: go test ./cmd/gc/ -run ClaudeHookOutput

func TestClaudeHookOutputUsesTheRefusalShapeForPostToolUse(t *testing.T) {
	// The measured contract: only {"decision":"block","reason":...} reaches
	// the model mid-turn. If this ever becomes additionalContext without the
	// experiment being re-run, the hook goes silently inert.
	got := claudeHookOutput("PostToolUse", "you have mail\n")
	if got == nil {
		t.Fatal("PostToolUse produced no payload, so gc would fall through to plain stdout -- measured at 0 of 1 delivered")
	}
	if got["decision"] != "block" {
		t.Fatalf(`decision = %v, want "block"`, got["decision"])
	}
	if got["reason"] != "you have mail" {
		t.Fatalf("reason = %v, want the content with its trailing newline trimmed", got["reason"])
	}
	if _, present := got["hookSpecificOutput"]; present {
		t.Fatal("payload carries hookSpecificOutput; that field is inert for PostToolUse and its presence suggests the shape was reverted to the documented one")
	}
}

func TestClaudeHookOutputLeavesBoundaryEventsOnPlainStdout(t *testing.T) {
	// Every Claude hook this city already runs is a boundary event delivering
	// plain stdout. Wrapping those in a refusal would turn each one into an
	// interruption, so the format must shape PostToolUse ALONE.
	for _, event := range []string{"UserPromptSubmit", "SessionStart", "Stop", "PreCompact", ""} {
		if got := claudeHookOutput(event, "hello"); got != nil {
			t.Fatalf("event %q produced %v, want nil so the caller writes plain stdout", event, got)
		}
	}
}

func TestClaudeHookOutputMatchesEventNameCaseInsensitively(t *testing.T) {
	// The event reaches this function from a command-line flag a human types
	// into a settings.json. A case mismatch there would fall through to plain
	// stdout and be inert -- the failure this whole file is about -- so it must
	// not depend on the caller's capitalization.
	if got := claudeHookOutput("posttooluse", "x"); got == nil {
		t.Fatal("lowercase event name fell through to plain stdout")
	}
}

func TestClaudeHookOutputWriterEmitsPostToolUseJSON(t *testing.T) {
	// Named for the suite prefix, not for the function it exercises. As
	// TestWriteProviderHookContextForEvent... it matched no alternative of the
	// -run filter this suite is verified and swept with, and never executed.
	// The end of the wire: what gc actually prints on stdout is what the
	// harness parses, so assert on the encoded bytes rather than the map.
	var out strings.Builder
	if err := writeProviderHookContextForEvent(&out, hookOutputFormatClaude, "PostToolUse", "REDIRECT: stop"); err != nil {
		t.Fatalf("write: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatalf("stdout is not one JSON document (%v): %q", err, out.String())
	}
	if decoded["decision"] != "block" || decoded["reason"] != "REDIRECT: stop" {
		t.Fatalf("decoded = %v, want the refusal shape carrying the message", decoded)
	}
}

func TestClaudeHookOutputWriterKeepsBoundaryEventsUnwrapped(t *testing.T) {
	var out strings.Builder
	if err := writeProviderHookContextForEvent(&out, hookOutputFormatClaude, "UserPromptSubmit", "plain text"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out.String() != "plain text" {
		t.Fatalf("stdout = %q, want the content written unwrapped", out.String())
	}
}

func TestClaudeHookOutputEmptyContentStillRefuses(t *testing.T) {
	// writeProviderHookContextForEvent short-circuits empty content before it
	// gets here, so this pins the unit's own behavior rather than the wire's:
	// an empty reason must not silently become a no-decision "{}" payload,
	// which Claude Code accepts and ignores.
	got := claudeHookOutput("PostToolUse", "")
	if got == nil || got["decision"] != "block" {
		t.Fatalf("empty content produced %v, want a block decision rather than a fall-through", got)
	}
}
