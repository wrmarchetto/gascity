// cmd_hook_claim_ordering_test.go pins WHICH bead a hook claim actually takes
// off a routed pool queue, by running the real generated work_query through the
// real claim loop against a fake bd that honors --sort and --limit.
//
// The suite exists because that outcome was previously asserted only as a flag
// string in the emitted command (internal/config/config_test.go) and as
// doc-to-code agreement on the same flag
// (test/docsync/dispatch_ordering_test.go). Neither can see the claim. A
// Go-side re-sort of the returned window into priority order would leave every
// one of them green while inverting the policy an operator acts on, and so
// would a claim loop that walked the candidate list backwards. The two
// orderings also only separate when bd is asked for more than one row, so the
// single-row fakes those tests drive cannot distinguish "claimed the oldest"
// from "claimed the highest priority" at all (ci-27eo, found while
// establishing ci-q2vx).
//
// Scope is the claim outcome for the two pool tiers -- routed (gc.routed_to)
// and pool-alias (assignee == the pool name) -- plus their unbounded candidate
// set. The migration fallback tier
// (gc.run_target + gc.kind=workflow) stays with internal/config, which owns the
// jq filter it depends on. Whether the ordering policy is still WRITTEN DOWN
// stays with test/docsync.
//
// The policy cases here are paired with controls that assert the rig can produce
// the opposite answer -- the priority-first head when the sort flag is gone, and
// the pool-alias bead when the routed tier is empty. Without them a fake that
// ignored --sort entirely, or a fixture whose two orderings happened to agree,
// would read as a pass. One control covers both pool tiers' ordering because the
// fake resolves every tier's fixture through one (tier, order) lookup.
//
// Run: go test ./cmd/gc/ -run HookClaimOrdering
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// routedQueueCandidateLimit is the unbounded --limit value emitted by both pool
// tiers. It is not authoritative: the fake bd logs what each tier actually asked
// for so a bounded query cannot make tail rows unreachable without failing here.
const routedQueueCandidateLimit = 0

// routedQueueFixtureRows is deliberately wider than the former twenty-row cap.
// Its tail proves --sort oldest orders all candidates without deciding which work
// the claim loop can see.
const routedQueueFixtureRows = 22

// orderingFixtureTarget is the pool route both tiers are probed with -- the
// PoolName of the slot the tests build, which is what buildWorkQuery resolves
// its target from. The fake bd matches its tier arms against it.
const orderingFixtureTarget = "hello-world/worker"

// orderingFixtureRow is one row of fake bd output -- the bd wire shape, not
// beads.Bead, because only the fields the ordering depends on matter here.
//
// priority is in the fixture solely so the fake can order by it. The claim loop
// never reads it, which is the whole point: the ordering an agent gets is
// whatever bd hands back, so dropping --sort oldest from the query changes the
// claim with nothing in Go to notice.
type orderingFixtureRow struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Priority  int               `json:"priority"`
	CreatedAt string            `json:"created_at"`
	Assignee  string            `json:"assignee,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// routedQueueFixture builds the shared routed-tier fixture: count rows in age
// order, oldest first, each row's priority DERIVED from its own index rather
// than picked per row.
//
// The derivation is what keeps the test honest. Priorities chosen by hand can
// drift into an arrangement where oldest-first and priority-first agree at the
// head, and a fake that ignored --sort would then satisfy the assertion by
// accident. Deriving priority as a descending function of age makes the two
// orderings disagree at the head by construction, and
// assertFixtureDiscriminates refuses the fixture if they ever agree again.
//
// The resulting shape, for the default 22 rows:
//
//	routed-00      oldest, P3   -- the bead FIFO must claim
//	routed-07      older,  P2
//	routed-14..20  newer,  P1   -- must not jump the queue
//	routed-21      newest, P0   -- bd's head if --sort oldest is dropped
func routedQueueFixture(count int) []orderingFixtureRow {
	base := time.Date(2026, 5, 20, 6, 9, 30, 0, time.UTC)
	rows := make([]orderingFixtureRow, 0, count)
	for i := range count {
		// Newer rows carry higher priority (a lower number), so age order and
		// priority order oppose each other. The clamp only matters if count
		// grows past 4*7 rows.
		priority := 3 - i/7
		if priority < 0 {
			priority = 0
		}
		rows = append(rows, orderingFixtureRow{
			ID:        fmt.Sprintf("routed-%02d", i),
			Status:    "open",
			Priority:  priority,
			CreatedAt: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			Metadata:  map[string]string{"gc.routed_to": orderingFixtureTarget},
		})
	}
	return rows
}

// poolAliasQueueFixture is routedQueueFixture in the pool-parked shape: the same
// age-against-priority opposition, but addressed by assignee with no gc.routed_to
// at all (the shape ci-c000 measured), and under its own id prefix so a fixture
// served to the wrong tier cannot satisfy an assertion by accident.
func poolAliasQueueFixture(count int) []orderingFixtureRow {
	rows := routedQueueFixture(count)
	for i := range rows {
		rows[i].ID = strings.Replace(rows[i].ID, "routed-", "alias-", 1)
		rows[i].Assignee = orderingFixtureTarget
		rows[i].Metadata = nil
	}
	return rows
}

// sortedOldestFirst returns rows in bd's --sort oldest order.
func sortedOldestFirst(rows []orderingFixtureRow) []orderingFixtureRow {
	out := append([]orderingFixtureRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// sortedPriorityFirst returns rows in bd's UNFLAGGED ready order,
// (priority, created_at, id). That default is what --sort oldest overrides, so
// it is the order the fake must fall back to when the flag is absent -- a fake
// that served oldest-first regardless would pass a query that had lost the flag.
func sortedPriorityFirst(rows []orderingFixtureRow) []orderingFixtureRow {
	out := append([]orderingFixtureRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// assertFixtureDiscriminates fails when the fixture's two orderings agree at the
// head. They must disagree or every assertion built on it passes whether or not
// the query asks bd to sort, which is the vacuum this suite was written to close.
func assertFixtureDiscriminates(t *testing.T, rows []orderingFixtureRow) {
	t.Helper()
	oldest := sortedOldestFirst(rows)[0].ID
	byPriority := sortedPriorityFirst(rows)[0].ID
	if oldest == byPriority {
		t.Fatalf("fixture head is %q under both orderings, so nothing here can detect a lost --sort oldest; "+
			"restore a fixture whose newest rows carry the highest priority", oldest)
	}
}

// orderingRig is the fake-bd rig the ordering tests claim against: a fixture
// directory, a refusal log, and the production shell runner
// (shellWorkQueryWithEnv) pointed at a fake bd on PATH.
//
// The fake serves a fixture per (tier, order) pair and REFUSES every argv it was
// not scripted for -- logged, exit 1 -- rather than answering "[]". The
// distinction matters because the generated query swallows bd's stderr and reads
// a non-JSON result as an empty tier, so a stand-in that answered everything
// with "[]" would silently let the query walk past the tier under test and the
// test would still see a plausible claim. refusals() is asserted empty in every
// case, which is also what proves the claim came from the tier the case meant to
// exercise.
type orderingRig struct {
	t          *testing.T
	fixtureDir string
	logPath    string
	workDir    string
	env        []string
	lastOutput string
}

func newOrderingRig(t *testing.T) *orderingRig {
	t.Helper()
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	fixtureDir := filepath.Join(tmp, "fixtures")
	for _, dir := range []string{binDir, fixtureDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	workDir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", workDir, err)
	}
	logPath := filepath.Join(tmp, "bd.log")
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(orderingFakeBdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return &orderingRig{
		t:          t,
		fixtureDir: fixtureDir,
		logPath:    logPath,
		workDir:    workDir,
		env: []string{
			"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"GC_FIXTURE_DIR=" + fixtureDir,
			"GC_FIXTURE_LOG=" + logPath,
			"GC_FIXTURE_TARGET=" + orderingFixtureTarget,
			// The pool tiers sit behind an origin gate. A worker session carries
			// "ephemeral"; the reconciler's context-free probe carries nothing.
			"GC_SESSION_ORIGIN=ephemeral",
			// GC_SESSION_ID / GC_SESSION_NAME / GC_ALIAS are deliberately absent
			// so the two assigned tiers skip themselves and the claim under test
			// is unambiguously a pool-tier claim.
		},
	}
}

// serveTier scripts one tier with rows, writing BOTH orderings so the fake can
// answer whichever the query asks for. Passing no rows scripts the tier as
// genuinely empty, which is different from leaving it unscripted (refused).
func (r *orderingRig) serveTier(tier string, rows []orderingFixtureRow) {
	r.t.Helper()
	for order, ordered := range map[string][]orderingFixtureRow{
		"oldest":   sortedOldestFirst(rows),
		"priority": sortedPriorityFirst(rows),
	} {
		var body bytes.Buffer
		for _, row := range ordered {
			encoded, err := json.Marshal(row)
			if err != nil {
				r.t.Fatalf("marshal fixture row %s: %v", row.ID, err)
			}
			body.Write(encoded)
			body.WriteByte('\n')
		}
		path := filepath.Join(r.fixtureDir, tier+"."+order+".jsonl")
		if err := os.WriteFile(path, body.Bytes(), 0o644); err != nil {
			r.t.Fatalf("write fixture %s: %v", path, err)
		}
	}
}

// run is the WorkQueryRunner the claim loop is given. It goes through the
// production shellWorkQueryWithEnv rather than a bespoke exec so the query
// crosses the same process boundary, quoting and env handling it does in a real
// session.
func (r *orderingRig) run(command, _ string) (string, error) {
	out, err := shellWorkQueryWithEnv(command, r.workDir, r.env)
	r.lastOutput = out
	return out, err
}

// log returns the fake's whole trace: one "served" line per answered tier and
// one "refused" line per argv it was not scripted for.
func (r *orderingRig) log() string {
	r.t.Helper()
	data, err := os.ReadFile(r.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		r.t.Fatalf("read fake bd log: %v", err)
	}
	return string(data)
}

func (r *orderingRig) refusals() []string {
	r.t.Helper()
	var out []string
	for _, line := range strings.Split(r.log(), "\n") {
		if strings.HasPrefix(line, "refused:") {
			out = append(out, line)
		}
	}
	return out
}

// assertTierCandidateLimit fails unless tier asks bd for an unbounded candidate
// set. The fake applies the requested limit, so a bounded query would otherwise
// silently hide tail rows from the assertions that follow.
func (r *orderingRig) assertTierCandidateLimit(tier string) {
	r.t.Helper()
	marker := "served: tier=" + tier + " order=oldest limit="
	for _, line := range strings.Split(r.log(), "\n") {
		rest, ok := strings.CutPrefix(line, marker)
		if !ok {
			continue
		}
		limit, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			r.t.Fatalf("fake bd logged an unparseable %s limit %q: %v", tier, rest, err)
		}
		if limit != routedQueueCandidateLimit {
			r.t.Fatalf("%s tier asked bd for --limit=%d, want unbounded --limit=%d so every ready row remains a claim candidate",
				tier, limit, routedQueueCandidateLimit)
		}
		return
	}
	r.t.Fatalf("fake bd never served the %s tier oldest-first; log:\n%s", tier, r.log())
}

func (r *orderingRig) assertCandidatesInclude(tier string, rows []orderingFixtureRow) {
	r.t.Helper()
	for _, row := range rows {
		if !strings.Contains(r.lastOutput, row.ID) {
			r.t.Fatalf("%s candidate %q (P%d) is missing from the unbounded work_query output: %s",
				tier, row.ID, row.Priority, r.lastOutput)
		}
	}
}

// orderingFakeBdScript is the fake bd. It resolves a tier from the argv, picks
// the fixture matching the sort the query asked for, and truncates to --limit.
//
// Only the two pool tiers are scripted. The migration and legacy-ephemeral
// tiers below them are deliberately left unscripted so that falling through to
// them is recorded as a refusal -- reaching them means the case under test lost
// the tier it meant to exercise. Written in POSIX sh with no jq, awk or paste so
// the rig cannot fail for a reason unrelated to what it is measuring.
const orderingFakeBdScript = `#!/bin/sh
argv="$*"
tier=""
case " $argv " in
  *" --metadata-field gc.routed_to=$GC_FIXTURE_TARGET "*) tier=routed ;;
  *" --assignee=$GC_FIXTURE_TARGET "*) tier=alias ;;
esac
order=priority
case " $argv " in
  *" --sort oldest "*) order=oldest ;;
esac
limit=0
for arg in "$@"; do
  case "$arg" in
    --limit=*) limit=${arg#--limit=} ;;
  esac
done
fixture="$GC_FIXTURE_DIR/$tier.$order.jsonl"
if [ -z "$tier" ] || [ ! -f "$fixture" ]; then
  printf 'refused: bd %s\n' "$argv" >>"$GC_FIXTURE_LOG"
  exit 1
fi
printf 'served: tier=%s order=%s limit=%s\n' "$tier" "$order" "$limit" >>"$GC_FIXTURE_LOG"
rows=""
n=0
while IFS= read -r row; do
  n=$((n + 1))
  if [ "$limit" -gt 0 ] && [ "$n" -gt "$limit" ]; then
    break
  fi
  if [ -n "$rows" ]; then
    rows="$rows,$row"
  else
    rows="$row"
  fi
done <"$fixture"
printf '[%s]' "$rows"
`

// orderingClaimProbe records which claim mutation ran on which bead. Both seams
// are always wired: the one that is wrong for the case under test fails the test
// itself rather than returning a success the assertions would have to catch.
type orderingClaimProbe struct {
	claimed     []string
	poolClaimed []string
}

// orderingClaimOps wires the rig to the claim loop. Every seam that would reach
// outside the process is stubbed, so the only I/O is the work query itself.
// ResolveWorkBranch returning "" (with no GC_SESSION_ID in opts.Env) leaves the
// claim-time metadata patch empty, which is what keeps StampWorkMeta a refusal
// rather than a real bd write.
func orderingClaimOps(t *testing.T, rig *orderingRig, probe *orderingClaimProbe) hookClaimOps {
	t.Helper()
	return hookClaimOps{
		Runner: rig.run,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			probe.claimed = append(probe.claimed, beadID)
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress"}, true, nil
		},
		PoolClaim: func(_ context.Context, _ string, _ []string, beadID, _, assignee string) (beads.Bead, bool, error) {
			probe.poolClaimed = append(probe.poolClaimed, beadID)
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress"}, true, nil
		},
		ResolveWorkBranch: func(string) string { return "" },
		StampWorkMeta: func(_ context.Context, _ string, _ []string, beadID string, _ string, patch map[string]string) error {
			t.Errorf("StampWorkMeta ran on %s with patch %v; this rig resolves no branch and no session, so the patch must be empty", beadID, patch)
			return nil
		},
		EmitClaimRejected: func(beadID, existing, attempted string) {
			t.Errorf("claim_rejected on %s (%s beat %s); no case here contests a bead", beadID, existing, attempted)
		},
		DrainAck: func(reason string, _ io.Writer) error {
			t.Errorf("drain-ack with reason %q; every case here has claimable work", reason)
			return nil
		},
	}
}

// claimOnce runs one claim against query and returns the decoded result.
func claimOnce(t *testing.T, rig *orderingRig, ops hookClaimOps, query string) hookClaimJSONResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := doHookClaim(query, rig.workDir, hookClaimOptions{
		Assignee:           "hello-world/worker-2",
		IdentityCandidates: []string{"hello-world/worker-2"},
		RouteTargets:       []string{orderingFixtureTarget},
		JSON:               true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0\nstdout: %s\nstderr: %s\nbd log:\n%s",
			code, stdout.String(), stderr.String(), rig.log())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode claim result: %v (stdout=%s)", err, stdout.String())
	}
	if refused := rig.refusals(); len(refused) > 0 {
		t.Fatalf("the query walked past the tier under test into unscripted bd calls: %v", refused)
	}
	return result
}

// routedQueuePoolSlot is the agent whose generated work_query every case here
// runs: one slot of a pool, so its query probes the pool ROUTE
// (orderingFixtureTarget) while the claiming session identity is the slot's own
// qualified name. That split is the deployed shape and it is what makes the
// pool-alias tier reachable at all -- at more than one slot no session's
// GC_ALIAS carries the bare pool name (ci-c000).
func routedQueuePoolSlotQuery() string {
	slot := config.Agent{Name: "worker-2", Dir: "hello-world", PoolName: orderingFixtureTarget}
	return slot.EffectiveWorkQuery()
}

func TestHookClaimOrderingTakesOldestRoutedBeadAheadOfNewerHigherPriority(t *testing.T) {
	// The policy an operator acts on: raising a bead's priority does NOT move it
	// up a pool queue. Asserted on the bead the claim mutation actually ran
	// against, so a Go-side re-sort of the returned window into priority order
	// fails here even though the emitted query still carries --sort oldest.
	rows := routedQueueFixture(routedQueueFixtureRows)
	assertFixtureDiscriminates(t, rows)

	rig := newOrderingRig(t)
	rig.serveTier("routed", rows)
	var probe orderingClaimProbe
	result := claimOnce(t, rig, orderingClaimOps(t, rig, &probe), routedQueuePoolSlotQuery())

	oldest := sortedOldestFirst(rows)[0]
	byPriority := sortedPriorityFirst(rows)[0]
	if result.BeadID != oldest.ID {
		t.Fatalf("claimed %q, want the oldest routed bead %q (P%d); the highest-priority row is %q (P%d)",
			result.BeadID, oldest.ID, oldest.Priority, byPriority.ID, byPriority.Priority)
	}
	if want := []string{oldest.ID}; len(probe.claimed) != 1 || probe.claimed[0] != want[0] {
		t.Fatalf("claim mutations = %v, want exactly %v", probe.claimed, want)
	}
	if len(probe.poolClaimed) > 0 {
		t.Fatalf("pool transfer ran on %v; a routed unassigned bead is taken by the plain claim", probe.poolClaimed)
	}
	rig.assertTierCandidateLimit("routed")
}

func TestHookClaimOrderingTakesHighestPriorityOnceTheOldestSortIsDropped(t *testing.T) {
	// The control for the case above, and the reason its result is attributable.
	// It proves the rig can produce the priority-first answer at all: the fake
	// honors --sort rather than serving one canned order, and the assertion is
	// sensitive to the flag the query carries. Without this, a fake that always
	// replied oldest-first would make the FIFO assertion unfalsifiable.
	//
	// The flag is stripped from every tier rather than from the routed tier's
	// exact invocation on purpose -- only the routed tier is scripted here, so
	// the two are equivalent, and a blanket strip does not rot when the tier's
	// flag order changes.
	rows := routedQueueFixture(routedQueueFixtureRows)
	assertFixtureDiscriminates(t, rows)

	query := routedQueuePoolSlotQuery()
	unsorted := strings.ReplaceAll(query, " --sort oldest", "")
	if unsorted == query {
		t.Fatalf("work_query carries no --sort oldest to strip, so this control cannot run; "+
			"if the ordering flag moved, this test and its sibling must both be re-derived.\nquery: %s", query)
	}

	rig := newOrderingRig(t)
	rig.serveTier("routed", rows)
	var probe orderingClaimProbe
	result := claimOnce(t, rig, orderingClaimOps(t, rig, &probe), unsorted)

	byPriority := sortedPriorityFirst(rows)[0]
	if result.BeadID != byPriority.ID {
		t.Fatalf("claimed %q from an unsorted query, want bd's own priority-first head %q (P%d); "+
			"the fake is not honoring --sort, so the FIFO assertion in the sibling test proves nothing",
			result.BeadID, byPriority.ID, byPriority.Priority)
	}
}

func TestHookClaimOrderingSeesEveryRoutedCandidate(t *testing.T) {
	// Sorting must order candidates, never decide which ready work is visible to
	// the claim loop. The tail contains newer, higher-priority work so a bounded
	// query cannot pass by merely returning the FIFO head correctly.
	rows := routedQueueFixture(routedQueueFixtureRows)

	rig := newOrderingRig(t)
	rig.serveTier("routed", rows)
	var probe orderingClaimProbe
	claimOnce(t, rig, orderingClaimOps(t, rig, &probe), routedQueuePoolSlotQuery())
	rig.assertTierCandidateLimit("routed")
	rig.assertCandidatesInclude("routed", rows)
}

func TestHookClaimOrderingTakesOldestPoolParkedBeadAheadOfNewerHigherPriority(t *testing.T) {
	// The same policy on the other pool tier, and the one ci-q2vx actually
	// measured. It needs its own multi-row case for two reasons: the tier is a
	// separate generated command whose --sort was inherited by copying the routed
	// tier's shape (ci-c000) rather than chosen, so nothing about the routed tier's
	// flags constrains it; and the take runs through the compare-and-swap transfer
	// rather than the plain claim, so it could regress on its own.
	//
	// No priority-first control beside this one, deliberately: the fake resolves its
	// fixture by (tier, order) through a single code path, so the routed control
	// already establishes that it honors --sort rather than serving a canned order.
	rows := poolAliasQueueFixture(routedQueueFixtureRows)
	assertFixtureDiscriminates(t, rows)

	rig := newOrderingRig(t)
	// The routed tier is scripted EMPTY rather than left unscripted: it is tried
	// first, and an unscripted tier is a refusal, which claimOnce fails on.
	rig.serveTier("routed", nil)
	rig.serveTier("alias", rows)
	var probe orderingClaimProbe
	result := claimOnce(t, rig, orderingClaimOps(t, rig, &probe), routedQueuePoolSlotQuery())

	oldest := sortedOldestFirst(rows)[0]
	byPriority := sortedPriorityFirst(rows)[0]
	if result.BeadID != oldest.ID {
		t.Fatalf("claimed %q, want the oldest pool-parked bead %q (P%d); the highest-priority row is %q (P%d)",
			result.BeadID, oldest.ID, oldest.Priority, byPriority.ID, byPriority.Priority)
	}
	if len(probe.poolClaimed) != 1 || probe.poolClaimed[0] != oldest.ID {
		t.Fatalf("pool transfers = %v, want exactly [%s]; work parked on the pool name is taken by the "+
			"compare-and-swap transfer, not the plain claim", probe.poolClaimed, oldest.ID)
	}
	rig.assertTierCandidateLimit("alias")
	rig.assertCandidatesInclude("alias", rows)
}

func TestHookClaimOrderingPrefersRoutedTierOverHigherPriorityPoolAlias(t *testing.T) {
	// Cross-tier precedence, which is decided by `exit 0` placement in the
	// generated shell alone -- nothing in Go compares the two tiers. The routed
	// bead here is strictly worse by both keys the tiers sort on (newer AND lower
	// priority) and must still win, because its tier is tried first and the first
	// non-empty tier ends the ladder.
	routed := []orderingFixtureRow{{
		ID:        "routed-newest-p3",
		Status:    "open",
		Priority:  3,
		CreatedAt: "2026-05-21T06:09:30Z",
		Metadata:  map[string]string{"gc.routed_to": orderingFixtureTarget},
	}}
	// Hand-assigned pool work carries the pool name as its assignee and no
	// gc.routed_to at all, which is the shape ci-c000 measured.
	alias := []orderingFixtureRow{{
		ID:        "alias-oldest-p0",
		Status:    "open",
		Priority:  0,
		CreatedAt: "2026-05-20T06:09:30Z",
		Assignee:  orderingFixtureTarget,
	}}

	rig := newOrderingRig(t)
	rig.serveTier("routed", routed)
	rig.serveTier("alias", alias)
	var probe orderingClaimProbe
	result := claimOnce(t, rig, orderingClaimOps(t, rig, &probe), routedQueuePoolSlotQuery())

	if result.BeadID != "routed-newest-p3" {
		t.Fatalf("claimed %q, want routed-newest-p3; the routed tier is tried first and ends the ladder, "+
			"so an older P0 parked on the pool alias must still wait", result.BeadID)
	}
	if len(probe.poolClaimed) > 0 {
		t.Fatalf("pool transfer ran on %v while the routed tier had work", probe.poolClaimed)
	}
	if !strings.Contains(rig.log(), "served: tier=routed") || strings.Contains(rig.log(), "served: tier=alias") {
		t.Fatalf("expected the ladder to stop at the routed tier; bd log:\n%s", rig.log())
	}
}

func TestHookClaimOrderingTakesPoolAliasWorkWhenTheRoutedTierIsEmpty(t *testing.T) {
	// The control for the case above. It proves the pool-alias bead is reachable
	// and claimable through this rig, so the routed tier winning there is
	// attributable to tier precedence and not to a fixture the query could never
	// have served or a bead the claim loop would have refused anyway.
	alias := []orderingFixtureRow{{
		ID:        "alias-oldest-p0",
		Status:    "open",
		Priority:  0,
		CreatedAt: "2026-05-20T06:09:30Z",
		Assignee:  orderingFixtureTarget,
	}}

	rig := newOrderingRig(t)
	rig.serveTier("routed", nil)
	rig.serveTier("alias", alias)
	var probe orderingClaimProbe
	result := claimOnce(t, rig, orderingClaimOps(t, rig, &probe), routedQueuePoolSlotQuery())

	if result.BeadID != "alias-oldest-p0" {
		t.Fatalf("claimed %q, want the pool-parked alias-oldest-p0 once the routed tier is empty", result.BeadID)
	}
	// A bead parked on the pool name is taken by the compare-and-swap transfer,
	// not the plain claim: bd refuses --claim on a bead assigned to another name.
	if len(probe.poolClaimed) != 1 || probe.poolClaimed[0] != "alias-oldest-p0" {
		t.Fatalf("pool transfers = %v, want exactly [alias-oldest-p0]", probe.poolClaimed)
	}
}
