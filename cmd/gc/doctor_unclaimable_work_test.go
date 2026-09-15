package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestUnclaimableWorkHonorsAnExplicitDoorlessMarker pins the short authoring
// window in which a bead must exist before its procedure artifact can name the
// bead and before it can safely be routed. The explicit label is the author's
// declaration that the otherwise-unaddressed state is intentional; removing it
// must make the same ready work report again.
func TestUnclaimableWorkHonorsAnExplicitDoorlessMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
		want   []string
	}{
		{
			name:   "deliberately doorless authoring bead",
			labels: []string{"gc:deliberately-doorless"},
		},
		{
			name: "marker removed exposes stranded work",
			want: []string{"W-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{{
				ID:     "W-1",
				Title:  "procedure artifact placeholder",
				Type:   "task",
				Status: "open",
				Labels: tc.labels,
			}}, nil)
			assertUnclaimable(t, got, tc.want...)
		})
	}
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

// TestUnclaimableWorkPassesExtmsgFabricRows reproduces the three rows a Slack
// adapter left behind on 2026-09-08 (ci-fdr7cf) and requires the check to pass
// over them while still reporting a real doorless bead standing beside them.
//
// The fixture is the observed shape, not a reduced one: type "task", no
// assignee, no route, no description, titled the way the fabric titles its
// rows. That is indistinguishable from work nobody picked up on every field
// the check reads, which is why the check was right to fire and why the fix
// had to be the ready-exclusion label rather than anything here.
//
// W-1 is the half that makes this a test rather than an assertion that the
// check reports nothing: an exclusion wide enough to swallow it would pass an
// extmsg-only fixture.
func TestUnclaimableWorkPassesExtmsgFabricRows(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{
			ID: "X-1", Title: "slack/default/C0C0JPH5E2Y", Type: "task", Status: "open",
			Labels: []string{"gc:extmsg-binding"},
		},
		{
			ID: "X-2", Title: "mayor -> slack/default/C0C0JPH5E2Y", Type: "task", Status: "open",
			Labels: []string{"gc:extmsg-membership"},
		},
		{
			ID: "X-3", Title: "slack/default/C0C0JPH5E2Y/state", Type: "task", Status: "open",
			Labels: []string{"gc:extmsg-transcript-state"},
		},
		{ID: "W-1", Title: "forgotten route", Type: "task", Status: "open"},
	}, nil)
	assertUnclaimable(t, got, "W-1")
}

// TestUnclaimableWorkExcludesTopologyWithoutHidingTasks pins the workflow
// topology boundary: generated specs and machine-closed gates carry structure,
// not work an agent may claim. An ordinary ready task alongside them must still
// report when it has no pool door.
func TestUnclaimableWorkExcludesTopologyWithoutHidingTasks(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{
			ID: "S-1", Title: "Step spec for review", Type: "spec", Status: "open",
			Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindSpec},
		},
		{
			ID: "G-1", Title: "machine gate", Type: "task", Status: "open",
			Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindGate},
		},
		{ID: "W-1", Title: "forgotten route", Type: "task", Status: "open"},
	}, nil)
	assertUnclaimable(t, got, "W-1")
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

// TestUnclaimableWorkIsNotSwampedByInboundChatTranscripts pins the composition:
// the extmsg exclusion has to survive all the way to the reported set, not just
// to classifyBacklog. A transcript bead has no assignee and no route, which is
// exactly the shape this check reports, so nothing but the work predicate keeps
// it out -- and on 2026-09-08 nothing did: 101 of the 108 rows it named were
// inbound Slack messages and the three real findings were unreadable under them
// (ci-3bktll).
//
// The real row is here for the same reason: an exclusion that also hid W-1
// would turn a swamped instrument into a silent one, which is the worse of the
// two failures.
func TestUnclaimableWorkIsNotSwampedByInboundChatTranscripts(t *testing.T) {
	got := unclaimableIDs(t, poolAgentCfg(4), []beads.Bead{
		{
			ID: "T-1", Title: "slack/default/C0C0JPH5E2Y#3", Type: "task", Status: "open",
			Labels: []string{"gc:extmsg-transcript"},
		},
		{
			ID: "T-2", Title: "slack/default/C0C0JPH5E2Y/state", Type: "task", Status: "open",
			Labels: []string{"gc:extmsg-transcript-state"},
		},
		{ID: "W-1", Title: "forgotten route", Type: "task", Status: "open"},
	}, nil)
	assertUnclaimable(t, got, "W-1")
}

// --- [doctor] unaddressed_grace ---

// graceCfg is a city whose operator has declared that an address can arrive
// asynchronously within grace. The pool agent is poolAgentCfg's so the
// admission tiers under test are the same ones every other test here uses.
func graceCfg(grace string) *config.City {
	cfg := poolAgentCfg(4)
	cfg.Doctor.UnaddressedGrace = grace
	return cfg
}

// unclaimableResultAt runs the check against a fixed wall clock, which is what
// makes an age assertion observable at all: a test that let the check read the
// real clock would be asserting on a window it cannot place a bead inside.
func unclaimableResultAt(cfg *config.City, now time.Time, population []beads.Bead) *doctor.CheckResult {
	store := beads.NewMemStoreFrom(0, population, nil)
	check := newUnclaimableWorkCheck(cfg, "/city", func(string) (beads.Store, error) { return store, nil })
	check.now = func() time.Time { return now }
	return check.Run(&doctor.CheckContext{})
}

func unclaimableDetailIDs(res *doctor.CheckResult) []string {
	var ids []string
	for _, d := range res.Details {
		ids = append(ids, strings.Fields(d)[0])
	}
	return ids
}

// TestUnclaimableWorkGraceWithholdsOnlyBeadsInsideTheDeclaredWindow pins the
// boundary of the operator's declaration in both directions from one
// population, because a grace asserted only on the young bead passes equally
// for a grace that suppresses everything.
//
// The window is the whole point: this check's predicate is a permanence claim
// ("nothing will EVER spawn a session to claim"), and a point sample cannot
// distinguish unaddressed-forever from unaddressed-for-four-seconds. A city
// whose routing arrives from a cooldown order -- this one stamps gc.routed_to
// from a label every 2m -- produces the second shape on every mint.
func TestUnclaimableWorkGraceWithholdsOnlyBeadsInsideTheDeclaredWindow(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	res := unclaimableResultAt(graceCfg("5m"), now, []beads.Bead{
		{
			ID: "W-young", Title: "minted a minute ago", Type: "task", Status: "open",
			CreatedAt: now.Add(-1 * time.Minute),
		},
		{
			ID: "W-old", Title: "unaddressed for an hour", Type: "task", Status: "open",
			CreatedAt: now.Add(-1 * time.Hour),
		},
	})

	if got := unclaimableDetailIDs(res); strings.Join(got, ",") != "W-old" {
		t.Fatalf("reported %v, want only W-old", got)
	}
}

// TestUnclaimableWorkGraceCountsAnUndatedBeadAsOld pins the direction the
// unknown answer falls in. A bead whose store did not report a creation time
// has no measurable age, and treating that as "just created" would withhold
// every finding from any store that stopped populating the column -- the check
// would go quiet and read as healthy. Reporting it is the recoverable error.
func TestUnclaimableWorkGraceCountsAnUndatedBeadAsOld(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	res := unclaimableResultAt(graceCfg("5m"), now, []beads.Bead{
		{ID: "W-undated", Title: "no creation time", Type: "task", Status: "open"},
	})

	if got := unclaimableDetailIDs(res); strings.Join(got, ",") != "W-undated" {
		t.Fatalf("reported %v, want W-undated: an unmeasurable age must not withhold", got)
	}
}

// TestUnclaimableWorkGraceNeverWithholdsAMisroutedBead pins the exclusion that
// keeps the window as narrow as the race it covers. A bead already carrying
// gc.routed_to is not waiting on an address -- it HAS one, naming nobody -- so
// no later write is coming to make it claimable and there is nothing to wait
// for. Withholding it would hide a misspelled route for the whole window.
func TestUnclaimableWorkGraceNeverWithholdsAMisroutedBead(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	res := unclaimableResultAt(graceCfg("5m"), now, []beads.Bead{
		{
			ID: "W-misrouted", Title: "routed at a typo", Type: "task", Status: "open",
			CreatedAt: now.Add(-1 * time.Second),
			Metadata:  map[string]string{beadmeta.RoutedToMetadataKey: "toolsimth"},
		},
	})

	if got := unclaimableDetailIDs(res); strings.Join(got, ",") != "W-misrouted" {
		t.Fatalf("reported %v, want W-misrouted: grace covers the unrouted race only", got)
	}
}

// TestUnclaimableWorkGraceIsOffWhenUndeclared pins that gc ships no window.
// Which asynchronous router a city runs, and how long its cooldown is, is
// city-local knowledge gc cannot derive; an invented default would hide real
// findings in every city that has no such router. Same design as [doctor]
// external_assignees, which also ships empty.
func TestUnclaimableWorkGraceIsOffWhenUndeclared(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	res := unclaimableResultAt(poolAgentCfg(4), now, []beads.Bead{
		{
			ID: "W-1", Title: "minted a second ago", Type: "task", Status: "open",
			CreatedAt: now.Add(-1 * time.Second),
		},
	})

	if got := unclaimableDetailIDs(res); strings.Join(got, ",") != "W-1" {
		t.Fatalf("reported %v, want W-1: an undeclared grace must withhold nothing", got)
	}
}

// TestUnclaimableWorkStatesWhatTheGraceWithheld pins that a withheld bead is
// still visible as a number on BOTH result paths. A suppression an operator
// cannot see turns a misconfigured window into a check that reads clean, which
// is the failure mode that gets a detector trusted when it is silent. The OK
// path matters more than the error path: its old sentence claimed every
// claimable bead was addressed, which a withheld bead makes false.
func TestUnclaimableWorkStatesWhatTheGraceWithheld(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	young := beads.Bead{
		ID: "W-young", Title: "minted a minute ago", Type: "task", Status: "open",
		CreatedAt: now.Add(-1 * time.Minute),
	}
	old := beads.Bead{
		ID: "W-old", Title: "unaddressed for an hour", Type: "task", Status: "open",
		CreatedAt: now.Add(-1 * time.Hour),
	}

	okRes := unclaimableResultAt(graceCfg("5m"), now, []beads.Bead{young})
	if okRes.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK", okRes.Status)
	}
	for _, want := range []string{"1", "unaddressed_grace"} {
		if !strings.Contains(okRes.Message, want) {
			t.Errorf("OK Message %q does not name %q", okRes.Message, want)
		}
	}
	if strings.Contains(okRes.Message, "every one of") {
		t.Errorf("OK Message %q still claims every bead is addressed while one is withheld", okRes.Message)
	}

	errRes := unclaimableResultAt(graceCfg("5m"), now, []beads.Bead{young, old})
	if errRes.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError", errRes.Status)
	}
	if !strings.Contains(errRes.Message, "unaddressed_grace") {
		t.Errorf("error Message %q does not state the withheld count", errRes.Message)
	}
}

// TestUnclaimableWorkRefusesToAnswerOnAnUnparseableGrace pins that a typo in
// the declaration is loud. Falling back to zero would report every young bead
// and look exactly like a working check, so the operator would never learn the
// window they wrote is not in effect -- and the reverse fallback, some default
// window, would silently withhold on a value nobody chose.
//
// Deliberately NOT a config-load validation error: that would make a typo in an
// optional doctor key refuse every gc command in the city, and this key's whole
// blast radius is one advisory check.
func TestUnclaimableWorkRefusesToAnswerOnAnUnparseableGrace(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	// "-5m" is the row that would otherwise pass unnoticed: it PARSES, so only
	// the sign check refuses it, and without this row that check could be
	// deleted with the suite green. A negative window withholds nothing, which
	// is indistinguishable from the key being absent -- the operator who wrote
	// it would never learn it does nothing.
	for _, tc := range []struct{ name, grace string }{
		{"not a duration", "5 minutes"},
		{"negative duration", "-5m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := unclaimableResultAt(graceCfg(tc.grace), now, []beads.Bead{
				{ID: "W-1", Title: "unrouted", Type: "task", Status: "open", CreatedAt: now.Add(-1 * time.Hour)},
			})

			if res.Status != doctor.StatusWarning {
				t.Fatalf("Status = %v, want StatusWarning; message %q", res.Status, res.Message)
			}
			for _, want := range []string{"unaddressed_grace", `"` + tc.grace + `"`} {
				if !strings.Contains(res.Message, want) {
					t.Errorf("Message %q does not name %q", res.Message, want)
				}
			}
		})
	}
}

// TestUnclaimableWorkUsesTheWallClockWhenUndirected pins that the production
// constructor installs a real clock. The seam above is a test affordance, and a
// nil one would panic on the first graced run rather than in any test here.
func TestUnclaimableWorkUsesTheWallClockWhenUndirected(t *testing.T) {
	check := newUnclaimableWorkCheck(graceCfg("5m"), "/city", func(string) (beads.Store, error) {
		return beads.NewMemStore(), nil
	})
	if check.now == nil {
		t.Fatal("now is nil: the graced path would panic in production and in no test")
	}
	if elapsed := time.Since(check.now()); elapsed < 0 || elapsed > time.Minute {
		t.Fatalf("now() is %v from the wall clock, want the wall clock", elapsed)
	}
}
