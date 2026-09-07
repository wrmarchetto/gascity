package api

import (
	"log"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/workrelease"
)

// releaseWorkFromClosedSession returns work the just-closed session still held
// to the queue, so a session closed from the dashboard behaves like one closed
// with `gc session close`.
//
// Both HTTP close handlers previously stopped at worker.Handle.CloseDetailed,
// which closes the session bead and nothing else. A claim was left in_progress
// under a session that no longer existed -- invisible to every work_query tier,
// since Tier 1 needs an assignee match and Tiers 2/3 only match "ready". Routed
// work recovered on the next reconcile tick via releaseOrphanedPoolAssignments,
// but UNROUTED work did not: that sweep skips a bead with no route, so the claim
// sat in_progress indefinitely, reported by `gc doctor` as closed-bead-owner and
// repaired by nothing (ci-dkgt9s).
//
// sessionBead must be the state captured BEFORE the close: the sweep reads the
// session's assignee identities off it, and the close retires some of them.
//
// Best-effort by contract, and deliberately not surfaced to the HTTP caller: the
// session IS closed by the time this runs and no error here can undo that, so
// failing the request would report a close that actually happened as a failure.
// The reconciler's orphan sweep remains the idempotent backstop for routed work.
//
// Scope this does NOT cover, deliberately: the cross-store fan-out. The CLI
// close sweeps attached rig stores as well, using rig stores it builds from
// config; the API server holds one session store here. Work in a rig store
// assigned to a city session closed over HTTP still waits for the reconciler.
// Narrowing that needs the API's own rig-store access, which is a larger change
// than the defect warrants.
func releaseWorkFromClosedSession(store beads.Store, sessionBead beads.Bead) {
	if store == nil {
		return
	}
	released, failed := workrelease.FromEndedSession(
		store,
		sessionBead,
		workrelease.IdentitiesFromBead(sessionBead),
		workrelease.FallbackRoute(sessionBead),
		workrelease.SeatSurvives,
		log.Writer(),
	)
	if released > 0 || failed > 0 {
		log.Printf("gc api: closing session %s released %d work bead(s), %d failed", sessionBead.ID, released, failed)
	}
	// The mail half of the same sweep. Added here for the reason the work half
	// was: both HTTP handlers stopped at worker.Handle.CloseDetailed, so a
	// session closed from the dashboard left everything addressed to it behind
	// -- and mail left behind is worse than a stranded claim, because no
	// reconciler pass repairs it and the sender is never told (ci-cw9wsk).
	//
	// The cross-store gap named above applies unchanged: mail in a rig store
	// addressed to a city session closed over HTTP is still not swept.
	moved, mailFailed := workrelease.RerouteMailFromEndedSession(
		store,
		sessionBead,
		workrelease.IdentitiesFromBead(sessionBead),
		workrelease.SeatSurvives,
		log.Writer(),
	)
	if moved > 0 || mailFailed > 0 {
		log.Printf("gc api: closing session %s rerouted %d message(s), %d failed", sessionBead.ID, moved, mailFailed)
	}
}
