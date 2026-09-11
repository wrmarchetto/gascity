package main

import "strings"

// Claude Code hook output, and the one event whose contract is not what the
// documentation says.
//
// Every other provider in hook_output.go reaches its model through an
// additional-context field. Claude Code's plain-stdout convention covers
// UserPromptSubmit and SessionStart, which is why gc has never needed a
// "claude" format at all. PostToolUse is the exception, and it is the only
// event that fires MID-TURN -- between an agent's tool calls, with no user
// prompt in between. That makes it the only channel by which a correction can
// reach a session that is in one long autonomous turn and will never submit
// another prompt (ci-tdk1lv, ci-auiqx3).
//
// MEASURED against Claude Code 2.1.267 on 2026-09-09, driving real one-shot
// sessions and grepping the stream-json transcript for a per-run salt. The
// positive control was the tool_result the model demonstrably received:
//
//	hookSpecificOutput.additionalContext, exit 0   0 of 5 runs reached the model
//	plain stdout, exit 0                           0 of 1
//	stderr, exit 2                                 1 of 4, and framed as an error
//	{"decision":"block","reason":...}, exit 0      delivered in every run where
//	                                               the model summarized its step
//
// So the documented additionalContext path -- the one an author would reach
// for, and the one the other three formats here use -- is silently inert for
// PostToolUse in this version. The refusal shape is what works. Do NOT
// "modernize" this to additionalContext without re-running that experiment;
// the failure is invisible, because the hook still exits 0 and the harness
// still reports success.
//
// The cost of being wrong is asymmetric and worth stating: an inert hook
// looks exactly like a working one from the outside, which is the same
// property that let the original incident run for 31 minutes.
//
// Pinned by hook_output_claude_test.go.

// hookOutputFormatClaude selects Claude Code's hook wire. It is a format like
// the others, not a default: an unset format keeps the plain-stdout behavior
// every existing Claude hook in this city relies on.
const hookOutputFormatClaude = "claude"

// claudeHookEventPostToolUse is the only event this format shapes differently.
const claudeHookEventPostToolUse = "PostToolUse"

// claudeHookOutput returns the JSON object gc must print for eventName, or nil
// when the event takes Claude Code's plain-stdout convention and the caller
// should write the content unwrapped.
//
// Returning nil rather than an empty map is the signal to fall through, so a
// future event added here cannot accidentally start emitting "{}" -- which
// Claude Code accepts and silently treats as "no decision", the exact
// inert-but-successful shape this file exists to avoid.
func claudeHookOutput(eventName, content string) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(eventName), claudeHookEventPostToolUse) {
		return nil
	}
	return map[string]any{
		"decision": "block",
		"reason":   strings.TrimRight(content, "\n"),
	}
}
