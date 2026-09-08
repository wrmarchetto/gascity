package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/extmsg"
)

// extmsgBindingReapOutcome is what one binding sweep did, returned so the
// reconciler tick can record it in the trace. The stderr line deliberately
// stays change-only -- the tick runs every few seconds and a per-tick
// "scanned=1" line would bury the lines reporting an actual repair -- which
// leaves a sweep that scanned and correctly changed nothing byte-identical in
// the log to a tick that never swept. That ambiguity cost ci-fdr7cf's author a
// wrong suspect: the supervisor log could not settle whether a stranded
// binding had ever been examined. Ran separates the two, and Err keeps a
// failed sweep from collapsing into either.
type extmsgBindingReapOutcome struct {
	Ran   bool
	Err   error
	Stats extmsg.BindingReapStats
}

// traceFields renders the outcome for recordPhase. Reported even when nothing
// changed: the zero counts are the answer to "was it examined".
func (o extmsgBindingReapOutcome) traceFields() map[string]any {
	fields := map[string]any{
		"ran":        o.Ran,
		"scanned":    o.Stats.Scanned,
		"reassigned": o.Stats.Reassigned,
		"cleared":    o.Stats.Cleared,
	}
	if o.Err != nil {
		fields["error"] = o.Err.Error()
	}
	return fields
}

// extmsgParticipantReapOutcome is the participant-side companion to
// extmsgBindingReapOutcome, for the same reason: that sweep's stderr line is
// change-only too, and it is the line whose silence was misread as an
// unfinished sweep.
type extmsgParticipantReapOutcome struct {
	Ran   bool
	Err   error
	Stats extmsg.ParticipantReapStats
}

// traceFields renders the outcome for recordPhase.
func (o extmsgParticipantReapOutcome) traceFields() map[string]any {
	fields := map[string]any{
		"ran":        o.Ran,
		"scanned":    o.Stats.Scanned,
		"reassigned": o.Stats.Reassigned,
	}
	if o.Err != nil {
		fields["error"] = o.Err.Error()
	}
	return fields
}

// reapStaleExtmsgBindings reconciles external-message conversation bindings
// against live session identity on each reconciler tick. A binding stores the
// session bead ID it was created against; when that session crashes and
// respawns under the same name it gets a fresh bead ID, leaving the binding
// pointing at a dead session so inbound triage silently drops and a fresh bind
// is rejected as a conflict. The reaper re-points bindings at the respawned
// session and clears bindings whose session is gone.
//
// It runs after session beads have been synced for the tick so a respawned
// session's replacement bead is already visible. Errors are logged and
// swallowed so a binding-store hiccup never stalls the reconciler loop; the
// returned outcome carries them onward for the tick trace.
func reapStaleExtmsgBindings(ctx context.Context, store beads.SessionStore, now time.Time, stderr io.Writer) extmsgBindingReapOutcome {
	if store.Store == nil {
		return extmsgBindingReapOutcome{}
	}
	if stderr == nil {
		stderr = io.Discard
	}
	out := extmsgBindingReapOutcome{Ran: true}
	out.Stats, out.Err = extmsg.ReapStaleBindings(ctx, store.Store, now)
	if out.Err != nil {
		fmt.Fprintf(stderr, "session reconciler: reaping stale extmsg bindings: %v\n", out.Err) //nolint:errcheck
		return out
	}
	if out.Stats.Reassigned > 0 || out.Stats.Cleared > 0 {
		fmt.Fprintf(stderr, "session reconciler: extmsg bindings reaped (reassigned=%d cleared=%d scanned=%d)\n", //nolint:errcheck
			out.Stats.Reassigned, out.Stats.Cleared, out.Stats.Scanned)
	}
	return out
}

// reapStaleExtmsgParticipants reconciles external-message group participants
// against live session identity on each reconciler tick — the participant-side
// companion to reapStaleExtmsgBindings. Group-participant routing self-heals at
// read time, but the group-owned transcript membership (keyed by session ID)
// does not, and a binding-less group participant whose session respawns is
// reached by no other backstop, so without this sweep its membership would stay
// stranded on the retired session bead. It runs on the same tick and after
// session beads have been synced. Errors are logged and swallowed so a
// participant-store hiccup never stalls the reconciler loop; the returned
// outcome carries them onward for the tick trace.
func reapStaleExtmsgParticipants(ctx context.Context, store beads.SessionStore, stderr io.Writer) extmsgParticipantReapOutcome {
	if store.Store == nil {
		return extmsgParticipantReapOutcome{}
	}
	if stderr == nil {
		stderr = io.Discard
	}
	out := extmsgParticipantReapOutcome{Ran: true}
	out.Stats, out.Err = extmsg.ReapStaleParticipants(ctx, store.Store)
	if out.Err != nil {
		fmt.Fprintf(stderr, "session reconciler: reaping stale extmsg participants: %v\n", out.Err) //nolint:errcheck
		return out
	}
	if out.Stats.Reassigned > 0 {
		fmt.Fprintf(stderr, "session reconciler: extmsg participants reaped (reassigned=%d scanned=%d)\n", //nolint:errcheck
			out.Stats.Reassigned, out.Stats.Scanned)
	}
	return out
}
