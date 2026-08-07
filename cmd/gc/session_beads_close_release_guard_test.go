package main

import (
	"bytes"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Guards the session-close WORK-RELEASE cascade against reopening work the
// backing store already shows closed.
//
// The cascade enumerates assigned work with OpenAssignedToBasic, a deliberately
// non-Live store.List that the production CachingStore serves from the
// in-process cache. An agent that closes its own work through the external bd
// CLI writes straight to the backing store, and that close stays invisible to
// the cache until the next reconcile tick (30-120s). Releasing on that stale
// snapshot reopens a finished bead, and the pool re-dispatches it -- the work
// runs twice (observed on bead ci-2hk, sessions ci-1si/ci-e6t).
//
// The suite drives releaseWorkFromClosedSessionBead directly rather than
// closeBead so nothing but the release decision is under test, and it asserts
// against the BACKING store, never the cache: the cache is the liar here, so a
// cache read could report the bead closed while the backing row was reopened.
//
//	go test ./cmd/gc/ -run TestReleaseWorkFromClosedSessionBead -count=1

// primedCloseCascadeCache returns a CachingStore over backing whose active-bead
// cache has been primed, so the non-Live enumeration inside the cascade is
// served from cache and cannot observe a later backing-only write.
func primedCloseCascadeCache(t *testing.T, backing beads.Store) *beads.CachingStore {
	t.Helper()
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	return cache
}

// closeCascadeFixture creates a session bead and one in_progress work bead
// assigned to it on backing, then returns both plus a primed cache. The work
// bead is created before the cache is primed so the cache holds the
// pre-close snapshot the cascade will enumerate.
func closeCascadeFixture(t *testing.T, backing beads.Store) (beads.Bead, beads.Bead, *beads.CachingStore) {
	t.Helper()

	sessionBead, err := backing.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "worker-1",
			"state":        "active",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	work, err := backing.Create(beads.Bead{
		Title:    "prescribed procedure",
		Assignee: sessionBead.ID,
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	inProgress := "in_progress"
	if err := backing.Update(work.ID, beads.UpdateOpts{Status: &inProgress, Assignee: &sessionBead.ID}); err != nil {
		t.Fatalf("mark work in_progress: %v", err)
	}
	work, err = backing.Get(work.ID)
	if err != nil {
		t.Fatalf("re-read work bead: %v", err)
	}

	return sessionBead, work, primedCloseCascadeCache(t, backing)
}

// TestReleaseWorkFromClosedSessionBeadLeavesExternallyClosedWorkClosed pins the
// invariant: work the backing store already shows closed must survive the
// session-close release cascade closed, with its close record intact, even when
// the cache the cascade enumerated still shows it in_progress and assigned.
//
// The external close is written directly to the backing store, exactly as
// `bd close` does from an agent's shell, and no cache reconcile is run
// before the cascade, so the cascade sees the stale snapshot the real
// 30-120s tick window exposes. Asserting gc.outcome as well as status
// catches the weaker failure where a guard preserves the status but the
// release still stomps the bead's metadata.
func TestReleaseWorkFromClosedSessionBeadLeavesExternallyClosedWorkClosed(t *testing.T) {
	backing := beads.NewMemStore()
	sessionBead, work, cache := closeCascadeFixture(t, backing)

	// The agent's own `bd close`: outcome recorded, then closed, both straight
	// to the backing store while the cache still holds the in_progress row.
	if err := backing.SetMetadata(work.ID, beadmeta.OutcomeMetadataKey, "success"); err != nil {
		t.Fatalf("record outcome: %v", err)
	}
	if err := backing.Close(work.ID); err != nil {
		t.Fatalf("external close: %v", err)
	}

	// Sanity: the premise of the bug. The cache must still be lying about the
	// bead, or the test proves nothing about the guard.
	stale, err := cache.List(beads.ListQuery{Assignee: sessionBead.ID, Status: "in_progress"})
	if err != nil {
		t.Fatalf("cached enumeration: %v", err)
	}
	found := false
	for _, b := range stale {
		if b.ID == work.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache no longer shows %s in_progress; the stale-snapshot premise does not hold and this test cannot observe the defect", work.ID)
	}

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(cache, sessionBead, &stderr)

	got, err := backing.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead from backing: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("work bead Status = %q, want %q: the close cascade reopened work the backing store already showed closed, and the pool will re-dispatch it (stderr: %s)", got.Status, "closed", stderr.String())
	}
	if got.Metadata[beadmeta.OutcomeMetadataKey] != "success" {
		t.Fatalf("work bead %s = %q, want %q: the release stomped the close record", beadmeta.OutcomeMetadataKey, got.Metadata[beadmeta.OutcomeMetadataKey], "success")
	}
}

// TestReleaseWorkFromClosedSessionBeadStillReleasesLiveWorkThroughCache is the
// companion regression guard: the conditional release must not turn into a
// blanket refusal. Work that really is still in_progress and assigned to the
// closing session, cache and backing store agreeing, must come back open
// and unassigned, or a reaped session strands its work forever.
func TestReleaseWorkFromClosedSessionBeadStillReleasesLiveWorkThroughCache(t *testing.T) {
	backing := beads.NewMemStore()
	sessionBead, work, cache := closeCascadeFixture(t, backing)

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(cache, sessionBead, &stderr)

	got, err := backing.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead from backing: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("work bead Status = %q, want %q: uncontended work must still be released (stderr: %s)", got.Status, "open", stderr.String())
	}
	if got.Assignee != "" {
		t.Fatalf("work bead Assignee = %q, want cleared (stderr: %s)", got.Assignee, stderr.String())
	}
}
