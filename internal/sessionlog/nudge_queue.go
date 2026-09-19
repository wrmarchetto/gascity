package sessionlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Claude Code's own message-queue ledger, read as a nudge-delivery witness.
//
// gc types a nudge into a TUI composer and has never been able to see what
// happened next. Every signal it reached for instead answered a question
// nobody asked: the submit's exit status and tmux last_active report that
// KEYS were sent, the busy indicator reports that the session is working, and
// a count of the transcript's type=user records reports that tool calls are
// happening. Measured on ci-br3r2x, one live transcript held 309 type=user
// records of which 303 were tool results and 6 were genuine messages -- so a
// count-based witness confirms delivery for any nudge into any working
// session, including one that received nothing.
//
// The ledger is different in kind. Claude Code writes its own queue
// transitions into the same JSONL transcript, each carrying the message text,
// so a verdict here is matched on CONTENT rather than counted. Content is
// what separates THIS nudge from the session's own traffic, and it is the
// property no count can have.
//
// WHY A CUTOFF AS WELL AS CONTENT. The claim backstop re-sends byte-identical
// text up to three times, so content alone cannot tell attempt 3 from
// attempt 1's delivery. Every caller therefore passes the instant it began
// the nudge and older records are skipped.
//
// THE BUSY AND IDLE PATHS LEAVE DIFFERENT RECORDS, which is the detail that
// sank the first fix proposed for ci-br3r2x. A message drained INTO a running
// turn is absorbed and never becomes a user entry of its own. It appears only
// as a remove/absorbed_mid_turn. A message the queue pops at idle appears as
// a content-free dequeue followed by an ordinary user turn. A watcher written
// to "a genuine delivery appends a user entry" reports the first case, which
// is the common one, as undelivered.
//
// EVERY FAILURE DIRECTION HERE IS TOWARD NudgeQueueUnrecorded: an unparsable
// line, a removal reason nobody has measured, a window too short to reach the
// enqueue. That is deliberate. Unrecorded costs an operator an unconfirmed
// report. The other direction reinstates the false success this reader exists
// to remove.
//
// Record shapes are quoted from live Claude Code 2.1.263 transcripts under
// ~/.claude-homes/*/.claude/projects/. Invariants are pinned by
// nudge_queue_test.go.

// NudgeQueueState is what a Claude Code transcript's queue ledger says about
// one specific nudge text.
type NudgeQueueState int

const (
	// NudgeQueueUnrecorded means the window examined holds no record of this
	// text. A nudge stranded in a composer -- the modal-dialog case, where
	// the TUI never took the text at all -- produces exactly this, and so
	// does a window too short to reach the record.
	NudgeQueueUnrecorded NudgeQueueState = iota
	// NudgeQueueEnqueued means the TUI took the text into its queue and has
	// not yet consumed it. Delivery is the vendor's to complete at the end
	// of the running turn.
	NudgeQueueEnqueued
	// NudgeQueueDelivered means the agent consumed the text: absorbed into a
	// running turn, or popped at idle and submitted as a user turn.
	NudgeQueueDelivered
)

// String names the state for operator-facing messages.
func (s NudgeQueueState) String() string {
	switch s {
	case NudgeQueueEnqueued:
		return "enqueued"
	case NudgeQueueDelivered:
		return "delivered"
	default:
		return "unrecorded"
	}
}

// nudgeLedgerTailBytes bounds how far back into a transcript a verdict looks.
//
// Sized against the gap it has to span, not picked round: a caller asks
// within seconds of its own paste, and the only thing that can push the
// enqueue out of the window in that time is a single enormous tool result.
// 1 MiB covers the largest this fork has observed with room to spare, and
// overshooting costs one bounded read on a path taken only when a nudge
// queued behind a running turn.
//
// A window that does fall short reads as NudgeQueueUnrecorded, which is the
// safe direction: the caller reports an unconfirmed nudge rather than a
// confirmed one.
const nudgeLedgerTailBytes = 1 << 20

// nudgeLedgerDeliveredReasons are the removal reasons that mean the agent
// CONSUMED the message. Both are quoted from live transcripts:
// absorbed_mid_turn is the busy path, delivered_to_agent the hook path.
//
// Deliberately a closed set. A removal reason nobody has measured may just as
// easily be a cancellation, and a reader that treats every removal as a
// delivery would confirm a queue the user cleared by hand.
var nudgeLedgerDeliveredReasons = map[string]bool{
	"absorbed_mid_turn":  true,
	"delivered_to_agent": true,
}

// nudgeLedgerRecord is the minimal shape decoded from each JSONL line. The
// queue-operation fields and the user envelope are decoded together because
// both kinds of record carry a verdict and the file interleaves them.
type nudgeLedgerRecord struct {
	Type      string          `json:"type"`
	Operation string          `json:"operation"`
	Reason    string          `json:"reason"`
	Content   string          `json:"content"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// ClaudeNudgeQueueState reports what path's transcript says about the nudge
// text, considering only records stamped at or after since.
//
// Returns (NudgeQueueUnrecorded, err) when the transcript cannot be read or
// the text is too empty to be a fingerprint. An observation failure is NOT a
// verdict: reporting it as Unrecorded with a nil error would make a broken
// reader indistinguishable from a stranded nudge, which is the pair ci-uihrrv
// spent an outage failing to tell apart.
func ClaudeNudgeQueueState(path, text string, since time.Time) (NudgeQueueState, error) {
	want := normalizeNudgeLedgerText(text)
	if want == "" {
		return NudgeQueueUnrecorded, fmt.Errorf("nudge text is empty after normalization: a witness that matches anything is not a witness")
	}
	f, err := os.Open(path) //nolint:gosec // caller-resolved transcript path
	if err != nil {
		return NudgeQueueUnrecorded, fmt.Errorf("opening transcript %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // best-effort close on a read-only file

	data, _, err := readTail(f, nudgeLedgerTailBytes)
	if err != nil {
		return NudgeQueueUnrecorded, fmt.Errorf("reading transcript tail %q: %w", path, err)
	}

	// Later records win, so the scan runs forward and overwrites: an enqueue
	// followed by its removal must settle on Delivered, never the reverse.
	state := NudgeQueueUnrecorded
	for _, line := range splitNudgeLedgerLines(data) {
		var rec nudgeLedgerRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Timestamp.IsZero() || rec.Timestamp.Before(since) {
			continue
		}
		switch rec.Type {
		case "queue-operation":
			// A dequeue carries no content and is skipped here rather than
			// counted. Attributing it by position would confirm this nudge on
			// whatever message the queue actually popped, and a pane can hold
			// several. Its delivery is read off the user turn that follows.
			if normalizeNudgeLedgerText(rec.Content) != want {
				continue
			}
			switch rec.Operation {
			case "enqueue":
				if state != NudgeQueueDelivered {
					state = NudgeQueueEnqueued
				}
			case "remove", "popAll":
				if nudgeLedgerDeliveredReasons[rec.Reason] {
					state = NudgeQueueDelivered
				} else if state == NudgeQueueUnrecorded {
					state = NudgeQueueEnqueued
				}
			}
		case "user":
			if nudgeLedgerUserText(rec.Message) == want {
				state = NudgeQueueDelivered
			}
		}
	}
	return state, nil
}

// nudgeLedgerUserText returns a user record's text when the record is a
// genuine message, and "" when it is anything else.
//
// The type check is the load-bearing part. Claude Code writes every tool
// result as a type=user record too, but with a CONTENT ARRAY rather than a
// string, so requiring a string here excludes the whole of a session's tool
// traffic structurally -- not by a filter somebody has to remember to keep
// current as block types are added.
func nudgeLedgerUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(msg.Content, &text); err != nil {
		return ""
	}
	return normalizeNudgeLedgerText(text)
}

// normalizeNudgeLedgerText reduces a message to the form both sides are
// compared in: trailing whitespace stripped per line, then the whole trimmed.
//
// Normalization is confined to whitespace on purpose. The comparison is
// EQUALITY, not containment: a longer message that merely begins with the
// nudge text would satisfy a prefix test, and the city routinely sends one
// nudge whose text is the opening of another.
func normalizeNudgeLedgerText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// splitNudgeLedgerLines splits a tail window into JSONL lines.
//
// It does NOT reuse this package's splitLines, and the difference is the
// whole point. splitLines runs a bufio.Scanner capped at 256 KiB, and a
// Scanner STOPS at the first token over its cap rather than skipping it --
// so one oversized tool-result line would end the scan and hide every record
// after it, which in a tail window means the newest ones. The verdict would
// read Unrecorded, which is safe, but it would read Unrecorded on exactly the
// busy sessions this witness exists for: the silently-never-fires failure.
//
// A plain split has no token limit. The window is already bounded at
// nudgeLedgerTailBytes, so nothing here is unbounded either way, and a
// partial first line from a mid-file read simply fails to parse.
func splitNudgeLedgerLines(data []byte) [][]byte {
	raw := bytes.Split(data, []byte("\n"))
	lines := make([][]byte, 0, len(raw))
	for _, line := range raw {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
