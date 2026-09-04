package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// Which of a closing session's assignee identities a session-ending release
// sweep may take back, and in which status.
//
// A session ends holding two different kinds of relationship to a work bead,
// and the sweep used to erase both. A CLAIM is the session's own: the assignee
// on an in_progress bead is the claim's own artifact, written by whoever took
// the bead, so clearing it is what returns the work to the pool it was
// addressed to. An ADDRESS on an OPEN bead is not: nobody claimed it, the
// assignee is the routing decision of whoever filed or slung it, and on a pool
// seat it names the SEAT rather than the session. Closing one session stripped
// every open bead addressed to its slot alias, which left them unassigned,
// unrouted and reaching no pool door (ci-8vx85v -- five beads on one close, nine
// on a supervisor restart, each needing a manual re-assign sweep).
//
// So the sweep is narrowed on the open half only, and narrowed by whether any
// FUTURE session can bear the identity. A pool instance alias and a configured
// named identity are re-borne by the seat's next occupant, so an open bead
// addressed to one is left alone. A session bead ID and a runtime session_name
// belong to one session and are never issued again, so an open bead addressed to
// one is released or it strands.
//
// Deliberately NOT narrowed: the in_progress half. Preserving a claim's assignee
// was considered and rejected -- it pins work that was addressed to a POOL onto
// the one slot that happened to claim it, because the hook admits routed work
// only while its assignee is empty (hookCandidateClaimable). Releasing the claim
// in full is what puts it back in front of every slot.
//
// Invariant, verified by cmd_session_close_addressed_work_test.go: after a
// session ends, no work bead is left in_progress under it, and no OPEN bead
// addressed to a still-bearable identity has lost its assignee.

// seatRetirement says whether the SEAT the ending session occupied is going away
// with it. Almost every caller is seatSurvives: a closed session, a reaped pool
// worker and a stranded-worker repair all leave the slot in config for the next
// occupant. seatRetired is the config-removal case -- a [[named_session]] deleted
// from config takes its identity with it, so an address on that identity is
// borne by nobody ever again and must be released like a claim.
type seatRetirement bool

const (
	seatSurvives seatRetirement = false
	seatRetired  seatRetirement = true
)

// sessionSeatIdentityScope partitions the identities a work bead can carry for
// this session into the ones that die with it and the ones its seat keeps.
// Returns (ephemeral, durable).
//
// The partition is a FILTER over sessionBeadAssigneeIdentities, never a second
// enumeration of the metadata. An identity added to that function therefore
// cannot go unswept here: it lands in ephemeral, which is what every identity
// got before this split existed. Re-reading the metadata instead would let a new
// identity be swept by neither half, silently.
func sessionSeatIdentityScope(sb beads.Bead) (ephemeral, durable []string) {
	return splitSeatIdentities(
		compactSessionAssignmentIdentifiers(sessionBeadAssigneeIdentities(sb)),
		[]string{
			sb.Metadata["configured_named_identity"],
			sb.Metadata["alias"],
		},
		session.AliasHistory(sb.Metadata),
	)
}

// sessionSeatIdentityScopeInfo is the session.Info mirror of
// sessionSeatIdentityScope, filtering the Info identity list by the same seat
// fields read through their typed accessors.
func sessionSeatIdentityScopeInfo(i session.Info) (ephemeral, durable []string) {
	return splitSeatIdentities(
		compactSessionAssignmentIdentifiers(sessionBeadAssigneeIdentitiesInfo(i)),
		[]string{
			i.ConfiguredNamedIdentity,
			i.Alias,
		},
		i.AliasHistory,
	)
}

// splitSeatIdentities splits identities into (ephemeral, durable) by membership
// in the seat-owned sets, preserving the input order in both halves so the
// enumeration a sweep runs stays stable. Callers pass the COMPACTED identity
// list, so a duplicate identity does not cost the sweep a second store query.
//
// An identity that is both -- an alias that happens to equal the runtime
// session_name -- is reported DURABLE, because it appears in a seat set at all.
// Preferring ephemeral there would restore the silent strip this split exists to
// remove, while the durable reading's worst case is an address nobody bears,
// which the city:unclaimable-assignee doctor check already reports out loud.
// Silent loss loses to loud staleness.
func splitSeatIdentities(identities []string, seatOwned ...[]string) (ephemeral, durable []string) {
	seat := make(map[string]struct{}, 4)
	for _, set := range seatOwned {
		for _, id := range set {
			if id = strings.TrimSpace(id); id != "" {
				seat[id] = struct{}{}
			}
		}
	}
	for _, id := range identities {
		if _, ok := seat[strings.TrimSpace(id)]; ok {
			durable = append(durable, id)
			continue
		}
		ephemeral = append(ephemeral, id)
	}
	return ephemeral, durable
}

// sessionReleaseTarget is one (identity, status) enumeration a session-ending
// sweep runs. Every bead an enumeration finds is released the same way -- the
// target list is where the narrowing lives, not the release verb.
type sessionReleaseTarget struct {
	Assignee string
	Status   string
}

// sessionReleaseTargets returns the enumeration a session-ending sweep must run
// over one closing session bead.
//
// Ephemeral identities are swept in both statuses, because an open bead
// addressed to a dead session's own name is as stranded as a claim it held.
// Durable identities are swept in in_progress ONLY: an open bead under a seat
// identity was never claimed, so the sweep has nothing to release and no
// standing to rewrite the address. When the seat retires, every identity is
// ephemeral.
//
// Status order is (open, in_progress), matching the enumeration order the
// release loops used before the split.
func sessionReleaseTargets(ephemeral, durable []string, retired seatRetirement) []sessionReleaseTarget {
	if retired {
		ephemeral = append(append([]string{}, ephemeral...), durable...)
		durable = nil
	}
	targets := make([]sessionReleaseTarget, 0, len(ephemeral)*2+len(durable))
	for _, status := range []string{"open", "in_progress"} {
		for _, assignee := range ephemeral {
			targets = append(targets, sessionReleaseTarget{Assignee: assignee, Status: status})
		}
	}
	for _, assignee := range durable {
		targets = append(targets, sessionReleaseTarget{Assignee: assignee, Status: "in_progress"})
	}
	return targets
}

// sessionReleaseTargetsForBead and sessionReleaseTargetsForInfo are the two
// call-site forms; they exist so no release loop re-derives the partition and
// risks disagreeing with its twin.
func sessionReleaseTargetsForBead(sb beads.Bead, retired seatRetirement) []sessionReleaseTarget {
	ephemeral, durable := sessionSeatIdentityScope(sb)
	return sessionReleaseTargets(ephemeral, durable, retired)
}

func sessionReleaseTargetsForInfo(i session.Info, retired seatRetirement) []sessionReleaseTarget {
	ephemeral, durable := sessionSeatIdentityScopeInfo(i)
	return sessionReleaseTargets(ephemeral, durable, retired)
}
