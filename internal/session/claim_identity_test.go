package session

import (
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: the ClaimIdentities codec only -- which values of a session bead a
// worker can hold work under. It delegates the mailbox question (which
// addresses a session RECEIVES mail at) to mailbox_address_test.go; the two
// read the same three metadata fields today and must stay independently
// pinned, because the mailbox pair has already forked once for API read
// semantics (see MailboxAddressesIncludingRuntimeName).
//
// Run: go test ./internal/session/ -run TestClaimIdentities

// TestClaimIdentitiesCoversEveryEnvVarTheAssignedTiersProbe pins that all three
// identity env vars the work query's assigned tiers read -- GC_SESSION_ID,
// GC_ALIAS, GC_SESSION_NAME -- come back for one bead, plus retained history.
//
// The expectation is written out literally rather than derived from the input
// map, because deriving it from the same metadata the function reads would pass
// even if the function returned only the key it happened to look at first.
func TestClaimIdentitiesCoversEveryEnvVarTheAssignedTiersProbe(t *testing.T) {
	b := beads.Bead{
		ID:   "ci-abc1",
		Type: BeadType,
		Metadata: beads.StringMap{
			"alias":         "toolsmith-3",
			"session_name":  "city--toolsmith-3",
			"alias_history": "toolsmith-1,toolsmith-2",
		},
	}
	got := ClaimIdentities(b)
	want := []string{"ci-abc1", "toolsmith-3", "city--toolsmith-3", "toolsmith-1", "toolsmith-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimIdentities() = %v, want %v", got, want)
	}
}

// TestClaimIdentitiesDropsEmptyAndDuplicateValues pins the hygiene a consumer
// building a membership set relies on: no empty string (which would make every
// unassigned bead look claimable) and no repeat when alias equals session_name.
func TestClaimIdentitiesDropsEmptyAndDuplicateValues(t *testing.T) {
	b := beads.Bead{
		ID:   "ci-abc2",
		Type: BeadType,
		Metadata: beads.StringMap{
			"alias":         "mayor",
			"session_name":  "mayor",
			"alias_history": "",
		},
	}
	got := ClaimIdentities(b)
	want := []string{"ci-abc2", "mayor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimIdentities() = %v, want %v", got, want)
	}
}

// TestClaimIdentitiesReturnsSessionNameWithoutAnAlias pins the unaliased worker
// case. `gc hook --claim` falls back through GC_SESSION_NAME to GC_SESSION_ID
// when GC_ALIAS is unset, so both must survive an empty alias -- dropping
// session_name here would make every bead a bare worker claimed look stranded.
func TestClaimIdentitiesReturnsSessionNameWithoutAnAlias(t *testing.T) {
	b := beads.Bead{
		ID:       "ci-abc3",
		Type:     BeadType,
		Metadata: beads.StringMap{"session_name": "city--adhoc-9f1"},
	}
	got := ClaimIdentities(b)
	want := []string{"ci-abc3", "city--adhoc-9f1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimIdentities() = %v, want %v", got, want)
	}
}
