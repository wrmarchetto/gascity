package main

// Scope: the pairing between gc's voluntary release verb and the pool demand
// probe that decides whether a session is ever minted for the released bead.
// The sibling suites own the CAS itself
// (TestDoBdReleaseIfCurrentUpdatesOnlyMatchingAssignment) and the wording of a
// refused release (bd_release_if_current_displaced_test.go); neither is
// re-asserted here.
//
// Why this suite exists (ci-9me69b). A rig PM released a `pm-question` bead it
// could not answer. The release cleared the assignee, the bead carried no
// gc.routed_to, and it came to rest open, unassigned and unrouted -- matching
// NO tier of the pool demand query, so nothing ever woke the PM for it again
// and the asker blocked behind a question no one held. Measured twice on
// 2026-09-18, 65 minutes apart, both times with `gc hook astoria-sel4/lab.pm`
// returning [] while the bead sat open in the store.
//
// The assertion is deliberately on the DEMAND PROBE and not on the metadata
// field the release writes. A test reading gc.routed_to back off the bead
// passes as soon as the key is present and says nothing about whether any
// query matches it -- which is the whole defect: the field the producer wrote
// and the field the consumer reads were not the same field. Both sides here
// come from production: the query string is config.Agent's own
// EffectivePoolDemandQuery, and the rows it filters are the store's, read back
// after the real release runs.
//
// The unreleased-but-assigned measurement is the control, not decoration. It
// proves the probe and its stand-in can report demand at all before the
// released case is asked to; without it a fake that answered 0 for everything
// would "confirm" the defect and then "confirm" the fix by never changing.
//
// Run: go test ./cmd/gc/ -run ReleaseIfCurrent.*Demand

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// questionAgent is the shape ask-pm.py addresses: a rig-scoped agent reached
// through an import binding, whose qualified name is the literal string the
// question bead's assignee carries. Both demand tiers in poolDemandCountShell
// take that one name as $target, which is the property the fix rests on -- an
// assignee that raised demand through the alias tier raises it through the
// routed tier when stamped as gc.routed_to, because the target is the same
// string in both.
var questionAgent = config.Agent{Name: "pm", BindingName: "lab", Dir: "astoria-sel4"}

// fakeBdForDemand is a stand-in for the bd binary that serves one fixed row
// set through the real demand query's own flags. It REFUSES any subcommand or
// flag it was not built for rather than answering []: an empty answer to an
// unrecognized flag would hand a pass to a query rewrite that started
// filtering on something else, and the staleness would be invisible from the
// outside because the count would still look right.
//
// What it does NOT model, stated so the proof is not mistaken for broader than
// it is: dependency blocking, priority ordering, ephemeral tiers, and bd's
// substring ID resolution. It models exactly `bd ready`'s status and
// deferral gate plus the four filters poolDemandCountShell passes.
const fakeBdForDemand = `#!/bin/sh
set -eu
sub=$1
shift
case "$sub" in
  ready) ;;
  query)
    # The legacy ephemeral tier. This store holds no wisps, and answering []
    # for it is scripted rather than a fallthrough.
    case "$*" in
      *"ephemeral=true AND status=open"*) printf '[]'; exit 0 ;;
      *) echo "fake bd: unscripted query: $*" >&2; exit 3 ;;
    esac ;;
  *) echo "fake bd: unscripted subcommand: $sub" >&2; exit 3 ;;
esac

# bd ready serves neither claimed nor deferred work. Modeled here because the
# control measurement depends on it: the question bead raises demand while it
# is open and addressed, and stops while a PM session holds it.
sel=' | select((.status // "") == "open") | select((.defer_until // "") == "")'
while [ $# -gt 0 ]; do
  case "$1" in
    --json) ;;
    --include-ephemeral) ;;
    --limit) shift ;;
    --sort) shift ;;
    --unassigned)
      sel="$sel | select((.assignee // \"\") == \"\")" ;;
    --assignee=*)
      v=${1#--assignee=}
      sel="$sel | select((.assignee // \"\") == \"$v\")" ;;
    --exclude-type=*)
      v=${1#--exclude-type=}
      sel="$sel | select((.issue_type // \"\") != \"$v\")" ;;
    --exclude-label)
      shift
      sel="$sel | select(([ (.labels // [])[] | select(. == \"$1\") ] | length) == 0)" ;;
    --metadata-field)
      shift
      k=${1%%=*}
      v=${1#*=}
      sel="$sel | select((.metadata[\"$k\"] // \"\") == \"$v\")" ;;
    *)
      echo "fake bd: unscripted flag: $1" >&2
      exit 3 ;;
  esac
  shift
done
jq "[ .[] $sel ]" <"$FAKE_BD_ROWS"
`

// poolDemandFor runs the agent's production pool demand query over every bead
// currently in store and returns the count it reports.
//
// The rows are read back from the store rather than staged by the test, so a
// release that writes the wrong field cannot be papered over by a fixture that
// writes the right one.
func poolDemandFor(t *testing.T, agent config.Agent, store beads.Store) string {
	t.Helper()

	all, err := store.List(beads.ListQuery{AllowScan: true})
	if err != nil {
		t.Fatalf("listing store rows for the demand probe: %v", err)
	}
	rows, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("encoding store rows for the demand probe: %v", err)
	}

	dir := t.TempDir()
	rowsPath := filepath.Join(dir, "rows.json")
	if err := os.WriteFile(rowsPath, rows, 0o644); err != nil {
		t.Fatalf("write demand rows: %v", err)
	}
	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte(fakeBdForDemand), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	cmd := exec.Command("sh", "-c", agent.EffectivePoolDemandQuery())
	cmd.Env = []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"FAKE_BD_ROWS=" + rowsPath,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pool demand query: %v; stderr=%s", err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// questionBeadFixture builds the store state ask-pm.py leaves behind: a
// question addressed to the rig PM by ASSIGNEE ALONE. The absent gc.routed_to
// is the fixture's whole point -- ask-pm.py writes no route (assets/scripts/
// ask-pm.py, which sets only the assignee), and that absence is what the
// release used to turn into permanent invisibility.
func questionBeadFixture(t *testing.T) (string, beads.Store, execStoreTarget, beads.Bead) {
	t.Helper()
	cityDir := t.TempDir()
	toml := "[workspace]\nname = \"demo\"\n\n[beads]\nprovider = \"file\"\n"
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title:    "Should e8 establish a third phantom word-1 bit?",
		Type:     "task",
		Assignee: questionAgent.QualifiedName(),
		Labels:   []string{"pm-question"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	target := execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "gc"}
	return cityDir, store, target, created
}

// TestReleaseIfCurrentKeepsUnroutedWorkVisibleToPoolDemand is the incident in
// one assertion: a question the PM claimed and then handed back must still be
// demand for the PM afterwards.
//
// The control runs first, against the same store and the same probe, so a
// zero after the release is attributable to the release and not to a probe
// that reports zero for everything.
func TestReleaseIfCurrentKeepsUnroutedWorkVisibleToPoolDemand(t *testing.T) {
	cityDir, store, target, created := questionBeadFixture(t)

	if got := poolDemandFor(t, questionAgent, store); got == "0" {
		t.Fatalf("control: an open question addressed to %q raised no demand (%q); the probe cannot report demand at all, so the released case below proves nothing", questionAgent.QualifiedName(), got)
	}

	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("claiming the question: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, questionAgent.QualifiedName(), &stdout, &stderr); code != 0 {
		t.Fatalf("doBdReleaseIfCurrent = %d, want 0; stderr=%q", code, stderr.String())
	}
	if line := strings.TrimSpace(stdout.String()); line != "released" {
		t.Fatalf("release output = %q, want released", line)
	}

	if got := poolDemandFor(t, questionAgent, store); got == "0" {
		t.Fatalf("released question raised no demand for %q: nothing will ever wake the agent for it, and every asker blocked on it waits forever", questionAgent.QualifiedName())
	}
}

// TestReleaseIfCurrentLeavesTheReleasedBeadClaimable pins the other half of a
// usable hand-back: demand alone is not enough if the bead cannot then be
// taken. The release must leave it open, so a fix that preserved the address
// by leaving the claim in place -- which would also keep demand nonzero -- is
// not mistaken for a release.
func TestReleaseIfCurrentLeavesTheReleasedBeadClaimable(t *testing.T) {
	cityDir, store, target, created := questionBeadFixture(t)
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("claiming the question: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, questionAgent.QualifiedName(), &stdout, &stderr); code != 0 {
		t.Fatalf("doBdReleaseIfCurrent = %d, want 0; stderr=%q", code, stderr.String())
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("released question status = %q, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Fatalf("released question assignee = %q, want it cleared -- the hand-back has to actually hand back", got.Assignee)
	}
	if route := got.Metadata[beadmeta.RoutedToMetadataKey]; route != questionAgent.QualifiedName() {
		t.Fatalf("released question %s = %q, want %q", beadmeta.RoutedToMetadataKey, route, questionAgent.QualifiedName())
	}
}
