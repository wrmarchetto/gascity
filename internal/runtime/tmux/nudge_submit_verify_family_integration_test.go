//go:build integration

package tmux

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// Scope: the pane-level half of the submit-verify family gate -- that a real
// tmux pane carrying a city codex GC_PROVIDER reaches the confirm-and-resend
// arm of NudgeSession, observable only through the arm's own return contract.
// The pure family predicate is pinned in nudge_submit_verify_family_test.go.
// Run:
//
//	go test -tags integration ./internal/runtime/tmux/ -run SubmitVerify

// TestCodexPaneTakesTheSubmitVerifyArm proves a codex pane reaches the
// confirm-and-resend loop, and proves it through BEHAVIOR rather than by
// calling the predicate: a test that only asserted submitVerifyEligible were
// true would still pass if NudgeSession stopped consulting it.
//
// The discriminator is the return value on a pane that can never go busy. A
// plain shell pane renders no busy indicator and holds no claude-shaped input
// box, so both evidence sources withhold confirmation: the verify arm burns
// its budget and returns ErrNudgeSubmitUnconfirmed, while the best-effort
// fallback arm returns nil after a single send. Those two are the only
// outcomes, and they are distinguishable -- which is exactly the distinction
// the defect erased for codex (a nudge that never submitted, reported as
// delivered).
func TestCodexPaneTakesTheSubmitVerifyArm(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	cases := []struct {
		name     string
		provider string
		// wantUnconfirmed is true for a family on the verify arm.
		wantUnconfirmed bool
	}{
		// The city names codex providers per role; none of its panes ever
		// carries the bare family name, so this is the value that matters.
		{name: "codex-rig-engineer", provider: "codex-rig-engineer", wantUnconfirmed: true},
		{name: "codex", provider: "codex", wantUnconfirmed: true},
		{name: "claude", provider: "claude", wantUnconfirmed: true},
		// grok has no measured busy indicator, so it must stay on
		// best-effort delivery. Without this row the test would still pass
		// if the gate were widened to every provider, which is the change
		// that would burn a full confirm budget on every nudge in the city.
		{name: "grok", provider: "grok", wantUnconfirmed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := testTmux()
			sessionName := fmt.Sprintf("gt-test-submitverify-%s-%d", tc.name, time.Now().UnixNano()%10000)
			_ = tm.KillSession(sessionName)
			if err := tm.NewSessionWithCommandAndEnv(sessionName, os.TempDir(), "cat -v", map[string]string{
				"GC_PROVIDER": tc.provider,
			}); err != nil {
				t.Fatalf("NewSessionWithCommandAndEnv: %v", err)
			}
			defer func() { _ = tm.KillSession(sessionName) }()
			time.Sleep(300 * time.Millisecond)

			if got := tm.submitVerifyEligible(sessionName); got != tc.wantUnconfirmed {
				t.Errorf("submitVerifyEligible(GC_PROVIDER=%q) = %v, want %v", tc.provider, got, tc.wantUnconfirmed)
			}

			// The draft fingerprint needs draftHeadMinRunes of first-line
			// text to be distinctive at all; a shorter message makes that
			// evidence abstain for a reason unrelated to the provider.
			err := tm.NudgeSession(sessionName, "confirm-arm probe message for the submit verify gate")
			switch {
			case tc.wantUnconfirmed && !errors.Is(err, ErrNudgeSubmitUnconfirmed):
				t.Fatalf("NudgeSession(GC_PROVIDER=%q) = %v, want ErrNudgeSubmitUnconfirmed (the verify arm's outcome on a never-busy pane)", tc.provider, err)
			case !tc.wantUnconfirmed && err != nil:
				t.Fatalf("NudgeSession(GC_PROVIDER=%q) = %v, want nil (the best-effort fallback arm's outcome)", tc.provider, err)
			}
		})
	}
}
