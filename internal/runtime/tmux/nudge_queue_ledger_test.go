package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/sessionlog"
)

// Scope: how a nudge that queued behind a running turn is classified once the
// provider's OWN queue ledger has been read.
//
// The suite exists because the three outcomes are reported to two callers
// that react to them differently, and the middle one is new. Until ci-tihynr
// ran, every queued nudge was reported "may never be submitted": the claim
// backstop logged it as a failed delivery and spent one of three attempts on
// it. The measured answer is that a BUSY queue drains unaided -- 06:24Z on
// toolsmith-2 and 23:28Z on lab.engineer-1, no key pressed by anyone -- and
// what actually strands is a pane blocked on a MODAL DIALOG, which never
// takes the text into the queue at all and therefore leaves the ledger empty.
//
// So the three states are not shades of the same answer. Delivered and
// Unrecorded are opposite verdicts, and collapsing Enqueued into either is
// the specific error this pins: into Delivered it becomes the false success
// ci-fo6au4 removed, into Unrecorded it becomes the false alarm that burned
// the backstop's budget.
//
// This suite cannot represent transcript resolution -- whether a pane's
// CLAUDE_CONFIG_DIR and working directory find the right JSONL file needs a
// live tmux server and is covered by the integration build tag.
//
//	go test ./internal/runtime/tmux/ -run QueuedNudge

func TestQueuedNudgeConfirmsOnceTheLedgerShowsItConsumed(t *testing.T) {
	reached, confirmed, err := classifyNudgeSubmitErr(errSubmitQueuedBehindRun, "sess-1", sessionlog.NudgeQueueDelivered)
	if !reached {
		t.Error("reachedTmux = false: the keys did reach tmux")
	}
	if !confirmed {
		t.Fatal("confirmed = false although the provider's ledger records the agent consuming this exact message: that is the drain gc could not previously see")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil for a confirmed delivery", err)
	}
}

func TestQueuedNudgeReportsPendingDrainWhileTheLedgerHoldsIt(t *testing.T) {
	reached, confirmed, err := classifyNudgeSubmitErr(errSubmitQueuedBehindRun, "sess-1", sessionlog.NudgeQueueEnqueued)
	if !reached {
		t.Error("reachedTmux = false: the keys did reach tmux")
	}
	if confirmed {
		t.Fatal("confirmed = true for a message still sitting in the queue: the drain has not been observed, and claiming it here is the witness ci-fo6au4 removed")
	}
	if !errors.Is(err, ErrNudgeQueuedPendingDrain) {
		t.Fatalf("err = %v, want it to wrap ErrNudgeQueuedPendingDrain so a caller can tell an observed queue entry from a possibly-stranded one", err)
	}
	// Every caller that already handles the unconfirmed path must keep
	// handling this one: it is a narrowing, not a new branch they must learn.
	if !errors.Is(err, ErrNudgeSubmitUnconfirmed) {
		t.Fatalf("err = %v, want it to remain an ErrNudgeSubmitUnconfirmed", err)
	}
	if !strings.Contains(err.Error(), "sess-1") {
		t.Errorf("err = %q, does not name the session", err)
	}
}

func TestQueuedNudgeWithAnEmptyLedgerStaysUnconfirmedAndSaysWhy(t *testing.T) {
	reached, confirmed, err := classifyNudgeSubmitErr(errSubmitQueuedBehindRun, "sess-1", sessionlog.NudgeQueueUnrecorded)
	if !reached {
		t.Error("reachedTmux = false: the keys did reach tmux")
	}
	if confirmed {
		t.Fatal("confirmed = true with no ledger record at all")
	}
	if errors.Is(err, ErrNudgeQueuedPendingDrain) {
		t.Fatal("an empty ledger was reported as an observed queue entry: this is the modal-dialog strand, where the TUI never took the text, and it must not borrow the drain's reassurance")
	}
	if !errors.Is(err, ErrNudgeSubmitUnconfirmed) {
		t.Fatalf("err = %v, want ErrNudgeSubmitUnconfirmed", err)
	}
	if !strings.Contains(err.Error(), "no record") {
		t.Errorf("err = %q, want it to say the ledger holds no record, so a reader knows an observation was made and came back empty", err)
	}
}

// The ledger verdict must not leak onto the other exits. An overlay is a
// different fault with a different remedy, and a genuine tmux failure must
// stay hard however the ledger reads.
func TestLedgerVerdictDoesNotTouchTheOtherExits(t *testing.T) {
	_, confirmed, err := classifyNudgeSubmitErr(errSubmitOverlayPresent, "sess-1", sessionlog.NudgeQueueDelivered)
	if confirmed {
		t.Error("an overlay exit was confirmed by a ledger reading that belongs to the queued path")
	}
	if !errors.Is(err, ErrNudgeSubmitUnconfirmed) || !strings.Contains(err.Error(), "overlay") {
		t.Errorf("err = %v, want the overlay wording preserved", err)
	}
	reached, confirmed, err := classifyNudgeSubmitErr(errors.New("tmux: no server running"), "sess-1", sessionlog.NudgeQueueDelivered)
	if reached || confirmed {
		t.Error("a genuine send failure was laundered into a delivery by the ledger verdict")
	}
	if errors.Is(err, ErrNudgeSubmitUnconfirmed) {
		t.Error("a genuine send failure was put on the unconfirmed path")
	}
}

// scriptedTmuxExecutor answers exactly the tmux calls it was given and
// REFUSES everything else.
//
// A stand-in that answered every call with success would hand a pass to
// whatever this suite forgot to script -- and what it would hide is precisely
// the seam under test, since a wrong `show-environment` key or a wrong
// display-message format still resolves to SOME path when the fake is
// permissive. The refusal is what makes a missed call visible.
type scriptedTmuxExecutor struct {
	answers map[string]string
	refused []string
}

func (s *scriptedTmuxExecutor) key(args []string) string { return strings.Join(args, " ") }

func (s *scriptedTmuxExecutor) execute(args []string) (string, error) {
	k := s.key(args)
	if out, ok := s.answers[k]; ok {
		return out, nil
	}
	s.refused = append(s.refused, k)
	return "", fmt.Errorf("unscripted tmux call: %s", k)
}

func (s *scriptedTmuxExecutor) executeCtx(_ context.Context, args []string) (string, error) {
	return s.execute(args)
}

// The resolution seam, end to end against a real transcript on disk: pane
// environment -> search root -> project slug -> JSONL file -> verdict.
//
// It is here rather than in internal/sessionlog because neither half can fail
// alone in a way the other notices. A reader that works on a path handed to
// it, plus a resolver that returns a path nobody reads, is two green suites
// and no witness -- and this witness is the one gc has never had.
// ledgerPane writes a transcript recording nudge as delivered and returns a
// carrier whose pane reports provider, its scripted executor, and the text.
func ledgerPane(t *testing.T, provider string) (*Tmux, *scriptedTmuxExecutor, string) {
	t.Helper()
	configDir := t.TempDir()
	workDir := t.TempDir()
	slugDir := filepath.Join(configDir, "projects", sessionlog.ProjectSlug(workDir))
	if err := os.MkdirAll(slugDir, 0o750); err != nil {
		t.Fatalf("creating slug dir: %v", err)
	}
	// Salted from the temp path, which varies per run, so a stale file or a
	// reader that matched on something other than content cannot pass. The
	// nudge text is DERIVED from the salt rather than chosen beside it, so
	// the two can never coincide by accident.
	nudge := "claim your work now, slot " + filepath.Base(workDir)
	ledger := fmt.Sprintf(
		`{"type":"queue-operation","operation":"enqueue","timestamp":%q,"content":%q}`+"\n"+
			`{"type":"queue-operation","operation":"remove","timestamp":%q,"content":%q,"reason":"absorbed_mid_turn"}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano), nudge,
		time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano), nudge,
	)
	if err := os.WriteFile(filepath.Join(slugDir, "live.jsonl"), []byte(ledger), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}

	exec := &scriptedTmuxExecutor{answers: map[string]string{
		"-u show-environment -t sess-1 GC_PROVIDER":                "GC_PROVIDER=" + provider,
		"-u show-environment -t sess-1 CLAUDE_CONFIG_DIR":          "CLAUDE_CONFIG_DIR=" + configDir,
		"-u display-message -t sess-1:^.0 -p #{pane_current_path}": workDir,
	}}
	return &Tmux{cfg: DefaultConfig(), exec: exec}, exec, nudge
}

func TestObserveNudgeLedgerResolvesThePanesOwnTranscript(t *testing.T) {
	tm, exec, nudge := ledgerPane(t, "claude")
	got := tm.observeNudgeLedger("sess-1", nudge, time.Now().Add(-time.Minute))
	if len(exec.refused) > 0 {
		t.Fatalf("the carrier made tmux calls this test did not script: %v", exec.refused)
	}
	if got != sessionlog.NudgeQueueDelivered {
		t.Fatalf("observeNudgeLedger = %v, want NudgeQueueDelivered", got)
	}
}

// The gate has to be WIRED, not merely present. This drives a codex pane over
// a worktree whose claude transcript records the nudge as delivered -- the
// stale-transcript shape paneLedgerReadable exists to refuse -- so a carrier
// that forgot to consult it confirms a delivery that never happened.
func TestObserveNudgeLedgerRefusesAPaneWhoseProviderDoesNotWriteIt(t *testing.T) {
	tm, _, nudge := ledgerPane(t, "codex")
	if got := tm.observeNudgeLedger("sess-1", nudge, time.Now().Add(-time.Minute)); got != sessionlog.NudgeQueueUnrecorded {
		t.Fatalf("observeNudgeLedger = %v for a codex pane, want NudgeQueueUnrecorded: this transcript belongs to claude and may be stale", got)
	}
}

// A pane whose transcript cannot be resolved must read as Unrecorded, never
// as an error the caller has to interpret and never as a confirmation. This
// is the whole reason the observer swallows its own failures.
func TestObserveNudgeLedgerReadsAnUnresolvableTranscriptAsUnrecorded(t *testing.T) {
	exec := &scriptedTmuxExecutor{answers: map[string]string{
		"-u show-environment -t sess-1 GC_PROVIDER":                "GC_PROVIDER=claude",
		"-u show-environment -t sess-1 CLAUDE_CONFIG_DIR":          "CLAUDE_CONFIG_DIR=" + t.TempDir(),
		"-u display-message -t sess-1:^.0 -p #{pane_current_path}": t.TempDir(),
	}}
	tm := &Tmux{cfg: DefaultConfig(), exec: exec}
	if got := tm.observeNudgeLedger("sess-1", "a nudge nobody recorded", time.Now().Add(-time.Minute)); got != sessionlog.NudgeQueueUnrecorded {
		t.Fatalf("observeNudgeLedger = %v, want NudgeQueueUnrecorded", got)
	}
}

// The other exit the ledger reaches: the confirm loop ran out of budget with
// every pane source abstaining. That is the ga-bwm lost-Enter shape and the
// short-turn repaint race (ci-mdfcgs), and in both the message may well have
// landed -- so the transcript gets the last word before gc reports a doubt.
//
// Only the confirming direction is taken, and the two negative cases below
// are what pin that. An Enqueued message has not reached the agent, and an
// unresolvable transcript is the same silence a stranded nudge produces: a
// classifier that upgraded either would rebuild the false success on a second
// source.
func TestUnconfirmedSubmitIsOverturnedOnlyByAnObservedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		queue   sessionlog.NudgeQueueState
		wantNil bool
	}{
		{"the agent consumed it", sessionlog.NudgeQueueDelivered, true},
		{"still sitting in the queue", sessionlog.NudgeQueueEnqueued, false},
		{"no record at all", sessionlog.NudgeQueueUnrecorded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyUnconfirmedSubmit("sess-1", tc.queue)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("err = %v, want nil: the transcript records the agent receiving this exact message", err)
				}
				return
			}
			if !errors.Is(err, ErrNudgeSubmitUnconfirmed) {
				t.Fatalf("err = %v, want ErrNudgeSubmitUnconfirmed", err)
			}
			if !strings.Contains(err.Error(), "sess-1") {
				t.Errorf("err = %q, does not name the session", err)
			}
		})
	}
}

// The ledger is claude's. Codex rides the same submit-verify loop and keeps
// its transcript somewhere else, so consulting this reader for a codex pane
// would resolve whatever claude transcript the worktree happens to hold --
// and a worktree that ran claude yesterday holds a stale one. The gate is
// cheap and the alternative is a witness whose soundness rests on the
// cutoff alone.
func TestLedgerIsNotConsultedForAProviderThatDoesNotWriteIt(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{"claude", true},
		{"", true}, // no GC_PROVIDER at all: the path resolution decides
		{"codex", false},
		{"gemini", false},
		// Not a family sessionlog recognizes, so it is not claude. Named
		// here because the opposite reading is tempting: the gate keys on
		// ProviderFamily exactly as submitVerifyEligible does, and a value
		// that does not resolve must fall the same way in both.
		{"claude-5", false},
	} {
		if got := paneLedgerReadable(tc.provider); got != tc.want {
			t.Errorf("paneLedgerReadable(%q) = %v, want %v", tc.provider, got, tc.want)
		}
	}
}
