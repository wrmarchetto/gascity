package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/workrelease"
)

// Scope: whether closing a session over the HTTP API releases the work that
// session held, the way `gc session close` does. Nothing else about the close --
// bead status, wake/hold clears, wait-nudge withdrawal, the delete=true variant
// -- is asserted here; session_model_phase0_lifecycle_spec_test.go owns those.
//
// Why it exists (ci-dkgt9s). Both API close handlers call
// worker.Handle.CloseDetailed and then stop. cmd/gc/cmd_session.go calls it and
// then sweeps the closed session's work back onto the queue, with the comment
// "Release any work beads still assigned to the closed session so the pool
// scale-check picks up the freed demand on the next reconcile tick". Same worker
// handle, two different outcomes: a session closed from the dashboard left its
// claim in_progress under a session that no longer exists, which is invisible to
// every work_query tier (Tier 1 needs an assignee match, Tiers 2/3 only match
// "ready").
//
// Run: go test ./internal/api/ -run SessionCloseReleases

// TestSessionCloseReleasesHeldWork drives the real HTTP route, which is the
// medium the divergence lives in -- a unit test on the handler body could not
// tell that the route reaches this code at all.
func TestSessionCloseReleasesHeldWork(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	id := phase0MaterializeCityScopedNamedWorker(t, srv, fs)

	sessionBead, err := fs.cityBeadStore.Get(id)
	if err != nil {
		t.Fatalf("Get(session bead): %v", err)
	}

	// A claim held under the session's own bead ID: no future session bears that
	// identity, so nothing re-addresses this work if the close drops it.
	work, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "claim held by the closing session",
		Type:     "task",
		Status:   "open",
		Assignee: sessionBead.ID,
	})
	if err != nil {
		t.Fatalf("Create(work): %v", err)
	}
	inProgress := "in_progress"
	if err := fs.cityBeadStore.Update(work.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark work in_progress: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPostRequest(cityURL(fs, "/session/"+id+"/close"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("close status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	closed, err := fs.cityBeadStore.Get(id)
	if err != nil {
		t.Fatalf("Get(session bead after close): %v", err)
	}
	if closed.Status != "closed" {
		t.Fatalf("session bead status = %q, want closed", closed.Status)
	}

	got, err := fs.cityBeadStore.Get(work.ID)
	if err != nil {
		t.Fatalf("Get(work after close): %v", err)
	}
	if got.Status != "open" {
		t.Errorf("work status = %q, want open; a claim left in_progress under a closed session is invisible to every work_query tier", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("work assignee = %q, want cleared; no future session bears a closed session's bead ID", got.Assignee)
	}
	// The released claim carried no route of its own, so the close must stamp
	// the session's template as the run_target fallback. Without it the work
	// lands open, unassigned AND unrouted -- which the reconciler's orphan sweep
	// skips, and is the shape that made this defect outlive a tick at all.
	if route := got.Metadata["gc.run_target"]; route != "worker" {
		t.Errorf("released work gc.run_target = %q, want %q; an unrouted release reaches no pool door", route, "worker")
	}
}

// TestSessionCloseKeepsWorkAddressedToTheSeat is the other direction, and the
// reason the API path calls the shared internal/workrelease rule rather than a
// sweep of its own: an OPEN bead addressed to an identity the seat's next
// occupant bears again -- here the alias -- is a routing decision the closing
// session did not make and may not discard (ci-8vx85v). A third hand-rolled
// sweep would have re-introduced that strip on this path alone.
func TestSessionCloseKeepsWorkAddressedToTheSeat(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	id := phase0MaterializeCityScopedNamedWorker(t, srv, fs)

	sessionBead, err := fs.cityBeadStore.Get(id)
	if err != nil {
		t.Fatalf("Get(session bead): %v", err)
	}
	alias := sessionBead.Metadata["alias"]
	if alias == "" {
		t.Fatal("fixture session bead has no alias; this test cannot express a seat address")
	}

	addressed, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "addressed to the seat, never claimed",
		Type:     "task",
		Status:   "open",
		Assignee: alias,
	})
	if err != nil {
		t.Fatalf("Create(addressed work): %v", err)
	}
	claimed, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "claimed under the seat identity",
		Type:     "task",
		Status:   "open",
		Assignee: alias,
	})
	if err != nil {
		t.Fatalf("Create(claimed work): %v", err)
	}
	inProgress := "in_progress"
	if err := fs.cityBeadStore.Update(claimed.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark claimed work in_progress: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPostRequest(cityURL(fs, "/session/"+id+"/close"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("close status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	gotAddressed, err := fs.cityBeadStore.Get(addressed.ID)
	if err != nil {
		t.Fatalf("Get(addressed work): %v", err)
	}
	if gotAddressed.Assignee != alias {
		t.Errorf("addressed work assignee = %q, want %q preserved; the seat's next occupant bears that name", gotAddressed.Assignee, alias)
	}
	if gotAddressed.Status != "open" {
		t.Errorf("addressed work status = %q, want open", gotAddressed.Status)
	}

	gotClaimed, err := fs.cityBeadStore.Get(claimed.ID)
	if err != nil {
		t.Fatalf("Get(claimed work): %v", err)
	}
	if gotClaimed.Status != "open" {
		t.Errorf("claimed work status = %q, want open; a claim held under the seat still returns to the queue", gotClaimed.Status)
	}
	if gotClaimed.Assignee != "" {
		t.Errorf("claimed work assignee = %q, want cleared; the assignee on an in_progress bead is the claim's artifact, not the filer's address", gotClaimed.Assignee)
	}
}

// TestSessionCloseOnV0RouteReleasesHeldWork covers the OTHER close handler.
//
// There are two, on two routes: the city-scoped route the tests above drive
// reaches the Huma handler, and POST /v0/session/{id}/close (server.go) reaches
// the plain REST one. They are separate function bodies that must agree, and a
// suite exercising one proves nothing about the other -- which is not
// hypothetical: deleting the sweep from the REST handler left every test above
// green, and only mutating each handler in turn revealed that this second route
// had no coverage at all.
func TestSessionCloseOnV0RouteReleasesHeldWork(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	id := phase0MaterializeCityScopedNamedWorker(t, srv, fs)

	sessionBead, err := fs.cityBeadStore.Get(id)
	if err != nil {
		t.Fatalf("Get(session bead): %v", err)
	}
	alias := sessionBead.Metadata["alias"]

	claim, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "claim held by the closing session",
		Type:     "task",
		Status:   "open",
		Assignee: sessionBead.ID,
	})
	if err != nil {
		t.Fatalf("Create(claim): %v", err)
	}
	inProgress := "in_progress"
	if err := fs.cityBeadStore.Update(claim.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark claim in_progress: %v", err)
	}
	addressed, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "addressed to the seat, never claimed",
		Type:     "task",
		Status:   "open",
		Assignee: alias,
	})
	if err != nil {
		t.Fatalf("Create(addressed): %v", err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/session/"+id+"/close", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("close status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	gotClaim, err := fs.cityBeadStore.Get(claim.ID)
	if err != nil {
		t.Fatalf("Get(claim): %v", err)
	}
	if gotClaim.Status != "open" || gotClaim.Assignee != "" {
		t.Errorf("claim = status %q assignee %q, want open/unassigned", gotClaim.Status, gotClaim.Assignee)
	}
	gotAddressed, err := fs.cityBeadStore.Get(addressed.ID)
	if err != nil {
		t.Fatalf("Get(addressed): %v", err)
	}
	if gotAddressed.Assignee != alias {
		t.Errorf("addressed work assignee = %q, want %q preserved on this route too", gotAddressed.Assignee, alias)
	}
}

// TestClosedSessionKeepsItsAliasReachableViaHistory pins the property that makes
// the capture order safe, rather than asserting an ordering that does not
// actually bite.
//
// The handlers read the session bead BEFORE CloseDetailed, and the close does
// retire identities -- measured on this fixture, `alias` and `session_name` are
// blanked. That looks load-bearing and is not: the retirement MOVES the alias
// into alias_history, which the assignee vocabulary also reads, so a post-close
// read yields the same identity set and the same ephemeral/durable split.
// Capturing after the close was tried as a mutation and changed nothing.
//
// So the pre-close read is defensive, not required -- and this test guards the
// retention it depends on. If a future change stops carrying a retired alias
// into alias_history, capture order becomes load-bearing silently, and work
// claimed under that alias would simply never be enumerated by a sweep reading
// the closed bead. This goes red first.
func TestClosedSessionKeepsItsAliasReachableViaHistory(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	id := phase0MaterializeCityScopedNamedWorker(t, srv, fs)

	// A value of its own, because the fixture otherwise has
	// alias == session_name == configured_named_identity and the retirement
	// would be invisible.
	const distinctAlias = "worker-seat-7"
	if err := fs.cityBeadStore.SetMetadataBatch(id, map[string]string{"alias": distinctAlias}); err != nil {
		t.Fatalf("SetMetadataBatch(alias): %v", err)
	}

	claim, err := fs.cityBeadStore.Create(beads.Bead{
		Title:    "claim held under the alias the close retires",
		Type:     "task",
		Status:   "open",
		Assignee: distinctAlias,
	})
	if err != nil {
		t.Fatalf("Create(claim): %v", err)
	}
	inProgress := "in_progress"
	if err := fs.cityBeadStore.Update(claim.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark claim in_progress: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPostRequest(cityURL(fs, "/session/"+id+"/close"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("close status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	closed, err := fs.cityBeadStore.Get(id)
	if err != nil {
		t.Fatalf("Get(session bead after close): %v", err)
	}
	if got := closed.Metadata["alias"]; got != "" {
		t.Fatalf("alias after close = %q, want cleared; the close no longer retires it and this test no longer describes reality", got)
	}
	identities := workrelease.IdentitiesFromBead(closed)
	if !slices.Contains(identities, distinctAlias) {
		t.Fatalf("post-close identities = %v, want to contain the retired alias %q via alias_history; without it a sweep reading the closed bead cannot see work claimed under that alias", identities, distinctAlias)
	}

	// And the claim is released either way, which is the outcome that matters.
	gotClaim, err := fs.cityBeadStore.Get(claim.ID)
	if err != nil {
		t.Fatalf("Get(claim): %v", err)
	}
	if gotClaim.Status != "open" || gotClaim.Assignee != "" {
		t.Errorf("claim = status %q assignee %q, want open/unassigned", gotClaim.Status, gotClaim.Assignee)
	}
}
