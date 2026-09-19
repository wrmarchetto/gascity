package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

// Scope: classifyBackstopDelivery, which decides whether a claim-backstop
// nudge is reported to an operator as delivered, queued, or failed.
//
// The suite exists because this backstop's output IS the operator's wedged-
// slot list, and until ci-tihynr resolved, every nudge into a busy pane
// landed in it. The measured behavior is that such a message drains at turn
// end unaided, so those lines described deliveries in progress -- and a false
// entry in a wedged-slot list is indistinguishable from a true one, which is
// how a mayor came to hand-clear the same non-wedge repeatedly.
//
// The narrowing is the part worth pinning. A bare unconfirmed submit must
// KEEP the failure label: it carries no observation, and the state it most
// often names now is a pane stranded behind a modal dialog, where nothing
// drains. A test that only proved the queued case is reported kindly would
// pass just as well against a classifier that swallowed every unconfirmed
// error.
//
//	go test ./cmd/gc/ -run BackstopDelivery

func TestBackstopDeliveryReportsAnObservedQueueEntryAsQueuedNotFailed(t *testing.T) {
	err := fmt.Errorf("%w: session %q: observed in the queue", runtime.ErrNudgeQueuedPendingDrain, "toolsmith-1")
	if got := classifyBackstopDelivery(err); got != backstopDeliveryQueued {
		t.Fatalf("classifyBackstopDelivery = %v, want backstopDeliveryQueued: the provider read its own ledger and found the message, so this is a delivery in progress", got)
	}
}

func TestBackstopDeliveryKeepsABareUnconfirmedSubmitAFailure(t *testing.T) {
	err := fmt.Errorf("%w: session %q", runtime.ErrNudgeSubmitUnconfirmed, "toolsmith-1")
	if got := classifyBackstopDelivery(err); got != backstopDeliveryFailed {
		t.Fatalf("classifyBackstopDelivery = %v, want backstopDeliveryFailed: nothing observed this message reaching the agent, and a modal-blocked pane reports exactly this", got)
	}
}

func TestBackstopDeliveryReportsACleanNudgeDelivered(t *testing.T) {
	if got := classifyBackstopDelivery(nil); got != backstopDeliveryDelivered {
		t.Fatalf("classifyBackstopDelivery(nil) = %v, want backstopDeliveryDelivered", got)
	}
}

func TestBackstopDeliveryReportsATransportFailureFailed(t *testing.T) {
	if got := classifyBackstopDelivery(errors.New("tmux: no server running")); got != backstopDeliveryFailed {
		t.Fatalf("classifyBackstopDelivery = %v, want backstopDeliveryFailed", got)
	}
}
