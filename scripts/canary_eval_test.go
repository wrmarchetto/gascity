// scripts/canary_eval_test.go
//
// Behavior contract for scripts/canary-eval.py, the evaluator that decides
// whether epic:agent-efficiency's new model and effort levels are kept or
// reverted.
//
// The suite exists because the criterion it enforces is one a human cannot be
// trusted to apply after the fact: the bead that commissioned the canary
// (gs-jbyc) names the failure directly -- "a threshold chosen after the
// numbers are in is not a criterion, it is a rationalization". The margins
// live in a committed TOML file and the baseline is recomputed from the stated
// window, so neither side of the comparison can be tuned once the window's
// figures exist. These tests pin the arithmetic and, more importantly, pin
// every way the evaluator is allowed to decline to answer: a run that cannot
// separate the regimes must exit 3, never 0.
//
// Every fixture uses invented agent types (alpha, beta, ctl). Real role names
// would couple the suite to this city's configuration and would put role names
// where AGENTS.md forbids them.
//
// Scope: the evaluator's arithmetic, its sample floors, its control handling
// and its exit codes, driven end to end as a subprocess over files. It does
// NOT cover collection -- scripts/canary-fetch.sh runs gc usage and bd, and
// what those emit is their own contract. It also cannot represent the live
// city: whether an agent's configured effort actually reached a running
// session is a process-table question the evaluator never sees, which is why
// engdocs/contributors/agent-efficiency-canary.md keeps that as a manual step.
//
// Run it with:
//
//	go test ./scripts/ -run TestCanary
package scripts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// canaryExitKeep, canaryExitRevert and canaryExitNoVerdict are the evaluator's
// three answers. They are distinct on purpose: "no verdict" shares an exit
// code with nothing, because a run that could not separate the regimes read as
// a pass is the single most expensive mistake this tool can make -- it would
// retire the epic's open question by silence.
const (
	canaryExitKeep      = 0
	canaryExitRevert    = 2
	canaryExitNoVerdict = 3
)

// canarySession is one row of a gc usage --by session --json report, reduced
// to the fields the evaluator reads. Written as a struct rather than a raw
// JSON string so a field rename in the fixture cannot silently stop matching.
type canarySession struct {
	Key         string   `json:"key"`
	AgentTypes  []string `json:"agent_types"`
	Invocations int      `json:"invocations"`
	OutputToks  int      `json:"output_tokens"`
	CacheRead   int      `json:"cache_read_tokens"`
}

// canaryBead is one row of bd list --json, reduced the same way.
type canaryBead struct {
	ID        string            `json:"id"`
	IssueType string            `json:"issue_type"`
	ClosedAt  string            `json:"closed_at"`
	CreatedBy string            `json:"created_by"`
	Labels    []string          `json:"labels"`
	Metadata  map[string]string `json:"metadata"`
}

// canaryFixture is one complete evaluator input set.
type canaryFixture struct {
	criteria       string
	baselineUsage  []canarySession
	windowUsage    []canarySession
	baselineBeads  []canaryBead
	windowBeads    []canaryBead
	extraArguments []string
}

// canaryResult is what the evaluator reported, for assertions that need to
// look past the exit code.
type canaryResult struct {
	exit   int
	stdout string
	stderr string
}

// runCanaryEval writes the fixture to a temp directory and runs the evaluator
// over it.
func runCanaryEval(t *testing.T, f canaryFixture) canaryResult {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, v any) string {
		p := filepath.Join(dir, name)
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	criteriaPath := filepath.Join(dir, "criteria.toml")
	if err := os.WriteFile(criteriaPath, []byte(f.criteria), 0o644); err != nil {
		t.Fatalf("write criteria: %v", err)
	}
	baselineUsage := write("baseline-usage.json", map[string]any{"groups": f.baselineUsage})
	windowUsage := write("window-usage.json", map[string]any{"groups": f.windowUsage})
	baselineBeads := write("baseline-beads.json", f.baselineBeads)
	windowBeads := write("window-beads.json", f.windowBeads)

	args := []string{
		filepath.Join(repoRoot(t), "scripts", "canary-eval.py"),
		"--criteria", criteriaPath,
		"--baseline-usage", baselineUsage,
		"--window-usage", windowUsage,
		"--baseline-beads", baselineBeads,
		"--window-beads", windowBeads,
	}
	args = append(args, f.extraArguments...)
	cmd := exec.Command("python3", args...) //nolint:gosec // fixed script path under the repo
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exit := 0
	if err != nil {
		ee := &exec.ExitError{}
		ok := errors.As(err, &ee)
		if !ok {
			t.Fatalf("run canary-eval.py: %v (stderr: %s)", err, errBuf.String())
		}
		exit = ee.ExitCode()
	}
	// An interpreter that could not open the script exits 2, which is also the
	// evaluator's revert code. Without this guard a deleted or renamed script
	// would hand a silent pass to every test that asserts only on exit 2.
	if strings.TrimSpace(out.String()) == "" {
		t.Fatalf("canary-eval.py produced no stdout (exit %d); stderr:\n%s",
			exit, errBuf.String())
	}
	return canaryResult{exit: exit, stdout: out.String(), stderr: errBuf.String()}
}

// canaryBeads makes n beads for one session-per-bead fixture, spread one per
// local day starting at day 4 of September so a day floor can be satisfied or
// deliberately starved.
func canaryBeads(prefix string, n, spreadDays int, sessionInvocations int,
	agentType string, out *[]canarySession,
) []canaryBead {
	beads := make([]canaryBead, 0, n)
	for i := 0; i < n; i++ {
		sess := fmt.Sprintf("%s-s%d", prefix, i)
		day := 4 + (i % spreadDays)
		beads = append(beads, canaryBead{
			ID:        fmt.Sprintf("%s-b%d", prefix, i),
			IssueType: "task",
			ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", day),
			Metadata:  map[string]string{"gc.session_id": sess, "gc.outcome": "pass"},
		})
		*out = append(*out, canarySession{
			Key:         sess,
			AgentTypes:  []string{agentType},
			Invocations: sessionInvocations,
			OutputToks:  sessionInvocations * 1000,
			CacheRead:   sessionInvocations * 100000,
		})
	}
	return beads
}

// oneSubjectCriteria is the smallest criteria file that judges anything: one
// subject on the per-bead invocation metric with a 20% margin.
const oneSubjectCriteria = `
schema = 1

[[agent]]
type = "alpha"
role = "subject"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "output_per_invocation_not_up"
`

// TestCanaryRevertFiresWhenASubjectCrossesItsLine pins the tool's whole
// purpose. The window runs 30 invocations per bead against a baseline of 20
// and a 20% margin, so the line is 24 and the window is past it.
func TestCanaryRevertFiresWhenASubjectCrossesItsLine(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 30, "alpha", &winSess)
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitRevert {
		t.Fatalf("exit = %d, want %d (revert)\nstdout:\n%s\nstderr:\n%s",
			got.exit, canaryExitRevert, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "alpha") {
		t.Fatalf("revert verdict does not name the agent to revert:\n%s", got.stdout)
	}
}

// TestCanaryASubjectInsideItsLineKeepsTheLevels is the other side of the same
// line: 23 per bead against a baseline of 20 is a real 15% rise and must NOT
// fire, because the margin was set above the baseline's own day-to-day spread
// precisely so ordinary jitter does not trip it.
func TestCanaryASubjectInsideItsLineKeepsTheLevels(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 23, "alpha", &winSess)
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d (keep)\nstdout:\n%s\nstderr:\n%s",
			got.exit, canaryExitKeep, got.stdout, got.stderr)
	}
}

// TestCanaryTheLineIsRecomputedFromTheBaselineNotPinnedInTheCriteria feeds one
// window against two different baselines and requires the verdict to flip.
//
// Constructed this way because the alternative design -- writing the baseline
// figures into the criteria file as literals -- would pass every other test in
// this file while quietly becoming a second copy of a derived value, drifting
// from the log it came from. Only comparing two baselines can tell a computed
// line from a pinned one.
func TestCanaryTheLineIsRecomputedFromTheBaselineNotPinnedInTheCriteria(t *testing.T) {
	var winSess []canarySession
	winBeads := canaryBeads("win", 6, 3, 30, "alpha", &winSess)

	var lowSess []canarySession
	lowBeads := canaryBeads("low", 6, 3, 20, "alpha", &lowSess)
	low := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: lowSess, windowUsage: winSess,
		baselineBeads: lowBeads, windowBeads: winBeads,
	})
	if low.exit != canaryExitRevert {
		t.Fatalf("baseline 20 vs window 30: exit = %d, want %d\n%s",
			low.exit, canaryExitRevert, low.stdout)
	}

	var highSess []canarySession
	highBeads := canaryBeads("high", 6, 3, 28, "alpha", &highSess)
	high := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: highSess, windowUsage: winSess,
		baselineBeads: highBeads, windowBeads: winBeads,
	})
	if high.exit != canaryExitKeep {
		t.Fatalf("baseline 28 vs window 30: exit = %d, want %d\n%s",
			high.exit, canaryExitKeep, high.stdout)
	}
}

// controlCriteria adds an unchanged agent whose job is to absorb fleet-wide
// drift: if ctl moves too, the move is not the effort change.
const controlCriteria = `
schema = 1

[[agent]]
type = "alpha"
role = "subject"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "output_per_invocation_not_up"

[[agent]]
type = "ctl"
role = "control"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "none"
`

// TestCanaryAMovedControlVoidsTheRunInsteadOfRevertingASubject is the reason
// the control row exists. Both agents rise by the same 50%, which is what a
// fleet-wide change -- a harder month of work, a provider-side change --
// produces. Reading that as a per-agent regression would revert a level on
// evidence that says nothing about it, so the run must decline to answer.
func TestCanaryAMovedControlVoidsTheRunInsteadOfRevertingASubject(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("basea", 6, 3, 20, "alpha", &baseSess)
	baseBeads = append(baseBeads, canaryBeads("basec", 6, 3, 20, "ctl", &baseSess)...)
	winBeads := canaryBeads("wina", 6, 3, 30, "alpha", &winSess)
	winBeads = append(winBeads, canaryBeads("winc", 6, 3, 30, "ctl", &winSess)...)
	got := runCanaryEval(t, canaryFixture{
		criteria: controlCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
	if !strings.Contains(got.stdout, "ctl") {
		t.Fatalf("void verdict does not name the control that moved:\n%s", got.stdout)
	}
}

// TestCanaryAHeldControlStillLetsASubjectRevert proves the control rig itself
// works before anything is attributed to it. A control that voided every run
// would be indistinguishable from a control that was doing its job, and every
// conclusion drawn through it would have to be withdrawn later.
func TestCanaryAHeldControlStillLetsASubjectRevert(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("basea", 6, 3, 20, "alpha", &baseSess)
	baseBeads = append(baseBeads, canaryBeads("basec", 6, 3, 20, "ctl", &baseSess)...)
	winBeads := canaryBeads("wina", 6, 3, 30, "alpha", &winSess)
	winBeads = append(winBeads, canaryBeads("winc", 6, 3, 21, "ctl", &winSess)...)
	got := runCanaryEval(t, canaryFixture{
		criteria: controlCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitRevert {
		t.Fatalf("exit = %d, want %d (revert)\nstdout:\n%s",
			got.exit, canaryExitRevert, got.stdout)
	}
}

// TestCanaryARowUnderItsBeadFloorIsNoVerdictNotAPass starves the window of
// beads. The margins in the criteria file were derived against a stated
// sample size; below it the line means something weaker than what was
// computed, and reporting that as a pass would retire the question on an
// unpowered reading.
func TestCanaryARowUnderItsBeadFloorIsNoVerdictNotAPass(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 3, 3, 20, "alpha", &winSess)
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryARowUnderItsDayFloorIsNoVerdictNotAPass gives the window enough
// beads but delivers all of them on one day.
//
// The bead count alone cannot catch this: the margin was derived from
// day-to-day spread, and a single day's mean is exactly the quantity that
// spread describes. A one-day window sits inside the noise the margin was
// sized against, so its agreement with the baseline is not evidence.
func TestCanaryARowUnderItsDayFloorIsNoVerdictNotAPass(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 1, 20, "alpha", &winSess)
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryAnAgentWithNoWindowDataIsNoVerdictNotAPass removes the agent from
// the window entirely. An agent that stopped running is a missing measurement;
// silently skipping it would let the epic be declared safe for an agent the
// window never observed.
func TestCanaryAnAgentWithNoWindowDataIsNoVerdictNotAPass(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := []canaryBead{}
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryASessionHoldingSeveralBeadsSplitsItsCost pins the join arithmetic.
//
// One session that closed two beads bought both with the same invocations, so
// charging its full count to each would report twice the per-bead cost and
// fire a revert on a session that was more efficient, not less. The fixture
// gives the window one two-bead session at 40 invocations against a baseline
// of 20 per bead: the correct reading is 20 per bead and no revert, while the
// double-count reads 40 and fires.
func TestCanaryASessionHoldingSeveralBeadsSplitsItsCost(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)

	winBeads := []canaryBead{}
	for i := 0; i < 6; i++ {
		sess := fmt.Sprintf("win-s%d", i/2)
		if i%2 == 0 {
			winSess = append(winSess, canarySession{
				Key: sess, AgentTypes: []string{"alpha"},
				Invocations: 40, OutputToks: 40000, CacheRead: 4000000,
			})
		}
		winBeads = append(winBeads, canaryBead{
			ID: fmt.Sprintf("win-b%d", i), IssueType: "task",
			ClosedAt: fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%3)),
			Metadata: map[string]string{"gc.session_id": sess, "gc.outcome": "pass"},
		})
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d (keep); a shared session was charged to "+
			"every bead it closed\nstdout:\n%s", got.exit, canaryExitKeep, got.stdout)
	}
}

// TestCanaryAWitnessGoingTheWrongWayVoidsTheRow guards the failure mode that
// cost this epic a day: a config change that never reached a running session.
//
// Lowering an agent's effort should not RAISE its output tokens per
// invocation. When it does, past the row's own margin, the most likely reading
// is that the row is not running the level the comparison assumes, and a
// comparison that assumes the wrong regime is worse than no comparison. The
// fixture holds invocations per bead flat so the only thing under test is the
// witness.
func TestCanaryAWitnessGoingTheWrongWayVoidsTheRow(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 20, "alpha", &winSess)
	for i := range winSess {
		winSess[i].OutputToks *= 2
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryMachineryBeadsAreNotUnitsOfWork pads the window with the records
// this city's orders write -- about nine thousand a day, carrying the
// order-tracking label -- and with a molecule step bead carrying gc.kind.
//
// The fixture keeps a genuine 50% regression in the subject's real work. If
// the machinery rows reached the denominator they would divide the same token
// spend by a bead count dominated by records that cost nothing, the per-bead
// figure would collapse toward zero, and the regression would read as a large
// improvement. That is the failure this filter exists to prevent, so the test
// asserts the revert still fires rather than asserting the count directly.
func TestCanaryMachineryBeadsAreNotUnitsOfWork(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 30, "alpha", &winSess)

	// Every padding bead names a real session of the agent under test, so the
	// filter is the only thing that can keep them out of the denominator.
	for i := 0; i < 50; i++ {
		padding := canaryBead{
			ID: fmt.Sprintf("pad-b%d", i), IssueType: "task",
			ClosedAt: "2026-09-05T18:00:00Z",
			Metadata: map[string]string{"gc.session_id": "win-s0", "gc.outcome": "pass"},
		}
		if i%2 == 0 {
			padding.Labels = []string{"order-tracking", "exec"}
		} else {
			padding.Metadata["gc.kind"] = "wisp"
		}
		winBeads = append(winBeads, padding)
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitRevert {
		t.Fatalf("exit = %d, want %d (revert); machinery records reached the "+
			"per-bead denominator\nstdout:\n%s", got.exit, canaryExitRevert, got.stdout)
	}
}

// TestCanaryDayFloorCountsLocalDaysNotUTCDays pins the timezone the day floor
// counts in.
//
// The two window beads close at 02:00Z and 18:00Z on the same UTC date, which
// is two different local dates in -05:00 -- the zone the baseline document
// states its window in. A floor of 2 is therefore satisfied by local
// bucketing and starved by UTC bucketing, and nothing else in this suite can
// tell the two apart: every other fixture closes beads at 18:00Z, where the
// two calendars agree.
func TestCanaryDayFloorCountsLocalDaysNotUTCDays(t *testing.T) {
	criteria := strings.Replace(oneSubjectCriteria,
		"min_beads = 5\nmin_days = 3", "min_beads = 2\nmin_days = 2", 1)
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)

	winBeads := []canaryBead{}
	for i, at := range []string{"2026-09-05T02:00:00Z", "2026-09-05T18:00:00Z"} {
		sess := fmt.Sprintf("win-s%d", i)
		winSess = append(winSess, canarySession{
			Key: sess, AgentTypes: []string{"alpha"},
			Invocations: 20, OutputToks: 20000, CacheRead: 2000000,
		})
		winBeads = append(winBeads, canaryBead{
			ID: fmt.Sprintf("win-b%d", i), IssueType: "task", ClosedAt: at,
			Metadata: map[string]string{"gc.session_id": sess, "gc.outcome": "pass"},
		})
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: criteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d (keep); the day floor did not count local "+
			"days\nstdout:\n%s", got.exit, canaryExitKeep, got.stdout)
	}
}

// reportOnlyCriteria carries a row the evaluator measures but never judges.
const reportOnlyCriteria = oneSubjectCriteria + `
[[agent]]
type = "beta"
role = "report-only"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "none"
`

// TestCanaryReportOnlyRowsNeverChangeTheVerdict keeps the thin agents in the
// table without letting them decide anything. A type with a handful of beads
// crosses any line on noise alone, and letting it vote would make the run
// answer at random rather than decline.
func TestCanaryReportOnlyRowsNeverChangeTheVerdict(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("basea", 6, 3, 20, "alpha", &baseSess)
	baseBeads = append(baseBeads, canaryBeads("baseb", 6, 3, 20, "beta", &baseSess)...)
	winBeads := canaryBeads("wina", 6, 3, 20, "alpha", &winSess)
	winBeads = append(winBeads, canaryBeads("winb", 6, 3, 200, "beta", &winSess)...)
	got := runCanaryEval(t, canaryFixture{
		criteria: reportOnlyCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d (keep)\nstdout:\n%s",
			got.exit, canaryExitKeep, got.stdout)
	}
	if !strings.Contains(got.stdout, "beta") {
		t.Fatalf("report-only row was dropped from the table entirely:\n%s", got.stdout)
	}
}

// authorCriteria judges an agent on the beads it AUTHORED rather than on what
// its own session cost -- the only shape available for an agent whose output
// is other agents' work.
const authorCriteria = `
schema = 1

[[agent]]
type = "alpha"
role = "subject"
metric = "author_fail_rate"
author = "alpha"
margin = 0.75
min_verdicts = 10
min_days = 3
witness = "none"
`

// TestCanaryAuthorFailRateJudgesAuthoredBeadsNotSessionCost pins the second
// metric. The baseline authors 20 beads with 2 failures (10%); the window
// authors 20 with 5 (25%), which is past the 17.5% line.
//
// Constructed with the authored beads deliberately carrying NO usage session,
// because the agent being judged here is one whose own token cost is not the
// thing at risk: a planner's cost says how hard it thought, not whether it
// thought correctly.
func TestCanaryAuthorFailRateJudgesAuthoredBeadsNotSessionCost(t *testing.T) {
	mk := func(prefix string, n, fails int) []canaryBead {
		out := make([]canaryBead, 0, n)
		for i := 0; i < n; i++ {
			outcome := "pass"
			if i < fails {
				outcome = "fail"
			}
			out = append(out, canaryBead{
				ID: fmt.Sprintf("%s-b%d", prefix, i), IssueType: "task",
				ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%4)),
				CreatedBy: "alpha",
				Metadata:  map[string]string{"gc.outcome": outcome},
			})
		}
		return out
	}
	got := runCanaryEval(t, canaryFixture{
		criteria:      authorCriteria,
		baselineBeads: mk("base", 20, 2),
		windowBeads:   mk("win", 20, 5),
	})
	if got.exit != canaryExitRevert {
		t.Fatalf("exit = %d, want %d (revert)\nstdout:\n%s\nstderr:\n%s",
			got.exit, canaryExitRevert, got.stdout, got.stderr)
	}
}

// TestCanaryAuthorFailRateUnderItsVerdictFloorIsNoVerdict starves the same
// metric. A fail rate over a handful of verdicts is binomial noise: the
// baseline rate measured for this city moved between 0% and 12.5% across
// single days at n around 40, so a rule that answered at n=6 would answer
// from nothing.
func TestCanaryAuthorFailRateUnderItsVerdictFloorIsNoVerdict(t *testing.T) {
	mk := func(prefix string, n, fails int) []canaryBead {
		out := make([]canaryBead, 0, n)
		for i := 0; i < n; i++ {
			outcome := "pass"
			if i < fails {
				outcome = "fail"
			}
			out = append(out, canaryBead{
				ID: fmt.Sprintf("%s-b%d", prefix, i), IssueType: "task",
				ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%4)),
				CreatedBy: "alpha",
				Metadata:  map[string]string{"gc.outcome": outcome},
			})
		}
		return out
	}
	got := runCanaryEval(t, canaryFixture{
		criteria:      authorCriteria,
		baselineBeads: mk("base", 20, 2),
		windowBeads:   mk("win", 6, 3),
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
			got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryAnAuthoredRateRowStillGetsAWitness closes the gap that the
// witness had while it was read off the bead join.
//
// The agent judged on what it authored closes no beads of its own, so a
// witness taken from the join is silently absent for it -- and that is the row
// whose level change carries the most risk, because a planner still running
// the old level would produce a clean-looking authored-fail rate under the new
// window's name. The witness therefore comes from the agent's whole recorded
// usage, which exists whether or not it ever claimed a bead. Here the authored
// rate is flat and only the witness is wrong.
func TestCanaryAnAuthoredRateRowStillGetsAWitness(t *testing.T) {
	criteria := strings.Replace(authorCriteria,
		`witness = "none"`, `witness = "output_per_invocation_not_up"`, 1)
	mk := func(prefix string, n, fails int) []canaryBead {
		out := make([]canaryBead, 0, n)
		for i := 0; i < n; i++ {
			outcome := "pass"
			if i < fails {
				outcome = "fail"
			}
			out = append(out, canaryBead{
				ID: fmt.Sprintf("%s-b%d", prefix, i), IssueType: "task",
				ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%4)),
				CreatedBy: "alpha",
				Metadata:  map[string]string{"gc.outcome": outcome},
			})
		}
		return out
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: criteria,
		baselineUsage: []canarySession{{
			Key: "base-s0", AgentTypes: []string{"alpha"},
			Invocations: 100, OutputToks: 100000,
		}},
		windowUsage: []canarySession{{
			Key: "win-s0", AgentTypes: []string{"alpha"},
			Invocations: 100, OutputToks: 400000,
		}},
		baselineBeads: mk("base", 20, 2),
		windowBeads:   mk("win", 20, 2),
	})
	if got.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d (no verdict); an authored-rate row was "+
			"judged with no witness\nstdout:\n%s", got.exit, canaryExitNoVerdict, got.stdout)
	}
}

// TestCanaryEveryCrossedSubjectIsNamedNotJustTheFirst matters because the
// revert decision is per agent: the bead commissioning this work says so
// directly -- dropping a technician from max to high and dropping the mayor
// from max to xhigh are not the same bet. A verdict that stopped at the first
// crossing would send someone to revert one level and leave the other.
func TestCanaryEveryCrossedSubjectIsNamedNotJustTheFirst(t *testing.T) {
	criteria := `
schema = 1

[[agent]]
type = "alpha"
role = "subject"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "none"

[[agent]]
type = "beta"
role = "subject"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 5
min_days = 3
witness = "none"
`
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("basea", 6, 3, 20, "alpha", &baseSess)
	baseBeads = append(baseBeads, canaryBeads("baseb", 6, 3, 20, "beta", &baseSess)...)
	winBeads := canaryBeads("wina", 6, 3, 40, "alpha", &winSess)
	winBeads = append(winBeads, canaryBeads("winb", 6, 3, 40, "beta", &winSess)...)
	got := runCanaryEval(t, canaryFixture{
		criteria: criteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if got.exit != canaryExitRevert {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s", got.exit, canaryExitRevert, got.stdout)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("revert verdict omits %q:\n%s", want, got.stdout)
		}
	}
}

// TestCanaryJSONReportCarriesEveryRowTheTableDoes keeps the machine output
// honest. A JSON consumer that saw only the crossed rows could not tell a run
// that judged six agents from one that judged one.
func TestCanaryJSONReportCarriesEveryRowTheTableDoes(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("basea", 6, 3, 20, "alpha", &baseSess)
	baseBeads = append(baseBeads, canaryBeads("baseb", 6, 3, 20, "beta", &baseSess)...)
	winBeads := canaryBeads("wina", 6, 3, 20, "alpha", &winSess)
	winBeads = append(winBeads, canaryBeads("winb", 6, 3, 20, "beta", &winSess)...)
	got := runCanaryEval(t, canaryFixture{
		criteria: reportOnlyCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
		extraArguments: []string{"--json"},
	})
	if got.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s", got.exit, canaryExitKeep, got.stdout)
	}
	var doc struct {
		Verdict string `json:"verdict"`
		Rows    []struct {
			Type string `json:"type"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, got.stdout)
	}
	if doc.Verdict != "keep" {
		t.Fatalf("json verdict = %q, want \"keep\"", doc.Verdict)
	}
	seen := map[string]bool{}
	for _, r := range doc.Rows {
		seen[r.Type] = true
	}
	for _, want := range []string{"alpha", "beta"} {
		if !seen[want] {
			t.Fatalf("json rows omit %q: %s", want, got.stdout)
		}
	}
}
