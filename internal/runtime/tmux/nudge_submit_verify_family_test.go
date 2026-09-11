package tmux

import (
	"testing"

	"github.com/gastownhall/gascity/internal/sessionlog"
)

// Scope: the provider-family gate on the confirm-and-resend submit loop
// (submitVerifyFamilies / providerEnvSubmitVerifyEligible) and the submit
// key sequence each family resolves to. Pane-level wiring -- that a real
// tmux pane carrying GC_PROVIDER routes through this gate and that an
// unconfirmed codex submit surfaces as ErrNudgeSubmitUnconfirmed rather
// than a false success -- is delegated to
// nudge_submit_verify_family_integration_test.go, which needs a live tmux
// server. Run:
//
//	go test ./internal/runtime/tmux/ -run 'SubmitVerify|SubmitSequence'

// TestCityCodexProviderNamesAreSubmitVerifyEligible pins the gate against the
// GC_PROVIDER values codex panes actually carry. The city names a provider per
// role (codex-rig-engineer, codex-toolsmith, ...), never the bare family, so a
// gate written against the literal "codex" would exclude every real pane while
// a test using "codex" alone still passed. sessionlog.ProviderFamily maps all
// of them to "codex" by substring, and that mapping is what this exercises.
func TestCityCodexProviderNamesAreSubmitVerifyEligible(t *testing.T) {
	eligible := []string{
		"claude",
		"codex",
		"codex-rig-engineer",
		"codex-toolsmith",
		"codex-engineer",
		"codex-adversarial-reader",
		"codex-bench-operator",
	}
	for _, provider := range eligible {
		if !providerEnvSubmitVerifyEligible(provider) {
			t.Errorf("providerEnvSubmitVerifyEligible(%q) = false, want true", provider)
		}
	}

	// The exclusions are the load-bearing half. Each family below has no
	// busy-state literal in paneContainsBusyIndicator that its TUI is known
	// to render, so confirming a submit against it would poll an indicator
	// that never appears: the loop would burn its full budget and re-send
	// Enter three times into every nudge. They stay on best-effort single
	// delivery until someone measures their indicator the way codex's was
	// measured (see TestCodexSubmitSequenceIsPlainEnterAsMeasured).
	ineligible := []string{"grok", "kimi", "opencode", "pi", "antigravity", "copilot", "", "some-unknown-provider"}
	for _, provider := range ineligible {
		if providerEnvSubmitVerifyEligible(provider) {
			t.Errorf("providerEnvSubmitVerifyEligible(%q) = true, want false", provider)
		}
	}
}

// TestSubmitVerifySequenceResolvesFromTheTableNotACopy derives its expectation
// from nudgeSubmitKeySequences itself rather than restating what the table is
// believed to hold. The predecessor test listed families inline and asserted
// [Enter] for each, so it agreed with the table only by coincidence: adding an
// entry for a listed family would have made the test wrong rather than red,
// and adding one for an unlisted family would have gone unnoticed entirely.
//
// The family set is likewise derived -- the union of every family gc routes
// submit behavior for -- so a family added to either list is covered here the
// moment it is added.
func TestSubmitVerifySequenceResolvesFromTheTableNotACopy(t *testing.T) {
	families := map[string]bool{}
	for _, f := range providersSkippingEscapeBeforeEnter {
		families[f] = true
	}
	for _, f := range submitVerifyFamilies {
		families[f] = true
	}
	for f := range nudgeSubmitKeySequences {
		families[f] = true
	}
	// A derived set that came back empty would make every assertion below
	// vacuous while the test still reported green.
	if len(families) == 0 {
		t.Fatal("derived family set is empty: this test would assert nothing")
	}

	// A name that resolves to no known family at all must also fall back,
	// not panic or return an empty sequence. These two are hand-picked
	// because no list gc keeps could derive them.
	for _, family := range []string{"", "some-unregistered-family"} {
		families[family] = true
	}

	for family := range families {
		got := nudgeSubmitKeySequenceForFamily(family)
		want, declared := nudgeSubmitKeySequences[family]
		if !declared {
			want = defaultNudgeSubmitKeySequence
		}
		if len(got) != len(want) {
			t.Errorf("nudgeSubmitKeySequenceForFamily(%q) = %v, want %v (declared=%v)", family, got, want, declared)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("nudgeSubmitKeySequenceForFamily(%q)[%d] = %q, want %q", family, i, got[i], want[i])
			}
		}
	}
}

// TestCodexSubmitSequenceIsPlainEnterAsMeasured carries a measurement, and it
// is written to FAIL the moment someone acts on the claim that measurement
// refuted.
//
// Upstream gascity#4706 states codex's submit sequence is Escape then Enter,
// on the theory that codex's TUI buffers a send-keys burst as a paste and
// swallows a lone trailing Enter as a composer newline. Measured against
// codex-cli 0.153.4 -- the binary the city's own codex panes run -- on
// 2026-09-11, that is false in every delivery shape gc uses:
//
//	send-keys -l, single line        -> Enter submitted
//	send-keys -l, embedded newlines  -> Enter submitted
//	paste-buffer -p, 11574 bytes     -> Enter submitted
//	paste-buffer -p, 48376 bytes,
//	  Enter with ZERO debounce       -> Enter submitted
//	Escape on a drafted composer     -> no-op, draft survived
//
// So an Escape-then-Enter entry here would add a keystroke that buys nothing
// against a version where plain Enter already works. If codex's TUI does
// change, this test is the thing that stops the entry landing on the strength
// of #4706's prose: re-run the five shapes above against the new build and
// record the result here before adding one.
func TestCodexSubmitSequenceIsPlainEnterAsMeasured(t *testing.T) {
	got := nudgeSubmitKeySequenceForFamily("codex")
	if len(got) != 1 || got[0] != "Enter" {
		t.Fatalf("codex submit sequence = %v, want [Enter] as measured against codex-cli 0.153.4 on 2026-09-11; re-measure before changing it", got)
	}
	// The name a city pane actually carries must resolve to the same thing.
	if seq := nudgeSubmitKeySequenceForFamily(sessionlog.ProviderFamily("codex-rig-engineer")); len(seq) != 1 || seq[0] != "Enter" {
		t.Fatalf("codex-rig-engineer submit sequence = %v, want [Enter]", seq)
	}
}
