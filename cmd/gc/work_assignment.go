package main

import (
	"errors"
	"fmt"
	"strings"

	sessionpkg "github.com/gastownhall/gascity/internal/session"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/workrelease"
)

// workAssignment is the typed boundary façade the SESSION reconciler uses to
// read WORK beads keyed by a session identity. The reconciler's stranded/awake/
// drain probes are WORK queries (List{Assignee,Status,...} / ReadyLive) that
// happen to be keyed by a session's assignment identifiers; left raw they reach
// the WORK store *through* the session store. Routing them through this façade
// makes the WORK store the explicit source.
//
// The façade carries a beads.WorkStore (the work class), but every optional
// capability (CachedList, ReadyLive's Backing()) is asserted on the embedded
// .Store, never the wrapper — wrapping a Store in WorkStore does NOT promote
// optional capabilities, and asserting on the wrapper would silently drop the
// cache/live fast-paths (the typed-nil trap, OBJECT-MODEL-FRONT-DOOR-DESIGN.md
// invariant 3/4). On a single-store city the work store IS the same object the
// session arm uses, so the bead reads emitted here are byte-identical to the
// raw ops they replace.
type workAssignment struct {
	store beads.WorkStore
}

// workAssignmentForStore wraps a resolved WORK store as the typed assignment
// façade. The caller passes the work store value (cityWorkStore / a rig work
// store); the underlying store is unchanged, so reads stay byte-identical.
func workAssignmentForStore(store beads.WorkStore) workAssignment {
	return workAssignment{store: store}
}

// unwrapped returns the underlying generic Store for optional-capability
// assertions (CachedList, ReadyLive Backing()). Asserting on the WorkStore
// wrapper instead would fail because the wrapper only embeds the Store
// interface and does not promote optional capabilities.
func (w workAssignment) unwrapped() beads.Store {
	return w.store.Store
}

// OpenAssignedTo returns the open or in-progress WORK beads in this store
// assigned to the given identity for the given tier mode, excluding session
// beads and mail message beads. It is the typed form of the raw
// List{Assignee,Status,Live,TierMode} probe the reconciler ran directly.
// status selects the bead status ("open" / "in_progress"); live mirrors the
// raw ListQuery.Live flag. Session beads (and repairable session beads) are
// filtered out, matching the raw probes; mail message beads are filtered out
// here too (ra-59207) — a mail wisp has no claim/routing semantics, so every
// caller that reassigns or releases what this returns must never see one.
func (w workAssignment) OpenAssignedTo(assignee, status string, tierMode beads.TierMode, live bool) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	items, err := store.List(beads.ListQuery{Assignee: assignee, Status: status, Live: live, TierMode: tierMode})
	if err != nil {
		return nil, err
	}
	return excludeMailMessageBeads(items), nil
}

// CachedOpenAssignedWisps returns cached open-assigned wisp-tier WORK beads when
// the underlying store exposes the CachedList fast-path, plus whether the cache
// answered. It is the typed form of the positive-only cache probe in
// sessionHasOpenAssignedWispWork; the assertion is on the embedded .Store so the
// fast-path is preserved.
func (w workAssignment) CachedOpenAssignedWisps(assignee, status string) ([]beads.Bead, bool) {
	store := w.unwrapped()
	if store == nil {
		return nil, false
	}
	query := beads.ListQuery{Assignee: assignee, Status: status, TierMode: beads.TierWisps}
	cache, ok := store.(interface {
		CachedList(beads.ListQuery) ([]beads.Bead, bool)
	})
	if !ok {
		return nil, false
	}
	return cache.CachedList(query)
}

// ReadyAssignedTo returns the ready (unblocked, actionable) WORK beads assigned
// to the given identity for the given tier mode. It is the typed form of the raw
// beads.ReadyLive(ReadyQuery{Assignee,TierMode}) probe. ReadyLive is called on
// the embedded .Store so its Backing() live-read fast-path is preserved.
func (w workAssignment) ReadyAssignedTo(assignee string, tierMode beads.TierMode) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	return beads.ReadyLive(store, beads.ReadyQuery{Assignee: assignee, TierMode: tierMode})
}

// HasNonSessionWork reports whether any bead in items is non-session WORK
// (skipping session beads and repairable session beads). Shared filter for the
// boolean readiness/open probes.
func (w workAssignment) HasNonSessionWork(items []beads.Bead) bool {
	for _, item := range items {
		if sessionpkg.IsSessionBeadOrRepairable(item) {
			continue
		}
		return true
	}
	return false
}

// OpenAssignedToBasic returns the WORK beads assigned to the given identity with
// the given status, using the no-flags List{Assignee,Status} query (no Live, no
// TierMode). It is the typed form of the raw probe in
// releaseWorkFromClosedSessionBead, kept distinct from OpenAssignedTo because the
// close-release path deliberately runs the unflagged query — making it byte-
// identical to OpenAssignedTo's flagged query would change the emitted bead op.
// Like OpenAssignedTo, mail message beads are excluded (ra-59207): they are not
// WORK and have no claim/routing semantics for the release path to act on.
func (w workAssignment) OpenAssignedToBasic(assignee, status string) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	items, err := store.List(beads.ListQuery{Assignee: assignee, Status: status})
	if err != nil {
		return nil, err
	}
	return excludeMailMessageBeads(items), nil
}

// excludeMailMessageBeads filters mail message beads (beadmail.IsMessageBead)
// out of a WORK query result. A mail wisp is a delivery route, not a claimable
// unit of work — it can be neither released nor reassigned — so every WORK
// enumeration in this file (and every caller downstream, all of which treat
// their results as releasable/reassignable WORK) must exclude it at the source
// rather than repeat the check at each call site (ra-59207: the session-close
// WORK-RELEASE sweep clearing a mail bead's assignee silently destroyed its
// only route to an inbox).
func excludeMailMessageBeads(items []beads.Bead) []beads.Bead {
	if len(items) == 0 {
		return items
	}
	out := items[:0:0]
	for _, item := range items {
		if beadmail.IsMessageBead(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// ReleaseWorkBead detaches one WORK bead from its (closed/retired) session: it
// clears the assignee (empty-string clear), clears stale session-affinity
// metadata, and resets an in_progress bead to open so a fresh worker can
// re-claim it via the routed queue. When runTargetFallback is non-empty AND the
// bead carries neither run_target nor routed_to, the fallback route is stamped so
// the reopened work stays reachable by the controller demand query. It emits the
// exact beads.UpdateOpts the raw release ops in releaseWorkFromClosedSessionBead
// and unclaimWorkAssignedToRetiredSessionBead emitted (proven byte-identical by
// the recording-fake write tests). Every session-ending caller passes the
// closing session's own template route: a release with no fallback lands the
// work open, unassigned and unrouted whenever the bead carried no route of its
// own, which is invisible to both the pool demand probe and
// releaseOrphanedPoolAssignments.
//
// The release is GUARDED on the assignee the caller enumerated. Both callers
// (releaseWorkFromClosedSessionBead and
// unclaimWorkAssignedToRetiredSessionInfo, cmd/gc/session_beads.go) read the
// work with OpenAssignedTo and write it here, and nothing else pins the
// assignee across that window. Unconditional, this reopened beads whose
// holder had changed since the read -- and because claim_routes puts two
// provider pools on one route, the reopened bead went to the OTHER pool while
// the first was still working it (ci-23nak7; the same defect the closed-session
// path already guards for after ci-2hk, where the work simply ran twice with
// no error anywhere). Preferring the store's CAS and falling back to a live
// recheck mirrors releaseWorkBeadFromClosedSession and
// releaseOrphanedPoolAssignment rather than inventing a third discipline.
//
// A refusal returns nil, not an error: nothing failed, and the bead is
// correctly left with its live holder. The callers' unclaimResult therefore
// tallies a refusal as Released -- it counts release ATTEMPTS that did not
// fault, not beads reopened. Splitting that counter is deliberately left
// undone here; it would touch every caller of unclaimResult for a reporting
// nicety, and no path reads the tally to make a decision.
func (w workAssignment) ReleaseWorkBead(item beads.Bead, runTargetFallback, workBranch string) error {
	store := w.unwrapped()
	if store == nil {
		return nil
	}
	released, handled, err := releaseWorkBeadIfCurrent(store, item)
	if err != nil {
		return err
	}
	if handled {
		if !released {
			return nil
		}
		// The CAS swapped status/assignee only, so the affinity clears and the
		// run_target fallback ride a separate metadata-only write. Both sides
		// read workrelease.Metadata, so this stamps byte-identically to the
		// unconditional path below.
		return store.Update(item.ID, beads.UpdateOpts{
			Metadata: withReleasedWorkBranch(releaseWorkBeadMetadata(item, runTargetFallback), item, workBranch),
		})
	}
	// Everything the CAS cannot arbitrate keeps the original unconditional
	// write, byte-identical to the raw ops: an open-status bead (outside
	// ReleaseIfCurrent's contract), an assignee-less snapshot, and a store
	// with no usable conditional verb.
	//
	// NOT given a live-recheck fallback, unlike
	// releaseWorkBeadFromClosedSession. A recheck only shrinks the window
	// rather than closing it, it costs a store read on every release, and it
	// would change the bead op this path emits -- the byte-identity the
	// recording-fake write tests exist to pin. All six stores in this tree
	// declare ConditionalAssignmentReleaser (the compile-time assertions in
	// internal/beads: Bd, Caching, File, Mem, NativeDolt, SQLite), and
	// production is Bd behind Caching, so the recheck would cover only a
	// backend that does not exist yet. If one appears it releases unguarded:
	// give it the recheck then, and pin it with the same two-arm test.
	opts := workrelease.Options(item, runTargetFallback)
	opts.Metadata = withReleasedWorkBranch(opts.Metadata, item, workBranch)
	return store.Update(item.ID, opts)
}

// withReleasedWorkBranch adds the gc.work_branch stamp to a release's metadata
// patch when the caller resolved a branch that differs from the one on the
// bead, and returns metadata unchanged otherwise.
//
// It exists because gc.work_branch is resolved ONCE, at claim time
// (hookClaimIdentityPatch), and an agent runs `gc hook --claim` exactly once
// per session -- BEFORE it cuts its feature branch. So the durable handle read
// `main` while the work sat on feat/<bead>-<slug>, and a bead released off a
// dead session pointed the next claimant at the wrong branch. It then redid
// work that was sitting in the dead slot's worktree, and the salvage had to be
// done by hand (ci-q3qbo9; measured on gs-eh2 and as-2mhs, 2026-09-07).
//
// An EMPTY branch leaves the existing stamp alone rather than clearing it. A
// pruned worktree, a detached HEAD, or a non-repo work_dir all resolve to ""
// (hookResolveWorkBranch), and erasing the handle in those cases would destroy
// the only record of where the predecessor was working -- strictly worse than
// a stale name. Clearing is therefore NOT an option this function offers.
//
// The stamp rides the same write as the rest of the release patch on both
// paths. A second write would be a second thing to fail, and the whole point
// is that the pointer and the reopening are consistent.
//
// Documented absence: nothing here records whether the worktree was DIRTY.
// A branch name is enough to find the work, and the dirty-tree question
// belongs to whatever decides adoption -- a behavior change, not this fix.
func withReleasedWorkBranch(metadata map[string]string, item beads.Bead, workBranch string) map[string]string {
	branch := strings.TrimSpace(workBranch)
	if branch == "" || strings.TrimSpace(item.Metadata[beadmeta.WorkBranchMetadataKey]) == branch {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]string, 1)
	}
	metadata[beadmeta.WorkBranchMetadataKey] = branch
	return metadata
}

// releaseWorkBeadIfCurrent attempts the store's atomic conditional release for
// a retirement/unclaim release.
//
// handled=false means the store cannot conditionally release this snapshot
// shape -- no ConditionalAssignmentReleaser, ErrConditionalReleaseUnsupported,
// or a snapshot outside the verb's in_progress+assigned contract -- and the
// caller keeps the unconditional write. handled=true with released=false
// means the store answered authoritatively that the assignee moved since the
// caller's enumeration, and the release must NOT be retried unconditionally.
//
// The store argument must be the UNWRAPPED store. Asserting an optional
// capability on a beads.WorkStore wrapper always fails (the typed-nil trap in
// this file's header), and a silent failure here degrades every release back
// to unconditional -- the exact defect this function exists to prevent.
//
// An error is returned rather than swallowed into the recheck fallback: a
// store that could not answer leaves ownership UNRESOLVED, and downgrading
// that to an unconditional write is exactly how a transient fault becomes a
// second holder. This differs from releasePoolAssignmentIfCurrent, which logs
// and skips the tick because it has no error channel to its caller; here both
// callers already report and tally what comes back.
//
// The open-status carve-out is deliberate and load-bearing, not defensive.
// ReleaseIfCurrent's contract covers in_progress assignments only, so a bead
// parked open on a pool route name has no CAS available; narrowing it to one
// would strand every parked bead a retiring session leaves behind.
// cmd/gc/work_assignment_release_cas_test.go pins both arms.
func releaseWorkBeadIfCurrent(store beads.Store, item beads.Bead) (released, handled bool, err error) {
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

// releaseWorkBeadMetadata is the cmd/gc name for the shared release metadata
// patch. The conditional (ReleaseIfCurrent) release path swaps only
// status/assignee and must therefore write this separately, so it has to stamp
// byte-identically to the single-Update path -- which is why both read the one
// implementation in internal/workrelease rather than each spelling the fallback
// condition out.
func releaseWorkBeadMetadata(item beads.Bead, runTargetFallback string) map[string]string {
	return workrelease.Metadata(item, runTargetFallback)
}

// ReassignWorkBead re-homes one WORK bead onto a new session identity, emitting
// the exact Update{Assignee:&new} the raw reassign op in
// reassignWorkAssignedToRetiredSessionBead emitted. It deliberately touches
// neither Status nor Metadata.
func (w workAssignment) ReassignWorkBead(beadID, newSessionID string) error {
	store := w.unwrapped()
	if store == nil {
		return nil
	}
	return store.Update(beadID, beads.UpdateOpts{Assignee: &newSessionID})
}

// ClearDetachedProbe clears the detached-probe metadata contract on a WORK bead,
// emitting SetMetadata(id, gc.detached, "") — the empty-string clear semantics
// the raw clearDetachedProbeMetadata op used. Best-effort: a nil store or empty
// id is a no-op, and a write error is returned for the caller to log.
func (w workAssignment) ClearDetachedProbe(beadID string) error {
	store := w.unwrapped()
	if store == nil || beadID == "" {
		return nil
	}
	return store.SetMetadata(beadID, beadmeta.DetachedMetadataKey, "")
}
