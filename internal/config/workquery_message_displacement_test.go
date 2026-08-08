package config

// Scope: the work_query's assigned tiers must not serve, or short-circuit on,
// a mail message bead -- on either the durable tier or the ephemeral tier.
//
// Why this suite runs the generated shell instead of asserting on the query
// string: the defect was not a wrong predicate, it was a CONTROL-FLOW
// short-circuit. The assigned-ready tier ran `bd ready --assignee=<id>
// --limit=1` and `exit 0` on any hit, so a message bead -- which carries the
// recipient in assignee, exactly like real assigned work -- ended the ladder
// before the routed-pool tier below it ever ran. A string assertion cannot
// catch that; only running the ladder can. These tests reproduce the causal
// experiment run against the live store (archive one message and
// `gc hook <agent>` goes from 1 item to 2) as a hermetic unit test.
//
// The fake bd honors --exclude-type, which is real bd behavior, not a
// convenience: `bd ready --help` and `bd list --help` both document
// "--exclude-type strings  Exclude issue types from results (comma-separated
// or repeatable)". A fake that ignored the flag would make these tests
// unfailable-by-construction, and a fake that answered every query with the
// message would hand a pass to whatever the suite forgot to script.
//
// Delegated elsewhere: the Go-side defense, filterUnreadyHookCandidates in
// cmd/gc, is pinned in cmd/gc/hook_message_demand_test.go. That layer catches
// a message reaching a consumer through a custom or legacy work_query this
// file does not generate.
//
// Run: go test ./internal/config/ -run 'WorkQueryMessage|WorkQueryStillServes'

import (
	"strings"
	"testing"
)

// fakeBdHonoringExcludeType models the two bd behaviors the assigned tiers
// depend on: --exclude-type=message suppresses message rows, and the
// unassigned routed query returns the real work waiting below.
//
// durableMsg controls whether the message is returned by the durable
// assignee tier (`bd ready --assignee=`) or by the ephemeral tier
// (`bd query 'ephemeral=true AND status=open'`). Real mail wisps are
// ephemeral, so the ephemeral variant is the live shape; the durable variant
// covers the tier that has the bd flag rather than the jq clause.
func fakeBdHonoringExcludeType(durableMsg bool) string {
	msgRow := `[{"id":"ci-wisp-p2gjmg","issue_type":"message","status":"open","assignee":"worker","title":"Queue order: take ci-fh4o first"}]`
	routed := `[{"id":"ci-fh4o","issue_type":"bug","priority":1},{"id":"ci-c0cu","issue_type":"feature","priority":2}]`

	durableCase := `printf '[]'`
	ephemeralCase := `printf '[]'`
	if durableMsg {
		durableCase = `case "$args" in *"--exclude-type=message"*) printf '[]';; *) printf '` + msgRow + `';; esac`
	} else {
		ephemeralCase = `printf '` + msgRow + `'`
	}

	return `#!/bin/sh
set -eu
args="$*"
case "$args" in
  *"ephemeral=true AND status=open"*)
    ` + ephemeralCase + `
    ;;
  *"--unassigned"*)
    printf '` + routed + `'
    ;;
  *"--assignee="*)
    ` + durableCase + `
    ;;
  *)
    printf '[]'
    ;;
esac
`
}

func runWorkQueryForWorker(t *testing.T, bdScript string) string {
	t.Helper()
	return runEffectiveWorkQuery(t, Agent{Name: "worker", Dir: "hello-world"}, map[string]string{
		"GC_ALIAS":          "worker",
		"GC_SESSION_ORIGIN": "ephemeral",
	}, bdScript)
}

// TestWorkQueryMessageDoesNotDisplaceRoutedWork is clause (b) of the gate
// invariant: adding an unread message to an agent's queue must not change the
// set of real work beads the query returns.
//
// This is the failure that cost ~1h35m of idle time. An agent holding mail AND
// real routed work saw only the mail, could not claim it, and sat idle -- a
// state indistinguishable from having no work at all, since `gc bd ready`
// still listed the hidden bead as ready and `gc status` still said running.
func TestWorkQueryMessageDoesNotDisplaceRoutedWork(t *testing.T) {
	for _, tc := range []struct {
		name       string
		durableMsg bool
	}{
		{"ephemeral mail wisp (the live shape)", false},
		{"durable message bead", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runWorkQueryForWorker(t, fakeBdHonoringExcludeType(tc.durableMsg))

			if strings.Contains(out, "ci-wisp-p2gjmg") {
				t.Errorf("work query served a message bead as work:\n%s", out)
			}
			if !strings.Contains(out, "ci-fh4o") {
				t.Errorf("message displaced the routed work waiting in the same batch; want ci-fh4o served:\n%s", out)
			}
		})
	}
}

// TestWorkQueryMessageOnlyReportsNoDemand is clause (a): a queue holding only
// mail must report NO work, because the claim path refuses message beads by
// construction (hookClaimCandidateIsMessage). When the two sides disagree the
// loop is structural and self-sustaining -- demand says work exists, the claim
// hands out nothing, the session drain-acks, the slot empties, and nothing
// about the message changed, so demand is identical on the next tick. 97
// sessions burned on one agent this way, each a full model launch.
//
// The absence this pins: mail deliberately does NOT create demand and will not
// spawn a session. That is not a regression to restore. A session cannot
// consume its own mail -- `gc hook --claim` is its FIRST command, so it
// drained before ever reading the message that spawned it.
func TestWorkQueryMessageOnlyReportsNoDemand(t *testing.T) {
	for _, tc := range []struct {
		name       string
		durableMsg bool
	}{
		{"ephemeral mail wisp (the live shape)", false},
		{"durable message bead", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same fake, minus any routed work: mail is all there is.
			bd := strings.Replace(fakeBdHonoringExcludeType(tc.durableMsg),
				`printf '[{"id":"ci-fh4o","issue_type":"bug","priority":1},{"id":"ci-c0cu","issue_type":"feature","priority":2}]'`,
				`printf '[]'`, 1)

			out := runWorkQueryForWorker(t, bd)
			if strings.Contains(out, "ci-wisp-p2gjmg") {
				t.Fatalf("mail-only queue reported demand; this is the spawn loop:\n%s", out)
			}
			if trimmed := strings.TrimSpace(out); trimmed != "[]" && trimmed != "" {
				t.Fatalf("mail-only queue = %q, want empty demand", trimmed)
			}
		})
	}
}

// TestWorkQueryStillServesRealAssignedWork guards the over-correction. Without
// it, a "fix" that dropped the assigned tier entirely would satisfy both tests
// above while silently breaking crash recovery for every agent -- the tier
// exists so a session that died mid-bead is handed its own work back.
func TestWorkQueryStillServesRealAssignedWork(t *testing.T) {
	out := runWorkQueryForWorker(t, `#!/bin/sh
set -eu
case "$*" in
  *"--assignee="*)
    printf '[{"id":"ci-real","issue_type":"task","status":"open","assignee":"worker"}]'
    ;;
  *)
    printf '[]'
    ;;
esac
`)

	if !strings.Contains(out, "ci-real") {
		t.Fatalf("assigned real work was not served:\n%s", out)
	}
}
