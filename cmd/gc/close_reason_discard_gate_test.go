package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: the close-time refusal that stops `gc bd close --reason` reporting
// success while beads silently discards the new reason on an already-closed
// bead (ci-yh6v84).
//
// This suite exists because the defect it guards is INVISIBLE from either end
// tested alone: bd exits 0 and prints the new reason, and the store keeps the
// old one, so a test that only reads bd's output and a test that only reads
// the store both go green. Every case below therefore drives the gate with a
// stored reason that DISAGREES with the argv, which is the only shape that
// can tell a refusal from a passthrough.
//
// The stored reason is served by an injected beads.CommandRunner rather than a
// real bd. That is a dependency, not a switch: there is deliberately no flag
// or environment variable that makes the gate reachable, because such a switch
// is read before the gate runs and a suite would then pin the short-circuit
// instead of the refusal.
//
// What this suite CANNOT represent: that upstream beads still discards the
// reason at all. That is a fact about a third-party binary, pinned separately
// by TestBdCloseReasonRewriteIsDiscardedUpstream in
// test/acceptance/beads_cli_contract_test.go, which runs the real bd.
//
// Run it with:
//
//	go test ./cmd/gc/ -run TestCloseReasonDiscardGate

// closedBead is an already-closed bead as the write-ID guard pre-fetches it.
// Status is the only field the gate reads from the pre-fetch -- close_reason is
// absent from beads.Bead, which is why the gate must go to bd for it.
func closedBead(id string) beads.Bead {
	return beads.Bead{ID: id, Status: "closed", Type: "task"}
}

// storedReasonRunner answers `bd show <id> --json` from reasons, and FAILS any
// other invocation. A runner that answered everything would hand a pass to
// whatever a case forgot to script -- the gate could stop consulting bd
// entirely and this suite would not notice.
func storedReasonRunner(t *testing.T, reasons map[string]string) beads.CommandRunner {
	t.Helper()
	return func(_, name string, args ...string) ([]byte, error) {
		if name != "bd" || len(args) < 2 || args[0] != "show" {
			t.Fatalf("gate ran an unscripted command: %s %v", name, args)
		}
		reason, ok := reasons[args[1]]
		if !ok {
			t.Fatalf("gate asked for an unscripted bead: %q", args[1])
		}
		return []byte(fmt.Sprintf(`[{"id":%q,"status":"closed","close_reason":%q}]`, args[1], reason)), nil
	}
}

func TestCloseReasonDiscardGateRefusesRewriteOfStoredReason(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "first reason"})

	var stderr strings.Builder
	blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "second reason, corrected"},
		prefetched, runner, "/scope", &stderr)
	if !blocked {
		t.Fatalf("a reason-rewriting re-close was forwarded to bd, which would report success and discard it; stderr=%q", stderr.String())
	}
	got := stderr.String()
	// The remedy is the whole point of refusing: a refusal that does not name
	// reopen-then-close leaves the author with no way to correct the record.
	for _, want := range []string{"ci-a", "already closed", "reopen"} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal does not name %q: %q", want, got)
		}
	}
}

// An identical-reason re-close is the retry beads itself depends on: cmd/bd
// close.go replays last-touched, --continue, --suggest-next, --claim-next and
// molecule auto-close healing on it. Refusing that would trade a silent
// record corruption for a stranded molecule root, so this is the case that
// keeps the fix from being a regression.
func TestCloseReasonDiscardGateAllowsIdenticalReasonRetry(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "first reason"})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "first reason"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("an idempotent retry was refused, breaking beads' own retry-safe re-close; stderr=%q", stderr.String())
	}
}

// A whitespace-only difference is still a rewrite bd will discard, so the
// comparison must not trim either side. This pins the no-trim decision: bd
// stores a reason verbatim, so a trimming gate would wave through a close
// whose new reason never lands.
func TestCloseReasonDiscardGateRefusesWhitespaceOnlyRewrite(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "  first reason  "})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "first reason"},
		prefetched, runner, "/scope", &stderr); !blocked {
		t.Fatalf("a whitespace-only rewrite was forwarded and would be discarded; stderr=%q", stderr.String())
	}
}

// An open target's close performs the real write, reason included. The gate
// must not consult bd at all here -- storedReasonRunner fails any call, so a
// gate that probed every close would fail this case rather than pass it.
func TestCloseReasonDiscardGateIgnoresOpenTarget(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": {ID: "ci-a", Status: "open", Type: "task"}}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "the first close"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("closing an open bead was refused; stderr=%q", stderr.String())
	}
}

// No --reason means nothing the caller authored can be discarded: bd defaults
// the reason to "Closed" (cmd/bd/close.go resolveCloseReasons). Refusing here
// would refuse every bare idempotent re-close in the city.
func TestCloseReasonDiscardGateIgnoresCloseWithoutReason(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a"}, prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("a re-close carrying no --reason was refused; stderr=%q", stderr.String())
	}
}

// `done` is a published alias of `close` (bdflags.AliasGroup), so it reaches
// the same discarding write. The gate reads the alias group rather than
// listing spellings: a listed spelling is what let gc shadow `bd heartbeat`
// for two months (ci-mosn).
func TestCloseReasonDiscardGateCoversCloseAliases(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "first reason"})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"done", "ci-a", "--reason", "rewritten via the alias"},
		prefetched, runner, "/scope", &stderr); !blocked {
		t.Fatalf("the `done` alias bypassed the gate; stderr=%q", stderr.String())
	}
}

// A global flag precedes the verb, so a gate reading argv[0] stops firing for
// exactly the authors who pass an explicit actor -- the failure bd_prewrite.go
// records for the pre-write validator.
func TestCloseReasonDiscardGateFindsVerbBehindGlobalFlags(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "first reason"})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"--actor", "bob", "close", "ci-a", "--reason", "rewritten behind a global flag"},
		prefetched, runner, "/scope", &stderr); !blocked {
		t.Fatalf("an explicit --actor hid the verb from the gate; stderr=%q", stderr.String())
	}
}

// One --reason applies to every id and N reasons map positionally to N ids
// (cmd/bd/close.go reasonForCloseIndex). A gate that compared every id against
// reasons[0] would clear the second id here against the first id's reason.
func TestCloseReasonDiscardGateMapsPerIDReasonsPositionally(t *testing.T) {
	prefetched := map[string]beads.Bead{
		"ci-a": closedBead("ci-a"),
		"ci-b": closedBead("ci-b"),
	}
	runner := storedReasonRunner(t, map[string]string{
		"ci-a": "kept for a",
		"ci-b": "stored for b",
	})

	var stderr strings.Builder
	blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "ci-b", "--reason", "kept for a", "--reason", "rewritten for b"},
		prefetched, runner, "/scope", &stderr)
	if !blocked {
		t.Fatalf("the second id's rewritten reason was not caught; stderr=%q", stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "ci-b") {
		t.Fatalf("refusal does not name the rewritten id: %q", got)
	}
	if strings.Contains(got, "ci-a") {
		t.Fatalf("refusal names ci-a, whose reason was unchanged: %q", got)
	}
}

// The gate cannot establish the stored reason when bd is unreachable, and a
// refusal it cannot justify would block a legitimate close. Falling open
// matches the write-ID and work-record gates, which take the same posture on
// an unavailable store.
func TestCloseReasonDiscardGateFallsOpenWhenStoredReasonUnreadable(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("dolt server unreachable")
	}

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "second reason"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate refused a close it could not justify; stderr=%q", stderr.String())
	}
}

// A bead absent from the pre-fetch is one the write-ID guard could not read --
// an ephemeral row, a projection lag, or an unavailable store. The gate has no
// status to reason from and must not invent one.
func TestCloseReasonDiscardGateFallsOpenWhenTargetNotPreFetched(t *testing.T) {
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "second reason"},
		nil, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate refused a close on a bead it never read; stderr=%q", stderr.String())
	}
}

// --reason-file is a DELIBERATE hole: the gate cannot compare a reason it
// would have to re-derive from a file with bd's own trimming, and a mismatched
// derivation would refuse a legitimate close. This test pins the hole so
// closing it is a decision rather than an accident -- it FAILS if the gate
// starts refusing this form, which is the prompt to verify the derivation
// against bd first.
func TestCloseReasonDiscardGateDoesNotActOnReasonFile(t *testing.T) {
	// Both spellings, because the inline form takes its value from the token
	// itself and the spaced form from the next one -- two separate branches,
	// and a gate that mistook the PATH for the reason would compare a filename
	// against the stored text and refuse every such close.
	for _, args := range [][]string{
		{"close", "ci-a", "--reason-file", "/tmp/reason.txt"},
		{"close", "ci-a", "--reason-file=/tmp/reason.txt"},
	} {
		prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
		runner := storedReasonRunner(t, map[string]string{})

		var stderr strings.Builder
		if blocked := evaluateCloseReasonDiscardGate(args, prefetched, runner, "/scope", &stderr); blocked {
			t.Fatalf("gate acted on %v, whose requested reason it cannot establish; stderr=%q", args, stderr.String())
		}
	}
}

// --reason and --reason-file together is a shape bd REFUSES outright
// ("cannot specify both --reason-file and --reason/..."), so no close happens
// and no reason is lost. A gate that read only --reason here would refuse it
// first, with a message blaming a discard that was never going to occur.
func TestCloseReasonDiscardGateDefersWhenReasonAndReasonFileCollide(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "--reason", "from the flag", "--reason-file", "/tmp/reason.txt"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate pre-empted bd's own refusal of --reason with --reason-file; stderr=%q", stderr.String())
	}
}

// bd refuses N reasons for M ids when N > 1 and N != M (resolveCloseReasons),
// so there is no close to guard. Admitting it would index reasons past its
// end for the third id.
func TestCloseReasonDiscardGateDefersWhenReasonCountMismatchesIDs(t *testing.T) {
	prefetched := map[string]beads.Bead{
		"ci-a": closedBead("ci-a"),
		"ci-b": closedBead("ci-b"),
		"ci-c": closedBead("ci-c"),
	}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "ci-b", "ci-c", "--reason", "one", "--reason", "two"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate acted on a reason/id count bd rejects; stderr=%q", stderr.String())
	}
}

// The discriminating case for the positional mapping: both beads STORE the
// first reason, and only the second bead's REQUESTED reason differs. A gate
// comparing every id against reasons[0] finds nothing wrong here, so this is
// the only shape that separates a positional mapping from a shared one.
func TestCloseReasonDiscardGateUsesTheSecondIDsOwnReason(t *testing.T) {
	prefetched := map[string]beads.Bead{
		"ci-a": closedBead("ci-a"),
		"ci-b": closedBead("ci-b"),
	}
	runner := storedReasonRunner(t, map[string]string{
		"ci-a": "shared stored text",
		"ci-b": "shared stored text",
	})

	var stderr strings.Builder
	blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "ci-a", "ci-b", "--reason", "shared stored text", "--reason", "rewritten for b only"},
		prefetched, runner, "/scope", &stderr)
	if !blocked {
		t.Fatalf("ci-b's own reason was not compared against ci-b's stored reason; stderr=%q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "ci-b") {
		t.Fatalf("refusal does not name ci-b: %q", got)
	}
}

// An unrecognized flag can consume the following token, so the id scan cannot
// be trusted. The gate declines rather than refusing on a misread argv --
// blocking here would reject a close over a flag a later beads bump added.
func TestCloseReasonDiscardGateFallsOpenOnAmbiguousArgs(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"close", "--newly-added-flag", "ci-a", "--reason", "second reason"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate refused on an argv it could not parse; stderr=%q", stderr.String())
	}
}

// Only `close` discards a reason. `bd update --status closed` reaches done
// through UpdateIssueInTx and carries no --reason at all, so admitting it
// would refuse writes that lose nothing.
func TestCloseReasonDiscardGateIgnoresNonCloseVerbs(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"update", "ci-a", "--status", "closed"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate acted on a non-close verb; stderr=%q", stderr.String())
	}
}

// `reopen` is the verb this gate's own remedy tells the caller to run, and it
// takes --reason too. It is the ONLY case that isolates the verb check: a
// verb carrying a flag close does not know (update --status) falls open
// through the unrecognized-flag path instead, so a gate with no verb check at
// all still passes that one. Dropping the check here would make the gate
// refuse the first half of the remedy it just printed.
func TestCloseReasonDiscardGateIgnoresReopenSharingCloseFlags(t *testing.T) {
	prefetched := map[string]beads.Bead{"ci-a": closedBead("ci-a")}
	runner := storedReasonRunner(t, map[string]string{"ci-a": "first reason"})

	var stderr strings.Builder
	if blocked := evaluateCloseReasonDiscardGate(
		[]string{"reopen", "ci-a", "--reason", "correcting the record"},
		prefetched, runner, "/scope", &stderr); blocked {
		t.Fatalf("gate refused a reopen, which persists its reason normally; stderr=%q", stderr.String())
	}
}
