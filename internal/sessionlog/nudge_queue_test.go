package sessionlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Scope: ClaudeNudgeQueueState, the only delivery witness gc has for a nudge
// that Claude Code took into its own message queue instead of submitting.
//
// The suite exists because every cheaper witness has already been tried and
// each one reported success for a nudge nobody received: the submit's exit
// status, tmux last_active, the busy indicator, and -- proposed and measured
// wrong on ci-br3r2x itself -- the transcript's type=user record COUNT, which
// advances on every tool call (309 records, 303 of them tool results, 6
// genuine messages). So the cases below are built around what a wrong
// implementation would accept rather than around the happy path: tool-result
// traffic, another message's ledger entries, and an identical earlier copy of
// the same nudge text all appear here as transcripts that must NOT confirm.
//
// Delegated elsewhere: whether the tmux carrier finds the right transcript at
// all is internal/runtime/tmux's problem (nudge_queue_ledger_test.go), and the
// record shapes themselves are quoted from live Claude Code 2.1.263 logs
// rather than invented here.
//
//	go test ./internal/sessionlog/ -run ClaudeNudgeQueueState

// ledgerLines writes a JSONL transcript and returns its path.
func ledgerLines(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	return path
}

const (
	ledgerNudge = "You are holding a pool slot with queued work you have not claimed."
	ledgerOther = "Some entirely different message that shares no text with it."
)

// The window every case below opens against. Records stamped before it are a
// previous nudge's, not this one's.
var ledgerSince = time.Date(2026, 9, 8, 0, 52, 0, 0, time.UTC)

// mustState asks the reader about ledgerNudge. The text is fixed rather than
// a parameter because every case here varies the TRANSCRIPT and holds the
// question constant -- that is what makes "another message's records" and
// "an identical earlier copy" distinguishable cases at all.
func mustState(t *testing.T, path string) NudgeQueueState {
	t.Helper()
	state, err := ClaudeNudgeQueueState(path, ledgerNudge, ledgerSince)
	if err != nil {
		t.Fatalf("ClaudeNudgeQueueState: %v", err)
	}
	return state
}

// An absorbed_mid_turn removal is the busy path's whole delivery record: the
// message rides into the running turn and never becomes a user entry of its
// own, so a watcher written to "a genuine delivery appends a user entry"
// reports this exact success as a failure.
func TestClaudeNudgeQueueStateReadsAnAbsorbedMidTurnRemovalAsDelivered(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerNudge+`"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerNudge+`","reason":"absorbed_mid_turn"}`,
	)
	if got := mustState(t, path); got != NudgeQueueDelivered {
		t.Fatalf("state = %v, want NudgeQueueDelivered", got)
	}
}

// The idle path's record instead: dequeue carries no content, and the delivery
// is the user turn that follows it.
func TestClaudeNudgeQueueStateReadsAQueuedUserTurnAsDelivered(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerNudge+`"}`,
		`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-09-08T00:52:27.009Z"}`,
		`{"type":"user","timestamp":"2026-09-08T00:52:27.016Z","promptSource":"queued","message":{"role":"user","content":"`+ledgerNudge+`"}}`,
	)
	if got := mustState(t, path); got != NudgeQueueDelivered {
		t.Fatalf("state = %v, want NudgeQueueDelivered", got)
	}
}

// A content-free dequeue must not be attributed to this nudge. Doing so would
// confirm our message on somebody else's queue entry, which is the position-
// based attribution this reader refuses.
func TestClaudeNudgeQueueStateRefusesToAttributeABareDequeue(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerNudge+`"}`,
		`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-09-08T00:52:27.009Z"}`,
		`{"type":"user","timestamp":"2026-09-08T00:52:27.016Z","promptSource":"queued","message":{"role":"user","content":"`+ledgerOther+`"}}`,
	)
	if got := mustState(t, path); got != NudgeQueueEnqueued {
		t.Fatalf("state = %v, want NudgeQueueEnqueued: the dequeue delivered a different message", got)
	}
}

// The state gc actually acts on: observed in the vendor's queue, not yet
// drained. It is evidence of delivery-in-progress and nothing more.
func TestClaudeNudgeQueueStateReportsEnqueuedWhileTheMessageStillSits(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerNudge+`"}`,
	)
	if got := mustState(t, path); got != NudgeQueueEnqueued {
		t.Fatalf("state = %v, want NudgeQueueEnqueued", got)
	}
}

// The repeat-nudge trap, and the reason this reader takes a cutoff at all. The
// claim backstop re-sends BYTE-IDENTICAL text up to three times, so content
// alone cannot tell attempt 3 from attempt 1's delivery -- and reading the old
// one would confirm a nudge that is at this moment stranded in a composer.
func TestClaudeNudgeQueueStateIgnoresAnIdenticalEarlierDelivery(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:41:01.590Z","content":"`+ledgerNudge+`"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:41:09.100Z","content":"`+ledgerNudge+`","reason":"absorbed_mid_turn"}`,
		`{"type":"user","timestamp":"2026-09-08T00:41:09.200Z","promptSource":"queued","message":{"role":"user","content":"`+ledgerNudge+`"}}`,
	)
	if got := mustState(t, path); got != NudgeQueueUnrecorded {
		t.Fatalf("state = %v, want NudgeQueueUnrecorded: every record predates the nudge under test", got)
	}
}

// The witness this reader replaces. A busy session writes a type=user record
// for every tool result, so a transcript can grow without bound while the
// nudge sits undelivered. None of that traffic may move the verdict.
func TestClaudeNudgeQueueStateIsNotSatisfiedByToolResultTraffic(t *testing.T) {
	lines := []string{}
	for i := 0; i < 40; i++ {
		lines = append(lines, `{"type":"user","timestamp":"2026-09-08T00:52:30.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"`+ledgerNudge+`"}]}}`)
	}
	if got := mustState(t, ledgerLines(t, lines...)); got != NudgeQueueUnrecorded {
		t.Fatalf("state = %v, want NudgeQueueUnrecorded: 40 tool results are session activity, not this nudge's delivery", got)
	}
}

// Another message's ledger entries are the same hazard one step removed: a
// pane can hold several queued messages, and only the matching content is ours.
func TestClaudeNudgeQueueStateIgnoresAnotherMessagesLedgerEntries(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerOther+`"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerOther+`","reason":"absorbed_mid_turn"}`,
	)
	if got := mustState(t, path); got != NudgeQueueUnrecorded {
		t.Fatalf("state = %v, want NudgeQueueUnrecorded", got)
	}
}

// Only the two removal reasons measured in live transcripts mean the agent
// consumed the message. A reason nobody has seen may well be a cancellation,
// and guessing in the confirming direction is how a false success gets built.
func TestClaudeNudgeQueueStateWillNotConfirmAnUnknownRemovalReason(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-08T00:52:20.219Z","content":"`+ledgerNudge+`"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerNudge+`","reason":"user_cleared_the_queue"}`,
	)
	if got := mustState(t, path); got != NudgeQueueEnqueued {
		t.Fatalf("state = %v, want NudgeQueueEnqueued: an unrecognized removal reason is not a delivery", got)
	}
}

// delivered_to_agent is the other reason live transcripts carry, and it is a
// delivery.
func TestClaudeNudgeQueueStateReadsDeliveredToAgentAsDelivered(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerNudge+`","reason":"delivered_to_agent"}`,
	)
	if got := mustState(t, path); got != NudgeQueueDelivered {
		t.Fatalf("state = %v, want NudgeQueueDelivered", got)
	}
}

// A composer that never reached the queue leaves NO record, and that is the
// whole distinction this reader draws: busy drains, modal-blocked strands, and
// the strand is an empty ledger rather than a pane the code has to recognize.
func TestClaudeNudgeQueueStateReportsUnrecordedForAStrandedNudge(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"assistant","timestamp":"2026-09-08T00:52:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`,
	)
	if got := mustState(t, path); got != NudgeQueueUnrecorded {
		t.Fatalf("state = %v, want NudgeQueueUnrecorded", got)
	}
}

// An unreadable transcript is an OBSERVATION failure, not a verdict. Returning
// Unrecorded with a nil error would let a broken reader look exactly like a
// stranded nudge, which is the pair ci-uihrrv spent an outage failing to tell
// apart.
func TestClaudeNudgeQueueStateErrorsOnAnUnreadableTranscript(t *testing.T) {
	state, err := ClaudeNudgeQueueState(filepath.Join(t.TempDir(), "absent.jsonl"), ledgerNudge, ledgerSince)
	if err == nil {
		t.Fatal("err = nil for a transcript that does not exist: an unreadable observer must not be reported as a stranded nudge")
	}
	if state != NudgeQueueUnrecorded {
		t.Fatalf("state = %v, want NudgeQueueUnrecorded alongside the error", state)
	}
}

// An empty nudge text matches everything if the comparison is sloppy, so it is
// refused outright rather than allowed to confirm on the first record present.
func TestClaudeNudgeQueueStateRefusesAnEmptyNudgeText(t *testing.T) {
	path := ledgerLines(t,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerNudge+`","reason":"absorbed_mid_turn"}`,
	)
	if _, err := ClaudeNudgeQueueState(path, "   ", ledgerSince); err == nil {
		t.Fatal("err = nil for an empty nudge text: a witness that matches anything is not a witness")
	}
}

// The silently-never-fires hazard, pinned. This package's shared splitLines
// runs a bufio.Scanner capped at 256 KiB, and a Scanner STOPS at the first
// oversized token rather than skipping it -- so one enormous tool-result line
// would hide every record after it, which in a tail window is the newest
// ones. The verdict would still be "safe" (Unrecorded) and would be wrong on
// exactly the busy sessions this witness exists for.
//
// A 400 KiB line sits above that cap and below the tail window, so a reader
// that reused splitLines goes red here and nowhere else.
func TestClaudeNudgeQueueStateSurvivesAnOversizedTranscriptLine(t *testing.T) {
	huge := `{"type":"assistant","timestamp":"2026-09-08T00:52:25.000Z","message":{"role":"assistant","content":"` +
		strings.Repeat("x", 400*1024) + `"}}`
	path := ledgerLines(t,
		huge,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-09-08T00:52:37.625Z","content":"`+ledgerNudge+`","reason":"absorbed_mid_turn"}`,
	)
	if got := mustState(t, path); got != NudgeQueueDelivered {
		t.Fatalf("state = %v, want NudgeQueueDelivered: a record after an oversized line must still be read", got)
	}
}
