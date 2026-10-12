package main

// cmd/gc/hook_claim_sessions.go
//
// gc.claim_sessions: the claim-time record of every session that ever claimed
// a bead, so authorship survives a reclaim.
//
// Why a separate accumulating key. gc.session_id is the dashboard's
// back-reference to the CURRENT holder and is overwritten by every reclaim,
// correctly for that purpose. Read as authorship it under-reports: a bead a
// Claude seat started and a Codex seat finished reads as Codex-only, and a
// review that picks its reader lanes from that reads one lane over mixed work
// (city docs/reviews/rollout/design.md section 7). The rejected alternative
// is stamping at close in work_record_gate.go. Its only caller is the
// `gc bd close` dispatch, so bare `bd`, an API force close and batch closes
// never reach it, while every pool seat passes through this claim.
//
// Shape of the value. A JSON array of "<session bead>:<provider family>:
// <session key>" strings, JSON because gc.attempt_log set that convention for
// list-valued metadata and because no separator inside a plain string is
// guaranteed absent from a future provider's session key. The family is the
// one gc RESOLVED when it created the session (provider_kind), never a guess
// from the provider's name. The session key is what lets a reader find the
// transcript, which carries the model.
//
// Invariants, pinned by cmd_hook_claim_sessions_test.go:
//   - A reclaim extends the list and never drops an earlier claimant.
//   - A tick that learns nothing new issues no write (the adoption path runs
//     every hook tick, and a write per tick is the bead.updated flood class).
//   - An entry only ever becomes MORE specific: a keyless or familyless entry
//     is completed in place by a later tick, never duplicated beside it.
//   - A value that does not decode is left exactly as found.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// hookClaimSessionEntry is one claimant. Family and Key are empty when the
// claim could not learn them, which a reader must treat as unknown.
type hookClaimSessionEntry struct {
	ID     string
	Family string
	Key    string
}

func (e hookClaimSessionEntry) String() string {
	return e.ID + ":" + e.Family + ":" + e.Key
}

// parseHookClaimSessionEntry splits on the first two colons only, so a
// session key that itself contains a colon survives intact. Session bead ids
// and built-in provider names contain none.
func parseHookClaimSessionEntry(raw string) hookClaimSessionEntry {
	parts := strings.SplitN(raw, ":", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return hookClaimSessionEntry{ID: parts[0], Family: parts[1], Key: parts[2]}
}

// covers reports whether e already says everything o says about the same
// session: same id, and each of o's known fields equal to e's.
func (e hookClaimSessionEntry) covers(o hookClaimSessionEntry) bool {
	return e.ID == o.ID &&
		(o.Family == "" || e.Family == o.Family) &&
		(o.Key == "" || e.Key == o.Key)
}

// hookClaimSessionsValue returns the gc.claim_sessions value this claim should
// write, and whether it differs from the bead's current one. It is skipped
// under the same conditions as gc.session_id: a sessionless claim names no
// claimant, and control beads stay session-free by graphroute's design.
//
// sessionBead is the claimant's own bead as hookClaimSessionBead resolved it.
// The zero bead still yields an entry, carrying only the id from
// GC_SESSION_ID, because "claimed by a session whose provider is unknown"
// selects both review lanes downstream while no entry at all would erase the
// claimant.
func hookClaimSessionsValue(bead beads.Bead, opts hookClaimOptions, sessionBead beads.Bead, stderr io.Writer) (string, bool) {
	sessionID := hookClaimSessionID(opts.Env)
	if sessionID == "" || beadmeta.IsControlKind(strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey])) {
		return "", false
	}
	// No id comparison here: hookClaimSessionBead already returns the zero
	// bead for a read answered by a different session, and the zero bead's
	// nil metadata yields an empty family and key.
	entry := hookClaimSessionEntry{
		ID:     sessionID,
		Family: hookClaimProviderFamily(sessionBead.Metadata),
		Key:    strings.TrimSpace(sessionBead.Metadata["session_key"]),
	}
	existing := bead.Metadata[beadmeta.ClaimSessionsMetadataKey]
	next, changed, err := appendHookClaimSession(existing, entry)
	if err != nil {
		// Rejected alternative: reset the list, as appendAttemptLogValue does
		// for a corrupt attempt log. That drops every earlier claimant and
		// turns mixed work into single-provider work, the exact under-report
		// this key exists to remove. Left alone, it reads as unknown.
		fmt.Fprintf(stderr, "gc hook --claim: %s on %s is not a JSON string array, so claimant %s was NOT recorded and the value was left as found: %v -- repair it with `gc bd update %s --set-metadata '%s=[...]'`\n", //nolint:errcheck
			beadmeta.ClaimSessionsMetadataKey, bead.ID, entry, err, bead.ID, beadmeta.ClaimSessionsMetadataKey)
		return "", false
	}
	return next, changed
}

// appendHookClaimSession merges entry into the encoded list. An entry that an
// existing one already covers changes nothing. Otherwise entry takes the place
// of the first existing entry it covers (a keyless claim completed by a later
// tick keeps its position in claim order), drops any further ones, or is
// appended when it covers none.
func appendHookClaimSession(existing string, entry hookClaimSessionEntry) (string, bool, error) {
	var list []string
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &list); err != nil {
			return "", false, err
		}
	}
	for _, raw := range list {
		if parseHookClaimSessionEntry(raw).covers(entry) {
			return existing, false, nil
		}
	}
	out := make([]string, 0, len(list)+1)
	placed := false
	for _, raw := range list {
		if entry.covers(parseHookClaimSessionEntry(raw)) {
			if !placed {
				out = append(out, entry.String())
				placed = true
			}
			continue
		}
		out = append(out, raw)
	}
	if !placed {
		out = append(out, entry.String())
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

// hookClaimProviderFamily returns the built-in provider family gc resolved for
// a session when it created it, or "" when gc's record does not establish one.
//
// provider_kind is stamped exactly when the resolved family differs from the
// provider name (resolvedProviderFamilyMetadata), so its absence means either
// "the name is the family" or "there is no family", and only a built-in name
// settles which. The rejected alternative is the cross-check sweep's prefix
// match on the name, which calls a custom provider "claude-ish" Claude with
// nothing in gc's record to say so.
//
// Documented absence: a city provider that shadows a built-in name AND
// declares an empty `base` resolves to no family, yet carries no
// provider_kind, so it reads here as the built-in. Telling it apart needs the
// city config, which the claim path does not load.
func hookClaimProviderFamily(meta map[string]string) string {
	if kind := strings.TrimSpace(meta["provider_kind"]); kind != "" {
		return kind
	}
	name := strings.TrimSpace(meta["provider"])
	if _, ok := config.BuiltinProviders()[name]; ok && name != "" {
		return name
	}
	return ""
}
