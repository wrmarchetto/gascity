package config

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

// This file pins the pool-alias demand tier: work hand-assigned to a pool's
// BARE name with `bd update --assignee <pool>` must be reachable by the pool's
// slot sessions.
//
// Why the suite is shaped this way. The bug it guards (ci-c000) is a silence --
// above max_active_sessions=1 every slot's GC_ALIAS is its own suffixed name, so
// the three identity tiers never probe the bare pool name, the bead is invisible,
// and the session drain-acks as if the queue were empty. There is no error to
// assert on, so the tests assert on the generated shell instead, and they assert
// the tier is present in BOTH the worker first-row form and the reconciler
// count-form: a fix in only the first leaves the reconciler blind to the demand
// and no slot is ever spawned to run the query (ci-rdbw), while a fix in only the
// second is the spawn-storm class of PR #1516.
//
// Run: go test ./internal/config/ -run PoolAlias

func TestPoolAliasDemandTierProbesTheBarePoolName(t *testing.T) {
	got := bdReadyPoolAliasDemandShell("--limit 0", false)
	if !strings.Contains(got, `--assignee="$target"`) {
		t.Errorf("bdReadyPoolAliasDemandShell() = %q, want an --assignee probe on the route positional", got)
	}
	// The route arrives as a positional to the outer sh -c, never interpolated,
	// so a pool name containing shell metacharacters stays data.
	if strings.Contains(got, `--unassigned`) {
		t.Errorf("bdReadyPoolAliasDemandShell() = %q, must not require --unassigned; the bead IS assigned", got)
	}
}

func TestPoolAliasDemandTierExcludesMailAddressedToThePool(t *testing.T) {
	got := bdReadyPoolAliasDemandShell("--limit 0", false)
	// A message bead carries its recipient in `assignee`, so `gc mail send
	// <pool>` is indistinguishable from pool-assigned work at the bd level. Left
	// in, it would raise demand no session can consume -- the session drains
	// without reading it, the message never changes, and the next tick spawns
	// another. That is #4419 at boot cadence, forever.
	if !strings.Contains(got, excludeMessageTypeArg) {
		t.Errorf("bdReadyPoolAliasDemandShell() = %q, missing %q", got, excludeMessageTypeArg)
	}
}

func TestPoolAliasDemandTierExcludesEpicsAndDispatchHolds(t *testing.T) {
	got := bdReadyPoolAliasDemandShell("--limit 0", false)
	if !strings.Contains(got, "--exclude-type=epic") {
		t.Errorf("bdReadyPoolAliasDemandShell() = %q, missing epic exclusion (gc-udx)", got)
	}
	for _, label := range beadmeta.DispatchHoldLabels {
		want := `--exclude-label "` + label + `"`
		if !strings.Contains(got, want) {
			t.Errorf("bdReadyPoolAliasDemandShell() = %q, missing %q; this tier creates spawn demand", got, want)
		}
	}
}

func TestWorkQueryProbesPoolAliasAssignedWorkBehindTheOriginGate(t *testing.T) {
	a := &Agent{Name: "worker", PoolName: "crew"}
	got := a.EffectiveWorkQuery()
	if !strings.Contains(got, `--assignee=\"$target\"`) && !strings.Contains(got, `--assignee="$target"`) {
		t.Fatalf("EffectiveWorkQuery() = %q, missing the pool-alias tier", got)
	}
	// The tier must sit inside probe_pool_demand, after the GC_SESSION_ORIGIN
	// gate: taking work addressed to the pool is pool behavior, so a named or
	// operator-attached session must not start claiming it. Placing it in the
	// identity loop instead would also hand the bare name to every slot as its
	// OWN identity, which is the ga-80pen8 adoption bug.
	gate := strings.Index(got, "GC_SESSION_ORIGIN")
	tier := strings.Index(got, `--assignee=`+`\"$target\"`)
	if tier < 0 {
		tier = strings.Index(got, `--assignee="$target"`)
	}
	if gate < 0 || tier < 0 || tier < gate {
		t.Errorf("EffectiveWorkQuery() = %q; pool-alias tier at %d must follow the GC_SESSION_ORIGIN gate at %d", got, tier, gate)
	}
}

func TestPoolDemandCountCountsPoolAliasAssignedWork(t *testing.T) {
	got := poolDemandCountShell("hello-world/worker", false)
	if !strings.Contains(got, `--assignee=`+`\"$target\"`) && !strings.Contains(got, `--assignee="$target"`) {
		t.Errorf("poolDemandCountShell() = %q, missing the pool-alias tier; the reconciler would never spawn a slot for hand-assigned pool work", got)
	}
}

func TestAssignedIdentityTiersStillDoNotProbeThePoolName(t *testing.T) {
	// The complement of the fix, and the ga-80pen8 invariant: the bare pool name
	// is ALSO a [[named_session]] holder's own identity. It must reach the slots
	// as route-scoped demand only, never as a fourth entry in the own-identity
	// loop, or a suffixed slot adopts the holder's live in_progress bead.
	for name, got := range map[string]string{
		"standardAssignedInProgressWorkQueryScript": standardAssignedInProgressWorkQueryScript(false),
		"standardAssignedReadyWorkQueryScript":      standardAssignedReadyWorkQueryScript(false),
	} {
		if strings.Contains(got, "$target") || strings.Contains(got, "GC_TEMPLATE") {
			t.Errorf("%s() = %q, own-identity tiers must not probe the pool route", name, got)
		}
	}
}
