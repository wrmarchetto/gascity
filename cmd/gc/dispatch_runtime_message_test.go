package main

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// Scope: the shell-generated control-ready query
// (workflowServeControlReadyQueryForBeads) must exclude mail from its
// assignee-scoped tier and must NOT carry the flag on its route-scoped tier.
// Behavior of the Go evaluation path that actually runs in production is
// delegated to dispatch_control_ready_message_test.go.
//
// Why the suite exists, and its honest standing: this shell is unreachable.
// TestWorkflowServeControlReadyQueryShellFallbackUnreachable proves
// tryControlReadyFromCacheOrFallback always intercepts a control-ready-shaped
// query, so these assertions are defense-in-depth on dead code, exactly like
// the hold-label pair beside them in dispatch_runtime_hold_label_test.go. They
// earn their place by keeping the two copies of this predicate from drifting:
// ci-bhvf is that drift -- 0de389a72 taught the worker's assigned tiers to
// exclude mail and left this one alone -- and both sides now anchor on the same
// config.ExcludeMessageTypeArg literal so an edit to one reaches the other.
//
//	go test ./cmd/gc/ -run 'TestWorkflowServeControlReadyQuery.*Message'

func TestWorkflowServeControlReadyQueryAssigneeReadyExcludesMessages(t *testing.T) {
	query := workflowServeControlReadyQuery(config.Agent{Name: config.ControlDispatcherAgentName, Dir: "gascity"})
	body, ok := controlReadyAssigneeReadyBody(query)
	if !ok {
		t.Fatalf("workflowServeControlReadyQuery() has no locatable assignee_ready() body: %s", query)
	}
	if !strings.Contains(body, config.ExcludeMessageTypeArg) {
		t.Errorf("assignee_ready() body = %q, want it to contain %q (mail carries its recipient in assignee, so this tier is where it enters)", body, config.ExcludeMessageTypeArg)
	}
}

// TestWorkflowServeControlReadyQueryRoutedReadyDoesNotExcludeMessages pins the
// absence on the route-scoped tier, matching internal/config's
// bdReadyPoolDemandShell: routed_ready() passes --unassigned and mail always
// carries its recipient in assignee, so the flag would filter nothing and
// would imply a vector that does not exist.
func TestWorkflowServeControlReadyQueryRoutedReadyDoesNotExcludeMessages(t *testing.T) {
	query := workflowServeControlReadyQuery(config.Agent{Name: config.ControlDispatcherAgentName, Dir: "gascity"})
	assigneeBody, ok := controlReadyAssigneeReadyBody(query)
	if !ok {
		t.Fatalf("workflowServeControlReadyQuery() has no locatable assignee_ready() body: %s", query)
	}
	// Everything outside assignee_ready() is the routed tier plus the driver
	// loop; scoping by subtraction keeps this from breaking on the exact
	// routed_ready() body delimiters.
	rest := strings.Replace(query, assigneeBody, "", 1)
	if strings.Contains(rest, config.ExcludeMessageTypeArg) {
		t.Errorf("control-ready query outside assignee_ready() contains %q, but routed_ready() is --unassigned and mail always has an assignee: %s", config.ExcludeMessageTypeArg, query)
	}
}

// TestAssignedTierMessageExclusionIsSharedByBothDispatchPaths is ci-bhvf's
// invariant stated directly: the worker work_query and the control-dispatcher
// readiness scan are two independent copies of the same assignee-scoped
// predicate, and a fix applied to one is not a fix. It fails if either path
// drops the exclusion, which is the failure 0de389a72 shipped.
func TestAssignedTierMessageExclusionIsSharedByBothDispatchPaths(t *testing.T) {
	workerCfg := config.Agent{Name: "toolsmith", Dir: "gascity"}
	worker := workerCfg.EffectiveWorkQuery()
	if !strings.Contains(worker, config.ExcludeMessageTypeArg) {
		t.Errorf("EffectiveWorkQuery() is missing %q: %s", config.ExcludeMessageTypeArg, worker)
	}
	control := workflowServeControlReadyQuery(config.Agent{Name: config.ControlDispatcherAgentName, Dir: "gascity"})
	if !strings.Contains(control, config.ExcludeMessageTypeArg) {
		t.Errorf("workflowServeControlReadyQuery() is missing %q: %s", config.ExcludeMessageTypeArg, control)
	}
}

// controlReadyAssigneeReadyBody returns the generated assignee_ready() shell
// function body. Shared by both tests above so they agree on what "the
// assignee tier" means, and separate from the hold-label file's inline
// version because that one asserts on a body it locates for a different flag.
func controlReadyAssigneeReadyBody(query string) (string, bool) {
	start := strings.Index(query, `assignee_ready() { `)
	if start < 0 {
		return "", false
	}
	relEnd := strings.Index(query[start:], `; }; `)
	if relEnd < 0 {
		return "", false
	}
	return query[start : start+relEnd], true
}
