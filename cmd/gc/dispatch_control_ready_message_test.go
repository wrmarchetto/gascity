package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Scope: mail must never enter the control dispatcher's ready queue
// (ci-bhvf). This is the control-dispatch counterpart of the assigned-tier
// exclusion internal/config/workquery_message_displacement_test.go pins for
// the worker path; the worker's own tiers are that file's business, not this
// one's.
//
// Why the suite exists: a message bead carries its recipient in `assignee`,
// the identical shape a control bead assigned to the dispatcher has, and
// beadsToHookBeads drops Type on the way out of the scan -- so nothing
// downstream can tell the two apart. drainWorkflowServeWork hands whatever
// the scan returns to ProcessControl, which has no case for a message and
// answers `unsupported control bead kind ""`;
// runControlDispatcherWithStoreAndConfig then quarantines the bead -- closed,
// labeled gc:control-quarantined, stamped gc.outcome=fail -- and returns nil,
// which the drain loop counts as progress. The message is destroyed unread and
// the dispatcher reports success, so nothing downstream will ever raise this.
// The scan is the last layer that can hold the rule.
//
// Reachability, measured 2026-08-10 against bd 1.1.1 and recorded because it
// bounds every claim below: no arm of this scan can surface mail today.
// CachedReady filters through beads.IsReadyCandidate, whose readyExcludeTypes
// list already contains "message"; every Store.Ready implementation applies
// the same predicate via IsReadyCandidateForTier; and `bd ready
// --include-ephemeral` returned 0 of the 5 open ephemeral message beads then
// in the city store. The unit tests below feed the filters directly and the
// fallback test drives a fake bd that DOES emit a message row, which is the
// only condition under which this guard does any work -- so they exercise it
// rather than confirming bd's behavior a second time.
//
//	go test ./cmd/gc/ -run 'Mail|ReadyExcludedMessageType'

func TestFilterReadyByAssigneeExcludesMailMessages(t *testing.T) {
	// "message" is spelled literally, not taken from a constant the
	// implementation also reads: a test that recomputes its expectation from
	// the production constant cannot catch that constant being dropped.
	ready := []beads.Bead{
		{ID: "ga-mail", Assignee: "cand", Type: "message"},
		{ID: "ga-control", Assignee: "cand", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindDrain}},
	}
	got := filterReadyByAssignee(ready, "cand", workflowServeScanLimit)
	want := []string{"ga-control"}
	if !stringSlicesEqual(beadIDs(got), want) {
		t.Fatalf("filterReadyByAssignee ids = %v, want %v (mail addressed to the dispatcher identity must not enter the control queue)", beadIDs(got), want)
	}
}

// TestFilterReadyByRouteDropsMailByRequiringAnEmptyAssignee pins the ABSENCE
// of a message exclusion on the route-scoped tier. filterReadyByRoute already
// requires an empty assignee and mail always carries its recipient there, so
// the exclusion would be unreachable code. Adding one would create a branch
// no test could honestly exercise; this test is what keeps the absence safe,
// by failing if the tier ever becomes assignee-transparent.
//
// The other half of the absence has no test because it has no producer: an
// UNASSIGNED message bead carrying gc.run_target or gc.routed_to would reach
// this tier, but beadmail always sets Assignee and never stamps a route.
func TestFilterReadyByRouteDropsMailByRequiringAnEmptyAssignee(t *testing.T) {
	const route = "gascity/control-dispatcher"
	ready := []beads.Bead{
		{ID: "ga-mail-routed", Assignee: "gascity--control-dispatcher", Type: "message", Metadata: map[string]string{beadmeta.RunTargetMetadataKey: route}},
	}
	if got := filterReadyByRoute(ready, beadmeta.RunTargetMetadataKey, route); len(got) != 0 {
		t.Fatalf("filterReadyByRoute ids = %v, want none (an assigned bead must not pass an unassigned tier)", beadIDs(got))
	}
}

func TestEvaluateControlReadyExcludesMailMessages(t *testing.T) {
	parsed := parsedControlReadyQuery{
		target:             "gascity/control-dispatcher",
		controlSessionName: "gascity--control-dispatcher",
	}
	envList := []string{
		"GC_SESSION_NAME=gascity--control-dispatcher",
		"GC_ALIAS=gascity/control-dispatcher",
	}
	// One message per assignee candidate the scan checks, so a fix that
	// covered only the session-name loop and not GC_CONTROL_TARGET still
	// fails.
	ready := []beads.Bead{
		{ID: "ga-mail-session", Assignee: "gascity--control-dispatcher", Type: "message"},
		{ID: "ga-mail-alias", Assignee: "gascity/control-dispatcher", Type: "message"},
		{ID: "ga-control", Assignee: "gascity--control-dispatcher", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindFanout}},
	}
	got := evaluateControlReady(ready, parsed, envList)
	want := []string{"ga-control"}
	if !stringSlicesEqual(beadIDs(got), want) {
		t.Fatalf("evaluateControlReady ids = %v, want %v (mail reaching ProcessControl is quarantined: closed, gc:control-quarantined, gc.outcome=fail)", beadIDs(got), want)
	}
}

// TestControlReadyCachePathLeansOnReadyExcludedMessageType makes the
// cross-package dependency that protects the cache arm fail loudly instead of
// silently.
//
// tryControlReadyFromCacheOrFallback's first arm serves from
// CachingStore.CachedReady, which filters through beads.IsReadyCandidate ->
// IsReadyExcludedBead, so a message bead is dropped before
// filterReadyByAssignee ever sees it. That makes an end-to-end cache-path test
// of the exclusion VACUOUS -- it would pass with the exclusion deleted. This
// assertion is the honest form: if "message" leaves beads.readyExcludeTypes,
// this test fails and names filterReadyByAssignee as the guard that has just
// become load-bearing.
func TestControlReadyCachePathLeansOnReadyExcludedMessageType(t *testing.T) {
	if !beads.IsReadyExcludedType("message") {
		t.Fatal("beads.IsReadyExcludedType(\"message\") = false: the control-ready cache arm no longer drops mail before filterReadyByAssignee, which is now the only guard on that arm -- add an end-to-end cache-path test for it")
	}
}

// TestTryControlReadyFromCacheOrFallbackExcludesMailOnFallbackPath is the one
// end-to-end test here that is not vacuous. The fake bd emits a message-typed
// row from `bd ready`, standing in for the only world where this exclusion
// matters: a bd whose ready no longer filters mail out on its own. The type is
// spelled as literal `issue_type` JSON on purpose -- that is the wire format bd
// emits, so substituting a Go constant would defeat the test.
func TestTryControlReadyFromCacheOrFallbackExcludesMailOnFallbackPath(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"test-city\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}

	tmp := t.TempDir()
	bdPath := filepath.Join(tmp, "bd")
	// `list) exit 7` fails CachingStore.PrimeActive so the cache arm declines
	// and the batched fallback runs; without it this test would silently
	// exercise the cache arm instead.
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
  list)
    exit 7
    ;;
esac
printf '[{"id":"ga-fallback-mail","issue_type":"message","assignee":"%s"},{"id":"ga-fallback-control","assignee":"%s","metadata":{"gc.kind":"drain"}}]'
`, "gascity--control-dispatcher", "gascity--control-dispatcher")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_SESSION_NAME", "gascity--control-dispatcher")

	agentCfg := config.Agent{Name: config.ControlDispatcherAgentName, Dir: "gascity"}
	queue, handled, err := tryControlReadyFromCacheOrFallback(
		workflowServeControlReadyQuery(agentCfg), cityDir, nil)
	if err != nil {
		t.Fatalf("tryControlReadyFromCacheOrFallback: %v", err)
	}
	if !handled {
		t.Fatalf("handled = false, want true")
	}
	var gotIDs []string
	for _, b := range queue {
		gotIDs = append(gotIDs, b.ID)
	}
	wantIDs := []string{"ga-fallback-control"}
	if !stringSlicesEqual(gotIDs, wantIDs) {
		t.Fatalf("queue ids = %#v, want %#v (ga-fallback-mail would reach ProcessControl as an unsupported control kind and be quarantined -- closed unread, stamped gc.outcome=fail)", gotIDs, wantIDs)
	}
}
