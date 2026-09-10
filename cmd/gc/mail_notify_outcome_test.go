// Scope: what `gc mail send --notify` tells its sender when the recipient did
// not take the notification live.
//
// The suite exists because the answer used to be nothing. A mid-turn recipient
// produced exit 0, an empty stderr, and `"notified": true` in --json, so the
// mayor issued a correction and had no way to learn it had not landed
// (ci-7b1ueb). The cases below pin the REPORT: which outcome the notify path
// returns, that the delivery stamp is not written for it, and that the CLI
// carries it to both stderr and --json.
//
// Delegated elsewhere: why the wait declines at all, and the vocabulary of
// reasons, belong to internal/session/nudge_skip_test.go; whether a queued
// nudge is ever CONSUMED is the poller's, not this suite's -- see the note on
// TestSendMailNotifyToBusySessionQueuesAndReportsMidTurn.
//
// Run: go test ./cmd/gc/ -run 'MailNotify|NotifyOutcome|BusySession'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// TestSendMailNotifyToBusySessionQueuesAndReportsMidTurn is the reproduction
// the bead asked for: a live claude session whose wait for an idle prompt
// times out.
//
// It asserts four things together, and each covers a way the other three can
// pass while the defect stands: the outcome names the recipient's state, no
// byte reached the pane (a mid-turn write is the unsafe thing the wait
// prevents), the delivery stamp was NOT written (the physical evidence the
// bead used to establish that no delivery happened), and the message did reach
// the queue.
//
// NOT asserted here, deliberately: that anything ever CONSUMES the queued
// item. That is a real hole and it is a different one -- in legacy dispatcher
// mode the only prompt-free reader is the nudge poller. Asserting presence
// here would read as coverage of delivery, which is exactly the mistake the
// bead warns about, so the absence is called out rather than left to be
// inferred.
func TestSendMailNotifyToBusySessionQueuesAndReportsMidTurn(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store, fake, nil)

	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{Template: "bench-engineer", Title: "Bench", Command: "claude", WorkDir: dir, Provider: "claude", Env: nil, Resume: session.ProviderResume{}, Hints: runtime.Config{WorkDir: dir}, ExtraMeta: map[string]string{"session_origin": "manual"}})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := mgr.Start(context.Background(), info.ID, "", runtime.Config{WorkDir: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The session is live and answering; it just never reaches an idle prompt.
	fake.WaitForIdleErrors[info.SessionName] = runtime.ErrIdleTimeout

	target := nudgeTarget{
		cityPath:    dir,
		agent:       config.Agent{Name: "bench-engineer"},
		sessionID:   info.ID,
		resolved:    &config.ResolvedProvider{Name: "claude"},
		sessionName: info.SessionName,
	}

	outcome, err := sendMailNotifyWithWorker(target, store, fake, "mayor")
	if err != nil {
		t.Fatalf("sendMailNotifyWithWorker: %v", err)
	}
	if outcome.Delivered {
		t.Fatal("outcome.Delivered = true, want false for a mid-turn recipient")
	}
	if !outcome.Queued {
		t.Fatal("outcome.Queued = false, want true; an undelivered notify must leave a durable record")
	}
	if outcome.Skip != session.NudgeSkipBusy {
		t.Fatalf("outcome.Skip = %q, want %q", outcome.Skip, session.NudgeSkipBusy)
	}

	for _, call := range fake.Calls {
		if call.Method == "Nudge" || call.Method == "NudgeNow" {
			t.Fatalf("calls = %#v, want no pane write to a mid-turn session", fake.Calls)
		}
	}

	refetched, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", info.ID, err)
	}
	if stamp := strings.TrimSpace(refetched.Metadata[session.MetadataLastNudgeDeliveredAt]); stamp != "" {
		t.Fatalf("%s = %q, want absent when nothing was delivered", session.MetadataLastNudgeDeliveredAt, stamp)
	}

	pending, _, _, err := listQueuedNudgesForTarget(dir, target, time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudgesForTarget: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending queued nudges = %d, want 1", len(pending))
	}
}

// TestMailNotifyOutcomeStringNamesTheRecoverableCase pins that the sender is
// told a queued message is still coming, rather than only that delivery
// failed. The distinction is the whole point of reporting at all: a sender who
// reads "not delivered" and nothing else re-sends or switches channel.
func TestMailNotifyOutcomeStringNamesTheRecoverableCase(t *testing.T) {
	busy := mailNotifyOutcome{Queued: true, Skip: session.NudgeSkipBusy}
	if got := busy.String(); !strings.Contains(got, "mid-turn") {
		t.Fatalf("busy outcome reads %q, want it to say the recipient is mid-turn", got)
	}
	if got := (mailNotifyOutcome{Delivered: true}).String(); !strings.Contains(got, "delivered") {
		t.Fatalf("delivered outcome reads %q, want it to say so", got)
	}
}

// TestMailSendReportsAQueuedRecipientInsteadOfClaimingNotified drives the CLI
// surface. Before ci-7b1ueb "notified": true was set from the nudge call not
// returning an error, which every queued outcome also satisfies -- so this
// pins the flag against the DELIVERY rather than against the call.
func TestMailSendReportsAQueuedRecipientInsteadOfClaimingNotified(t *testing.T) {
	store := beads.NewMemStore()
	mp := beadmail.New(store)
	recipients := map[string]bool{"human": true, "bench-engineer": true}

	nf := func(_ string) (mailNotifyOutcome, error) {
		return mailNotifyOutcome{Queued: true, Skip: session.NudgeSkipBusy}, nil
	}

	var stdout, stderr bytes.Buffer
	code := doMailSendJSON(mp, events.Discard, recipients, "mayor", []string{"bench-engineer", "stop and re-read the spec"}, nf, true, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doMailSendJSON = %d, want 0; stderr: %s", code, stderr.String())
	}

	var result mailActionResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", stdout.String(), err)
	}
	if result.Notified {
		t.Fatal("notified = true for a message that only reached the queue")
	}
	if !result.NotifyQueued {
		t.Fatal("notify_queued = false, want true")
	}
	if result.NotifySkip != session.NudgeSkipBusy {
		t.Fatalf("notify_skip = %q, want %q", result.NotifySkip, session.NudgeSkipBusy)
	}

	// The stderr line has to name the recipient: the sender is often
	// broadcasting, and "someone was busy" is not actionable.
	if !strings.Contains(stderr.String(), "bench-engineer") {
		t.Fatalf("stderr = %q, want the recipient named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "mid-turn") {
		t.Fatalf("stderr = %q, want the mid-turn reason", stderr.String())
	}
}

// TestMailSendPrintsNothingExtraOnALiveDelivery is the control on the case
// above. A warning printed on every send is one the reader learns to skip,
// which would undo the reporting it is meant to provide.
func TestMailSendPrintsNothingExtraOnALiveDelivery(t *testing.T) {
	store := beads.NewMemStore()
	mp := beadmail.New(store)
	recipients := map[string]bool{"human": true, "bench-engineer": true}

	nf := func(_ string) (mailNotifyOutcome, error) {
		return mailNotifyOutcome{Delivered: true}, nil
	}

	var stdout, stderr bytes.Buffer
	code := doMailSendJSON(mp, events.Discard, recipients, "mayor", []string{"bench-engineer", "carry on"}, nf, true, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doMailSendJSON = %d, want 0; stderr: %s", code, stderr.String())
	}
	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("stderr = %q, want empty on a live delivery", stderr.String())
	}

	var result mailActionResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", stdout.String(), err)
	}
	if !result.Notified {
		t.Fatal("notified = false for a live delivery")
	}
	if result.NotifyQueued {
		t.Fatal("notify_queued = true for a live delivery")
	}
	if result.NotifySkip != session.NudgeSkipNone {
		t.Fatalf("notify_skip = %q, want empty for a live delivery", result.NotifySkip)
	}
}

// TestMailSendAllReportsNotifiedOnlyWhenEveryRecipientTookItLive pins the
// broadcast reading of the flag. Any-recipient was the previous behavior and
// it makes the flag unusable: one live session among ten mid-turn ones
// reported a notified broadcast.
func TestMailSendAllReportsNotifiedOnlyWhenEveryRecipientTookItLive(t *testing.T) {
	store := beads.NewMemStore()
	mp := beadmail.New(store)
	recipients := map[string]bool{"alpha": true, "beta": true}

	nf := func(recipient string) (mailNotifyOutcome, error) {
		if recipient == "alpha" {
			return mailNotifyOutcome{Delivered: true}, nil
		}
		return mailNotifyOutcome{Queued: true, Skip: session.NudgeSkipBusy}, nil
	}

	var stdout, stderr bytes.Buffer
	code := doMailSendAllJSON(mp, events.Discard, recipients, "mayor", []string{"all hands", "stand by"}, nf, true, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doMailSendAllJSON = %d, want 0; stderr: %s", code, stderr.String())
	}

	var result mailActionResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", stdout.String(), err)
	}
	if result.Notified {
		t.Fatal("notified = true for a broadcast where a recipient was mid-turn")
	}
	if !result.NotifyQueued {
		t.Fatal("notify_queued = false, want true when any recipient was queued")
	}
	if !strings.Contains(stderr.String(), "beta") {
		t.Fatalf("stderr = %q, want the queued recipient named", stderr.String())
	}
	if strings.Contains(stderr.String(), "alpha") {
		t.Fatalf("stderr = %q, want no line for the recipient that took it live", stderr.String())
	}
}
