package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beads"
)

// The close-time refusal that stops `gc bd close --reason` reporting a success
// it did not perform.
//
// Why this exists at all, rather than being fixed where the defect is: beads'
// close is one UPDATE guarded by the row's own status --
//
//	UPDATE issues SET status=?, closed_at=?, updated_at=?, close_reason=?, ...
//	  WHERE id = ? AND status != 'closed'
//
// (beads internal/storage/issueops/close.go, closeIssueInTx). The trailing
// status test is what makes a re-close idempotent on LIFECYCLE state, and in
// doing so it discards the close_reason write too. rows==0 then returns
// AlreadyClosed, and cmd/bd/close.go deliberately keeps output parity with the
// old path: it prints its success line naming the NEW reason and exits 0 while
// the store keeps the OLD one, with revision and updated_at untouched. So the
// stored record is corrected in the operator's terminal and nowhere else
// (ci-yh6v84).
//
// The simpler fix a future editor will reach for is to correct the reason
// instead of refusing -- either reopen-then-close, or a direct write to the
// close_reason column through the store bridge. Both were rejected. Reopen
// rewrites closed_at and emits a reopen/close event pair, so an audit trail
// gains two lifecycle transitions that never happened; the direct column write
// bypasses bd's row_lock, which is the cell a concurrent reclaim collides on,
// and a close that does not rewrite it is exactly the silent cell-merge that
// lock exists to prevent. Refusing costs the caller one extra command and
// invents no history.
//
// It BLOCKS rather than warning, unlike the work-record gate next door, which
// ships warn-only behind GC_WORK_RECORD_ENFORCE. A warning would not restore
// the invariant: it lands on stderr beside bd's own success line and leaves
// the exit status 0, so every script and agent that checks the exit code still
// reads the close as recorded. Exit status is the only channel the caller
// cannot miss.
//
// DELIBERATELY NOT FIXED UPSTREAM, where it belongs: beads is a third-party
// module with no writable checkout on this host, and the required check
// scripts/check-gomod-replace.sh refuses a replace directive to a local fork --
// "automated workers may NEVER self-authorize an unreleased dependency". So
// bare `bd close` keeps the defect; only the `gc bd` seam is guarded. Present
// in beads through 62d211937bd3 (2026-08-25), the newest commit available
// here, i.e. upstream has not fixed it.
//
// Invariants, all pinned by close_reason_discard_gate_test.go:
//   - An identical-reason re-close is NEVER refused. beads replays
//     last-touched, --continue, --suggest-next, --claim-next and molecule
//     auto-close healing on it, and a stranded molecule root is a worse
//     failure than the one this gate closes.
//   - The gate refuses only what it can establish. An unreadable stored
//     reason, an unread bead, or an argv it cannot parse all fall open.
//   - Every spelling of close is covered, read from bdflags.AliasGroup rather
//     than listed.

// closeReasonProbeArgs is the read that establishes the stored reason.
// close_reason is absent from beads.Bead -- it is a Huma response and SSE wire
// type, so adding a field there reaches the OpenAPI spec and the generated
// dashboard types -- and the pre-fetched bead therefore supplies only the
// status. That status is what keeps this subprocess off the common path: it
// runs only for a close that is already closed AND carries a --reason.
func closeReasonProbeArgs(id string) []string {
	return []string{"show", id, "--json"}
}

// runCloseReasonDiscardGate refuses a `gc bd close --reason` that would report
// success while beads discards the new reason. preFetched is the write-ID
// guard's already-read beads, reused for their status -- this gate opens no
// store of its own.
//
// The argv is screened before the runner is built. bdCommandRunnerForCity
// reads the scope's storage binding from disk, and this gate sits on the path
// every `gc bd` command takes, so constructing it unconditionally would charge
// `gc bd list` for a close-only guard. Parsing the argv twice for that is
// free by comparison.
func runCloseReasonDiscardGate(bdArgs []string, cityPath string, preFetched map[string]beads.Bead, scopeRoot string, stderr io.Writer) bool {
	if _, _, ok := closeReasonRewriteTargets(bdArgs); !ok {
		return false
	}
	return evaluateCloseReasonDiscardGate(bdArgs, preFetched, bdCommandRunnerForCity(cityPath), scopeRoot, stderr)
}

// evaluateCloseReasonDiscardGate is the injectable half. runBd is a real
// dependency rather than a mode flag: a flag would be read before the gate
// ran, so a suite would pin the short-circuit and the comparison below could
// be stubbed out with every test still green.
func evaluateCloseReasonDiscardGate(bdArgs []string, preFetched map[string]beads.Bead, runBd beads.CommandRunner, scopeRoot string, stderr io.Writer) (block bool) {
	ids, reasons, ok := closeReasonRewriteTargets(bdArgs)
	if !ok || runBd == nil {
		return false
	}
	for i, id := range ids {
		bead, cached := preFetched[id]
		// Not pre-fetched means the write-ID guard could not read it: an
		// ephemeral row, a projection lag, or an unavailable store. There is no
		// status to reason from, and a refusal built on an assumed one would
		// block a legitimate first close.
		if !cached || !strings.EqualFold(strings.TrimSpace(bead.Status), "closed") {
			continue
		}
		stored, err := storedCloseReason(runBd, scopeRoot, id)
		if err != nil {
			continue
		}
		requested := reasonForCloseIndex(reasons, i)
		// Exact bytes, deliberately not TrimSpace'd on either side. bd stores
		// a reason verbatim -- `--reason "  padded  "` round-trips with both
		// runs of spaces intact (measured 2026-09-07 against bd
		// bf97b73749ac), and nonEmptyCloseReasons only drops the empty string
		// rather than trimming. Trimming here would clear a rewrite that
		// changed nothing but whitespace, which is still a rewrite bd will
		// discard.
		if stored == requested {
			continue
		}
		refusal := fmt.Sprintf("gc bd: close-reason gate: %s is already closed, so beads would discard this --reason while printing success: stored %q, requested %q.\n"+
			"  Correct the recorded reason with: gc bd reopen %s && gc bd close %s --reason %q\n"+
			"  Re-run the close unchanged (same --reason, or none) if this was an idempotent retry.\n",
			id, stored, requested, id, id, requested)
		fmt.Fprint(stderr, refusal) //nolint:errcheck // best-effort stderr
		block = true
	}
	return block
}

// closeReasonRewriteTargets reports the close targets and the --reason values
// of a bd argv that could lose a reason, or ok=false for every argv that
// cannot.
//
// The verb is located with bdflags.SplitGlobalFlags rather than read from
// args[0]. The naive read takes "bob" out of
// `bd --actor bob close <id> --reason ...`, and the gate then stops firing for
// exactly the authors who pass an explicit actor -- with its own suite green,
// because it parses the argv it is handed and is simply never handed this one
// (the failure bd_prewrite.go records for the pre-write validator).
//
// TWO DELIBERATE HOLES, both pinned by the suite so closing either is a
// decision:
//   - --reason-file. The requested reason would have to be re-derived from a
//     file, and a derivation that trims differently from bd's refuses a
//     legitimate close. The status quo only loses a reason.
//   - `bd done <id> <reason>`, where the alias takes its LAST positional as
//     the reason (cmd/bd/close.go resolveCloseReasons). That token is
//     indistinguishable here from a second bead id, and reading it as a reason
//     would make a two-id close unguardable.
//
// Absent for a third reason: a close carrying NO reason. bd defaults it to
// "Closed", so nothing the caller authored is discarded, and refusing would
// reject every bare idempotent re-close in the city.
func closeReasonRewriteTargets(bdArgs []string) (ids, reasons []string, ok bool) {
	verb, rest := bdflags.SplitGlobalFlags(bdArgs)
	if verb == "" || !isCloseSpelling(verb) {
		return nil, nil, false
	}
	valueFlags := bdflags.ValueFlags("close")
	boolFlags := bdflags.BoolFlags("close")
	positional := false
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if positional {
			// bd parses no flags past "--", so every remaining token is a
			// bead id and no reason can appear here.
			if arg != "" {
				ids = append(ids, arg)
			}
			continue
		}
		if arg == "--" {
			positional = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if arg != "" {
				ids = append(ids, arg)
			}
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		if hasInline {
			if isCloseReasonFileFlag(name) {
				return nil, nil, false
			}
			if isCloseReasonFlag(name) {
				reasons = append(reasons, inline)
			}
			// An inline --flag=value consumes no following token, so an
			// unrecognized name in this form cannot shift a positional and
			// needs no ambiguity refusal (the same call bd_prewrite.go makes).
			continue
		}
		if isCloseReasonFileFlag(name) {
			return nil, nil, false
		}
		if valueFlags[name] {
			if i+1 >= len(rest) {
				// A value flag with nothing after it is a malformed argv bd
				// will reject on its own terms.
				return nil, nil, false
			}
			if isCloseReasonFlag(name) {
				reasons = append(reasons, rest[i+1])
			}
			i++
			continue
		}
		if boolFlags[name] {
			continue
		}
		// An unrecognized flag may consume the following token, so every id
		// read after it is a guess. Declining is not fail-closed here on
		// purpose: refusing would reject a close over a flag a later beads
		// bump added, which is a worse failure than losing a reason.
		return nil, nil, false
	}
	if len(ids) == 0 || len(reasons) == 0 {
		return nil, nil, false
	}
	// bd itself refuses this shape (resolveCloseReasons), so there is no close
	// to guard.
	if len(reasons) > 1 && len(reasons) != len(ids) {
		return nil, nil, false
	}
	return ids, reasons, true
}

// reasonForCloseIndex maps the i-th id to its reason exactly as bd does: one
// shared reason applies to every id, otherwise reasons map positionally
// (cmd/bd/close.go reasonForCloseIndex). Comparing every id against reasons[0]
// would clear a rewritten second id against the first id's reason.
func reasonForCloseIndex(reasons []string, i int) string {
	if len(reasons) == 1 {
		return reasons[0]
	}
	return reasons[i]
}

func isCloseSpelling(verb string) bool {
	for _, spelling := range bdflags.AliasGroup("close") {
		if verb == spelling {
			return true
		}
	}
	return false
}

func isCloseReasonFlag(name string) bool {
	return name == "--reason" || name == "-r"
}

func isCloseReasonFileFlag(name string) bool {
	return name == "--reason-file"
}

// storedCloseReason reads the reason bd currently holds for id. `bd show
// --json` emits an ARRAY of issues even for a single id, so a decode into a
// bare object silently yields the zero value and would read every stored
// reason as empty -- making every reason look rewritten.
func storedCloseReason(runBd beads.CommandRunner, scopeRoot, id string) (string, error) {
	out, err := runBd(scopeRoot, "bd", closeReasonProbeArgs(id)...)
	if err != nil {
		return "", err
	}
	var rows []struct {
		ID          string `json:"id"`
		CloseReason string `json:"close_reason"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", fmt.Errorf("parse bd show --json for %s: %w", id, err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row.CloseReason, nil
		}
	}
	// bd resolved something other than the requested id, or nothing at all.
	// The write-ID guard owns that refusal; this gate has no reason to compare.
	return "", fmt.Errorf("bd show returned no row for %s", id)
}
