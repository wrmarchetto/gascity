package workrelease

import (
	"errors"
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

// ReleaseIfCurrent attempts the store's atomic conditional release of item
// from the assignee the CALLER enumerated, so a bead re-claimed since that
// read is not displaced.
//
// Returns handled=false when no CAS is available for this snapshot -- the
// store does not implement the verb, answers
// ErrConditionalReleaseUnsupported, or the snapshot is outside the verb's
// contract -- and the caller must then take its own unconditional path.
// handled=true with released=false is an AUTHORITATIVE refusal: the
// assignment moved, and the release must not be retried unconditionally.
//
// A fault is returned as an error rather than folded into handled=false. An
// unresolved CAS means ownership is UNKNOWN, and treating unknown as "not
// held" is how a transient backend error becomes a second holder.
//
// THE VERB'S CONTRACT IS THE in_progress+assigned SHAPE ONLY, which is why
// this reports handled=false rather than refusing for anything else. bd
// backends may persist an unassigned bead as SQL NULL rather than ”, and an
// open bead parked on a pool route name has no assignment to compare, so
// narrowing a caller to the CAS alone would strand exactly the beads these
// sweeps exist to recover (#2793).
//
// THIS IS THE THIRD COPY OF THIS LOGIC and it is the one that should survive.
// cmd/gc has releasePoolAssignmentIfCurrent (pool_session_name.go) and, on
// fix/ci-23nak7-cross-pool-double-claim, releaseWorkBeadIfCurrent
// (work_assignment.go). It lives here because workrelease already owns the
// other half of a release -- Metadata and Options -- and the CAS swaps only
// status/assignee, so the two halves have to agree. Collapse the cmd/gc pair
// onto this once that branch lands; they differ only in how they log.
func ReleaseIfCurrent(store beads.Store, item beads.Bead) (released, handled bool, err error) {
	expectedAssignee := strings.TrimSpace(item.Assignee)
	if item.Status != "in_progress" || expectedAssignee == "" {
		return false, false, nil
	}
	releaser, ok := store.(beads.ConditionalAssignmentReleaser)
	if !ok {
		return false, false, nil
	}
	released, err = releaser.ReleaseIfCurrent(item.ID, expectedAssignee)
	if err != nil {
		if errors.Is(err, beads.ErrConditionalReleaseUnsupported) {
			return false, false, nil
		}
		return false, true, fmt.Errorf("conditionally releasing %s from %q: %w", item.ID, expectedAssignee, err)
	}
	return released, true, nil
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
// This deliberately does NOT do the cross-store fan-out the reconciler's
// cached-enumeration path needs; it reads one store live, and a caller with rig
// stores calls it once per store.
//
// It DOES compare-and-swap. Reading live only narrows the read-to-write
// window, it does not close it: the List above and the Update below are
// separate round trips, and a bead re-claimed between them was reopened out
// from under its new holder -- then, because claim_routes puts two provider
// pools on one route, handed to the OTHER pool while the first was still
// working it. That was the argument for leaving this unguarded and it is
// wrong; the same window was fixed in ReleaseWorkBead (ci-23nak7) and in the
// closed-session path (ci-2hk, where the work simply ran twice with no error
// anywhere). Settled under ci-q5spdz and driven into failure first, by a
// stale-List fake, because a live store can never disagree with itself.
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
			swapped, handled, err := ReleaseIfCurrent(store, item)
			if err != nil {
				// A fault, not a refusal: ownership is unknown, so the
				// unconditional write below must NOT run. Counted in failed,
				// which is the signal the stranded-worker repair reads to
				// refuse to report success -- the contract forbids returning
				// the error itself.
				fmt.Fprintf(stderr, "work release: releasing work %s from ended session %s: %v\n", item.ID, sessionBead.ID, err) //nolint:errcheck
				failed++
				continue
			}
			if handled {
				if !swapped {
					// Authoritative refusal: a different session holds this
					// bead now, and it is correctly left with them. Counted
					// as released because this tally is release ATTEMPTS that
					// did not fault -- the same reading ReleaseWorkBead's
					// unclaimResult takes. A third counter was rejected: it
					// would touch every caller for a reporting nicety, and no
					// path reads the tally to make a decision beyond
					// failed > 0.
					fmt.Fprintf(stderr, "work release: skipping work %s from ended session %s: assignment changed since enumeration\n", item.ID, sessionBead.ID) //nolint:errcheck
					released++
					continue
				}
				// The CAS swapped status and assignee only, so the affinity
				// clears and the run_target fallback ride a separate
				// metadata-only write. Both sides read Metadata, so this
				// stamps byte-identically to the single-Update path below.
				// It runs ONLY after a successful swap: on a refusal this
				// write is not benign -- it would clear
				// gc.session_affinity and gc.continuation_group, and can
				// stamp a run_target, on a bead a live session is holding.
				if err := store.Update(item.ID, beads.UpdateOpts{Metadata: Metadata(item, fallbackRoute)}); err != nil {
					fmt.Fprintf(stderr, "work release: clearing affinity on released work %s from ended session %s: %v\n", item.ID, sessionBead.ID, err) //nolint:errcheck
					failed++
					continue
				}
				released++
				continue
			}
			// Everything the CAS cannot arbitrate keeps the original
			// unconditional write: an open bead parked on a seat identity
			// (outside ReleaseIfCurrent's contract) and any store without the
			// verb. Narrowing this to a refusal would strand the parked work
			// this sweep exists to recover, which is what
			// TestFromEndedSessionKeepsTheUnconditionalWriteWhereNoCASApplies
			// holds. No live-recheck fallback, for the reason ci-23nak7
			// recorded: all six stores in the tree declare
			// ConditionalAssignmentReleaser and production is Bd behind
			// Caching, so a recheck would cover only a backend that does not
			// exist yet.
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
