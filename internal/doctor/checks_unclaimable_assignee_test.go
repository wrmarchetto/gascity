package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the unclaimable-assignee check only -- whether an open bead's
// assignee resolves to a name some session in this city can hold work under.
// It delegates the per-session half of that question (which values one session
// bead carries) to internal/session's ClaimIdentities and its own tests.
//
// The suite exists because the failure this check detects is SILENT: bd accepts
// any string as an assignee, so a typo, or a slot name that stopped existing
// when a cap dropped, produces a bead no query will ever return and no log line
// will ever mention. Every test below therefore pins a *specific* claim tier,
// not just "the check fired" -- a check that flagged everything would report
// the stranded beads too, and be turned off within a day.
//
// Run: go test ./internal/doctor/ -run TestUnclaimableAssignee

// runUnclaimableAssigneeCheck runs the check over an in-memory store seeded
// with beadList. A real MemStore is used rather than a scripted fake so an
// assertion cannot pass against a query the fake never implemented.
func runUnclaimableAssigneeCheck(t *testing.T, cfg *config.City, beadList []beads.Bead) *CheckResult {
	t.Helper()
	store := beads.NewMemStoreFrom(len(beadList)+1, beadList, nil)
	check := NewUnclaimableAssigneeCheck(cfg, t.TempDir(), func(string) (beads.Store, error) {
		return store, nil
	})
	return check.Run(&CheckContext{})
}

// workBead builds an open task assigned to assignee. Titles carry the assignee
// so a failure message names which row survived or was dropped.
func workBead(id, assignee string) beads.Bead {
	return beads.Bead{ID: id, Title: "work for " + assignee, Status: "open", Type: "task", Assignee: assignee}
}

// liveSessionBead builds an open session bead carrying alias -- the shape a
// running session leaves in the store, and the source of the own-identity
// claim tier.
func liveSessionBead(id, alias string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Title:  "session " + alias,
		Status: "open",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: beads.StringMap{
			"alias":        alias,
			"session_name": "city--" + alias,
		},
	}
}

// assertReports fails unless the check reported exactly the given bead ids.
// Both directions matter: a missed id is a stranded bead left invisible, and
// an extra id is the false positive that gets the check disabled.
func assertReports(t *testing.T, r *CheckResult, wantIDs ...string) {
	t.Helper()
	detail := strings.Join(r.Details, "\n")
	if len(wantIDs) == 0 {
		if r.Status != StatusOK {
			t.Fatalf("status = %v, want OK; msg = %q; details = %v", r.Status, r.Message, r.Details)
		}
		return
	}
	if r.Status != StatusWarning {
		t.Fatalf("status = %v, want Warning; msg = %q; details = %v", r.Status, r.Message, r.Details)
	}
	for _, id := range wantIDs {
		if !strings.Contains(detail, id) {
			t.Errorf("details missing bead %q; got:\n%s", id, detail)
		}
	}
	if got := len(r.Details); got != len(wantIDs) {
		t.Errorf("reported %d bead(s), want %d; got:\n%s", got, len(wantIDs), detail)
	}
}

// TestUnclaimableAssigneeReportsATypoedName pins the first shape ci-n785 names:
// `bd update --assignee toolsmth` is accepted at write time by bd and matches
// nothing afterwards. The correctly-spelled sibling is present in the same run
// so the assertion cannot pass by the check simply reporting every bead.
func TestUnclaimableAssigneeReportsATypoedName(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: "toolsmith", MaxActiveSessions: intPtr(2)},
	}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		workBead("ci-typo", "toolsmth"),
		workBead("ci-ok", "toolsmith-1"),
	})
	assertReports(t, r, "ci-typo")
}

// TestUnclaimableAssigneeReportsASlotAboveTheCap pins ci-n785's second shape:
// a name that WAS an identity and stopped being one when max_active_sessions
// dropped. toolsmith-3 was claimable at max=3 and is claimable by nothing at
// max=2, with no write anywhere recording that it changed meaning.
func TestUnclaimableAssigneeReportsASlotAboveTheCap(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: "toolsmith", MaxActiveSessions: intPtr(2)},
	}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		workBead("ci-stranded", "toolsmith-3"),
		workBead("ci-live", "toolsmith-2"),
	})
	assertReports(t, r, "ci-stranded")
}

// TestUnclaimableAssigneeAcceptsTheBarePoolName pins the route tier ci-c000
// added (bdReadyPoolAliasDemandShell): work hand-assigned to the bare pool name
// is claimed by any slot via atomic transfer, so it is NOT stranded even though
// no slot's GC_ALIAS carries that name above max_active_sessions=1.
//
// Regressing this is the specific way this check would have flagged the queue
// ci-c000 had just un-stranded.
func TestUnclaimableAssigneeAcceptsTheBarePoolName(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: "toolsmith", MaxActiveSessions: intPtr(2)},
	}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{workBead("ci-pooled", "toolsmith")})
	assertReports(t, r)
}

// TestUnclaimableAssigneeAcceptsALiveSessionAlias pins the own-identity tier
// against ci-n785's explicit verification note: the live mayor session carries
// GC_ALIAS=mayor, so beads on bare "mayor" are legitimately claimable.
//
// The config here declares NO mayor agent. That is deliberate: were the agent
// present, the config tier would accept the name and the test would pass with
// the live-session lookup deleted entirely.
func TestUnclaimableAssigneeAcceptsALiveSessionAlias(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		liveSessionBead("ci-sess1", "mayor"),
		workBead("ci-mail-to-mayor", "mayor"),
	})
	assertReports(t, r)
}

// TestUnclaimableAssigneeAcceptsALiveSessionBeadID pins that $GC_SESSION_ID --
// the session's own bead id, the first identity the assigned tiers probe -- is
// claimable. Work self-assigned by id is what crash recovery re-serves.
func TestUnclaimableAssigneeAcceptsALiveSessionBeadID(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		liveSessionBead("ci-sess7", "toolsmith-1"),
		workBead("ci-by-id", "ci-sess7"),
	})
	assertReports(t, r)
}

// TestUnclaimableAssigneeReportsAnAliasOfAClosedSession pins the counterpart:
// a closed session's alias is claimable by nobody. Accepting it would hide
// exactly the work an on_death hook failed to release -- the case where the
// bead is stranded AND a session once answered to the name.
func TestUnclaimableAssigneeReportsAnAliasOfAClosedSession(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	dead := liveSessionBead("ci-sess9", "adhoc-9f1")
	dead.Status = "closed"
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		dead,
		workBead("ci-orphaned", "adhoc-9f1"),
	})
	assertReports(t, r, "ci-orphaned")
}

// TestUnclaimableAssigneeAcceptsAnySlotOfAnUnlimitedPool pins that an agent
// with no max_active_sessions (nil = unlimited) strands nothing: slot 47 is a
// session that has not spawned yet, not a name nobody can carry.
func TestUnclaimableAssigneeAcceptsAnySlotOfAnUnlimitedPool(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "polecat"}}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{workBead("ci-far-slot", "polecat-47")})
	assertReports(t, r)
}

// TestUnclaimableAssigneeDegradesAPathologicalCapToThePrefixRule pins that a
// cap nothing rejects at config load does not turn a health check into a
// million-entry allocation.
//
// The probe is a slot ABOVE the declared cap: only the prefix fallback covers
// swarm-9999, so slot-by-slot enumeration would report it and fail here. The
// non-numeric sibling is in the same run because the fallback has to stay a
// predicate -- a blanket "starts with swarm-" pass would accept both.
func TestUnclaimableAssigneeDegradesAPathologicalCapToThePrefixRule(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: "swarm", MaxActiveSessions: intPtr(enumerationCap + 1)},
	}}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		workBead("ci-far-slot", "swarm-9999"),
		workBead("ci-not-a-slot", "swarm-alpha"),
	})
	assertReports(t, r, "ci-not-a-slot")
}

// TestUnclaimableAssigneeAcceptsANamedSessionIdentity pins that a
// [[named_session]] public identity resolves even when it differs from the
// backing agent template name -- the whole point of the name field.
func TestUnclaimableAssigneeAcceptsANamedSessionIdentity(t *testing.T) {
	cfg := &config.City{
		Agents:        []config.Agent{{Name: "overseer", MaxActiveSessions: intPtr(1)}},
		NamedSessions: []config.NamedSession{{Name: "mayor", Template: "overseer"}},
	}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{workBead("ci-named", "mayor")})
	assertReports(t, r)
}

// TestUnclaimableAssigneeAcceptsConfiguredExternalAssignees pins the operator's
// escape hatch. "human" is written here and in the city's own TOML, never in Go
// source -- gc must not carry an opinion about which names mean "a person".
func TestUnclaimableAssigneeAcceptsConfiguredExternalAssignees(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}},
		Doctor: config.DoctorConfig{ExternalAssignees: []string{"human"}},
	}
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		workBead("ci-for-operator", "human"),
		workBead("ci-for-nobody", "ops-rota"),
	})
	assertReports(t, r, "ci-for-nobody")
}

// TestUnclaimableAssigneeIgnoresClosedUnassignedAndMailBeads pins the three
// exclusions in one run so a widened scan cannot pass by fixing one of them.
//
// Closed: the work is done; its assignee is history.
// Unassigned: routed or unrouted, an empty assignee is a different defect and
// has its own checks.
// Mail: every claim tier carries --exclude-type=message (see
// config.ExcludeMessageTypeArg), so a message is never claimable BY DESIGN and
// reporting one would be a permanent false positive on every city.
//
// Documented absence: molecule, step and gate types are NOT excluded. The
// in_progress crash-recovery tier serves them, so one parked on a dead name is
// genuinely stranded.
func TestUnclaimableAssigneeIgnoresClosedUnassignedAndMailBeads(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	closed := workBead("ci-done", "toolsmth")
	closed.Status = "closed"
	mail := workBead("ci-msg", "someone-else")
	mail.Type = "message"
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{
		closed,
		mail,
		{ID: "ci-unassigned", Title: "routed", Status: "open", Type: "task"},
	})
	assertReports(t, r)
}

// TestUnclaimableAssigneeReportsAnInProgressBead pins that the scan covers
// in_progress and not just open. A stranded bead is USUALLY in_progress: the
// slot that claimed it died, and nothing since has been able to answer to the
// name it was left on.
func TestUnclaimableAssigneeReportsAnInProgressBead(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	inflight := workBead("ci-inflight", "toolsmith-9")
	inflight.Status = "in_progress"
	r := runUnclaimableAssigneeCheck(t, cfg, []beads.Bead{inflight})
	assertReports(t, r, "ci-inflight")
}

// TestUnclaimableAssigneeOffersBothRemediesAndDeclinesToFix pins ci-n785's
// explicit contract: reassigning an operator-addressed bead is a judgment call,
// so the check names both remedies and repairs nothing.
func TestUnclaimableAssigneeOffersBothRemediesAndDeclinesToFix(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	store := beads.NewMemStoreFrom(2, []beads.Bead{workBead("ci-typo", "toolsmth")}, nil)
	check := NewUnclaimableAssigneeCheck(cfg, t.TempDir(), func(string) (beads.Store, error) {
		return store, nil
	})
	r := check.Run(&CheckContext{})

	if check.CanFix() {
		t.Error("CanFix() = true, want false: the remedy is a judgment call")
	}
	if r.Severity != SeverityAdvisory {
		t.Errorf("Severity = %v, want SeverityAdvisory: a stray assignee must not gate dispatch", r.Severity)
	}
	for _, remedy := range []string{"reassign", "gc.routed_to"} {
		if !strings.Contains(r.FixHint, remedy) {
			t.Errorf("FixHint %q does not name remedy %q", r.FixHint, remedy)
		}
	}
}

// TestUnclaimableAssigneeReportsAStoreOpenFailure pins that an unreadable store
// is a warning and not a silent pass. Reporting OK here would make an
// unreachable Dolt server indistinguishable from a clean city -- the check
// would go permanently green at exactly the moment it stopped running.
func TestUnclaimableAssigneeReportsAStoreOpenFailure(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "toolsmith", MaxActiveSessions: intPtr(2)}}}
	check := NewUnclaimableAssigneeCheck(cfg, t.TempDir(), func(string) (beads.Store, error) {
		return nil, errors.New("dolt server unreachable at 127.0.0.1:0")
	})
	r := check.Run(&CheckContext{})

	if r.Status == StatusOK {
		t.Fatalf("status = OK on store-open failure; msg = %q", r.Message)
	}
	if !strings.Contains(r.Message, "dolt server unreachable") {
		t.Errorf("message = %q, want the underlying store error", r.Message)
	}
}

// TestUnclaimableAssigneeSkipsWithoutConfig pins the no-config path. Without
// city.toml there is no identity set to resolve against, so every assignee
// would look unclaimable -- the check must stand down rather than report the
// whole store.
func TestUnclaimableAssigneeSkipsWithoutConfig(t *testing.T) {
	r := runUnclaimableAssigneeCheck(t, nil, []beads.Bead{workBead("ci-typo", "toolsmth")})
	assertReports(t, r)
}
