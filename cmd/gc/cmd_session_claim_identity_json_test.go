package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: whether `gc session list --json` exposes enough to resolve a work
// bead's assignee back to the session that claimed it. It says nothing about
// which rung a session's own claim path picks -- that choice lives in
// cmd_hook.go and is a separate defect (city bead ci-yfuh3a).
//
// The suite exists because the producer and the consumer of an assignee string
// are tested at opposite ends and agree with each other about nothing. gc
// writes a claim under any of the five rungs session.AssigneeIdentities
// enumerates; a consumer joining `gc session list --json` on the agent name
// alone silently resolves none of them, and an unresolvable holder reads as an
// unheld claim rather than as a lookup that failed. That is what happened to
// eight governor wakes in the city on 2026-09-06: five recorded assignee
// "governor" and three "governor-ci-0ft7eq" on one unchanged route, and the
// consumer joining on the first spelling saw three holders vanish (ci-reqb5f
// fixed that one consumer; this closes the hole for every other one).
//
// Run: go test ./cmd/gc/ -run TestSessionListJSONClaimIdentities

// sessionListJSONClaimIdentityFixture is one session bead carrying a DISTINCT
// value in every identity metadata key, so no assertion below can pass because
// two rungs happen to hold the same string.
var sessionListJSONClaimIdentityFixture = map[string]string{
	"session_name":              "worker-ci-0ft7eq",
	"alias":                     "worker-2",
	"configured_named_identity": "overseer",
	"alias_history":             "worker-1,worker-0",
	"template":                  "worker",
	"state":                     "asleep",
}

// runSessionListJSONForClaimIdentities seeds the fixture bead and returns its
// id alongside the decoded row, so an assertion can name the bead-id rung
// without hardcoding a generated id.
func runSessionListJSONForClaimIdentities(t *testing.T) (string, *sessionListJSONRow, string) {
	t.Helper()
	clearGCEnv(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := t.TempDir()
	t.Setenv("GC_CITY", cityDir)
	writeNamedSessionCityTOML(t, cityDir)

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(%q): %v", cityDir, err)
	}
	created, err := store.Create(beads.Bead{
		Title:    "worker-ci-0ft7eq",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: sessionListJSONClaimIdentityFixture,
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList("", "", true, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--json) = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON session list object: %v; stdout=%q", err, stdout.String())
	}
	row := sessionListJSONRowBySessionName(got.Sessions, "worker-ci-0ft7eq")
	if row == nil {
		t.Fatalf("missing worker-ci-0ft7eq row in JSON output:\n%s", stdout.String())
	}
	return created.ID, row, stdout.String()
}

// TestSessionListJSONClaimIdentitiesCoversEveryRung is the coverage gate, and
// it derives what must be present from session.AssigneeIdentities -- the same
// function the claim, reconciler orphan-release and assignee-filter paths read
// -- rather than from a list written out here. A hand-kept list rots at the
// next rung added to the ladder, and the rot is invisible: the new rung simply
// resolves to nobody, which is the defect this gate exists to prevent.
//
// The two sides are not the same source. The required set comes from the
// ladder; the observed set comes from JSON bytes that went through
// cmdSessionList, the store and the row projection.
func TestSessionListJSONClaimIdentitiesCoversEveryRung(t *testing.T) {
	beadID, row, raw := runSessionListJSONForClaimIdentities(t)

	info := session.Info{
		ID:                      beadID,
		SessionNameMetadata:     sessionListJSONClaimIdentityFixture["session_name"],
		ConfiguredNamedIdentity: sessionListJSONClaimIdentityFixture["configured_named_identity"],
		Alias:                   sessionListJSONClaimIdentityFixture["alias"],
		AliasHistory:            session.AliasHistory(sessionListJSONClaimIdentityFixture),
	}
	want := sessionBeadAssigneeIdentitiesInfo(info)
	if len(want) < 5 {
		t.Fatalf("fixture exercises only %d rung(s) (%v); it has to populate every one or the gate is vacuous", len(want), want)
	}
	for _, identity := range want {
		if !slices.Contains(row.ClaimIdentities, identity) {
			t.Errorf("claim_identities %v omits rung %q; a claim assigned under it resolves to no session\nraw: %s",
				row.ClaimIdentities, identity, raw)
		}
	}
}

// TestSessionListJSONClaimIdentitiesSurfacesTheTwoMissingColumns pins the two
// rungs ci-yfuh3a named as unreachable, by literal rather than through the
// ladder: a broken ladder would satisfy the coverage gate above with both sides
// wrong in the same way, and these two are exactly the values no column carried.
//
// Documented absence: no raw session_name column is added. The existing
// session_name is Info.SessionName, which falls back to sessionNameFor(ID) --
// admitting that derived value into an assignee lookup would match work the
// session was never assigned, which is why AssigneeIdentities reads
// SessionNameMetadata instead. claim_identities carries the raw value, so the
// published column keeps its established meaning (the tmux session name).
func TestSessionListJSONClaimIdentitiesSurfacesTheTwoMissingColumns(t *testing.T) {
	_, row, raw := runSessionListJSONForClaimIdentities(t)

	if got, want := row.ConfiguredNamedIdentity, "overseer"; got != want {
		t.Errorf("configured_named_identity = %q, want %q\nraw: %s", got, want, raw)
	}
	if got, want := strings.Join(row.AliasHistory, ","), "worker-1,worker-0"; got != want {
		t.Errorf("alias_history = %q, want %q\nraw: %s", got, want, raw)
	}
	if !strings.Contains(raw, `"configured_named_identity"`) || !strings.Contains(raw, `"alias_history"`) {
		t.Errorf("JSON uses Go field names for the new columns:\n%s", raw)
	}
}

// TestSessionListJSONClaimIdentitiesOmitsEmptyRungs pins that a session with no
// alias, no configured identity and no history adds no keys. Emitting empty
// strings and empty arrays would make "never had an alias" and "alias is the
// empty string" the same wire value, and the whole point of the ladder is that
// an empty rung is skipped rather than matched.
func TestSessionListJSONClaimIdentitiesOmitsEmptyRungs(t *testing.T) {
	clearGCEnv(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := t.TempDir()
	t.Setenv("GC_CITY", cityDir)
	writeNamedSessionCityTOML(t, cityDir)

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(%q): %v", cityDir, err)
	}
	if _, err := store.Create(beads.Bead{
		Title:  "bare-session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "bare-session",
			"template":     "worker",
			"state":        "asleep",
		},
	}); err != nil {
		t.Fatalf("store.Create(bare session bead): %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList("", "", true, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--json) = %d, want 0; stderr=%s", code, stderr.String())
	}
	raw := stdout.String()
	for _, key := range []string{`"configured_named_identity"`, `"alias_history"`} {
		if strings.Contains(raw, key) {
			t.Errorf("bare session emitted %s with nothing to report:\n%s", key, raw)
		}
	}
	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON session list object: %v", err)
	}
	row := sessionListJSONRowBySessionName(got.Sessions, "bare-session")
	if row == nil {
		t.Fatalf("missing bare-session row:\n%s", raw)
	}
	// The bead id is always a rung, so claim_identities is never empty even
	// here -- an empty list would mean the session cannot be resolved at all.
	if len(row.ClaimIdentities) == 0 {
		t.Errorf("claim_identities is empty; the session bead id is always claimable\nraw: %s", raw)
	}
}
