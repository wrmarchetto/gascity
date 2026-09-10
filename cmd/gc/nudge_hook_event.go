package main

import "strings"

// The provider hook event a `gc nudge drain` invocation is running on, and the
// one distinction gc has to draw between them.
//
// Until ci-auiqx3 drain had exactly one caller shape -- a prompt-boundary hook
// -- so the event was a string literal at the two write sites. It is a
// parameter now because PostToolUse is a different kind of moment, not a
// different spelling of the same one: it fires BETWEEN an agent's tool calls,
// which is the only point at which anything can reach a session that is in one
// long autonomous turn and will never submit another prompt.

// nudgeHookEventUserPromptSubmit is the historical event and stays the default,
// so every existing caller and every existing city hook keeps its behavior
// without naming an event.
const nudgeHookEventUserPromptSubmit = "UserPromptSubmit"

// normalizeNudgeHookEvent resolves an unset event to the prompt-boundary
// default. It deliberately does NOT validate the name against a set: gc does
// not own the provider's event vocabulary, and an unrecognized event simply
// takes the plain-stdout path the default already takes.
func normalizeNudgeHookEvent(event string) string {
	if strings.TrimSpace(event) == "" {
		return nudgeHookEventUserPromptSubmit
	}
	return strings.TrimSpace(event)
}

// isMidTurnNudgeHookEvent reports whether event fires while a turn is already
// under way, rather than at one of its boundaries.
//
// PostToolUse is the only member today. SessionStart, UserPromptSubmit, Stop
// and PreCompact are all boundaries: the agent is between turns, prompt-
// boundary orientation is wanted there, and injecting it costs nothing. It is
// membership of THIS set, not the event's name, that suppresses the clock line
// and the formula-step line -- so an event added here inherits that suppression
// instead of needing its own branch at each write site.
func isMidTurnNudgeHookEvent(event string) bool {
	return strings.EqualFold(strings.TrimSpace(event), claudeHookEventPostToolUse)
}
