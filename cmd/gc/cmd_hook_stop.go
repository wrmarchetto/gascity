// cmd/gc/cmd_hook_stop.go
//
// gc hook stop -- the Stop-event gate that refuses to end an agent turn while
// the session's closing contract is still unfinished.
//
// Why this exists: an agent that finishes its engineering work, writes its
// narrative summary, and ends the turn there leaves the rest of its prompt
// contract -- set result metadata, close the bead, gc runtime drain-ack --
// simply never run. The session then idles at its prompt forever. Nothing in
// the city recovered that state before this gate: the nudge machinery is
// wired to UserPromptSubmit, which fires only when a prompt IS submitted, so
// the one recovery path structurally cannot reach a session that has already
// stopped submitting. gc status reports the session running, which is true
// and useless. For an agent capped at max_active_sessions = 1 the stall
// blocks the whole queue behind it.
//
// Why a gate and not more prompt text: the prompt already says it plainly
// ("No confirmation, no waiting... When the work is closed, drain and exit")
// and gives the closing block verbatim. A rule with no gate behind it rots;
// this is the gate.
//
// Where it is NOT installed, and this absence is deliberate:
// internal/hooks/config/claude.json. That template is //go:embed-ed into the
// binary and written into EVERY city gc installs, so wiring the gate there
// ships one deployment's closing contract to deployments that have no such
// contract and no gc hook stop expectation. A city that wants the gate adds a
// Stop block to its own <city>/.claude/settings.json, which
// internal/hooks/hooks.go's desiredClaudeSettings merges OVER the embedded
// defaults -- so that city gains the gate and keeps inheriting every default
// added later. TestInstallClaudeMergesCityOverrideAddingANewHookEvent in
// internal/hooks/hooks_test.go pins both halves. The command itself stays
// unconditionally registered: an unwired gate costs nothing, and a city
// wiring one that does not exist is the worse failure.
//
// Editing constraints, each of which can make this worse than the bug it
// fixes. TestStopGate* in cmd_hook_stop_test.go pins all four:
//
//  1. The gate must no-op when the session holds no claimed work. The
//     settings file carrying it is shared by EVERY session in the city,
//     including long-lived named sessions that idle legitimately. A gate that
//     fires on an idle session traps it in a loop it cannot exit.
//  2. stop_hook_active must be honored, first and unconditionally. The
//     provider re-enters this hook with that flag set after a block; ignoring
//     it spins a genuinely stuck agent forever. One block per stop sequence
//     is the whole budget.
//  3. Every path except a PROVEN-incomplete contract fails open. A store that
//     will not answer, an unresolvable agent, a suspended city -- none of
//     those may wedge every session in the city at its stop boundary.
//  4. The blocking verdict must be reachable in a test that fails when the
//     gate is inert. A stop-gate test asserting only that clean sessions pass
//     cannot tell a working gate from a hook that never fires.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Stop-hook exit codes. The provider reads the gate's verdict from the exit
// status alone: 2 blocks the stop and feeds stderr back to the agent as the
// reason, anything else lets the turn end (0 silently, other codes after
// showing stderr to the operator). There is deliberately no JSON decision
// payload -- the exit-code form carries the whole contract and needs no
// schema to stay in sync.
const (
	stopGateAllowExitCode = 0
	stopGateBlockExitCode = 2
)

// sessionOriginEphemeral marks a session the controller spawned to service
// routed work, as opposed to a named holder or an operator's manual session.
// It is the discriminator for the drain-ack half of the gate: only an
// ephemeral worker owes the controller a drain-ack, because only its exit
// releases an agent concurrency slot. config/workquery.go's
// poolDemandOriginGateScript gates on the same value for the same reason.
const sessionOriginEphemeral = "ephemeral"

// stopHookInput is the provider's Stop-event payload. Only stop_hook_active
// is read: the remaining fields (session_id, transcript_path, cwd) describe
// the provider's own session, and this gate reads the CITY's session identity
// from the environment instead, which is the identity the work beads carry.
type stopHookInput struct {
	StopHookActive bool `json:"stop_hook_active"`
}

// stopGateFacts is the complete input to the gate's decision. Every field is
// established by the caller and passed in, so evaluateStopGate stays a pure
// function with no store, environment, or provider access of its own -- the
// blocking branch is then reachable from a test without faking a whole city.
type stopGateFacts struct {
	stopHookActive   bool
	sessionID        string
	sessionOrigin    string
	restartRequested bool
	drainAcked       bool
	// outstanding holds the ids of beads still on this session's hook:
	// in_progress, assigned to one of this session's identities, and not
	// dependency-blocked. The query that produces it already excludes mail and
	// blocked steps, so anything here is work this session can act on right now.
	outstanding []string
	// unknownWork records that the outstanding-work query could not be
	// answered. Distinct from an empty outstanding list, because "no work" and
	// "could not tell" must reach opposite verdicts on a gate that fails open.
	unknownWork bool
}

// stopGateVerdict is the gate's decision plus the text handed back to the
// agent when it blocks. The reason is the agent's only prompt to finish, so
// it names the outstanding ids and the exact remaining commands.
type stopGateVerdict struct {
	block  bool
	reason string
}

// evaluateStopGate decides whether the turn may end.
//
// The order of the early allows is load-bearing and is the loop-safety
// argument: stop_hook_active is checked before anything else, so a session
// the gate has already blocked once always gets to stop on its next attempt
// no matter what the store says.
func evaluateStopGate(f stopGateFacts) stopGateVerdict {
	allow := stopGateVerdict{}
	switch {
	case f.stopHookActive:
		// The provider is already continuing because this gate blocked. One
		// block per stop sequence, unconditionally.
		return allow
	case f.sessionID == "":
		// Not a managed agent session -- an operator's own provider run in the
		// city directory picks up the same settings file and has no contract.
		return allow
	case f.restartRequested:
		// Waiting to be killed by the controller. The bead is deliberately left
		// open for the replacement session; prompting this one to finish would
		// ask it to do work it has already decided it lacks context for.
		return allow
	case f.unknownWork:
		// Could not tell. Not the same as "nothing outstanding": blocking here
		// would wedge every session in the city the moment the store went down.
		return allow
	}
	if len(f.outstanding) > 0 {
		return stopGateVerdict{block: true, reason: stopGateOutstandingReason(f)}
	}
	// No claimed work. Only an ephemeral worker still owes a drain-ack; a named
	// holder never received a drain to acknowledge, so gating it on drainAcked
	// would block it at every turn boundary with no way out.
	if !strings.EqualFold(strings.TrimSpace(f.sessionOrigin), sessionOriginEphemeral) {
		return allow
	}
	if f.drainAcked {
		return allow
	}
	return stopGateVerdict{block: true, reason: stopGateDrainAckReason()}
}

// stopGateOutstandingReason renders the block text for a session still
// holding claimed work.
//
// It names ids and the shape of the remaining steps rather than a literal
// command line, because the exact bead invocation differs per agent -- an
// agent whose work_dir sits outside the city calls `gc bd`, every other agent
// calls `bd`. Spelling one of them here would hand half the fleet a command
// that fails.
func stopGateOutstandingReason(f stopGateFacts) string {
	var b strings.Builder
	b.WriteString("Stop blocked: this session's closing contract is not finished.\n\n")
	b.WriteString("Still open and assigned to this session:\n")
	for _, id := range f.outstanding {
		fmt.Fprintf(&b, "  %s\n", id)
	}
	b.WriteString("\nFinish the closing steps your prompt specifies for each id above")
	b.WriteString(" -- set the result metadata the bead asks for, then close it")
	b.WriteString(" with a reason saying what changed and how it was verified.\n")
	if strings.EqualFold(strings.TrimSpace(f.sessionOrigin), sessionOriginEphemeral) {
		b.WriteString("\nThen, as your final action:\n  gc runtime drain-ack\n")
	}
	b.WriteString("\nIf the work cannot be completed, close it as failed with a")
	b.WriteString(" failure class rather than leaving it open. If you need a")
	b.WriteString(" decision before you can close it, mail the mayor and say so")
	b.WriteString(" in the close reason -- but do not end the turn with the bead")
	b.WriteString(" still on your hook.\n")
	return b.String()
}

// stopGateDrainAckReason renders the block text for an ephemeral session that
// finished its work but never released its slot.
//
// This is the second observed shape of the bug and it is NOT covered by the
// outstanding-work branch: the bead was closed correctly and the turn still
// ended one command early. The session then holds its agent's concurrency
// slot with nothing left to do.
func stopGateDrainAckReason() string {
	return "Stop blocked: this session closed its work but has not released" +
		" its slot. The controller keeps the session -- and the agent" +
		" concurrency slot behind it -- alive until the drain is" +
		" acknowledged.\n\nRun your final closing action now:\n" +
		"  gc runtime drain-ack\n"
}

// newHookStopCmd builds `gc hook stop`.
//
// Takes no stdout writer, unlike its sibling constructors: the gate's entire
// verdict travels on the exit code and stderr (the text the provider feeds
// back to the agent), so there is no stdout channel to hand it. Adding one
// would be worse than unused -- gc hook run buffers child stdout and flushes
// it to the provider, where on a Stop event it is not injected anywhere.
func newHookStopCmd(stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Refuse to end a turn while the session's closing contract is unfinished",
		Long: `Stop-event gate for agent sessions.

Reads the provider's Stop hook payload on stdin. Exits 2 (blocking the stop,
with the reason on stderr) when this session still holds claimed work that is
not closed, or when an ephemeral session has finished its work but has not
acknowledged its drain. Exits 0 in every other case, including any case the
gate cannot establish: a session with no claimed work must always be able to
end its turn.

Wired as the Stop hook in the city's managed settings file. Not intended for
manual use, though running it by hand is safe and reports the same verdict.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return exitForCode(cmdHookStop(c.InOrStdin(), stderr))
		},
	}
}

// cmdHookStop gathers the gate's facts and applies the verdict.
//
// Fail-open is enforced here rather than at each fact: every gathering step
// that cannot answer leaves its fact unset (or sets unknownWork) and the
// verdict function turns that into an allow. The only exit path that can
// block is evaluateStopGate returning block on established facts.
func cmdHookStop(stdin io.Reader, stderr io.Writer) int {
	facts := gatherStopGateFacts(stdin, stderr)
	verdict := evaluateStopGate(facts)
	if !verdict.block {
		return stopGateAllowExitCode
	}
	fmt.Fprint(stderr, verdict.reason) //nolint:errcheck // best-effort stderr
	return stopGateBlockExitCode
}

// gatherStopGateFacts reads the gate's inputs from the provider payload, the
// session environment, and the city's stores.
//
// The two cheap environment facts are read before any store access so the
// common allow paths -- a re-entered hook, a session with no city identity --
// cost nothing. A session with no GC_SESSION_ID is not a managed agent
// session at all (an operator running the provider by hand in the city
// directory lands here) and must never be gated.
func gatherStopGateFacts(stdin io.Reader, stderr io.Writer) stopGateFacts {
	facts := stopGateFacts{
		stopHookActive: readStopHookActive(stdin),
		sessionID:      strings.TrimSpace(os.Getenv("GC_SESSION_ID")),
		sessionOrigin:  strings.TrimSpace(os.Getenv("GC_SESSION_ORIGIN")),
	}
	if facts.stopHookActive || facts.sessionID == "" {
		return facts
	}
	facts.restartRequested, facts.drainAcked = readStopGateSessionSignals(stderr)
	if facts.restartRequested {
		return facts
	}
	facts.outstanding, facts.unknownWork = readStopGateOutstandingWork(stderr)
	return facts
}

// readStopHookActive reports the provider's stop_hook_active flag.
//
// An absent or unparseable payload reports false, which evaluates the gate
// normally. That is the safe direction here even though it is the
// less-forgiving one: the provider always writes the payload on a pipe, so an
// absent payload means a human ran the command by hand, where the verdict is
// informational and there is no turn to loop. Reporting true instead would
// disable the gate entirely for any provider that changed its payload shape,
// and it would do so silently.
func readStopHookActive(stdin io.Reader) bool {
	data := readStopHookStdin(stdin)
	if len(data) == 0 {
		return false
	}
	var in stopHookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return false
	}
	return in.StopHookActive
}

// readStopHookStdin returns the provider payload, skipping an interactive
// terminal. Mirrors readHookStdin's char-device guard: a terminal stdin never
// reaches EOF, so reading it during a manual invocation would hang the
// command instead of reporting a verdict.
func readStopHookStdin(stdin io.Reader) []byte {
	if stdin == nil {
		return nil
	}
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return nil
	}
	return data
}

// readStopGateSessionSignals reports (restartRequested, drainAcked) for the
// current session.
//
// The two defaults are deliberately OPPOSITE, and reading them as a matched
// pair is the mistake to avoid. restartRequested defaults to false so an
// unreadable signal keeps the gate evaluating; drainAcked defaults to TRUE so
// an unreadable signal cannot manufacture a block. The asymmetry is forced by
// which direction each fact blocks in: drainAcked is consulted exactly when
// the outstanding list is an established empty, so a false there is the whole
// verdict, and an infrastructure fault would tell every ephemeral session in
// the city to drain-ack simultaneously.
//
// All three ways this can fail to establish drain state -- no session context,
// no session provider, an unreadable drain flag -- therefore report acked.
// TestStopGateSessionSignalsFailOpenWhenSessionContextIsUnavailable pins the
// first two, which is where they previously disagreed with the third.
func readStopGateSessionSignals(stderr io.Writer) (bool, bool) {
	target, err := currentSessionRuntimeTarget()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook stop: session context unavailable: %v\n", err) //nolint:errcheck
		return false, true
	}
	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook stop: session provider unavailable: %v\n", err) //nolint:errcheck
		return false, true
	}
	dops := newDrainOps(sp)
	restart, err := dops.isRestartRequested(target.sessionName)
	if err != nil {
		restart = false
	}
	acked, err := dops.isDrainAcked(target.sessionName)
	if err != nil {
		// Unreadable drain state is not "not acked": an ephemeral session whose
		// session bead has already gone would otherwise be blocked for failing to
		// ack a drain nothing can record. Report acked so the gate falls through.
		return restart, true
	}
	return restart, acked
}

// hookStopProbe carries the stop gate's read-only query result back out of
// cmdHookWithOptions. Err is set for a query that could not be answered, which
// the gate must distinguish from an answered "nothing outstanding" -- the two
// reach opposite verdicts.
//
// Declared here rather than beside the hookCommandOptions field that carries
// it: cmd_hook.go is upstream-owned and this type is not, so keeping it in
// this file leaves the gate one hunk in that file instead of two. The field
// and its branch cannot follow -- they ARE the seam, and duplicating
// cmdHookWithOptions' store resolution to avoid them would let the gate and
// the claim drift apart, which is the one thing the design forbids.
type hookStopProbe struct {
	Outstanding []beads.Bead
	Err         error
}

// probeStopGateOutstanding runs the agent's assigned-in-progress query across
// the federated store set and records the decoded beads on probe.
//
// The query is EffectiveAssignedInProgressQueryForBeads rather than the
// ready-work query the claim path uses: the gate asks "what is still on this
// session's hook", not "what could this session pick up next". The template
// expansion mirrors the claim path's for the same reason it exists there -- a
// user-supplied work_query is the caller-owned discovery contract and is
// returned verbatim by the assigned-in-progress builder, so its {{.Rig}} and
// {{.AgentBase}} placeholders must be expanded here too or the query runs
// with literal template text in it.
//
// The FIRST store with rows wins; later stores are not consulted. One
// outstanding bead anywhere is enough to block, and the ids only feed the
// reason text.
func probeStopGateOutstanding(cfg *config.City, cityPath, cityName string, a *config.Agent, stores []hookStore, probe *hookStopProbe, stderr io.Writer) {
	query := a.EffectiveAssignedInProgressQueryForBeads(cfg.Beads)
	query = expandAgentCommandTemplate(cityPath, cityName, a, cfg.Rigs, "work_query", query, stderr)
	var lastErr error
	answered := 0
	for _, st := range stores {
		out, err := shellWorkQueryWithEnv(query, st.dir, st.env)
		if err != nil {
			lastErr = err
			continue
		}
		candidates, _, decodeErr := decodeHookClaimBeads(strings.TrimSpace(out))
		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		if len(candidates) > 0 {
			probe.Outstanding = candidates
			return
		}
		answered++
	}
	// At least one store answered "nothing" -> that is an established no-work
	// fact and Err stays nil, so a single flaky rig store alongside a healthy
	// city store still yields a verdict instead of a fail-open. Err is set only
	// when NO store answered, which is the genuine "could not tell" case.
	if answered == 0 && lastErr != nil {
		probe.Err = lastErr
	}
}

// readStopGateOutstandingWork returns the ids of beads still on this
// session's hook, and whether the query could be answered at all.
//
// It runs the agent's assigned-in-progress query over the SAME federated
// store set gc hook --claim uses, so the gate sees exactly the beads the
// claim saw. That query already excludes mail beads and dependency-blocked
// steps, which is the behavior wanted here for free: a step this session
// cannot progress must not hold its turn open.
func readStopGateOutstandingWork(stderr io.Writer) ([]string, bool) {
	probe := &hookStopProbe{}
	opts := hookCommandOptions{StopProbe: probe}
	if cmdHookWithOptions(nil, opts, io.Discard, stderr) != 0 || probe.Err != nil {
		return nil, true
	}
	ids := make([]string, 0, len(probe.Outstanding))
	for _, bead := range probe.Outstanding {
		if id := strings.TrimSpace(bead.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, false
}
