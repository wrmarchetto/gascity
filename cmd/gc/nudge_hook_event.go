package main

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/extmsg"
)

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

// formatNudgeMidTurnInjectOutput renders queued nudges for delivery INSIDE a
// turn, where the boundary wording is not merely off-tone but actively wrong.
//
// The prompt-boundary text (formatNudgeInjectOutput) closes with "Handle them
// after this turn." Delivered mid-turn that instruction reproduces the exact
// defect this delivery path was built for: the agent receives the mayor's
// redirect, defers it past the close, and lands the superseded work anyway
// (ci-tdk1lv). The message has to say act NOW.
//
// The "not a tool failure" line is not padding either. Claude Code's only
// mid-turn injection shape is a refusal (hook_output_claude.go), so the text
// arrives where a tool error would. Measured 2026-09-09 against 2.1.267: with
// a bare token as the reason, the model reported "a post-command hook error"
// and suggested checking the hooks configuration; with the message framed as
// an incoming message it went and looked for the message instead. The framing
// is what turns a delivered string into a read one.
//
// Sanitization mirrors formatNudgeInjectOutput and is not optional: the
// message body is attacker-controllable, and without it a sender can close the
// system-reminder block and break out (gastownhall/gascity#2195).
func formatNudgeMidTurnInjectOutput(items []queuedNudge) string {
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString("Your tool call SUCCEEDED. This is not a tool failure and nothing is wrong ")
	sb.WriteString("with your command -- an incoming message is being delivered mid-task, ")
	sb.WriteString("which this runtime can only do by interrupting a tool call.\n\n")
	if len(items) == 1 {
		sb.WriteString("Message:\n\n")
	} else {
		fmt.Fprintf(&sb, "%d messages:\n\n", len(items))
	}
	for _, item := range items {
		source := extmsg.SanitizeForSystemReminder(item.Source)
		message := extmsg.SanitizeForSystemReminder(item.Message)
		fmt.Fprintf(&sb, "- [%s] %s\n", source, message)
	}
	sb.WriteString("\nAct on this NOW, before your next step. It may supersede what you are ")
	sb.WriteString("doing. Do NOT defer it to the end of the turn -- work finished against ")
	sb.WriteString("superseded instructions is why this channel exists.\n")
	sb.WriteString("</system-reminder>\n")
	return sb.String()
}
