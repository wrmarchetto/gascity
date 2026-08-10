package session

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the session-class READ half of the CLAIM-identity codec: which
// assignee values a live session can hold work under. It lives beside the
// mailbox codec (mailbox_address.go) for the same reason that one does --
// keeping the session-bead metadata vocabulary (alias / alias_history /
// session_name) out of the packages that ask the question.

// ClaimIdentities returns every assignee value a live session of this bead can
// hold work under: its bead id, its current alias, its runtime session name,
// and any retained alias history. Order is id, alias, session_name, then
// history; empty and duplicate values are dropped.
//
// The first three are exactly the env vars the work query's assigned tiers
// probe -- $GC_SESSION_ID is the bead id, $GC_ALIAS the alias metadata,
// $GC_SESSION_NAME the session_name metadata (see
// standardAssignedWorkQueryScript in internal/config/workquery.go). Alias
// history is included because it is retained precisely so an identity a
// session used to answer to keeps resolving to it.
//
// This is deliberately NOT MailboxAddressesIncludingRuntimeName, whose output
// set is identical today. That function answers "which addresses does this
// session RECEIVE mail at", a question that has already forked once for API
// read semantics; reusing it would silently couple the claim ladder to the
// next mail-delivery decision. The fields coincide because both are the
// session's names, not because the two questions are the same.
//
// Documented absence: nothing here reports whether the session is LIVE. The
// caller decides which beads to feed in -- a closed session's identities are
// not claimable by anyone, and treating them as claimable would hide work its
// on_death hook failed to release.
func ClaimIdentities(b beads.Bead) []string {
	seen := make(map[string]struct{}, 4)
	identities := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		identities = append(identities, value)
	}
	add(b.ID)
	add(b.Metadata["alias"])
	add(b.Metadata["session_name"])
	for _, alias := range AliasHistory(b.Metadata) {
		add(alias)
	}
	return identities
}
