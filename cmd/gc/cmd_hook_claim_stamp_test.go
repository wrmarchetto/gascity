package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// stampMetaSpy captures the (beadID, assignee, patch) a claim writes through the
// StampWorkMeta seam, and lets a test inject a write error to prove the stamp
// never fails the claim. The patch is copied so the assertion is stable.
type stampMetaSpy struct {
	calls    int
	beadID   string
	assignee string
	patch    map[string]string
	err      error
}

func (s *stampMetaSpy) fn(_ context.Context, _ string, _ []string, beadID, assignee string, patch map[string]string) error {
	s.calls++
	s.beadID, s.assignee = beadID, assignee
	s.patch = map[string]string{}
	for k, v := range patch {
		s.patch[k] = v
	}
	return s.err
}

// noopPublishRunMap keeps claim tests focused on work-bead identity stamping.
func noopPublishRunMap(string, string, ...string) error {
	return nil
}

// noopStampWorkMeta suppresses the work-bead identity stamp so claim tests that
// don't assert on it stay hermetic — the default seam issues a real bd subprocess
// write, which fires now that a claim stamps session identity whenever GC_SESSION_ID
// is set.
func noopStampWorkMeta(context.Context, string, []string, string, string, map[string]string) error {
	return nil
}

// poolClaimWorkerDir is the worktree the session bead in poolClaimOps names.
// The location keys are only stamped when a worker dir resolves, so every
// fixture here must carry one -- which is the point: the claim now records
// where the AGENT is, and a suite that could omit the agent's tree entirely
// and still see a branch was the hole ci-hdnj73 closed. Which directory the
// branch is actually resolved FROM is pinned over real git in
// cmd_hook_claim_workerdir_realgit_test.go; the resolver here is a constant.
const poolClaimWorkerDir = "/tmp/worktrees/pool-1"

// poolClaimSessionsRecord is the gc.claim_sessions value a claim by the
// session bead in poolClaimOps writes. Spelled out as a literal rather than
// built with the production encoder, so a change to the recorded shape fails
// here instead of moving both sides. A fixture modeling a bead stamped
// BEFORE the tick under test carries it, the way a current bead would.
const poolClaimSessionsRecord = `["mc-sess1:claude:key-pool-1"]`

// poolClaimSessionBead is the claiming session's own bead: it names the
// worktree and carries the provider and session key the claimant record
// is built from.
func poolClaimSessionBead(id string, workerDir string) beads.Bead {
	meta := map[string]string{"provider": "claude", "session_key": "key-pool-1"}
	if workerDir != "" {
		meta[beadmeta.WorkerDirMetadataKey] = workerDir
	}
	return beads.Bead{ID: id, Type: "session", Metadata: meta}
}

// poolClaimOps builds the seam for a pool slot claiming an unassigned,
// route-matched candidate: the runner yields it, Claim returns it owned by us,
// the session bead names poolClaimWorkerDir, the branch resolver returns
// branch, and StampWorkMeta is captured by spy.
func poolClaimOps(runner string, claimedMeta map[string]string, branch string, spy *stampMetaSpy) hookClaimOps {
	return hookClaimOps{
		Runner: func(string, string) (string, error) { return runner, nil },
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: claimedMeta}, true, nil
		},
		ReadSessionBead: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
			return poolClaimSessionBead(id, poolClaimWorkerDir), nil
		},
		ResolveWorkBranch: func(string) string { return branch },
		StampWorkMeta:     spy.fn,
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			meta := map[string]string{}
			for k, v := range claimedMeta {
				meta[k] = v
			}
			meta[beadmeta.SessionIDMetadataKey] = "mc-sess1"
			meta[beadmeta.SessionNameMetadataKey] = "gc__role-mc-sess1"
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: meta}, nil
		},
		PublishRunMap: noopPublishRunMap,
	}
}

// poolClaimOpts is a pool slot's claim options: assignee is the pool session name
// and the env carries both GC_SESSION_ID (the session bead id) and
// GC_SESSION_NAME (the pool session name).
func poolClaimOpts() hookClaimOptions {
	return hookClaimOptions{
		Assignee:           "gc__role-mc-sess1",
		IdentityCandidates: []string{"gc__role-mc-sess1"},
		RouteTargets:       []string{"worker"},
		Env:                []string{"GC_SESSION_ID=mc-sess1", "GC_SESSION_NAME=gc__role-mc-sess1"},
		JSON:               true,
	}
}

// TestDoHookClaimStampsSessionIdentity is the primary claim-time back-reference
// test: a fresh pool claim stamps gc.session_id + gc.session_name onto the work
// bead alongside gc.work_branch, in ONE patch. Fails before the fix, which stamped
// only the branch.
func TestDoHookClaimStampsSessionIdentity(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-pool","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-pool",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if spy.calls != 1 {
		t.Fatalf("StampWorkMeta calls = %d, want 1", spy.calls)
	}
	// The fixture bead carries no title or description, so the brief digest
	// is the digest of the empty brief -- derived here rather than written as
	// a literal, so a change to the digest's framing moves both sides.
	want := map[string]string{
		beadmeta.WorkBranchMetadataKey:    "bd-hw-pool",
		beadmeta.WorkDirMetadataKey:       poolClaimWorkerDir,
		beadmeta.SessionIDMetadataKey:     "mc-sess1",
		beadmeta.SessionNameMetadataKey:   "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   beadBriefDigest("", ""),
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	if !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("patch = %v, want %v", spy.patch, want)
	}
	if spy.beadID != "hw-pool" || spy.assignee != "gc__role-mc-sess1" {
		t.Fatalf("stamp target = bead %q assignee %q, want hw-pool / gc__role-mc-sess1", spy.beadID, spy.assignee)
	}
}

// TestDoHookClaimStampsSessionIdentityOnAdoption covers the adoption path: a bead
// already in_progress and owned by this session (existing_assignment, no fresh
// Claim) still receives the session back-reference, since the stamp re-runs on
// every hook tick that adopts the bead.
func TestDoHookClaimStampsSessionIdentityOnAdoption(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[{"id":"hw-adopt","status":"in_progress","assignee":"gc__role-mc-sess1","metadata":{"gc.routed_to":"worker"}}]`, nil
		},
		Claim: func(context.Context, string, []string, string, string) (beads.Bead, bool, error) {
			t.Error("Claim must not be called on the existing-assignment path")
			return beads.Bead{}, false, nil
		},
		ReadSessionBead: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
			return poolClaimSessionBead(id, ""), nil // no worktree
		},
		ResolveWorkBranch: func(string) string { return "" },
		StampWorkMeta:     spy.fn,
		PublishRunMap:     noopPublishRunMap,
	}

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{
		beadmeta.SessionIDMetadataKey:     "mc-sess1",
		beadmeta.SessionNameMetadataKey:   "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   beadBriefDigest("", ""),
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	if spy.calls != 1 || !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("stamp = {calls:%d patch:%v}, want {1 %v}", spy.calls, spy.patch, want)
	}
}

// TestDoHookClaimStampsSessionIdentityWithoutWorktree pins sessionVerify #1: when
// the worktree resolves no branch (no repo / detached HEAD), the session
// back-reference is STILL stamped — it must not be buried behind the branch
// early-return. gc.work_dir is stamped here too: the session named its tree,
// and only the BRANCH is unresolvable. The case where the tree itself is
// unknown belongs to cmd_hook_claim_workerdir_realgit_test.go, and stamps
// neither location key.
func TestDoHookClaimStampsSessionIdentityWithoutWorktree(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-nobranch","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"", // no branch
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{
		beadmeta.WorkDirMetadataKey:       poolClaimWorkerDir,
		beadmeta.SessionIDMetadataKey:     "mc-sess1",
		beadmeta.SessionNameMetadataKey:   "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   beadBriefDigest("", ""),
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	if spy.calls != 1 || !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("stamp = {calls:%d patch:%v}, want {1 %v} (session id/name even with no branch)", spy.calls, spy.patch, want)
	}
}

// TestDoHookClaimSkipsStampWhenIdentityUnchanged pins the mandatory idempotence
// guard (sessionVerify #2): a candidate already carrying the current branch AND
// session identity produces NO write, so the per-tick adoption re-run does not
// flood bead.updated events.
func TestDoHookClaimSkipsStampWhenIdentityUnchanged(t *testing.T) {
	spy := &stampMetaSpy{}
	// gc.brief_digest is part of "already current": it is stamped once on the
	// first tick that sees the bead without one, and the no-write invariant
	// this test pins applies from the tick after that.
	digest := beadBriefDigest("", "")
	current := map[string]string{
		"gc.routed_to":                    "worker",
		"gc.work_branch":                  "bd-hw-idem",
		beadmeta.WorkDirMetadataKey:       poolClaimWorkerDir,
		"gc.session_id":                   "mc-sess1",
		"gc.session_name":                 "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   digest,
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	ops := poolClaimOps(
		fmt.Sprintf(`[{"id":"hw-idem","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-hw-idem","gc.work_dir":%q,"gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.brief_digest":%q,"gc.claim_sessions":%q}}]`, poolClaimWorkerDir, digest, poolClaimSessionsRecord),
		current,
		"bd-hw-idem",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if spy.calls != 0 {
		t.Fatalf("StampWorkMeta calls = %d, want 0 (branch + session identity already current)", spy.calls)
	}
}

// TestDoHookClaimStampsOnlyChangedIdentityKeys proves the patch is minimal: a
// candidate whose session identity is current but whose branch changed writes ONLY
// the branch, leaving the unchanged session keys out of the patch.
func TestDoHookClaimStampsOnlyChangedIdentityKeys(t *testing.T) {
	spy := &stampMetaSpy{}
	digest := beadBriefDigest("", "")
	current := map[string]string{
		"gc.routed_to":                    "worker",
		"gc.work_branch":                  "bd-old",
		beadmeta.WorkDirMetadataKey:       poolClaimWorkerDir,
		"gc.session_id":                   "mc-sess1",
		"gc.session_name":                 "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   digest,
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	ops := poolClaimOps(
		fmt.Sprintf(`[{"id":"hw-partial","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-old","gc.work_dir":%q,"gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.brief_digest":%q,"gc.claim_sessions":%q}}]`, poolClaimWorkerDir, digest, poolClaimSessionsRecord),
		current,
		"bd-new",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{beadmeta.WorkBranchMetadataKey: "bd-new"}
	if spy.calls != 1 || !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("stamp = {calls:%d patch:%v}, want {1 %v} (only the changed key)", spy.calls, spy.patch, want)
	}
}

// TestDoHookClaimSkipsSessionIdentityForControlBead pins the control-bead edge
// policy (sessionVerify #3): a control-dispatcher session claiming a control bead
// (gc.kind in ControlKinds) must NOT acquire a session back-reference — control
// steps stay session-free by graphroute's design — while gc.work_branch is still
// stamped as before.
func TestDoHookClaimSkipsSessionIdentityForControlBead(t *testing.T) {
	spy := &stampMetaSpy{}
	// gc.kind=ralph stands in for "any member of ControlKinds"; the policy is
	// per-set, not per-kind. It was "check" until ci-zg0l retired that kind.
	ops := poolClaimOps(
		`[{"id":"hc-check","status":"open","metadata":{"gc.routed_to":"worker","gc.kind":"ralph"}}]`,
		map[string]string{"gc.routed_to": "worker", "gc.kind": "ralph"},
		"bd-hc-check",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{
		beadmeta.WorkBranchMetadataKey: "bd-hc-check",
		beadmeta.WorkDirMetadataKey:    poolClaimWorkerDir,
	}
	if spy.calls != 1 || !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("stamp = {calls:%d patch:%v}, want {1 %v} (no session keys on a control bead)", spy.calls, spy.patch, want)
	}
}

// TestDoHookClaimSkipsSessionIdentityWhenNoSessionID: a non-session run (no
// GC_SESSION_ID) has no session bead to reference, so NOTHING is stamped --
// not the session keys, and not the location keys either.
//
// The location half is the part worth stating: gc.work_branch used to be
// stamped here from the bead store's shared checkout, which is precisely the
// operator's own tree. With no session there is no agent whose worktree could
// be resolved, so the truthful record is silence (ci-hdnj73).
func TestDoHookClaimSkipsSessionIdentityWhenNoSessionID(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-nosess","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-nosess",
		spy,
	)
	opts := poolClaimOpts()
	opts.Env = []string{"GC_SESSION_NAME=gc__role-mc-sess1"} // GC_SESSION_ID absent

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", opts, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if spy.calls != 0 {
		t.Fatalf("stamp = {calls:%d patch:%v}, want no write at all (no session id ⇒ no identity and no worktree)", spy.calls, spy.patch)
	}
}

// TestDoHookClaimIdentityStampFailureDoesNotFailClaim proves the stamp is
// best-effort: a failing StampWorkMeta logs to stderr but the claim still exits 0
// and reports the claimed bead id.
func TestDoHookClaimIdentityStampFailureDoesNotFailClaim(t *testing.T) {
	spy := &stampMetaSpy{err: errors.New("dolt boom")}
	ops := poolClaimOps(
		`[{"id":"hw-err","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-err",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0 (stamp error must not fail the claim); stderr=%s", code, stderr.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.BeadID != "hw-err" || result.Reason != "claimed" {
		t.Fatalf("claim result = %+v, want bead hw-err reason claimed", result)
	}
}

func TestDoHookClaimEmitsStartedOnlyAfterDurableSessionReadback(t *testing.T) {
	spy := &stampMetaSpy{}
	meta := map[string]string{
		"gc.routed_to": "worker", beadmeta.RootBeadIDMetadataKey: "gcg-run",
		beadmeta.StepIDMetadataKey: "build", beadmeta.NativeStepDependenciesMetadataKey: `["prepare"]`,
	}
	ops := poolClaimOps(`[{"id":"gcg-attempt","status":"open","metadata":{"gc.routed_to":"worker"}}]`, meta, "", spy)
	var emitted []beads.Bead
	ops.EmitExecutionStepStarted = func(b beads.Bead, _ string, _ []string, _ string) { emitted = append(emitted, b) }
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d; stderr=%s", code, stderr.String())
	}
	if len(emitted) != 1 || emitted[0].ID != "gcg-attempt" || emitted[0].Metadata[beadmeta.SessionIDMetadataKey] != "mc-sess1" || emitted[0].Status != "in_progress" {
		t.Fatalf("started emission = %#v, want one durable in-progress session-stamped step", emitted)
	}

	spy.err = errors.New("stamp failed")
	emitted = nil
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("failed-stamp claim = %d; stderr=%s", code, stderr.String())
	}
	if len(emitted) != 0 {
		t.Fatalf("failed stamp emitted started event: %#v", emitted)
	}
}

// TestDoHookClaimAdoptionReconcilesDurableStartedFact covers the crash window
// between a durable claim-time identity stamp and event recording. A later hook
// tick adopts the same in-progress assignment, verifies its durable identity,
// and must re-emit the idempotent started fact rather than leave a permanent
// lifecycle gap.
func TestDoHookClaimAdoptionReconcilesDurableStartedFact(t *testing.T) {
	spy := &stampMetaSpy{}
	digest := beadBriefDigest("", "")
	meta := map[string]string{
		"gc.routed_to": "worker", beadmeta.RootBeadIDMetadataKey: "gcg-run",
		beadmeta.StepIDMetadataKey: "build", beadmeta.SessionIDMetadataKey: "mc-sess1",
		beadmeta.SessionNameMetadataKey:   "gc__role-mc-sess1",
		beadmeta.BriefDigestMetadataKey:   digest,
		beadmeta.ClaimSessionsMetadataKey: poolClaimSessionsRecord,
	}
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) {
			return fmt.Sprintf(`[{"id":"gcg-attempt","status":"in_progress","assignee":"gc__role-mc-sess1","metadata":{"gc.routed_to":"worker","gc.root_bead_id":"gcg-run","gc.step_id":"build","gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.brief_digest":%q,"gc.claim_sessions":%q}}]`, digest, poolClaimSessionsRecord), nil
		},
		ReadSessionBead: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
			return poolClaimSessionBead(id, ""), nil
		},
		ResolveWorkBranch: func(string) string { return "" },
		StampWorkMeta:     spy.fn,
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: meta}, nil
		},
		PublishRunMap: noopPublishRunMap,
	}
	var emitted []beads.Bead
	ops.EmitExecutionStepStarted = func(b beads.Bead, _ string, _ []string, _ string) { emitted = append(emitted, b) }

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d; stderr=%s", code, stderr.String())
	}
	if spy.calls != 0 {
		t.Fatalf("StampWorkMeta calls = %d, want 0 for an already durable identity", spy.calls)
	}
	if len(emitted) != 1 || emitted[0].ID != "gcg-attempt" || emitted[0].Metadata[beadmeta.SessionIDMetadataKey] != "mc-sess1" {
		t.Fatalf("started emission = %#v, want durable adopted step", emitted)
	}
}

// TestClaimReadsSessionBeadFromCityStoreNotWorkStore pins that a rig-scoped
// session's claim reads its OWN session bead from the city store, not from
// the rig store it claimed the work from.
//
// Modeled on astoria-sel4/lab.pm adopting as-wddx6 (ci-d9w7fl): the work bead
// lives in the rig store, the session bead only in the city store, and the
// read through the claim's own store answered `bead not found`. The stand-in
// reader answers ONLY for the city store's dir and env and refuses every other
// store the way the rig store did, so the stamped claimant record and worktree
// can only appear if the read went to the right store. Before the fix the
// patch carried gc.claim_sessions ["mc-sess1::"] and no gc.work_dir -- the
// shape measured on 58 of 58 claimant entries in the astoria-sel4 store.
func TestClaimReadsSessionBeadFromCityStoreNotWorkStore(t *testing.T) {
	const (
		rigDir  = "/rigs/astoria-sel4"
		cityDir = "/city"
	)
	spy := &stampMetaSpy{}
	reads := 0
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[{"id":"as-wddx6","status":"in_progress","assignee":"gc__role-mc-sess1","metadata":{"gc.routed_to":"worker"}}]`, nil
		},
		Claim: func(context.Context, string, []string, string, string) (beads.Bead, bool, error) {
			t.Error("Claim must not be called on the existing-assignment path")
			return beads.Bead{}, false, nil
		},
		ReadSessionBead: func(_ context.Context, dir string, env []string, id, _ string) (beads.Bead, error) {
			reads++
			if scope, _ := lookupEnvValue(env, "GC_STORE_SCOPE"); dir != cityDir || scope != "city" {
				return beads.Bead{}, fmt.Errorf("getting bead %q: %w", id, beads.ErrNotFound)
			}
			return poolClaimSessionBead(id, poolClaimWorkerDir), nil
		},
		ResolveWorkBranch: func(string) string { return "bd-as-wddx6" },
		StampWorkMeta:     spy.fn,
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee}, nil
		},
		PublishRunMap: noopPublishRunMap,
	}
	opts := poolClaimOpts()
	opts.Env = append(opts.Env, "GC_STORE_SCOPE=rig")
	opts.SessionStore = hookStore{dir: cityDir, env: []string{"GC_STORE_SCOPE=city"}}

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", rigDir, opts, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if reads == 0 {
		t.Fatal("claim never read the session bead, so the store it reads from is unpinned")
	}
	if got := spy.patch[beadmeta.WorkDirMetadataKey]; got != poolClaimWorkerDir {
		t.Errorf("gc.work_dir = %q, want %q; stderr=%s", got, poolClaimWorkerDir, stderr.String())
	}
	if got := spy.patch[beadmeta.ClaimSessionsMetadataKey]; got != poolClaimSessionsRecord {
		t.Errorf("gc.claim_sessions = %q, want %q", got, poolClaimSessionsRecord)
	}
	if strings.Contains(stderr.String(), "bead not found") {
		t.Errorf("claim reported the session bead missing: %s", stderr.String())
	}
}
