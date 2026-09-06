package tmux

import "testing"

// Every fixture below is a VERBATIM line captured from a live Claude Code pane
// on this city's tmux socket (`tmux -L city capture-pane -p`) on 2026-09-06,
// with the escape sequences stripped and nothing else altered. They are
// literals rather than a generated shape because the whole question this
// evidence answers is what Claude Code actually renders, and a fixture invented
// from the same assumption as the code cannot answer it.
//
// The discriminator they pin: the INPUT BOX prompt is the glyph plus U+00A0 at
// column zero, while the transcript echo of a message that HAS submitted is
// indented and uses an ordinary space. A predicate keyed on the glyph alone
// reads a submitted message as still drafted, which would make the confirm loop
// re-send Enter into a session that already answered.
const (
	// The input box holding an unsent draft.
	fixtureDraftInBox = "❯ You are holding a pool slot with queued work you have not claimed."
	// The same message AFTER it submitted, echoed into the transcript.
	fixtureTranscriptEcho = "  ❯ You are holding a pool slot with queued work you have not claimed."
	// The input box after a submit, while the turn is queued or running.
	fixtureQueuedBox = "❯ Press up to edit queued messages"
	// The input box, idle and empty. Captured from the mayor's pane.
	fixtureEmptyBox = "❯ "

	nudgeDraft = "You are holding a pool slot with queued work you have not claimed. Re-run\nyour startup protocol unmodified: gc hook --claim --drain-ack --json."
)

// TestDraftInInputBoxSeparatesADraftFromItsOwnEcho is the case the whole
// mechanism turns on. Both lines contain the same sentence and the same glyph;
// only the prefix distinguishes "not yet submitted" from "submitted, and this
// is the transcript". Getting it backwards inverts the confirmation.
func TestDraftInInputBoxSeparatesADraftFromItsOwnEcho(t *testing.T) {
	if !draftInInputBox([]string{"chrome", fixtureDraftInBox}, nudgeDraft) {
		t.Error("an unsent draft in the input box read as absent")
	}
	// The echo sits ABOVE the input box, so a real pane carries both. The
	// input box is empty underneath it, and that is what must win.
	pane := []string{fixtureTranscriptEcho, "───", fixtureEmptyBox}
	if draftInInputBox(pane, nudgeDraft) {
		t.Error("a submitted message's transcript echo read as a still-drafted input box")
	}
}

// TestDraftInInputBoxReadsTheLastPromptLine pins that the input box is the LAST
// prompt line, not the first. The transcript scrolls above it, so an earlier
// match is history -- and on a busy session the transcript holds every previous
// nudge, all of them identical to this one.
func TestDraftInInputBoxReadsTheLastPromptLine(t *testing.T) {
	pane := []string{fixtureDraftInBox, "───", fixtureQueuedBox}
	if draftInInputBox(pane, nudgeDraft) {
		t.Error("an earlier prompt line outranked the real input box")
	}
	if got, ok := claudeInputBoxContent(pane); !ok || got != "Press up to edit queued messages" {
		t.Errorf("input box = %q ok=%v, want the queued placeholder", got, ok)
	}
}

// TestDraftInInputBoxIsAbsentWhenNoInputBoxWasFound proves the evidence
// ABSTAINS rather than guessing. A pane with no prompt line at all -- a startup
// screen, a dialog, a provider whose TUI this fork has not measured -- must not
// be read as "the draft is gone", because that would confirm a submit nobody
// observed.
func TestDraftInInputBoxIsAbsentWhenNoInputBoxWasFound(t *testing.T) {
	if draftInInputBox([]string{"Loading...", "no prompt here"}, nudgeDraft) {
		t.Error("a pane with no input box read as holding the draft")
	}
	if _, ok := claudeInputBoxContent([]string{"nothing"}); ok {
		t.Error("claudeInputBoxContent found an input box that is not there")
	}
}

// TestDraftInInputBoxAbstainsOnAnIndistinctDraft pins the lower bound. A draft
// of a few characters is not a fingerprint: it would collide with the queued
// placeholder or leftover chrome, and a false "still drafted" costs a duplicate
// submit into a live turn. Below the bound the evidence reports absent, which
// hands the decision back to the busy indicator alone.
func TestDraftInInputBoxAbstainsOnAnIndistinctDraft(t *testing.T) {
	short := "ok"
	if draftInInputBox([]string{"❯ " + short}, short) {
		t.Errorf("a %d-rune draft was fingerprinted; the floor is %d", len(short), draftHeadMinRunes)
	}
	// And the boundary itself is exercised, so the constant cannot drift
	// upward without a test noticing.
	atFloor := "0123456789ab"
	if len([]rune(atFloor)) != draftHeadMinRunes {
		t.Fatalf("fixture is %d runes, floor is %d", len([]rune(atFloor)), draftHeadMinRunes)
	}
	if !draftInInputBox([]string{"❯ " + atFloor}, atFloor) {
		t.Error("a draft exactly at the floor was not fingerprinted")
	}
}

// TestDraftInInputBoxUsesOnlyTheFirstLine pins why: the box wraps a long paste
// across several rows and renders the continuation rows WITHOUT the prompt
// prefix, so only the first row is guaranteed to sit on the prompt line. A
// comparison against the whole draft would never match a wrapped one and the
// evidence would silently never fire -- the failure mode being fixed here, one
// layer down.
func TestDraftInInputBoxUsesOnlyTheFirstLine(t *testing.T) {
	firstLineOnly := "❯ You are holding a pool slot with queued work you have not claimed."
	if !draftInInputBox([]string{firstLineOnly}, nudgeDraft) {
		t.Error("a multi-line draft did not match its own first line in the box")
	}
}

// TestClaudeInputBoxContentRejectsAnIndentedPromptLine pins the column-zero
// half of the prefix. The transcript echo of a submitted message carries the
// same glyph, indented; if that counted as an input box then a capture window
// that happened to miss the real box would read a message that HAS submitted as
// one still drafted, and the loop would re-submit into a live turn.
//
// Driven against the helper directly rather than through submitEnterAndConfirm,
// because the only pane that reaches this branch is one with no input box in
// the captured tail -- and manufacturing that through the caller would mean
// shrinking the capture window, which is a real check traded for a coverage
// number.
//
// WHAT THIS CASE ACTUALLY CATCHES, stated precisely because mutation testing
// showed the obvious reading is wrong. Allowing indentation ALONE leaves this
// case green: the transcript echo puts an ordinary space after its glyph, so
// the NBSP in the prefix rejects it anyway. Loosening the prefix to the bare
// glyph ALONE is caught by the value assertion below. The two guards are
// complementary, and only removing BOTH slips past this package -- at which
// point the last-prompt-line rule, which IS pinned by
// TestDraftInInputBoxReadsTheLastPromptLine, is the remaining defense. Nothing
// here is a substitute for that rule; these two narrow the window it leaves.
func TestClaudeInputBoxContentRejectsAnIndentedPromptLine(t *testing.T) {
	if _, ok := claudeInputBoxContent([]string{fixtureTranscriptEcho}); ok {
		t.Error("an indented transcript echo was read as the input box")
	}
	// And the real box, at column zero, is still found.
	if _, ok := claudeInputBoxContent([]string{fixtureTranscriptEcho, fixtureEmptyBox}); !ok {
		t.Error("the input box at column zero was not found")
	}
}

// TestDraftEvidencePrefixIsExactlyWhatThePaneRenders guards the constant
// against a well-meaning simplification. It is the one assertion here that
// pins a value rather than a behavior, and it earns that because the value was
// MEASURED off a live pane and cannot be re-derived from anything in the tree:
// U+276F then U+00A0, no ordinary space.
//
// DEFERRED, and deliberately not faked: the NBSP itself has no independent
// behavioral test. Loosening the prefix to the bare glyph leaves every other
// case in this package green, because the column-zero rule and the
// last-prompt-line rule already separate the transcript echo from the input
// box in every pane Claude Code actually draws. Reaching it would need a pane
// carrying the glyph plus an ORDINARY space at column zero, which nothing has
// been observed to render. So the NBSP is defense-in-depth for a capture that
// missed the input box, this assertion is what stops it being dropped by
// accident, and the honest statement is that its behavior is untested rather
// than that it is covered.
func TestDraftEvidencePrefixIsExactlyWhatThePaneRenders(t *testing.T) {
	if claudeInputPromptPrefix != "❯ " {
		t.Errorf("prefix = %q, want U+276F U+00A0 as measured off a live pane", claudeInputPromptPrefix)
	}
	if claudeInputPromptPrefix == "❯ " {
		t.Error("prefix uses an ordinary space; the input box renders U+00A0")
	}
}
