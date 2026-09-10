package main

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: the claim-time half of the brief-redirect gate -- whether
// hookClaimIdentityPatch arms the gate, and (the load-bearing one) whether it
// leaves the digest alone afterwards. The gate's close-time half is pinned by
// brief_redirect_gate_test.go; this suite exists separately because the
// failure it catches is invisible from the close side. A digest quietly
// refreshed on the hook tick that follows the mayor's edit makes every
// close-time assertion pass while the gate never fires in production.
//
// Run: go test ./cmd/gc/ -run HookClaimBriefDigest

// hookClaimBriefDigestPatch runs the identity patch for a session claim of
// bead, with a work branch resolver that returns a fixed branch.
func hookClaimBriefDigestPatch(bead beads.Bead) map[string]string {
	opts := hookClaimOptions{
		Assignee: "toolsmith-1",
		Env:      []string{"GC_SESSION_ID=ci-sess", "GC_SESSION_NAME=toolsmith-ci-sess"},
	}
	ops := hookClaimOps{ResolveWorkBranch: func(string) string { return "fix/ci-brief" }}
	return hookClaimIdentityPatch(bead, opts, ops, "/tmp/tree")
}

func TestHookClaimBriefDigestStampsAnUnstampedBead(t *testing.T) {
	patch := hookClaimBriefDigestPatch(beads.Bead{
		ID: "ci-brief", Type: "task", Title: "do the thing", Description: "as specified",
	})
	want := beadBriefDigest("do the thing", "as specified")
	if got := patch[beadmeta.BriefDigestMetadataKey]; got != want {
		t.Fatalf("claim stamped %s=%q, want %q -- an unstamped bead leaves the close gate disarmed", beadmeta.BriefDigestMetadataKey, got, want)
	}
}

func TestHookClaimBriefDigestSurvivesAReclaimAfterTheBriefChanged(t *testing.T) {
	// The measured shape: the bead was claimed, the mayor rewrote the
	// description, and the session kept ticking. Every later tick re-enters
	// this patch with the NEW description in hand. Re-stamping here would
	// erase the only record that the brief moved.
	stale := beadBriefDigest("do the thing", "as specified")
	patch := hookClaimBriefDigestPatch(beads.Bead{
		ID: "ci-brief", Type: "task",
		Title:       "do the thing",
		Description: "as specified\n\nREDIRECT: superseded, do NOT build this",
		Metadata:    map[string]string{beadmeta.BriefDigestMetadataKey: stale},
	})
	if got, ok := patch[beadmeta.BriefDigestMetadataKey]; ok {
		t.Fatalf("re-claim rewrote %s to %q; the stamp must be write-once or the gate never fires", beadmeta.BriefDigestMetadataKey, got)
	}
}

func TestHookClaimBriefDigestSkipsEveryControlKind(t *testing.T) {
	// Control beads are closed by the dispatch engine, not by a worker reading
	// a brief, and graphroute deliberately leaves them session-free. Stamping
	// one would gate a close no human instruction ever reached.
	//
	// The set is iterated from beadmeta.ControlKinds rather than sampled: a
	// kind added there later must be excluded here too, and a spot-check of
	// one member cannot notice that it was not.
	for _, kind := range beadmeta.ControlKinds {
		patch := hookClaimBriefDigestPatch(beads.Bead{
			ID: "ci-ctl", Type: "task", Title: "t", Description: "d",
			Metadata: map[string]string{beadmeta.KindMetadataKey: kind},
		})
		if got, ok := patch[beadmeta.BriefDigestMetadataKey]; ok {
			t.Fatalf("control bead of kind %q was stamped %s=%q", kind, beadmeta.BriefDigestMetadataKey, got)
		}
	}
}

func TestHookClaimBriefDigestStampsANonControlGraphStep(t *testing.T) {
	// DELIBERATELY IN SCOPE, and the reason is the mirror of the exclusion
	// above: a graph step carries a gc.kind but is still read and executed by
	// a worker, so its description is instructions and can be superseded the
	// same way. The exclusion is ControlKinds membership, not the mere
	// presence of gc.kind -- the broader "any gc.kind" test the sibling
	// upstream-probe gate uses would drop every graph step out of this gate.
	patch := hookClaimBriefDigestPatch(beads.Bead{
		ID: "ci-step", Type: "task", Title: "t", Description: "d",
		Metadata: map[string]string{beadmeta.KindMetadataKey: "step"},
	})
	if patch[beadmeta.BriefDigestMetadataKey] == "" {
		t.Fatal("a worker-executed graph step was left unstamped, so its brief can be rewritten unnoticed")
	}
}

func TestHookClaimBriefDigestSkipsASessionlessClaim(t *testing.T) {
	// No GC_SESSION_ID means no session read the brief, so there is nothing to
	// hold that reader to. Stamping here would gate a close on behalf of a
	// reader that never existed.
	ops := hookClaimOps{ResolveWorkBranch: func(string) string { return "" }}
	patch := hookClaimIdentityPatch(
		beads.Bead{ID: "ci-brief", Type: "task", Title: "t", Description: "d"},
		hookClaimOptions{Assignee: "toolsmith-1"}, ops, "/tmp/tree")
	if got, ok := patch[beadmeta.BriefDigestMetadataKey]; ok {
		t.Fatalf("sessionless claim was stamped %s=%q", beadmeta.BriefDigestMetadataKey, got)
	}
}

func TestHookClaimBriefDigestCostsExactlyOneExtraWritePerBead(t *testing.T) {
	// The comment on stampHookClaimIdentity warns that an unconditional write
	// here emits a bead.updated per tick per in-progress bead -- the
	// cache-reconcile flood class. Adding a key to that patch reopens the
	// question, so bound it: a bead already carrying current branch and
	// session identity but no digest is written ONCE, and the tick after that
	// writes nothing. One extra event per bead, at the moment this ships, and
	// never again.
	digest := beadBriefDigest("", "")
	base := map[string]string{
		"gc.routed_to":              "worker",
		"gc.work_branch":            "bd-hw-once",
		beadmeta.WorkDirMetadataKey: poolClaimWorkerDir,
		"gc.session_id":             "mc-sess1",
		"gc.session_name":           "gc__role-mc-sess1",
	}
	readyNoDigest := fmt.Sprintf(`[{"id":"hw-once","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-hw-once","gc.work_dir":%q,"gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1"}}]`, poolClaimWorkerDir)

	first := &stampMetaSpy{}
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(),
		poolClaimOps(readyNoDigest, base, "bd-hw-once", first), &stdout, &stderr); code != 0 {
		t.Fatalf("first claim = %d; stderr=%s", code, stderr.String())
	}
	want := map[string]string{beadmeta.BriefDigestMetadataKey: digest}
	if first.calls != 1 || !reflect.DeepEqual(first.patch, want) {
		t.Fatalf("first tick = {calls:%d patch:%v}, want {1 %v} (only the missing digest)", first.calls, first.patch, want)
	}

	stamped := map[string]string{beadmeta.BriefDigestMetadataKey: digest}
	for k, v := range base {
		stamped[k] = v
	}
	second := &stampMetaSpy{}
	stdout.Reset()
	stderr.Reset()
	readyStamped := fmt.Sprintf(`[{"id":"hw-once","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-hw-once","gc.work_dir":%q,"gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.brief_digest":%q}}]`, poolClaimWorkerDir, digest)
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(),
		poolClaimOps(readyStamped, stamped, "bd-hw-once", second), &stdout, &stderr); code != 0 {
		t.Fatalf("second claim = %d; stderr=%s", code, stderr.String())
	}
	if second.calls != 0 {
		t.Fatalf("second tick wrote %d times with patch %v, want 0 -- the stamp must not re-fire per tick", second.calls, second.patch)
	}
}
