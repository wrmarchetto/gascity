package workrelease

import (
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

// Metadata returns the metadata patch a work release writes: the
// session-affinity clears, plus the run_target fallback when one is offered AND
// the bead carries no route of its own.
//
// Shared so the conditional (ReleaseIfCurrent) release path, whose CAS swaps
// only status/assignee and must therefore write the metadata separately, stamps
// byte-identically to the single-Update path. Duplicating the fallback condition
// at the second call site is how the two drift.
func Metadata(item beads.Bead, runTargetFallback string) map[string]string {
	metadata := make(map[string]string, len(beadmeta.SessionAffinityMetadataKeys)+1)
	for _, key := range beadmeta.SessionAffinityMetadataKeys {
		metadata[key] = ""
	}
	if runTargetFallback != "" &&
		strings.TrimSpace(item.Metadata[beadmeta.RunTargetMetadataKey]) == "" &&
		strings.TrimSpace(item.Metadata[beadmeta.RoutedToMetadataKey]) == "" {
		metadata[beadmeta.RunTargetMetadataKey] = runTargetFallback
	}
	return metadata
}

// Options returns the UpdateOpts one release emits: the assignee cleared
// (empty-string clear), stale session-affinity metadata cleared, and an
// in_progress bead reset to open so a fresh worker can re-claim it via the
// routed queue. An already-open bead keeps its status.
//
// Reopening is not cosmetic: an in_progress bead is invisible to every
// work_query tier once its session is gone -- Tier 1 needs an assignee match,
// Tiers 2/3 only match "ready".
func Options(item beads.Bead, runTargetFallback string) beads.UpdateOpts {
	empty := ""
	update := beads.UpdateOpts{
		Assignee: &empty,
		Metadata: Metadata(item, runTargetFallback),
	}
	if item.Status == "in_progress" {
		open := "open"
		update.Status = &open
	}
	return update
}

// Releasable reports whether one enumerated bead is WORK this sweep may act on.
//
// Session beads are excluded because a sweep releasing its own session bead is
// nonsense. Mail beads are excluded because a mail wisp carries its recipient in
// `assignee` and has no claim semantics at all: clearing it does not "release"
// anything, it destroys the wisp's only route to an inbox (ra-59207). The check
// lives here, at the source, rather than at each call site.
func Releasable(item beads.Bead) bool {
	return !session.IsSessionBeadOrRepairable(item) && !beadmail.IsMessageBead(item)
}

// FromEndedSession sweeps one store for work held by a session that has just
// ended, and releases what Targets says it may. It returns the number of beads
// released and the number of releases that failed.
//
// identities is the caller's own vocabulary of assignee values for this session
// (see SeatIdentityScope); fallbackRoute is the ending session's own template,
// stamped as gc.run_target only on a bead that would otherwise be left unrouted.
// Pass "" only when the caller genuinely has no route to offer -- a release with
// no fallback leaves otherwise-unrouted work open, unassigned AND unrouted,
// invisible to both the pool demand probe and the orphan sweep.
//
// Best-effort by contract: per-bead errors are counted and logged, never
// returned, because every caller runs this after the session bead is already
// closed and none of them can undo that. The counts let a caller that must know
// (a stranded-worker repair deciding whether to close the session bead) refuse
// to report success.
//
// This deliberately does NOT do the cross-store fan-out or the compare-and-swap
// re-verification that the reconciler's cached-enumeration path needs; it reads
// one store live. A caller with rig stores calls it once per store.
func FromEndedSession(
	store beads.Store,
	sessionBead beads.Bead,
	identities []string,
	fallbackRoute string,
	retired SeatRetirement,
	stderr io.Writer,
) (released, failed int) {
	if store == nil || strings.TrimSpace(sessionBead.ID) == "" {
		return 0, 0
	}
	if stderr == nil {
		stderr = io.Discard
	}
	seen := make(map[string]struct{})
	for _, target := range Targets(SeatFromBead(sessionBead), identities, retired) {
		work, err := store.List(beads.ListQuery{Assignee: target.Assignee, Status: target.Status})
		if err != nil {
			fmt.Fprintf(stderr, "work release: listing work assigned to ended session %s via %q: %v\n", sessionBead.ID, target.Assignee, err) //nolint:errcheck
			continue
		}
		for _, item := range work {
			if !Releasable(item) {
				continue
			}
			if _, dup := seen[item.ID]; dup {
				continue
			}
			seen[item.ID] = struct{}{}
			if err := store.Update(item.ID, Options(item, fallbackRoute)); err != nil {
				fmt.Fprintf(stderr, "work release: releasing work %s from ended session %s: %v\n", item.ID, sessionBead.ID, err) //nolint:errcheck
				failed++
				continue
			}
			released++
		}
	}
	return released, failed
}
