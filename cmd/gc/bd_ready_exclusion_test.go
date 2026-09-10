package main

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// The pinned bd must accept the two flags the ready view is built on. Without
// this gate a beads bump that renamed or dropped either one would turn every
// `gc bd ready` into an unknown-flag error, and the unit tests below would
// stay green throughout: they assert the argv gc builds, not that bd parses
// it. bdflags' manifest is re-derived from the beads module source on every
// run (TestValueFlagsMatchModuleSource), so this reads the real flag set of
// the version go.mod requires rather than a transcription.
func TestPinnedBdAcceptsTheReadyExclusionFlags(t *testing.T) {
	valueFlags := bdflags.ValueFlags("ready")
	for _, flag := range []string{bdExcludeLabelFlag, bdExcludeTypeFlag} {
		if !valueFlags[flag] {
			t.Errorf("beads %s does not register %s on `bd ready`; gc bd ready would forward an unknown flag",
				bdflags.SourcedBeadsVersion, flag)
		}
	}
}

func TestBdReadyArgsCarryGcsExclusionSets(t *testing.T) {
	got := augmentBdReadyArgs([]string{"ready"})
	if len(got) != 5 || got[0] != "ready" {
		t.Fatalf("augmentBdReadyArgs([ready]) = %v, want the verb plus two flag pairs", got)
	}
	wantLabels := strings.Join(beads.ReadyExcludedLabels(), ",")
	wantTypes := strings.Join(beads.ReadyExcludedTypes(), ",")
	want := []string{"ready", bdExcludeLabelFlag, wantLabels, bdExcludeTypeFlag, wantTypes}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("augmentBdReadyArgs([ready]) =\n\t%v\nwant\n\t%v", got, want)
	}
}

// Every label gc forwards must be one gc's own Ready() would drop. This is
// what makes the CLI view the same view, rather than a filter that merely
// looks similar: it re-checks each forwarded token against the predicate the
// stores call, so a hand-edited literal in the forwarded set is caught even
// though both sides are built from the same package.
func TestForwardedExclusionsAreTheOnesReadyItselfApplies(t *testing.T) {
	args := augmentBdReadyArgs([]string{"ready"})
	labels := strings.Split(args[2], ",")
	types := strings.Split(args[4], ",")

	for _, label := range labels {
		if !beads.IsReadyExcludedBead(beads.Bead{Type: "task", Labels: []string{label}}) {
			t.Errorf("gc bd ready hides label %q, but beads.Ready() would show it", label)
		}
	}
	for _, typ := range types {
		if !beads.IsReadyExcludedBead(beads.Bead{Type: typ}) {
			t.Errorf("gc bd ready hides type %q, but beads.Ready() would show it", typ)
		}
	}
	if len(labels) == 0 || len(types) == 0 {
		t.Fatal("forwarded an empty exclusion set")
	}
}

func TestBdReadyAugmentationLeavesOtherVerbsAlone(t *testing.T) {
	// `bd list --ready` reaches the same ready semantics by another door and
	// is DELIBERATELY not augmented. Filtering it would mean deciding what
	// `gc bd list` shows when --ready is absent, which is an ordinary list
	// the fabric rows legitimately belong in; the flag would have to be
	// parsed out of an argv gc otherwise forwards whole. Recorded rather
	// than fixed: an operator chasing a fabric row still has that view.
	cases := [][]string{
		{"list", "--ready"},
		{"show", "ci-1"},
		{"readyish"},
		{},
		{"--json"},
	}
	for _, args := range cases {
		before := append([]string(nil), args...)
		// slices.Equal, not reflect.DeepEqual: the empty-argv case returns
		// the caller's own empty slice and DeepEqual calls that unequal to a
		// nil one, which is a fact about the copy in the test rather than
		// about the function.
		if got := augmentBdReadyArgs(args); !slices.Equal(got, before) {
			t.Errorf("augmentBdReadyArgs(%v) = %v, want unchanged", before, got)
		}
	}
}

// The verb is located through bdflags.SplitGlobalFlags rather than read from
// args[0], so a caller who passes an explicit global flag still gets the
// filtered view. The naive read takes "bob" out of `--actor bob ready` and
// the filter then silently stops applying for exactly the callers that set an
// actor -- the same shape that disarmed the pre-write gate (see
// bdPreWriteMutation).
func TestBdReadyAugmentationFindsTheVerbBehindGlobalFlags(t *testing.T) {
	got := augmentBdReadyArgs([]string{"--actor", "bob", "ready", "--json"})
	want := []string{
		"--actor", "bob", "ready",
		bdExcludeLabelFlag, strings.Join(beads.ReadyExcludedLabels(), ","),
		bdExcludeTypeFlag, strings.Join(beads.ReadyExcludedTypes(), ","),
		"--json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("global-flag argv =\n\t%v\nwant\n\t%v", got, want)
	}
}

// gc's flags are INSERTED after the verb, never appended, so a caller's own
// exclusions survive and a trailing "--" keeps everything after it positional.
// Appending is what a future editor would reach for and it puts gc's flags on
// the far side of the separator, where bd reads them as positional arguments
// and rejects the command.
func TestBdReadyAugmentationComposesWithCallerFlags(t *testing.T) {
	got := augmentBdReadyArgs([]string{"ready", "--exclude-label", "mine", "--", "tail"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--exclude-label mine") {
		t.Errorf("caller's own --exclude-label was dropped: %v", got)
	}
	if !strings.Contains(joined, bdExcludeLabelFlag+" "+strings.Join(beads.ReadyExcludedLabels(), ",")) {
		t.Errorf("gc's exclusion set is missing: %v", got)
	}
	if got[len(got)-2] != "--" || got[len(got)-1] != "tail" {
		t.Errorf("the -- separator did not stay ahead of its tail: %v", got)
	}
}

// The augmentation must not write through the caller's backing array. doBd
// hands the same slice to the guards that run after this point, and an
// in-place write would show them an argv the caller never typed.
func TestBdReadyAugmentationDoesNotMutateItsInput(t *testing.T) {
	args := []string{"ready", "--json"}
	before := append([]string(nil), args...)
	augmentBdReadyArgs(args)
	if !reflect.DeepEqual(args, before) {
		t.Errorf("input mutated: %v, want %v", args, before)
	}
}

// The argv the bd SUBPROCESS receives, which is the only thing the operator's
// view actually depends on. The unit tests above pin what augmentBdReadyArgs
// returns and would stay green with the function never called -- a passing
// suite over dead code is the failure mode this end-to-end leg exists to
// close. It drives the real doBd through the recording bd stand-in and reads
// the argv off the far side.
func TestGcBdReadyForwardsTheExclusionSetsToBd(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)
	resetFlags(t)
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }

	cityDir, _ := redirectedWorktreeCityOnDisk(t)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
	readCapture := installRecordingBd(t)

	var stdout, stderr bytes.Buffer
	if code := doBd([]string{"ready"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doBd(ready) = %d, want 0; stderr=%q", code, stderr.String())
	}

	gotArgs := readCapture()["args"]
	wantLabels := bdExcludeLabelFlag + " " + strings.Join(beads.ReadyExcludedLabels(), ",")
	wantTypes := bdExcludeTypeFlag + " " + strings.Join(beads.ReadyExcludedTypes(), ",")
	if !strings.Contains(gotArgs, wantLabels) {
		t.Errorf("bd received args %q, missing %q", gotArgs, wantLabels)
	}
	if !strings.Contains(gotArgs, wantTypes) {
		t.Errorf("bd received args %q, missing %q", gotArgs, wantTypes)
	}
	if !strings.HasPrefix(gotArgs, "ready ") {
		t.Errorf("bd received args %q, want the ready verb still first", gotArgs)
	}
}

// The same path for a verb gc must not touch. Without this leg an
// augmentation wired too early -- before the verb check, or on every argv --
// would pass the test above and quietly hand `bd show` two flags it does not
// register.
func TestGcBdLeavesNonReadyArgvUntouched(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)
	resetFlags(t)
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }

	cityDir, _ := redirectedWorktreeCityOnDisk(t)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
	readCapture := installRecordingBd(t)

	var stdout, stderr bytes.Buffer
	if code := doBd([]string{"list", "--ready"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doBd(list) = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := readCapture()["args"]; got != "list --ready" {
		t.Errorf("bd received args %q, want %q", got, "list --ready")
	}
}
