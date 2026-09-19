package tmux

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/sessionlog"
)

// classifyNudgeSubmitErr decides two things a caller cannot recover later:
// whether the keys REACHED TMUX (which is what makes a nudge "delivered"), and
// what the operator is told. Both sentinel branches were unreachable from any
// unit test while the logic sat inline in NudgeSession, which is why the
// queued-behind-run branch is extracted and driven here rather than trusted.
//
// The hazard is one-directional and specific: routing a sentinel onto the
// hard-error path tells the caller the SEND failed. That is false -- the keys
// reached tmux -- and it takes the ack/retry split the wrong way, a split that
// has already been corrected once (ci-uihrrv).
//
//	go test ./internal/runtime/tmux/ -run ClassifyNudgeSubmitErr
func TestClassifyNudgeSubmitErrKeepsSentinelsOnTheUnconfirmedPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantSay string
	}{
		{"overlay", errSubmitOverlayPresent, "overlay"},
		{"queued behind a running turn", errSubmitQueuedBehindRun, "already busy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached, confirmed, mapped := classifyNudgeSubmitErr(tc.err, "sess-1", sessionlog.NudgeQueueUnrecorded)
			if confirmed {
				t.Error("confirmed = true with an empty queue ledger: nothing observed this message reaching the agent")
			}
			if !reached {
				t.Error("reachedTmux = false: the keys did reach tmux, so the nudge is delivered-but-unconfirmed, not a send failure")
			}
			if !errors.Is(mapped, ErrNudgeSubmitUnconfirmed) {
				t.Errorf("mapped = %v, want it to wrap ErrNudgeSubmitUnconfirmed", mapped)
			}
			// Each sentinel must NAME its cause. A bare "unconfirmed" sends a
			// reader hunting a wedged agent when the pane is merely showing a
			// menu, or is merely busy.
			if !strings.Contains(mapped.Error(), tc.wantSay) {
				t.Errorf("mapped = %q, want it to say %q so the operator is not sent hunting the wrong thing", mapped, tc.wantSay)
			}
			if !strings.Contains(mapped.Error(), "sess-1") {
				t.Errorf("mapped = %q, does not name the session", mapped)
			}
		})
	}
}

// An ordinary tmux failure must NOT be laundered into the unconfirmed path:
// that would tell a caller the keys landed when they did not, and the retry it
// then skips is the one that would have delivered the message.
func TestClassifyNudgeSubmitErrKeepsARealSendFailureHard(t *testing.T) {
	sendErr := fmt.Errorf("tmux: no server running")
	reached, confirmed, mapped := classifyNudgeSubmitErr(sendErr, "sess-1", sessionlog.NudgeQueueUnrecorded)
	if confirmed {
		t.Error("confirmed = true for a genuine send failure")
	}
	if reached {
		t.Error("reachedTmux = true for a genuine send failure: nothing reached tmux, so this must not be reported delivered")
	}
	if errors.Is(mapped, ErrNudgeSubmitUnconfirmed) {
		t.Error("a real send failure was laundered onto the unconfirmed path")
	}
	if !errors.Is(mapped, sendErr) {
		t.Errorf("mapped = %v, must wrap the underlying tmux error", mapped)
	}
}
