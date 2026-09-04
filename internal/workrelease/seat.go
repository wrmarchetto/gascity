// Package workrelease owns one question, asked whenever a session ends: which
// of the work beads addressed to that session may be taken back, and what
// "taken back" means for each.
//
// It sits above internal/session and internal/mail and below every caller, for
// one reason: the sweep must exclude mail beads, and internal/session may not
// import internal/mail (layering invariant 1, no upward dependencies). Putting
// the rule here rather than in either of them is what lets `gc session close`,
// the two HTTP session-close handlers, and the reconciler run ONE implementation
// instead of three that drift -- which they did: the API handlers ran no sweep
// at all, so a session closed from the dashboard left its claim in_progress
// under a session that no longer existed, invisible to every work_query tier
// (ci-dkgt9s).
//
// A session ends holding two different kinds of relationship to a work bead,
// and a sweep that erases both is a defect. A CLAIM is the session's own: the
// assignee on an in_progress bead is the claim's artifact, written by whoever
// took the bead, so clearing it is what returns the work to the pool it was
// addressed to. An ADDRESS on an OPEN bead is not: nobody claimed it, the
// assignee is the routing decision of whoever filed or slung it, and on a pool
// seat it names the SEAT rather than the session. Erasing those stripped every
// bead addressed to a closing session's slot alias and left them unrouted,
// reaching no pool door (ci-8vx85v).
//
// Invariant, verified by internal/workrelease/release_test.go and by
// cmd/gc/cmd_session_close_addressed_work_test.go at the CLI boundary: after a
// session ends, no work bead is left in_progress under it, and no OPEN bead
// addressed to a still-bearable identity has lost its assignee.
package workrelease

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// SeatRetirement says whether the SEAT the ending session occupied is going away
// with it. Almost every caller is SeatSurvives: a closed session, a reaped pool
// worker and a stranded-worker repair all leave the slot in config for the next
// occupant. SeatRetired is the config-removal case -- a [[named_session]]
// deleted from config takes its identity with it, so an address on that identity
// is borne by nobody ever again and must be released like a claim.
type SeatRetirement bool

const (
	// SeatSurvives means the slot stays in config for the next occupant, so
	// its identities are still bearable.
	SeatSurvives SeatRetirement = false
	// SeatRetired means the seat goes with the session, so every identity it
	// held is unbearable and an address on one must release like a claim.
	SeatRetired SeatRetirement = true
)

// Target is one (identity, status) enumeration a session-ending sweep runs.
// Every bead an enumeration finds is released the same way -- the target list is
// where the narrowing lives, not the release verb.
type Target struct {
	Assignee string
	Status   string
}

// Seat is the set of identities the SEAT owns -- the ones the next session to
// occupy it bears again. Read from a session bead's metadata or from the typed
// session.Info projection; the two constructors below are the only readers of
// that vocabulary in this package.
type Seat struct {
	Alias         string
	NamedIdentity string
	AliasHistory  []string
}

// SeatFromBead reads the seat vocabulary off a raw session bead.
func SeatFromBead(sessionBead beads.Bead) Seat {
	return Seat{
		Alias:         sessionBead.Metadata["alias"],
		NamedIdentity: sessionBead.Metadata[session.NamedSessionIdentityMetadata],
		AliasHistory:  session.AliasHistory(sessionBead.Metadata),
	}
}

// SeatFromInfo is the session.Info mirror of SeatFromBead, reading the same
// three fields through their typed accessors.
func SeatFromInfo(i session.Info) Seat {
	return Seat{
		Alias:         i.Alias,
		NamedIdentity: i.ConfiguredNamedIdentity,
		AliasHistory:  i.AliasHistory,
	}
}

// SeatIdentityScope partitions identities into the ones that die with this
// session and the ones its seat keeps. Returns (ephemeral, durable).
//
// The partition is a FILTER over the identity list the caller supplies, never a
// second enumeration of the session bead's metadata. An identity added to that
// list therefore cannot go unswept: it lands in ephemeral, which is what every
// identity got before this split existed. Re-deriving the list here would let a
// new identity be swept by neither half, silently.
//
// Callers differ in what they consider an identity -- the reconciler's list
// includes configured_named_identity, the session package's ClaimIdentities does
// not -- so the list is a parameter and only the seat-owned set is read from the
// session.
//
// An identity that is both -- an alias that happens to equal the runtime
// session_name -- is reported DURABLE. Preferring ephemeral would restore the
// silent strip this split exists to remove, while the durable reading's worst
// case is an address nobody bears, which the city:unclaimable-assignee doctor
// check already reports out loud. Silent loss loses to loud staleness.
func SeatIdentityScope(seat Seat, identities []string) (ephemeral, durable []string) {
	owned := make(map[string]struct{}, 4)
	add := func(value string) {
		if value = strings.TrimSpace(value); value != "" {
			owned[value] = struct{}{}
		}
	}
	add(seat.NamedIdentity)
	add(seat.Alias)
	for _, prior := range seat.AliasHistory {
		add(prior)
	}

	seen := make(map[string]struct{}, len(identities))
	for _, id := range identities {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if _, isSeat := owned[id]; isSeat {
			durable = append(durable, id)
			continue
		}
		ephemeral = append(ephemeral, id)
	}
	return ephemeral, durable
}

// Targets returns the enumeration a session-ending sweep must run over one
// ending session bead.
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
func Targets(seat Seat, identities []string, retired SeatRetirement) []Target {
	ephemeral, durable := SeatIdentityScope(seat, identities)
	if retired {
		ephemeral = append(ephemeral, durable...)
		durable = nil
	}
	targets := make([]Target, 0, len(ephemeral)*2+len(durable))
	for _, status := range []string{"open", "in_progress"} {
		for _, assignee := range ephemeral {
			targets = append(targets, Target{Assignee: assignee, Status: status})
		}
	}
	for _, assignee := range durable {
		targets = append(targets, Target{Assignee: assignee, Status: "in_progress"})
	}
	return targets
}

// IdentitiesFromBead is the assignee vocabulary for callers that hold a raw
// session bead and no typed projection -- the HTTP close handlers.
//
// It is session.ClaimIdentities COMPOSED WITH the configured named identity,
// not a re-listing of the metadata: ClaimIdentities stays the one place the
// id/alias/session_name/alias_history vocabulary is spelled out, so an identity
// added there arrives here too. The named identity is appended because
// ClaimIdentities deliberately answers "what can a LIVE session claim under",
// and a configured named session does hold work under its configured identity.
//
// Order differs from the reconciler's list and does not matter: Targets dedupes,
// and each target is enumerated against the store independently.
func IdentitiesFromBead(sessionBead beads.Bead) []string {
	identities := session.ClaimIdentities(sessionBead)
	if named := strings.TrimSpace(sessionBead.Metadata[session.NamedSessionIdentityMetadata]); named != "" {
		identities = append(identities, named)
	}
	return identities
}

// FallbackRoute returns the route an ending session offers to work that carries
// none of its own: its configured template, or its agent name. Stamped as
// gc.run_target by a release so the reopened work stays reachable by the
// controller demand query instead of landing open, unassigned and unrouted.
func FallbackRoute(sessionBead beads.Bead) string {
	if route := strings.TrimSpace(sessionBead.Metadata["template"]); route != "" {
		return route
	}
	return strings.TrimSpace(sessionBead.Metadata["agent_name"])
}
