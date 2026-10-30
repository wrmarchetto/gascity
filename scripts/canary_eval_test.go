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

// canaryExitUsage is a refusal of the criteria file, with nothing on stdout.
const canaryExitUsage = 1

// canarySession is one row of a gc usage --by session --json report, reduced
// to the fields the evaluator reads. Written as a struct rather than a raw
// JSON string so a field rename in the fixture cannot silently stop matching.
type canarySession struct {
	Key         string   `json:"key"`
	AgentTypes  []string `json:"agent_types"`
	Invocations int      `json:"invocations"`
	OutputToks  int      `json:"output_tokens"`
	CacheRead   int      `json:"cache_read_tokens"`
	SpanSeconds float64  `json:"span_seconds_lower_bound"`
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
	extraBaselines [][]canaryBead // each passed as one more --baseline-beads dump
	extraWindows   [][]canaryBead // each passed as one more --window-beads dump
	extraArguments []string
	arguments      []string // when set, the whole command line after the script
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
		"--zone", "America/Chicago",
	}
	for i, dump := range f.extraBaselines {
		args = append(args, "--baseline-beads", write(fmt.Sprintf("baseline-beads-%d.json", i+2), dump))
	}
	for i, dump := range f.extraWindows {
		args = append(args, "--window-beads", write(fmt.Sprintf("window-beads-%d.json", i+2), dump))
	}
	args = append(args, f.extraArguments...)
	if f.arguments != nil {
		args = append(args[:1:1], f.arguments...)
	}
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
	// An interpreter that could not open the script exits 2, the revert code,
	// so empty stdout fails here. A refusal prints to stderr alone and exits 1,
	// as a traceback does, so a test expecting one asserts on its text.
	if strings.TrimSpace(out.String()) == "" && exit != canaryExitUsage {
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
witness_margin = 0.20
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
witness_margin = 0.20

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

// TestCanaryTheBaselineFloorIsSeparateFromTheWindowFloor pins a distinction
// the first cut of this tool got wrong.
//
// The two sides of the comparison are not the same size and never will be:
// the baseline is a closed six-day window that cannot grow, and the canary
// window is twice that. A single floor sized for the window is therefore
// unsatisfiable on the baseline side, and the row reports "no-baseline"
// forever -- which is not a conservative failure, it is a row that silently
// never participates. Found by running the evaluator against the real
// baseline before the window opened; the planner row was dead on arrival.
//
// The default is deliberately the strict one: a row that names no baseline
// floor gets the window's, so forgetting the key cannot loosen anything.
func TestCanaryTheBaselineFloorIsSeparateFromTheWindowFloor(t *testing.T) {
	strict := `
schema = 1

[[agent]]
type = "alpha"
role = "subject"
metric = "invocations_per_bead"
margin = 0.20
min_beads = 10
min_days = 3
witness = "none"
`
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 12, 3, 20, "alpha", &winSess)

	withoutFloor := runCanaryEval(t, canaryFixture{
		criteria: strict, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if withoutFloor.exit != canaryExitNoVerdict {
		t.Fatalf("exit = %d, want %d; a baseline under the shared floor must "+
			"not be judged\nstdout:\n%s",
			withoutFloor.exit, canaryExitNoVerdict, withoutFloor.stdout)
	}

	relaxed := strings.Replace(strict, "min_beads = 10",
		"min_beads = 10\nmin_baseline_beads = 5", 1)
	withFloor := runCanaryEval(t, canaryFixture{
		criteria: relaxed, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
	})
	if withFloor.exit != canaryExitKeep {
		t.Fatalf("exit = %d, want %d; min_baseline_beads did not relax the "+
			"baseline side\nstdout:\n%s",
			withFloor.exit, canaryExitKeep, withFloor.stdout)
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

	// A bead read twice is not a second bead to split across. Counted, these
	// halve or thin the per-bead cost and read a regression as keep.
	for _, tc := range []struct {
		name                string
		extraBase, extraWin [][]canaryBead
		bead                string
	}{
		{"the same dump twice", nil, [][]canaryBead{winBeads}, "'win-b0'"},
		{"two dumps sharing a bead", nil, [][]canaryBead{winBeads[:1]}, "'win-b0'"},
		{"a baseline bead twice", [][]canaryBead{baseBeads[:1]}, nil, "'" + baseBeads[0].ID + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCanaryEval(t, canaryFixture{
				criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
				baselineBeads: baseBeads, windowBeads: winBeads,
				extraBaselines: tc.extraBase, extraWindows: tc.extraWin,
			})
			if got.exit != canaryExitUsage || !strings.Contains(got.stderr, tc.bead) ||
				strings.Contains(got.stderr, "Traceback") {
				t.Fatalf("exit = %d, want %d and a refusal naming %s\nstdout:\n%s\nstderr:\n%s",
					got.exit, canaryExitUsage, tc.bead, got.stdout, got.stderr)
			}
		})
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

// TestCanaryDayFloorCountsLocalDaysNotUTCDays: each case closes two window
// beads on one date of a wrong calendar -- UTC, or a fixed -05:00 across the
// DST change -- and two local dates in the named zone, so a floor of 2 passes
// only under local bucketing. Every other fixture closes at 18:00Z.
func TestCanaryDayFloorCountsLocalDaysNotUTCDays(t *testing.T) {
	criteria := strings.Replace(oneSubjectCriteria,
		"min_beads = 5\nmin_days = 3", "min_beads = 2\nmin_days = 2", 1)
	var baseSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)

	for _, tc := range []struct {
		name     string
		closedAt []string
	}{
		{"one UTC date", []string{"2026-09-05T02:00:00Z", "2026-09-05T18:00:00Z"}},
		// 05:30Z is 23:30 on 11-01 in CST, and 00:30 on 11-02 at -05:00.
		{"across the DST change", []string{"2026-11-02T05:30:00Z", "2026-11-02T18:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var winSess []canarySession
			winBeads := []canaryBead{}
			for i, at := range tc.closedAt {
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
		})
	}

	for _, zone := range []string{"", "Mars/Olympus", "-05:00"} {
		got := runCanaryEval(t, canaryFixture{
			criteria: criteria, baselineUsage: baseSess, windowUsage: baseSess,
			baselineBeads: baseBeads, windowBeads: baseBeads,
			extraArguments: []string{"--zone=" + zone},
		})
		if got.exit != canaryExitUsage || !strings.Contains(got.stderr, "names no IANA zone") ||
			strings.Contains(got.stderr, "Traceback") {
			t.Fatalf("--zone=%q: exit = %d, want %d and a refusal naming the zone\nstderr:\n%s",
				zone, got.exit, canaryExitUsage, got.stderr)
		}
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

// TestCanaryAnAuthoredRateWithNoDrawableLineIsNoVerdict covers a window of
// too few verdicts, and a zero baseline, whose relative line sits at zero.
func TestCanaryAnAuthoredRateWithNoDrawableLineIsNoVerdict(t *testing.T) {
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
	for _, tc := range []struct {
		name      string
		baseline  []canaryBead
		window    []canaryBead
		wantState string
	}{
		{"window under its verdict floor", mk("base", 20, 2), mk("win", 6, 3), "insufficient"},
		{"baseline with no failures", mk("base", 20, 0), mk("win", 20, 1), "no-baseline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCanaryEval(t, canaryFixture{
				criteria:      authorCriteria,
				baselineBeads: tc.baseline,
				windowBeads:   tc.window,
			})
			if got.exit != canaryExitNoVerdict {
				t.Fatalf("exit = %d, want %d (no verdict)\nstdout:\n%s",
					got.exit, canaryExitNoVerdict, got.stdout)
			}
			if !strings.Contains(got.stdout, "NO VERDICT alpha: "+tc.wantState) {
				t.Fatalf("void is not reported as %s:\n%s", tc.wantState, got.stdout)
			}
		})
	}
}

// TestCanaryABeadWithNoRecordedVerdictIsNotAPass pads the window with beads
// that closed on no outcome or a non-verdict one. Only pass and fail may
// enter the rate or the day floor, and the others are reported beside it.
func TestCanaryABeadWithNoRecordedVerdictIsNotAPass(t *testing.T) {
	mk := func(prefix string, n, fails, verdictDays int, pad string, padN int) []canaryBead {
		out := make([]canaryBead, 0, n+padN)
		for i := 0; i < n; i++ {
			outcome := "pass"
			if i < fails {
				outcome = "fail"
			}
			out = append(out, canaryBead{
				ID: fmt.Sprintf("%s-b%d", prefix, i), IssueType: "task",
				ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%verdictDays)),
				CreatedBy: "alpha",
				Metadata:  map[string]string{"gc.outcome": outcome},
			})
		}
		for i := 0; i < padN; i++ {
			meta := map[string]string{}
			if pad != "" {
				meta["gc.outcome"] = pad
			}
			out = append(out, canaryBead{
				ID: fmt.Sprintf("%s-u%d", prefix, i), IssueType: "task",
				ClosedAt:  fmt.Sprintf("2026-09-%02dT18:00:00Z", 4+(i%4)),
				CreatedBy: "alpha",
				Metadata:  meta,
			})
		}
		return out
	}
	// 5 fails in 20 is 25% against a 17.5% line. Forty padding beads folded
	// in as passes read 8.3%, inside it.
	for _, tc := range []struct {
		name        string
		pad         string
		verdictDays int
		want        int
		reported    string
	}{
		{"no outcome", "", 4, canaryExitRevert, "outside the rate: - -> -"},
		{"canceled", "canceled", 4, canaryExitRevert, "canceled 40"},
		{"skipped", "skipped", 4, canaryExitRevert, "skipped 40"},
		{"missing_root", "missing_root", 4, canaryExitRevert, "missing_root 40"},
		{
			"a day of only canceled beads covers no day", "canceled", 1,
			canaryExitNoVerdict, "covered 1 local days",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCanaryEval(t, canaryFixture{
				criteria:      authorCriteria,
				baselineBeads: mk("base", 20, 2, 4, "", 0),
				windowBeads:   mk("win", 20, 5, tc.verdictDays, tc.pad, 40),
			})
			if got.exit != tc.want {
				t.Fatalf("exit = %d, want %d; beads closed with no verdict "+
					"entered the rate or the day floor\nstdout:\n%s",
					got.exit, tc.want, got.stdout)
			}
			if !strings.Contains(got.stdout, tc.reported) {
				t.Fatalf("report does not carry %q:\n%s", tc.reported, got.stdout)
			}
		})
	}
}

// TestCanaryAnAuthoredRateRowStillGetsAWitness reads the witness off the
// agent's whole usage, since an authoring agent closes no beads, and against
// a witness_margin of its own: the planner's 1.05 rate margin, reused, let
// output per invocation double unseen.
func TestCanaryAnAuthoredRateRowStillGetsAWitness(t *testing.T) {
	witnessed := strings.Replace(authorCriteria, "margin = 0.75", "margin = 1.05", 1)
	witnessed = strings.Replace(witnessed,
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
	run := func(criteria string) canaryResult {
		return runCanaryEval(t, canaryFixture{
			criteria: criteria,
			baselineUsage: []canarySession{{
				Key: "base-s0", AgentTypes: []string{"alpha"},
				Invocations: 100, OutputToks: 100000,
			}},
			windowUsage: []canarySession{{
				Key: "win-s0", AgentTypes: []string{"alpha"},
				Invocations: 100, OutputToks: 200000,
			}},
			baselineBeads: mk("base", 20, 2),
			windowBeads:   mk("win", 20, 2),
		})
	}

	t.Run("a doubled output voids against its own margin", func(t *testing.T) {
		got := run(witnessed + "witness_margin = 0.20\n")
		if got.exit != canaryExitNoVerdict {
			t.Fatalf("exit = %d, want %d (no verdict); an authored-rate row was "+
				"judged with no witness\nstdout:\n%s",
				got.exit, canaryExitNoVerdict, got.stdout)
		}
	})
	t.Run("a witness with no margin of its own is refused", func(t *testing.T) {
		got := run(witnessed)
		if got.exit != canaryExitUsage ||
			!strings.Contains(got.stderr, "row 'alpha'") ||
			!strings.Contains(got.stderr, "no witness_margin") ||
			strings.Contains(got.stderr, "Traceback") {
			t.Fatalf("exit = %d, want %d with a refusal naming the row\n"+
				"stdout:\n%s\nstderr:\n%s",
				got.exit, canaryExitUsage, got.stdout, got.stderr)
		}
	})
}

// TestCanaryAMalformedCriteriaRowIsRefusedNotReadLooser runs each malformation
// over a window whose correct verdict is revert, so a rule that fell back to a
// default reads keep or revert instead of the exit-1 refusal.
func TestCanaryAMalformedCriteriaRowIsRefusedNotReadLooser(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 40, "alpha", &winSess)
	run := func(criteria string) canaryResult {
		return runCanaryEval(t, canaryFixture{
			criteria: criteria, baselineUsage: baseSess, windowUsage: winSess,
			baselineBeads: baseBeads, windowBeads: winBeads,
		})
	}
	if got := run(oneSubjectCriteria); got.exit != canaryExitRevert {
		t.Fatalf("well-formed criteria: exit = %d, want %d, so no case below can "+
			"tell a refusal from a misreading\nstdout:\n%s\nstderr:\n%s",
			got.exit, canaryExitRevert, got.stdout, got.stderr)
	}

	for _, tc := range []struct {
		name, find, replace string
		want                []string
	}{
		{
			"misspelled role", `role = "subject"`, `role = "subjet"`,
			[]string{"row 'alpha' has role 'subjet'"},
		},
		{
			"no role", "role = \"subject\"\n", "",
			[]string{"row 'alpha' states no role"},
		},
		{
			"no type", "type = \"alpha\"\n", "",
			[]string{"[[agent]] row 1 states no type"},
		},
		{
			"misspelled metric", `metric = "invocations_per_bead"`,
			`metric = "invocation_per_bead"`,
			[]string{"row 'alpha' has metric 'invocation_per_bead'"},
		},
		{
			"misspelled witness", `witness = "output_per_invocation_not_up"`,
			`witness = "output_per_invocation_not_upp"`,
			[]string{"row 'alpha' has witness 'output_per_invocation_not_upp'"},
		},
		{
			"misspelled floor key", "min_beads = 5", "min_bead = 5",
			[]string{
				"row 'alpha' has unknown key 'min_bead'",
				"row 'alpha' states no min_beads",
			},
		},
		{
			"no day floor", "min_days = 3\n", "",
			[]string{"row 'alpha' states no min_days"},
		},
		{
			"a floor its metric never reads", "min_beads = 5",
			"min_beads = 5\nmin_verdicts = 5",
			[]string{"row 'alpha' has unknown key 'min_verdicts'"},
		},
		{
			"an author on a row judged on session cost", "min_beads = 5",
			"min_beads = 5\nauthor = \"alpha\"",
			[]string{"row 'alpha' has unknown key 'author'"},
		},
		{
			"a witness margin on an unwitnessed row",
			`witness = "output_per_invocation_not_up"`, `witness = "none"`,
			[]string{"row 'alpha' has unknown key 'witness_margin'"},
		},
		{"no schema", "schema = 1\n", "", []string{"schema is None, expected 1"}},
		{
			"an unknown schema", "schema = 1", "schema = 2",
			[]string{"schema is 2, expected 1"},
		},
		{
			"an unknown top-level key", "schema = 1", "schema = 1\nwindow_days = 14",
			[]string{"unknown top-level key 'window_days'"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(oneSubjectCriteria, tc.find) {
				t.Fatalf("fixture carries no %q to replace", tc.find)
			}
			got := run(strings.Replace(oneSubjectCriteria, tc.find, tc.replace, 1))
			if got.exit != canaryExitUsage || got.stdout != "" ||
				strings.Contains(got.stderr, "Traceback") {
				t.Fatalf("exit = %d, want %d with nothing on stdout and no "+
					"traceback\nstdout:\n%s\nstderr:\n%s",
					got.exit, canaryExitUsage, got.stdout, got.stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(got.stderr, want) {
					t.Fatalf("refusal does not say %q:\n%s", want, got.stderr)
				}
			}
		})
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

// TestCanaryReportCarriesTokensAndWallClockNotJustTheTriggerMetric pins the
// reported columns against quiet loss.
//
// gs-jbyc asks the window to compare per-bead tokens, wall-clock and rework
// signals. Only one figure fires the revert rule -- deliberately, since the
// lever under test moves tokens per turn by construction -- but the other two
// are part of the answer, and a tool that computed them and printed only its
// own trigger would satisfy every other test in this file while delivering
// less than was asked for.
//
// Wall-clock carries its observation count beside it. The baseline records a
// span for most sessions and a true wall-clock for 8.5% of them, so a row
// reporting a small number needs to say whether that is a short window or a
// thin one; a bare figure cannot distinguish not-observed from zero.
func TestCanaryReportCarriesTokensAndWallClockNotJustTheTriggerMetric(t *testing.T) {
	var baseSess, winSess []canarySession
	baseBeads := canaryBeads("base", 6, 3, 20, "alpha", &baseSess)
	winBeads := canaryBeads("win", 6, 3, 20, "alpha", &winSess)
	for i := range winSess {
		winSess[i].SpanSeconds = 900
	}
	for i := range baseSess {
		baseSess[i].SpanSeconds = 600
	}
	got := runCanaryEval(t, canaryFixture{
		criteria: oneSubjectCriteria, baselineUsage: baseSess, windowUsage: winSess,
		baselineBeads: baseBeads, windowBeads: winBeads,
		extraArguments: []string{"--json"},
	})
	var doc struct {
		Rows []struct {
			Type                 string   `json:"type"`
			BaselineOutputPerB   *float64 `json:"baseline_output_tokens_per_bead"`
			WindowOutputPerBead  *float64 `json:"window_output_tokens_per_bead"`
			BaselineCachePerBead *float64 `json:"baseline_cache_read_tokens_per_bead"`
			WindowCachePerBead   *float64 `json:"window_cache_read_tokens_per_bead"`
			BaselineSpanPerBead  *float64 `json:"baseline_span_seconds_per_bead"`
			WindowSpanPerBead    *float64 `json:"window_span_seconds_per_bead"`
			WindowSpanObserved   *int     `json:"window_span_observed_beads"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, got.stdout)
	}
	if len(doc.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(doc.Rows))
	}
	row := doc.Rows[0]
	for name, field := range map[string]*float64{
		"baseline_output_tokens_per_bead":     row.BaselineOutputPerB,
		"window_output_tokens_per_bead":       row.WindowOutputPerBead,
		"baseline_cache_read_tokens_per_bead": row.BaselineCachePerBead,
		"window_cache_read_tokens_per_bead":   row.WindowCachePerBead,
		"baseline_span_seconds_per_bead":      row.BaselineSpanPerBead,
		"window_span_seconds_per_bead":        row.WindowSpanPerBead,
	} {
		if field == nil {
			t.Fatalf("json row carries no %s:\n%s", name, got.stdout)
		}
	}
	if *row.WindowOutputPerBead != 20000 {
		t.Fatalf("window output tokens per bead = %v, want 20000", *row.WindowOutputPerBead)
	}
	if *row.WindowSpanPerBead != 900 {
		t.Fatalf("window span seconds per bead = %v, want 900", *row.WindowSpanPerBead)
	}
	if row.WindowSpanObserved == nil || *row.WindowSpanObserved != 6 {
		t.Fatalf("window span observation count missing or wrong; a span figure "+
			"with no count cannot distinguish a thin window from a short one: %v",
			row.WindowSpanObserved)
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

// TestCanaryAMalformedCommandLineIsAUsageErrorNotARevert: stock argparse exits
// 2 on a usage error, the revert code, and a reading is taken by its exit.
func TestCanaryAMalformedCommandLineIsAUsageErrorNotARevert(t *testing.T) {
	criteria := filepath.Join(t.TempDir(), "criteria.toml")
	if err := os.WriteFile(criteria, []byte(oneSubjectCriteria), 0o644); err != nil {
		t.Fatalf("write criteria: %v", err)
	}
	for _, tc := range []struct {
		name             string
		arguments, extra []string
		want             string
	}{
		{"no criteria", []string{"--zone", "America/Chicago"}, nil, "--criteria"},
		{"misspelled flag", []string{"--criteria", criteria, "--windowbeads", "x"}, nil, "--windowbeads"},
		{"the fetch script's --window", []string{"--criteria", criteria, "--window", "x"}, nil, "--window"},
		{"a unique prefix", nil, []string{"--zon", "UTC"}, "--zon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCanaryEval(t, canaryFixture{
				criteria: oneSubjectCriteria, arguments: tc.arguments, extraArguments: tc.extra,
			})
			if got.exit != canaryExitUsage || !strings.Contains(got.stderr, tc.want) ||
				strings.Contains(got.stderr, "Traceback") {
				t.Fatalf("exit = %d, want %d and an error naming %s\nstderr:\n%s",
					got.exit, canaryExitUsage, tc.want, got.stderr)
			}
		})
	}
}
