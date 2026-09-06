package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: what the pool-create path RECORDS when it cannot reserve a session's
// alias. It says nothing about whether the refusal was correct -- that is the
// reservation rule in internal/session/names.go and its own tests.
//
// The suite exists because the refusal is the branch that decides which
// assignee spelling a session writes for its whole life, and it left no trace
// of any kind. A session created alias-less resolves its claim assignee to the
// session-name rung (session.AssigneeIdentifier falls through to
// SessionNameMetadata), so its claims become invisible to any consumer joining
// on the agent name -- and with nothing recorded, the only way to learn that
// this is what happened is to reconstruct it from event-log archeology, which
// is how city bead ci-yfuh3a came to exist. The sibling branch ten lines below
// the refusal already prints "creating without alias" on a lock failure, so the
// file's own convention was to report a lost alias; the reservation branch was
// the one that did not.
//
// Run: go test ./cmd/gc/ -run TestPoolSessionAliasRefusal

// poolSessionAliasRefusalFixture builds a one-slot pool agent plus a store, and
// returns build params ready for createPoolSessionBeadWithGuardedAlias.
func poolSessionAliasRefusalFixture(t *testing.T) (*agentBuildParams, *config.City, beads.Store, *bytes.Buffer) {
	t.Helper()
	store := beads.NewMemStore()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			StartCommand:      "true",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(2),
		}},
	}
	stderr := &bytes.Buffer{}
	bp := newAgentBuildParams("test-city", t.TempDir(), cfg, runtime.NewFake(), time.Now().UTC(), store, stderr)
	bp.sessionBeads = newSessionBeadSnapshot(nil)
	return bp, cfg, store, stderr
}

// openPredecessorHoldingAlias seeds the shape measured in the city: an outgoing
// incarnation of the SAME slot, not yet closed, holding the alias. It is swept
// only AFTER its replacement is created -- 7.06s and 7.16s after, for the two
// governor sessions this was traced through -- and that ordering is what
// manufactures the overlap.
//
// It holds alias AND agent_name because a healthy predecessor holds both, and
// the alias branch of ensureSessionAliasAvailable is the one that fires first.
// Documented absence: the agent_name-ONLY predecessor is a real refusal path in
// that function too (it has a self-owner exception the alias branch lacks) but
// was NOT observed in the city, so no fixture here claims it. Pinning the
// unobserved branch as if it were the cause is how a fix aims at the wrong one.
func openPredecessorHoldingAlias(t *testing.T, store beads.Store, agentName string) beads.Bead {
	t.Helper()
	created, err := store.Create(beads.Bead{
		Title:  agentName,
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "worker-ci-predecessor",
			"alias":        agentName,
			"agent_name":   agentName,
			"template":     "worker",
			"state":        "awake",
			"pool_managed": "true",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(predecessor session bead): %v", err)
	}
	return created
}

// TestPoolSessionAliasRefusalIsRecordedOnTheBead pins that the refusal leaves a
// DURABLE trace, not a log line. A stderr warning here reaches the controller's
// rotating log and nothing else, and the question it answers -- why does this
// session claim under the session-name spelling -- is asked days later against
// the store.
func TestPoolSessionAliasRefusalIsRecordedOnTheBead(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	predecessor := openPredecessorHoldingAlias(t, store, "worker-1")

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: the fixture no longer reproduces a refused reservation, so this test has stopped testing one", info.Alias)
	}

	stored, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", info.ID, err)
	}
	if got, want := stored.Metadata[aliasReservationRefusedMetadataKey], "worker-1"; got != want {
		t.Errorf("%s = %q, want %q", aliasReservationRefusedMetadataKey, got, want)
	}
	reason := stored.Metadata[aliasReservationRefusedReasonMetadataKey]
	if reason == "" {
		t.Fatalf("%s is empty; the record has to say why, or it only repeats what an absent alias already showed", aliasReservationRefusedReasonMetadataKey)
	}
	// The blocking bead id is what makes the record decidable: it distinguishes
	// a genuine collision with an unrelated live session from this slot's own
	// outgoing incarnation, and those have opposite remedies.
	if !strings.Contains(reason, predecessor.ID) {
		t.Errorf("%s = %q, want it to name the blocking bead %s", aliasReservationRefusedReasonMetadataKey, reason, predecessor.ID)
	}
}

// TestPoolSessionAliasRefusalReachesTheSessionListRow pins the consumer end. A
// key written by the create path and read by nobody is invisible to a suite
// that tests each end separately, and the whole defect being closed here is a
// producer whose choice no consumer could see.
func TestPoolSessionAliasRefusalReachesTheSessionListRow(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	openPredecessorHoldingAlias(t, store, "worker-1")

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	// The returned Info is projected from the created bead by
	// infoFromPersistedBead, so asserting on it exercises the metadata->Info
	// decoder rather than a value the create path happened to keep in hand.
	if got, want := info.AliasReservationRefused, "worker-1"; got != want {
		t.Errorf("Info.AliasReservationRefused = %q, want %q: the metadata key is not decoded into Info", got, want)
	}

	rows := sessionListJSONRows([]session.Info{info})
	if len(rows) != 1 {
		t.Fatalf("sessionListJSONRows returned %d rows, want 1", len(rows))
	}
	if got, want := rows[0].AliasReservationRefused, "worker-1"; got != want {
		t.Errorf("row alias_reservation_refused = %q, want %q", got, want)
	}
	if rows[0].AliasReservationRefusedReason == "" {
		t.Errorf("row alias_reservation_refused_reason is empty; got row %#v", rows[0])
	}
}

// TestPoolSessionAliasRefusalRecordsNothingWhenTheAliasIsWon pins that the keys
// mean "a reservation was refused", not "a reservation was attempted". Stamping
// them unconditionally would make every session look degraded and the record
// would stop carrying information -- and a consumer keying on presence would
// then treat every session as alias-less.
func TestPoolSessionAliasRefusalRecordsNothingWhenTheAliasIsWon(t *testing.T) {
	bp, cfg, store, stderr := poolSessionAliasRefusalFixture(t)

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "worker-1" {
		t.Fatalf("alias = %q, want %q: with no predecessor the reservation must succeed, or this test proves nothing", info.Alias, "worker-1")
	}
	stored, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", info.ID, err)
	}
	for _, key := range []string{aliasReservationRefusedMetadataKey, aliasReservationRefusedReasonMetadataKey} {
		if got := stored.Metadata[key]; got != "" {
			t.Errorf("%s = %q on a session that won its alias, want absent", key, got)
		}
	}
	if strings.Contains(stderr.String(), "alias reservation refused") {
		t.Errorf("stderr warns about a refusal that did not happen:\n%s", stderr.String())
	}
}
