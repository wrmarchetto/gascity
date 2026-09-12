package execenv

import (
	"slices"
	"testing"
)

// TestBDMetricsDefaultedOffAppendsWhenTheParentIsSilent pins the default half of
// the contract: an environment that says nothing about bd telemetry comes back
// carrying the opt-out. This is the case every allowlist-built child
// environment is in, and it is the one that decides whether a gate run under a
// substituted HOME emits.
func TestBDMetricsDefaultedOffAppendsWhenTheParentIsSilent(t *testing.T) {
	got := WithBDMetricsDefaultedOff([]string{"HOME=/some/city", "PATH=/usr/bin"})

	want := []string{"HOME=/some/city", "PATH=/usr/bin", "BD_DISABLE_METRICS=1"}
	if !slices.Equal(got, want) {
		t.Errorf("WithBDMetricsDefaultedOff() = %q, want %q", got, want)
	}
}

// TestBDMetricsDefaultedOffKeepsAnExplicitOptIn pins the half that keeps this
// from being a policy override. "0" is bd's own opt-IN value, so a parent that
// set it has consented in the one channel a rewritten HOME cannot destroy;
// replacing it would cancel a deliberate `bd metrics on` invisibly.
//
// The assertion is on the whole slice rather than on "0 is still in there":
// appending a second, contradicting entry would leave the original present and
// still change what bd resolves, since the last assignment wins.
func TestBDMetricsDefaultedOffKeepsAnExplicitOptIn(t *testing.T) {
	got := WithBDMetricsDefaultedOff([]string{"BD_DISABLE_METRICS=0", "HOME=/some/city"})

	want := []string{"BD_DISABLE_METRICS=0", "HOME=/some/city"}
	if !slices.Equal(got, want) {
		t.Errorf("WithBDMetricsDefaultedOff() = %q, want %q", got, want)
	}
}

// TestBDMetricsDefaultedOffLeavesTheCallersSliceAlone pins that the append
// cannot write through a caller's spare capacity.
//
// Constructed with spare capacity on purpose: a plain append onto a slice whose
// backing array has room mutates the array in place, so a caller holding a
// longer view of it -- the shape every `env := make([]string, 0, n)` builder
// produces -- silently acquires the entry. The defect is invisible to a test
// that only reads the returned slice, which is why this one reads the input's
// backing array afterwards.
func TestBDMetricsDefaultedOffLeavesTheCallersSliceAlone(t *testing.T) {
	backing := make([]string, 1, 4)
	backing[0] = "HOME=/some/city"

	_ = WithBDMetricsDefaultedOff(backing)

	grown := backing[:2]
	if grown[1] != "" {
		t.Errorf("caller's backing array was written: slot 1 = %q, want empty", grown[1])
	}
}

// TestBDMetricsDisabledEntryIsWhatBDReads pins the literal against bd's own
// constant. bd checks BD_DISABLE_METRICS before any config file
// (resolveMetricsEnabled, beads cmd/bd/main.go), and treats any value that is
// not "", "0" or "false" as truthy. A typo in the key is the failure this
// catches, and nothing else here would: a misspelled key produces an
// environment that looks right and disables nothing.
func TestBDMetricsDisabledEntryIsWhatBDReads(t *testing.T) {
	if BDMetricsDisableEnv != "BD_DISABLE_METRICS" {
		t.Errorf("BDMetricsDisableEnv = %q, want BD_DISABLE_METRICS", BDMetricsDisableEnv)
	}
	if BDMetricsDisabledEntry != "BD_DISABLE_METRICS=1" {
		t.Errorf("BDMetricsDisabledEntry = %q, want BD_DISABLE_METRICS=1", BDMetricsDisabledEntry)
	}
}
