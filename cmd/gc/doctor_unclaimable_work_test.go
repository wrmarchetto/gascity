package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// Scope: the unclaimable-work doctor check (cmd/gc/doctor_unclaimable_work.go).
// Every test here pins one admission or exclusion rule of the demand predicate,
// because each rule is the difference between a report an operator acts on and
// an alarm they turn off.
//
// Delegated elsewhere: which beads count as claimable Ready-tier work at all is
// classifyBacklog's contract, pinned by TestClassifyBacklog; the demand
// resolvers this check calls (controllerDemandRouteTarget,
// agentutil.NormalizePoolRouteTarget) are pinned by
// cmd/gc/build_desired_state_pool_alias_demand_test.go and the agentutil suite.
// These tests assert only the composition.
//
//	go test ./cmd/gc/ -run UnclaimableWork

// poolAgentCfg is a city with one pool agent capped at slots. The cap is
// load-bearing in several tests below: NormalizePoolRouteTarget refuses to
// collapse a slot index above it, which is what makes an assignment to a slot
// no cap allows report.
func poolAgentCfg(slots int) *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "demo"},
		Agents: []config.Agent{{
			Name:              "toolsmith",
			MaxActiveSessions: &slots,
		}},
	}
}

func unclaimableIDs(t *testing.T, cfg *config.City, population []beads.Bead, deps []beads.Dep) []string {
	t.Helper()
	store := beads.NewMemStoreFrom(0, population, deps)
	check := newUnclaimableWorkCheck(cfg, "/city", func(string) (beads.Store, error) { return store, nil })
	res := check.Run(&doctor.CheckContext{})
	if res.Status == doctor.StatusWarning {
		t.Fatalf("check could not answer: %s", res.Message)
	}
	var ids []string
	for _, d := range res.Details {
		// Details are "<id> <title> (<reason>)"; the ID is the first field.
		ids = append(ids, strings.Fields(d)[0])
	}
	return ids
}

func assertUnclaimable(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unclaimable = %v, want %v", got, want)
	}
}

// TestUnclaimableWorkReportsWorkThatNamesNoRoute pins the case ci-i19e was
// filed for: a filer who forgets to route or assign leaves a bead that raises
// demand for no pool, so no session is ever spawned to claim it (ci-mqqe
// measured 7h23m of exactly this with a ready P1 in the queue).
func TestUnclaimableWorkReportsWorkThatNamesNoRoute(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{ID: "W-1", Title: "forgotten flag", Type: "bug", Status: "open"},
	}, nil)
	assertUnclaimable(t, got, "W-1")
}

// TestUnclaimableWorkScansActiveRigStores pins the city-wide health boundary:
// ready work can be created in a rig's own bead store, so reading only the
// city store makes an unhealthy rig queue indistinguishable from an empty one.
func TestUnclaimableWorkScansActiveRigStores(t *testing.T) {
	cfg := poolAgentCfg(4)
	cfg.Agents[0].Dir = "rig-a"
	cfg.Rigs = []config.Rig{{Name: "rig-a", Path: "/rig-a"}}

	cityStore := beads.NewMemStore()
	rigStore := beads.NewMemStoreFrom(0, []beads.Bead{{
		ID:     "R-1",
		Title:  "forgotten rig route",
		Type:   "bug",
		Status: "open",
	}}, nil)
	check := newUnclaimableWorkCheck(cfg, "/city", func(path string) (beads.Store, error) {
		switch path {
		case "/city":
			return cityStore, nil
		case "/rig-a":
			return rigStore, nil
		default:
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError; message %q", res.Status, res.Message)
	}
	if len(res.Details) != 1 || !strings.Contains(res.Details[0], `rig "rig-a": R-1`) {
		t.Fatalf("Details = %q, want rig-qualified R-1 finding", res.Details)
	}
	if !strings.Contains(res.Message, "across the city store and 1 rig store") {
		t.Fatalf("Message = %q, want aggregate store scope", res.Message)
	}
}

func TestUnclaimableWorkIgnoresAddressedRigStoreWork(t *testing.T) {
	cfg := poolAgentCfg(4)
	cfg.Agents[0].Dir = "rig-a"
	cfg.Rigs = []config.Rig{{Name: "rig-a", Path: "/rig-a"}}

	cityStore := beads.NewMemStore()
	rigStore := beads.NewMemStoreFrom(0, []beads.Bead{{
		ID:       "R-1",
		Title:    "routed rig work",
		Type:     "bug",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "rig-a/toolsmith"},
	}}, nil)
	check := newUnclaimableWorkCheck(cfg, "/city", func(path string) (beads.Store, error) {
		if path == "/city" {
			return cityStore, nil
		}
		return rigStore, nil
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK; message %q", res.Status, res.Message)
	}
}

func TestUnclaimableWorkWarnsWhenActiveRigStoreIsUnreadable(t *testing.T) {
	cfg := poolAgentCfg(4)
	cfg.Agents[0].Dir = "rig-a"
	cfg.Rigs = []config.Rig{{Name: "rig-a", Path: "/rig-a"}}

	check := newUnclaimableWorkCheck(cfg, "/city", func(path string) (beads.Store, error) {
		if path == "/city" {
			return beads.NewMemStore(), nil
		}
		return nil, fmt.Errorf("dolt unreachable")
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; message %q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, `opening rig "rig-a" bead store: dolt unreachable`) {
		t.Fatalf("Message = %q, want rig-store failure", res.Message)
	}
}

func TestUnclaimableWorkSkipsSuspendedRigStores(t *testing.T) {
	cfg := poolAgentCfg(4)
	cfg.Rigs = []config.Rig{{Name: "rig-a", Path: "/rig-a", SuspendedOnStart: true}}

	check := newUnclaimableWorkCheck(cfg, "/city", func(path string) (beads.Store, error) {
		if path != "/city" {
			return nil, fmt.Errorf("suspended rig store %q must not be opened", path)
		}
		return beads.NewMemStore(), nil
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK; message %q", res.Status, res.Message)
	}
}

// TestUnclaimableWorkPassesRoutedWork pins the routed admission tier: a bead
// carrying gc.routed_to for a configured agent is pool-door demand, so the
// reconciler spawns for it and it must not be reported.
func TestUnclaimableWorkPassesRoutedWork(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{
			ID: "W-1", Title: "routed", Type: "bug", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "toolsmith"},
		},
	}, nil)
	assertUnclaimable(t, got)
}

// TestUnclaimableWorkDefersEveryAssignedShapeToTheAssigneeCheck pins the
// boundary with unclaimable-assignee (internal/doctor, ci-n785): a bead that
// carries an assignee is that check's to report, resolvable or not. Each row
// below is a shape it owns -- a bare pool name any slot may claim, a slot the
// cap allows, a slot the cap no longer allows, a name that never resolved, and
// a name the operator declared external in [doctor] external_assignees. Two
// checks reporting one bead is the second-mechanism failure ci-mqqe warned
// about, and duplicating the external-name row here would report the operator's
// own declaration back at them.
func TestUnclaimableWorkDefersEveryAssignedShapeToTheAssigneeCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		slots    int
		assignee string
	}{
		{"bare pool name", 4, "toolsmith"},
		{"slot within the cap", 4, "toolsmith-3"},
		{"slot the cap no longer allows", 2, "toolsmith-3"},
		{"name that never resolved", 4, "toolsmth"},
		{"declared external name", 4, "human"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unclaimableIDs(t, poolAgentCfg(tc.slots), []beads.Bead{
				{ID: "W-1", Title: tc.name, Type: "bug", Status: "open", Assignee: tc.assignee},
			}, nil)
			assertUnclaimable(t, got)
		})
	}
}

// TestUnclaimableWorkReportsWorkRoutedToASuspendedAgent pins the suspended case
// ci-i19e names explicitly. A suspended agent raises no demand, so a route to it
// strands the bead exactly as an absent route does -- and because the bead
// carries no assignee, no other check looks at it.
func TestUnclaimableWorkReportsWorkRoutedToASuspendedAgent(t *testing.T) {
	cfg := poolAgentCfg(4)
	cfg.Agents[0].Suspended = true
	got := unclaimableIDs(t, cfg, []beads.Bead{
		{
			ID: "W-1", Title: "routed to a suspended pool", Type: "bug", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "toolsmith"},
		},
	}, nil)
	assertUnclaimable(t, got, "W-1")
}

// TestUnclaimableWorkPassesUnassignedWorkOnAHold pins the one park this check
// still owns. A hold label on an UNADDRESSED bead is the sanctioned way to say
// it waits on an actor rather than on a routing decision, and no assignee makes
// it unclaimable-assignee's, so without this exclusion every held bead reports.
func TestUnclaimableWorkPassesUnassignedWorkOnAHold(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{
			ID: "W-1", Title: "waiting on the mayor", Type: "bug", Status: "open",
			Labels: []string{beadmeta.HoldMayorLabel},
		},
		{
			ID: "W-2", Title: "waiting on a vendor", Type: "bug", Status: "open",
			Labels: []string{beadmeta.HoldExternalLabel},
		},
	}, nil)
	assertUnclaimable(t, got)
}

// TestUnclaimableWorkPassesNonClaimableBacklog pins that the check inherits
// classifyBacklog's population rather than re-deriving it: session beads, nudge
// and mail chores, epics and dep-blocked work are not claimable work, so none
// of them can be stranded work. Without this the report would be dominated by
// the control plane, which is the false alarm gastownhall/gascity#3021 records.
func TestUnclaimableWorkPassesNonClaimableBacklog(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{ID: "S-1", Title: "toolsmith-1", Type: "session", Status: "open"},
		{ID: "N-1", Title: "nudge:abc", Type: "chore", Status: "open"},
		{ID: "E-1", Title: "EPIC: rollout", Type: "epic", Status: "open"},
		{ID: "R-1", Title: "the blocker", Type: "bug", Status: "open", Assignee: "toolsmith"},
		{ID: "B-1", Title: "blocked behind R-1", Type: "bug", Status: "open"},
	}, []beads.Dep{{IssueID: "B-1", DependsOnID: "R-1", Type: "blocks"}})
	assertUnclaimable(t, got)
}

// TestUnclaimableWorkNamesTheRemedyItCannotChoose pins that the check reports
// without routing. Which pool an unrouted bead belongs to is a judgment call
// that must stay out of Go (AGENTS.md), so the result carries the two remedies
// and no choice between them.
func TestUnclaimableWorkNamesTheRemedyItCannotChoose(t *testing.T) {
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "W-1", Title: "forgotten flag", Type: "bug", Status: "open"},
	}, nil)
	check := newUnclaimableWorkCheck(poolAgentCfg(4), "/city", func(string) (beads.Store, error) { return store, nil })
	res := check.Run(&doctor.CheckContext{})

	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError", res.Status)
	}
	if res.Severity != doctor.SeverityAdvisory {
		t.Fatalf("Severity = %v, want SeverityAdvisory: a routing gap is the mayor's to close, not a reason to fail a gate", res.Severity)
	}
	if check.CanFix() {
		t.Fatal("CanFix() = true, want false: choosing the pool is the judgment this check must not make")
	}
	for _, want := range []string{"--assignee", "gc.routed_to"} {
		if !strings.Contains(res.FixHint, want) {
			t.Errorf("FixHint %q does not name %q", res.FixHint, want)
		}
	}
}

// TestUnclaimableWorkSummaryNamesBeadsAndStatesItsOverflow pins that the
// one-line summary is actionable on its own. Details is shown only under
// --verbose and the warm-up mailer drops it, so a bare count reaches the reader
// as something they cannot act on. The bound is asserted at a population LARGER
// than it, because a summary that happened to fit would pass either way.
func TestUnclaimableWorkSummaryNamesBeadsAndStatesItsOverflow(t *testing.T) {
	var population []beads.Bead
	for _, id := range []string{"W-1", "W-2", "W-3", "W-4", "W-5", "W-6", "W-7"} {
		population = append(population, beads.Bead{ID: id, Title: "unrouted " + id, Type: "bug", Status: "open"})
	}
	store := beads.NewMemStoreFrom(0, population, nil)
	check := newUnclaimableWorkCheck(poolAgentCfg(4), "/city", func(string) (beads.Store, error) { return store, nil })
	res := check.Run(&doctor.CheckContext{})

	if len(res.Details) != len(population) {
		t.Fatalf("Details carries %d lines, want %d: the full list must survive somewhere", len(res.Details), len(population))
	}
	for _, want := range []string{"W-1", "W-5", "(+2 more)"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("Message %q does not contain %q", res.Message, want)
		}
	}
	// W-6 and W-7 are past the bound: naming them would mean the bound is not
	// applied, and omitting them WITHOUT the overflow count asserted above would
	// be a silent truncation.
	if strings.Contains(res.Message, "W-6") {
		t.Errorf("Message %q names a bead past the summary bound", res.Message)
	}
}

// unclaimableWorkFailingStore fails one read and REFUSES every other call. The
// embedded nil beads.Store is what does the refusing: an unscripted method
// panics rather than answering, so a branch that later reaches for a query
// nobody scripted cannot be handed a silent zero value and pass.
type unclaimableWorkFailingStore struct {
	beads.Store
	listOpenErr error
	readyErr    error
}

func (s unclaimableWorkFailingStore) ListOpen(...string) ([]beads.Bead, error) {
	return nil, s.listOpenErr
}

func (s unclaimableWorkFailingStore) Ready(...beads.ReadyQuery) ([]beads.Bead, error) {
	return nil, s.readyErr
}

// TestUnclaimableWorkReportsAnUnanswerableStoreAsUnknown pins the one status
// this check must never reach by accident, across every path that ends without
// an answer.
//
// Each row is pinned separately rather than through the single branch that is
// easiest to reach, because the tempting zero value on any of them -- an empty
// bead slice -- renders as "every one of 0 claimable bead(s) is addressed": a
// StatusOK indistinguishable from a healthy city. Reporting a store it could
// not read as a store with nothing stranded is precisely the silence this check
// exists to remove, so the check may not be able to regress into it down any
// path. The message assertion is part of the invariant, not decoration -- a
// warning that does not name which read failed leaves an operator no way to
// tell a stopped Dolt server from a broken query.
func TestUnclaimableWorkReportsAnUnanswerableStoreAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		newStore func(string) (beads.Store, error)
		want     string
	}{
		{
			name:     "no store configured",
			newStore: nil,
			want:     "no city bead store configured",
		},
		{
			name:     "store will not open",
			newStore: func(string) (beads.Store, error) { return nil, fmt.Errorf("dolt unreachable") },
			want:     "opening city bead store: dolt unreachable",
		},
		{
			name: "open beads unreadable",
			newStore: func(string) (beads.Store, error) {
				return unclaimableWorkFailingStore{listOpenErr: fmt.Errorf("list failed")}, nil
			},
			want: "listing open beads: list failed",
		},
		{
			name: "ready projection unreadable",
			newStore: func(string) (beads.Store, error) {
				return unclaimableWorkFailingStore{readyErr: fmt.Errorf("ready unavailable")}, nil
			},
			want: "listing ready beads: ready unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := newUnclaimableWorkCheck(poolAgentCfg(4), "/city", tc.newStore).Run(&doctor.CheckContext{})
			if res.Status != doctor.StatusWarning {
				t.Fatalf("Status = %v, want StatusWarning; message %q", res.Status, res.Message)
			}
			if !strings.Contains(res.Message, tc.want) {
				t.Fatalf("Message = %q does not name %q", res.Message, tc.want)
			}
		})
	}
}
