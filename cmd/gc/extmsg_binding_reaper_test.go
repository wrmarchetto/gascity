package main

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/extmsg"
	"github.com/gastownhall/gascity/internal/session"
)

func TestReapStaleExtmsgBindingsRepointsRespawnedSession(t *testing.T) {
	store := beads.NewMemStore()
	now := time.Date(2026, time.March, 23, 9, 0, 0, 0, time.UTC)

	oldSession, err := store.Create(beads.Bead{
		Title:    "session gc-pl",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "gc-pl"},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	ref := extmsg.ConversationRef{
		ScopeID:        "city-1",
		Provider:       "slack",
		AccountID:      "acct-1",
		ConversationID: "C0B25SS12CD",
		Kind:           extmsg.ConversationRoom,
	}
	svc := extmsg.NewServices(store).Bindings
	if _, err := svc.Bind(context.Background(), extmsg.Caller{Kind: extmsg.CallerController, ID: "test"}, extmsg.BindInput{
		Conversation: ref,
		SessionID:    oldSession.ID,
		Now:          now,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// Respawn: close the old bead, mint a fresh one under the same name.
	if err := store.Close(oldSession.ID); err != nil {
		t.Fatalf("close old session: %v", err)
	}
	newSession, err := store.Create(beads.Bead{
		Title:    "session gc-pl",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "gc-pl"},
	})
	if err != nil {
		t.Fatalf("recreate session bead: %v", err)
	}

	var stderr bytes.Buffer
	reapStaleExtmsgBindings(context.Background(), beads.SessionStore{Store: store}, now, &stderr)

	got, err := svc.ResolveByConversation(context.Background(), ref)
	if err != nil {
		t.Fatalf("ResolveByConversation: %v", err)
	}
	if got == nil || got.SessionID != newSession.ID {
		t.Fatalf("binding not re-pointed at respawned session: got %+v want SessionID=%s", got, newSession.ID)
	}
}

func TestReapStaleExtmsgBindingsNilStoreNoPanic(_ *testing.T) {
	// Defensive: a tick before the bead store is wired must be a no-op.
	reapStaleExtmsgBindings(context.Background(), beads.SessionStore{}, time.Now(), nil)
}

func TestReapStaleExtmsgParticipantsMigratesMembershipToRespawn(t *testing.T) {
	store := beads.NewMemStore()

	oldSession, err := store.Create(beads.Bead{
		Title:    "session gc-pl",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "gc-pl"},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	ref := extmsg.ConversationRef{
		ScopeID:        "city-1",
		Provider:       "slack",
		AccountID:      "acct-1",
		ConversationID: "C0B25SS12CD",
		Kind:           extmsg.ConversationRoom,
	}
	fabric := extmsg.NewServices(store)
	caller := extmsg.Caller{Kind: extmsg.CallerController, ID: "test"}
	group, err := fabric.Groups.EnsureGroup(context.Background(), caller, extmsg.EnsureGroupInput{
		RootConversation: ref,
		Mode:             extmsg.GroupModeLauncher,
		DefaultHandle:    "alpha",
	})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	participant, err := fabric.Groups.UpsertParticipant(context.Background(), caller, extmsg.UpsertParticipantInput{
		GroupID:   group.ID,
		Handle:    "alpha",
		SessionID: oldSession.ID,
	})
	if err != nil {
		t.Fatalf("UpsertParticipant: %v", err)
	}

	// Respawn: close the old bead, mint a fresh one under the same name. No
	// binding exists, so the binding reaper never observes this session — only
	// the participant reaper can converge it.
	if err := store.Close(oldSession.ID); err != nil {
		t.Fatalf("close old session: %v", err)
	}
	newSession, err := store.Create(beads.Bead{
		Title:    "session gc-pl",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "gc-pl"},
	})
	if err != nil {
		t.Fatalf("recreate session bead: %v", err)
	}

	var stderr bytes.Buffer
	reapStaleExtmsgParticipants(context.Background(), beads.SessionStore{Store: store}, &stderr)

	// Persistent heal: the participant now points at the respawned bead.
	bead, err := store.Get(participant.ID)
	if err != nil {
		t.Fatalf("get participant bead: %v", err)
	}
	if bead.Metadata["session_id"] != newSession.ID {
		t.Fatalf("participant session_id = %q, want %q (respawned bead)", bead.Metadata["session_id"], newSession.ID)
	}

	// The group-owned transcript membership followed the respawn. Routing already
	// self-heals at read time, so membership migration is the reaper's unique
	// contribution and the thing finding-3 guards against stranding.
	memberships, err := fabric.Transcript.ListMemberships(context.Background(), caller, ref)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	var sessionIDs []string
	for _, m := range memberships {
		sessionIDs = append(sessionIDs, m.SessionID)
	}
	if slices.Contains(sessionIDs, oldSession.ID) {
		t.Errorf("group membership still stranded on retired session %s: %v", oldSession.ID, sessionIDs)
	}
	if !slices.Contains(sessionIDs, newSession.ID) {
		t.Errorf("group membership did not follow respawn to %s: %v", newSession.ID, sessionIDs)
	}
}

func TestReapStaleExtmsgParticipantsNilStoreNoPanic(_ *testing.T) {
	// Defensive: a tick before the bead store is wired must be a no-op.
	reapStaleExtmsgParticipants(context.Background(), beads.SessionStore{}, nil)
}

// TestExtmsgReapOutcomeDistinguishesNoOpSweepFromNoSweep pins the property the
// supervisor log could not answer during ci-fdr7cf: whether a binding was ever
// examined. Both a sweep that scanned rows and correctly changed nothing and a
// tick that never swept at all leave the change-only stderr line silent, so the
// discriminator has to be the returned outcome and the trace fields built from
// it. Asserting only on stderr would go green over exactly the regression --
// the outcome being dropped again -- that this test exists to catch.
func TestExtmsgReapOutcomeDistinguishesNoOpSweepFromNoSweep(t *testing.T) {
	store := beads.NewMemStore()
	now := time.Date(2026, time.September, 8, 9, 0, 0, 0, time.UTC)

	live, err := store.Create(beads.Bead{
		Title:    "session gc-pl",
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "gc-pl"},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	ref := extmsg.ConversationRef{
		ScopeID:        "city-1",
		Provider:       "slack",
		AccountID:      "acct-1",
		ConversationID: "C0B25SS12CD",
		Kind:           extmsg.ConversationRoom,
	}
	if _, err := extmsg.NewServices(store).Bindings.Bind(context.Background(),
		extmsg.Caller{Kind: extmsg.CallerController, ID: "test"},
		extmsg.BindInput{Conversation: ref, SessionID: live.ID, Now: now}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	var stderr bytes.Buffer
	swept := reapStaleExtmsgBindings(context.Background(), beads.SessionStore{Store: store}, now, &stderr)
	if !swept.Ran {
		t.Fatalf("sweep over a live binding reported Ran=false: %+v", swept)
	}
	if swept.Stats.Scanned != 1 {
		t.Fatalf("scanned = %d, want 1 (the one active binding)", swept.Stats.Scanned)
	}
	if swept.Stats.Reassigned != 0 || swept.Stats.Cleared != 0 {
		t.Fatalf("a correctly-pointed binding was mutated: %+v", swept.Stats)
	}
	if stderr.Len() != 0 {
		t.Fatalf("no-op sweep wrote to stderr, which the reconciler tick rate cannot afford: %q", stderr.String())
	}

	skipped := reapStaleExtmsgBindings(context.Background(), beads.SessionStore{}, now, &stderr)
	if skipped.Ran {
		t.Fatalf("unwired store reported Ran=true: %+v", skipped)
	}

	sweptFields := swept.traceFields()
	skippedFields := skipped.traceFields()
	if sweptFields["ran"] != true || skippedFields["ran"] != false {
		t.Fatalf("trace fields do not separate the two sweeps: swept=%v skipped=%v", sweptFields, skippedFields)
	}
	if sweptFields["scanned"] != 1 {
		t.Fatalf("trace fields dropped the scan count: %v", sweptFields)
	}
}

// TestExtmsgReapOutcomeCarriesSweepError pins that a failed sweep is reported
// as a distinct third state rather than collapsing into the never-ran one. The
// two demand opposite responses from a reader: never-ran means look at the
// tick, failed means look at the store.
func TestExtmsgReapOutcomeCarriesSweepError(t *testing.T) {
	var stderr bytes.Buffer
	store := beads.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := reapStaleExtmsgBindings(ctx, beads.SessionStore{Store: store}, time.Now(), &stderr)
	if !got.Ran {
		t.Fatalf("an attempted sweep reported Ran=false: %+v", got)
	}
	if got.Err == nil {
		t.Fatalf("canceled sweep reported no error: %+v", got)
	}
	if fields := got.traceFields(); fields["error"] == nil {
		t.Fatalf("trace fields dropped the sweep error: %v", fields)
	}
	if stderr.Len() == 0 {
		t.Fatal("a failed sweep must still be reported on stderr")
	}
}

// TestExtmsgParticipantReapOutcomeReportsScan is the participant-side companion
// to TestExtmsgReapOutcomeDistinguishesNoOpSweepFromNoSweep. It exists
// separately because the participant sweep's stderr line is change-only for the
// same reason and reads identically when it never ran -- the log line that
// misdirected ci-fdr7cf's author was this one.
func TestExtmsgParticipantReapOutcomeReportsScan(t *testing.T) {
	store := beads.NewMemStore()
	var stderr bytes.Buffer

	skipped := reapStaleExtmsgParticipants(context.Background(), beads.SessionStore{}, &stderr)
	if skipped.Ran {
		t.Fatalf("unwired store reported Ran=true: %+v", skipped)
	}
	ran := reapStaleExtmsgParticipants(context.Background(), beads.SessionStore{Store: store}, &stderr)
	if !ran.Ran {
		t.Fatalf("wired store reported Ran=false: %+v", ran)
	}
	if fields := ran.traceFields(); fields["ran"] != true || fields["scanned"] != 0 {
		t.Fatalf("participant trace fields wrong: %v", fields)
	}
}
