package main

// cmd/gc/claim_lease_renewal_test.go
//
// Pins the selection rule behind controller-side claim-lease renewal: which
// in-progress claims get their bd lease pushed forward on a patrol tick, and
// -- the half that carries the safety property -- which ones must NOT.
//
// The suite exists because the lease is the only signal `bd reclaim` reads,
// and bd applies it with no liveness check of its own. A renewal that fires
// for a holder the controller considers gone would pin a dead claim forever;
// a renewal that fails to fire for a live holder re-opens the theft the
// renewal was written to close (ci-pzejlf). Both directions are asserted here.
//
// Delegated elsewhere and deliberately not re-tested: the identity matching
// and cross-store scoping inside openSessionOwnsWork, covered by the
// orphan-release suite in pool_session_name_test.go. This suite reaches that
// predicate through the shared openSessionOwnership and asserts only that the
// two consumers cannot disagree
// (TestClaimLeaseRenewalNeverRenewsWhatOrphanReleaseReleases). Also not
// represented here: the bd subprocess. renewClaimLeaseViaBd spawns bd, and no
// unit test drives it -- the seam under test ends at the target list.
//
// One mutant survives the sweep and is left alive on purpose: deleting the
// empty-assignee short-circuit in due(). It changes no selection, because
// claimHolderIsPresent refuses an empty assignee through both of its sources.
// Killing it would mean asserting on throttle-map bookkeeping no consumer
// reads -- a test written to kill a mutant rather than to pin an invariant.
//
//	go test ./cmd/gc/ -run TestClaimLease

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// claimLeaseTestFixture is one open session plus the work bead it claimed.
type claimLeaseTestFixture struct {
	store   beads.Store
	cfg     *config.City
	session beads.Bead
	work    beads.Bead
}

// claimLeaseHolderName is the runtime session name every fixture here claims
// under. Its exact spelling carries nothing -- identity matching is the
// orphan-release suite's subject, not this one's.
const claimLeaseHolderName = "worker-mc-live"

// newClaimLeaseFixture builds the shape the controller sees on a steady tick:
// a pool-managed open session bead, and an in-progress work bead assigned to
// that session's runtime name. It mirrors the orphan-release fixtures so the
// agreement test can feed one fixture to both paths.
func newClaimLeaseFixture(t *testing.T) claimLeaseTestFixture {
	t.Helper()
	sessionName := claimLeaseHolderName
	store := beads.NewMemStore()
	sess, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":         sessionName,
			"template":             "worker",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "claimed pool work",
		Assignee: sessionName,
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}
	return claimLeaseTestFixture{
		store:   store,
		cfg:     &config.City{Agents: []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)}}},
		session: sess,
		work:    work,
	}
}

func (f claimLeaseTestFixture) openInfos() []sessionpkg.Info {
	return []sessionpkg.Info{seedSessionInfo(f.session)}
}

func renewalIDs(targets []claimLeaseRenewal) []string {
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.BeadID)
	}
	return ids
}

func TestClaimLeaseRenewalRenewsClaimHeldByAnOpenSession(t *testing.T) {
	f := newClaimLeaseFixture(t)
	r := newClaimLeaseRenewer()

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, time.Now())

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s]", renewalIDs(got), f.work.ID)
	}
	if got[0].Assignee != "worker-mc-live" {
		t.Errorf("Assignee = %q, want worker-mc-live -- bd matches a lease on holder = actor, so the renewal must present the holder", got[0].Assignee)
	}
}

func TestClaimLeaseRenewalSkipsClaimWhoseHolderHasNoOpenSession(t *testing.T) {
	// The safety property. An assignee that names no open session bead is the
	// controller's definition of a holder that is gone; renewing such a claim
	// would keep a dead worker's lease alive forever and make lease expiry
	// mean nothing in the other direction.
	f := newClaimLeaseFixture(t)
	r := newClaimLeaseRenewer()

	// Neither liveness source may find the holder: no session in the snapshot,
	// and no store to fall back to. With the fixture store passed here the
	// live-session fallback would resolve the very session bead the fixture
	// created, and this test would pass for the wrong reason.
	got := r.due("", f.cfg, nil, nil, []beads.Bead{f.work}, nil, time.Now())

	if len(got) != 0 {
		t.Fatalf("due = %v, want none when no open session holds the claim", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalRenewsHolderMissingFromTheSessionSnapshot(t *testing.T) {
	// A live session absent from a tick's snapshot is the case the release
	// path's store fallback exists for. Renewal must read the same fallback,
	// or exactly the holder the controller refuses to reap is the one whose
	// lease lapses under it.
	f := newClaimLeaseFixture(t)
	r := newClaimLeaseRenewer()

	got := r.due("", f.cfg, f.store, nil, []beads.Bead{f.work}, nil, time.Now())

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s] from the live-session store fallback", renewalIDs(got), f.work.ID)
	}
}

func TestClaimLeaseRenewalSkipsBeadsThatCarryNoClaim(t *testing.T) {
	// Only an in-progress bead with an assignee holds a lease. An open bead
	// carrying a stale assignee, and an in-progress bead with none, both reach
	// the controller through the same assigned-work snapshot; neither has a
	// lease to push forward, and a bd heartbeat on one is a wasted subprocess
	// that bd refuses anyway.
	f := newClaimLeaseFixture(t)

	openWithAssignee := f.work
	openWithAssignee.ID = "w-open"
	openWithAssignee.Status = "open"

	inProgressNoAssignee := f.work
	inProgressNoAssignee.ID = "w-unassigned"
	inProgressNoAssignee.Assignee = ""

	r := newClaimLeaseRenewer()
	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{openWithAssignee, inProgressNoAssignee}, nil, time.Now())

	if len(got) != 0 {
		t.Fatalf("due = %v, want none -- neither bead holds a claim", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalThrottlesToTheRenewInterval(t *testing.T) {
	// The patrol tick is far faster than the renew interval, so without a
	// throttle every claim would spawn a bd subprocess on every tick.
	f := newClaimLeaseFixture(t)
	r := newClaimLeaseRenewer()
	start := time.Now()

	if got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, start); len(got) != 1 {
		t.Fatalf("first due = %v, want the claim", renewalIDs(got))
	}
	if got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, start.Add(claimLeaseRenewInterval-time.Second)); len(got) != 0 {
		t.Fatalf("due inside the interval = %v, want none", renewalIDs(got))
	}
	if got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, start.Add(claimLeaseRenewInterval)); len(got) != 1 {
		t.Fatalf("due at the interval = %v, want the claim", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalForgetsClaimsThatLeftTheSnapshot(t *testing.T) {
	// The throttle map is keyed by bead id and lives as long as the
	// controller. Without pruning it grows by one entry per bead ever claimed
	// in the city's lifetime.
	f := newClaimLeaseFixture(t)
	r := newClaimLeaseRenewer()
	start := time.Now()

	r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, start)
	if len(r.renewedAt) != 1 {
		t.Fatalf("renewedAt = %v, want the one claim recorded", r.renewedAt)
	}
	r.due("", f.cfg, f.store, f.openInfos(), nil, nil, start.Add(time.Second))
	if len(r.renewedAt) != 0 {
		t.Fatalf("renewedAt = %v, want empty once the claim left the snapshot", r.renewedAt)
	}
}

func TestClaimLeaseRenewalNeverRenewsWhatOrphanReleaseReleases(t *testing.T) {
	// The invariant that makes the lease worth reading: a claim's lease is
	// live exactly while the controller would not reopen the claim. Asserted
	// against the release path's own behavior rather than against a shared
	// helper, so a future divergence in either body fails here.
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)}}}
	work, err := store.Create(beads.Bead{
		Title:    "claim held by a session that is gone",
		Assignee: "worker-mc-dead",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}

	// Renewal is asked first, against the same store the release path is about
	// to reopen from: it must decline while the claim is still in_progress and
	// assigned, not merely after the release has already cleared it.
	r := newClaimLeaseRenewer()
	got := r.due("", cfg, store, nil, []beads.Bead{work}, nil, time.Now())
	if len(got) != 0 {
		t.Fatalf("due = %v, want none for a claim whose holder is gone", renewalIDs(got))
	}

	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, "", nil,
		[]beads.Bead{work}, []beads.Store{store}, nil, nil,
	)
	if len(released) != 1 || released[0].ID != work.ID {
		t.Fatalf("released = %v, want the claim reopened -- the fixture must be one the release path acts on", released)
	}
}

func TestClaimLeaseRenewIntervalFitsTwiceInTheClaimLeaseTTL(t *testing.T) {
	// bd grants a claim lease with a TTL it does not expose through any store
	// surface, so gc cannot read it and this constant is the measurement
	// standing in for it. Two renewal opportunities per TTL means a single
	// missed or slow patrol tick still cannot lapse a live holder's lease.
	if claimLeaseRenewInterval*2 > bdClaimLeaseTTLMeasured {
		t.Fatalf("claimLeaseRenewInterval=%s leaves fewer than two tries per measured TTL %s", claimLeaseRenewInterval, bdClaimLeaseTTLMeasured)
	}
}

func TestRenewLiveClaimLeasesSkipsAPartialSnapshot(t *testing.T) {
	// A session snapshot missing a live holder makes its claim look unheld, so
	// a partial tick must renew nothing rather than decide from half a view.
	// Asserted on the renewer never being built, not on the returned count: a
	// zero count is also what a sweep that ran and failed every bd call
	// returns, and those are different outcomes.
	f := newClaimLeaseFixture(t)
	cr := &CityRuntime{cfg: f.cfg, stderr: io.Discard}

	if got := cr.renewLiveClaimLeases(context.Background(), f.openInfos(), []beads.Bead{f.work}, nil, DesiredStateResult{SessionQueryPartial: true}); got != 0 {
		t.Fatalf("renewed = %d, want 0 on a partial session snapshot", got)
	}
	if cr.clr != nil {
		t.Fatal("renewer was built on a partial snapshot; the guard must precede the sweep")
	}

	if got := cr.renewLiveClaimLeases(context.Background(), f.openInfos(), []beads.Bead{f.work}, nil, DesiredStateResult{StoreQueryPartial: true}); got != 0 {
		t.Fatalf("renewed = %d, want 0 on a partial store snapshot", got)
	}
	if cr.clr != nil {
		t.Fatal("renewer was built on a partial store snapshot")
	}
}

func TestClaimLeaseRenewalWarnsWhenPatrolCannotSustainIt(t *testing.T) {
	// Renewal fires on patrol ticks, so a patrol interval at or above the lease
	// TTL silently restores the original defect. Nothing can refuse that config
	// -- bd's TTL is not readable from gc -- so the warning is the whole
	// mechanism, and it must name the key and the remedy.
	var buf bytes.Buffer
	cr := &CityRuntime{
		cfg:    &config.City{Daemon: config.DaemonConfig{PatrolInterval: bdClaimLeaseTTLMeasured.String()}},
		stderr: &buf,
	}

	cr.warnIfPatrolOutrunsClaimLeaseTTL()

	got := buf.String()
	for _, want := range []string{"patrol_interval", "gc reload", bdClaimLeaseTTLMeasured.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not name %q", got, want)
		}
	}

	buf.Reset()
	cr.cfg = &config.City{Daemon: config.DaemonConfig{PatrolInterval: "30s"}}
	cr.warnIfPatrolOutrunsClaimLeaseTTL()
	if buf.Len() != 0 {
		t.Errorf("warned at the default patrol cadence: %q", buf.String())
	}
}

// --- a holder that has retired itself ---

// drainAckRefusedFixture is the steady tick a refused drain acknowledgement
// leaves behind: the session is open and alive, its claim is still in_progress
// and still assigned to it, and the only thing that changed is the state reason
// and refusal instant the controller wrote when it declined the
// acknowledgement. A zero refusedAt writes the reason alone, the shape of a
// refusal recorded before the instant was stamped.
//
// The session also carries the two fields the real transcript probe resolves
// on, session_key salted per run, so that only an Info projected from the
// whole bead can reach the scripted transcript.
func drainAckRefusedFixture(t *testing.T, refusedAt time.Time) claimLeaseTestFixture {
	t.Helper()
	f := newClaimLeaseFixture(t)
	patch := map[string]string{
		"state_reason": sessionpkg.DrainAckAssignedWorkReason,
		"session_key":  fmt.Sprintf("sk-%d", time.Now().UnixNano()),
		"work_dir":     t.TempDir(),
	}
	if !refusedAt.IsZero() {
		patch[sessionpkg.DrainAckRefusedAtMetadataKey] = refusedAt.UTC().Format(time.RFC3339)
	}
	for k, v := range patch {
		f.session.Metadata[k] = v
		if err := f.store.SetMetadata(f.session.ID, k, v); err != nil {
			t.Fatalf("SetMetadata(%s): %v", k, err)
		}
	}
	return f
}

// scriptedTranscript stands in for the transcript-mtime probe. It answers
// only for the fixture session's own key and FAILS the test for any other
// key, so a renewer that probes the wrong holder cannot borrow an answer meant
// for someone else.
//
// An Info with no session key or work dir gets the zero reading, as
// sessionTranscriptActivity gives it -- the keyed resolver cannot attribute
// such a row. Answering on Info.ID instead let a fallback that skipped the
// session front door, and so carried no key, read the scripted transcript
// anyway: the fallback-skips-front-door row SURVIVED the first sweep.
func scriptedTranscript(t *testing.T, f claimLeaseTestFixture, last time.Time) transcriptActivityProbe {
	t.Helper()
	want := f.session.Metadata["session_key"]
	return func(info sessionpkg.Info, _ []string) time.Time {
		if strings.TrimSpace(info.SessionKey) == "" || strings.TrimSpace(info.WorkDir) == "" {
			return time.Time{}
		}
		if info.SessionKey != want {
			t.Errorf("transcript probed for session key %q, want only %q", info.SessionKey, want)
			return time.Time{}
		}
		return last
	}
}

func TestClaimLeaseRenewalStopsOnceTheHolderHasDrainAcked(t *testing.T) {
	// The measured defect (ci-amflbh). A session answered drain at 05:36Z and
	// sat at an idle prompt for the next twelve hours. Its runtime stayed
	// alive, so claimHolderIsPresent kept saying yes and the controller pushed
	// the lease forward every two minutes until a human ran `gc bd unclaim
	// --force`. `gc bd show` read "heartbeat just now" the whole time, which is
	// the single sentence a lease exists to be able to say truthfully.
	//
	// The transcript is written a few seconds AFTER the refusal, not before
	// it. drain-ack pokes the controller, so the refusal lands while the
	// agent's turn is still closing out, and every measured refusal has that
	// trailing burst. A fixture whose transcript stopped before the refusal
	// would pass a renewer that keys on "any write since the refusal" -- the
	// rule that can never lapse anyone.
	now := time.Now()
	refusedAt := now.Add(-drainAckedHolderQuietWindow - 30*time.Minute)
	f := drainAckRefusedFixture(t, refusedAt)
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, refusedAt.Add(5*time.Second))

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, now)

	if len(got) != 0 {
		t.Fatalf("due = %v, want none -- the holder told the controller it was finished and has been silent since", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalResumesOnceADrainAckedHolderTakesAnotherTurn(t *testing.T) {
	// The defect this file was reopened for (ci-kkezqd). as-ognbh's holder
	// was refused at 02:07:47Z, went back to work on that same bead, and
	// lost its lease anyway because the state reason it carried is never
	// cleared. 40 of 46 refused sessions resume unaided (ci-8ajvxn), so the
	// refusal alone describes the common healthy case as a retirement.
	//
	// The refusal is dated long before the window so that only the fresh
	// transcript write can be what keeps the lease -- a refusal inside the
	// window would renew on its own and this test would not see the
	// transcript read deleted.
	now := time.Now()
	refusedAt := now.Add(-3 * drainAckedHolderQuietWindow)
	f := drainAckRefusedFixture(t, refusedAt)
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, now.Add(-time.Minute))

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, now)

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s] -- the holder wrote to its transcript after the refusal", renewalIDs(got), f.work.ID)
	}
}

func TestClaimLeaseRenewalCarriesADrainAckedHolderThroughAQuietGap(t *testing.T) {
	// A resumed holder is not writing continuously. Measured over 98 refused
	// sessions, quiet gaps that ENDED in more transcript writes reached
	// 1635s, with p95 at the 10-minute tool cap. Lapsing the lease at the
	// first silent tick would re-open the theft for every one of those, so
	// the holder keeps its lease until the silence outlasts the window.
	//
	// Set just inside the window, against a transcript that went quiet at
	// the refusal, so a renewer measuring from the wrong instant, or with
	// the comparison flipped, fails here.
	now := time.Now()
	refusedAt := now.Add(-drainAckedHolderQuietWindow + time.Minute)
	f := drainAckRefusedFixture(t, refusedAt)
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, refusedAt.Add(-time.Minute))

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, now)

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s] -- the holder has been quiet for less than the window", renewalIDs(got), f.work.ID)
	}
}

func TestClaimLeaseRenewalDatesAnUnattributableHolderFromItsRefusal(t *testing.T) {
	// A zero probe reading is ABSENT, not old: a provider with no keyed
	// transcript, or a file not yet written. The refusal instant is then the
	// only date there is, so the holder gets the window from the refusal and
	// no more. Both arms are asserted because each alone passes a renewer
	// that ignores the refusal stamp in one direction.
	now := time.Now()
	for _, tc := range []struct {
		name      string
		refusedAt time.Time
		want      int
	}{
		{"refused inside the window", now.Add(-time.Minute), 1},
		{"refused beyond the window", now.Add(-drainAckedHolderQuietWindow - time.Minute), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := drainAckRefusedFixture(t, tc.refusedAt)
			r := newClaimLeaseRenewer()
			r.lastTranscript = scriptedTranscript(t, f, time.Time{})

			got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, now)

			if len(got) != tc.want {
				t.Fatalf("due = %v, want %d renewal(s)", renewalIDs(got), tc.want)
			}
		})
	}
}

func TestClaimLeaseRenewalStopsForAnUndatedRefusalWithNoTranscript(t *testing.T) {
	// A state reason with no refusal instant and no transcript reading leaves
	// nothing to measure a resumption from. The exclusion then holds as it did
	// before transcripts were read, rather than renewing a holder nothing can
	// show is working.
	f := drainAckRefusedFixture(t, time.Time{})
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, time.Time{})

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, time.Now())

	if len(got) != 0 {
		t.Fatalf("due = %v, want none -- nothing dates the holder's last turn", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalStopsForADrainAckedHolderMissingFromTheSnapshot(t *testing.T) {
	// The snapshot is not the only liveness source: renewal falls back to a
	// store query for a holder this tick's snapshot missed, and that fallback
	// is what TestClaimLeaseRenewalRenewsHolderMissingFromTheSessionSnapshot
	// pins. Reading the refusal out of the snapshot ALONE would leave the
	// fallback renewing a retired holder's lease forever, and the fallback is
	// reached on exactly the ticks nobody is watching.
	now := time.Now()
	refusedAt := now.Add(-drainAckedHolderQuietWindow - 30*time.Minute)
	f := drainAckRefusedFixture(t, refusedAt)
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, refusedAt.Add(5*time.Second))

	got := r.due("", f.cfg, f.store, nil, []beads.Bead{f.work}, nil, now)

	if len(got) != 0 {
		t.Fatalf("due = %v, want none -- the store says this holder retired", renewalIDs(got))
	}
}

func TestClaimLeaseRenewalResumesForADrainAckedHolderMissingFromTheSnapshot(t *testing.T) {
	// The fallback's other direction. A fallback that read the refusal but
	// not the transcript would lapse a resumed holder on exactly the ticks
	// its session missed the snapshot.
	now := time.Now()
	refusedAt := now.Add(-3 * drainAckedHolderQuietWindow)
	f := drainAckRefusedFixture(t, refusedAt)
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, now.Add(-time.Minute))

	got := r.due("", f.cfg, f.store, nil, []beads.Bead{f.work}, nil, now)

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s] from the store fallback -- the holder took another turn", renewalIDs(got), f.work.ID)
	}
}

func TestClaimLeaseRenewalKeepsRenewingUnderAnyOtherStateReason(t *testing.T) {
	// The negative control, and the reason the guard is keyed on the refusal
	// rather than on state_reason being set at all. Every healthy session in
	// the city carries a state reason -- "session,config", "creation_complete",
	// "reactivated" -- so a guard that read the key's presence would lapse the
	// lease of every live holder in the city and hand `bd reclaim` the whole
	// board.
	//
	// The probe is scripted to report an ancient transcript, so this also
	// pins that only a refused holder is ever judged on activity. That is
	// what keeps the header's rejection of activity-keyed renewal intact.
	f := newClaimLeaseFixture(t)
	for k, v := range map[string]string{
		"state_reason": "creation_complete",
		"session_key":  fmt.Sprintf("sk-%d", time.Now().UnixNano()),
		"work_dir":     t.TempDir(),
	} {
		f.session.Metadata[k] = v
		if err := f.store.SetMetadata(f.session.ID, k, v); err != nil {
			t.Fatalf("SetMetadata(%s): %v", k, err)
		}
	}
	now := time.Now()
	r := newClaimLeaseRenewer()
	r.lastTranscript = scriptedTranscript(t, f, now.Add(-24*time.Hour))

	got := r.due("", f.cfg, f.store, f.openInfos(), []beads.Bead{f.work}, nil, now)

	if len(got) != 1 || got[0].BeadID != f.work.ID {
		t.Fatalf("due = %v, want [%s] -- an ordinary state reason is not a retirement", renewalIDs(got), f.work.ID)
	}
}

func TestDrainAckedHolderQuietWindowSitsBetweenTheMeasuredRegimes(t *testing.T) {
	// The window is sited against a measurement, so it is pinned to the
	// measured figures rather than recomputed from itself. Measured
	// 2026-10-16 over the 97 refused sessions whose Claude transcripts were
	// attributable: the longest quiet gap that ended in the holder writing
	// again was 1635s (p95 600s), and the shortest hours-scale silence never
	// followed by another turn was 4248s. Shorter terminal silences exist
	// (one at 1674s) and are deliberately on the renewing side -- see the
	// constant's comment for why cost, not separation, places it. A new
	// measurement that moves either edge past the window fails here, which
	// is the prompt to re-site it.
	const (
		longestResumedQuiet = 1635 * time.Second
		shortestWedgedQuiet = 4248 * time.Second
	)
	if drainAckedHolderQuietWindow <= longestResumedQuiet {
		t.Errorf("window %s does not clear the longest measured resumed gap %s", drainAckedHolderQuietWindow, longestResumedQuiet)
	}
	if drainAckedHolderQuietWindow >= shortestWedgedQuiet {
		t.Errorf("window %s does not fall below the shortest measured wedge %s", drainAckedHolderQuietWindow, shortestWedgedQuiet)
	}
}
