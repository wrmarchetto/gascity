package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/workrelease"
)

// The seat-release rule -- which of an ending session's assignee identities a
// sweep may take back, and in which status -- lives in internal/workrelease, so
// `gc session close`, the reconciler, and the two HTTP close handlers run one
// implementation. This file is the cmd/gc adapter: it supplies the reconciler's
// own identity vocabulary (sessionBeadAssigneeIdentities, which includes
// configured_named_identity) and nothing else.
//
// Read internal/workrelease's package doc for why the rule exists and what it
// protects; the reasoning is not duplicated here.

// Local aliases so call sites read the same as before the rule moved out.
type seatRetirement = workrelease.SeatRetirement

const (
	seatSurvives = workrelease.SeatSurvives
	seatRetired  = workrelease.SeatRetired
)

type sessionReleaseTarget = workrelease.Target

// sessionSeatIdentityScope partitions this session bead's assignee identities
// into (ephemeral, durable). The identity list is the reconciler's, not
// session.ClaimIdentities: it additionally carries configured_named_identity,
// which a named session genuinely holds work under.
func sessionSeatIdentityScope(sb beads.Bead) (ephemeral, durable []string) {
	return workrelease.SeatIdentityScope(workrelease.SeatFromBead(sb), sessionBeadAssigneeIdentities(sb))
}

func sessionReleaseTargetsForBead(sb beads.Bead, retired seatRetirement) []sessionReleaseTarget {
	return workrelease.Targets(workrelease.SeatFromBead(sb), sessionBeadAssigneeIdentities(sb), retired)
}

func sessionReleaseTargetsForInfo(i session.Info, retired seatRetirement) []sessionReleaseTarget {
	return workrelease.Targets(workrelease.SeatFromInfo(i), sessionBeadAssigneeIdentitiesInfo(i), retired)
}
