// Package tmux test: composer sigil neutralization for nudge delivery.
//
// Scope: neutralizeComposerSigil and the two nudge entry points that must
// apply it. The measurements that chose a leading space over the three
// alternatives are recorded in nudge_sigil.go and on ci-ybl5yx; this file pins
// the resulting behavior and the source-level obligation.
//
// What it CANNOT represent, stated so the manual check is not mistaken for
// redundant: no assertion here establishes that a vendor composer still reads
// "/" and "!" as mode prefixes, or that it does not read some THIRD character
// that way. Those came from driving the real TUIs and would have to be
// re-established against a new build. A pure-Go suite over this function is
// green either way.
//
// Run:
//
//	go test ./internal/runtime/tmux/ -run Sigil
package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestNeutralizeComposerSigilPrefixesOnlySigils is the behavior table. The
// negative rows matter as much as the positive ones: a guard that prefixed
// every message would change the text of every nudge in the city to buy
// nothing.
func TestNeutralizeComposerSigilPrefixesOnlySigils(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"slash command", "/status and then some prose", " /status and then some prose"},
		{"bare slash", "/", " /"},
		{"bang runs a shell command", "!touch /tmp/x", " !touch /tmp/x"},
		{"ordinary prose", "You are holding a pool slot", "You are holding a pool slot"},
		{"hash is not a sigil in this build", "#remember this", "#remember this"},
		{"already space-prefixed", " /status", " /status"},
		{"tab-prefixed", "\t/status", "\t/status"},
		{"newline-prefixed", "\n/status", "\n/status"},
		{"empty", "", ""},
		{"slash later in the line", "run /status now", "run /status now"},
		{"multibyte first rune", "héllo /status", "héllo /status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := neutralizeComposerSigil(tc.in); got != tc.want {
				t.Fatalf("neutralizeComposerSigil(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNeutralizeComposerSigilIsIdempotent pins that a message cannot collect a
// space per hop. Nudges pass through retries and, on the pool paths, through
// more than one delivery attempt for the same text.
func TestNeutralizeComposerSigilIsIdempotent(t *testing.T) {
	once := neutralizeComposerSigil("/status")
	if twice := neutralizeComposerSigil(once); twice != once {
		t.Fatalf("second pass changed %q to %q", once, twice)
	}
}

// TestNeutralizedMessageStillMatchesTheDraftEvidence pins a coupling that is
// currently accidental and would break silently.
//
// submitEnterAndConfirm confirms a submit partly by reading the draft back out
// of the input box. That comparison survives the added space only because
// claudeInputBoxContent TrimSpaces what it reads AND draftHead TrimSpaces the
// draft -- two independent trims, neither written with this in mind. If either
// stops trimming, the draft evidence silently never fires again, which is the
// never-fires-and-looks-fine failure this area keeps producing.
func TestNeutralizedMessageStillMatchesTheDraftEvidence(t *testing.T) {
	message := neutralizeComposerSigil("/status and then some prose that follows")
	// The composer line as claude renders it: prompt glyph, NBSP, then the
	// pasted text including its new leading space.
	lines := []string{"transcript line", claudeInputPromptPrefix + message}
	if !draftInInputBox(lines, message) {
		t.Fatalf("draftInInputBox did not recognize a neutralized draft in %q", lines[1])
	}
}

// TestEveryNudgeEntryPointNeutralizesTheMessage is the mechanical half.
//
// The two entry points apply the neutralization themselves rather than having
// sendKeysLiteralWithRetry do it, because that helper is a generic literal
// send and a nudge-specific transformation does not belong inside it. The cost
// of that choice is that a third nudge path could forget, so this reads the
// source instead of trusting review: any function whose name starts with Nudge
// and which sends literal text must also neutralize.
func TestEveryNudgeEntryPointNeutralizesTheMessage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tmux.go", nil, 0)
	if err != nil {
		t.Fatalf("parse tmux.go: %v", err)
	}
	checked := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fn.Name.Name, "Nudge") || fn.Body == nil {
			continue
		}
		var sends, neutralizes bool
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name == "sendKeysLiteralWithRetry" {
					sends = true
				}
			case *ast.Ident:
				if fun.Name == "neutralizeComposerSigil" {
					neutralizes = true
				}
			}
			return true
		})
		if !sends {
			continue
		}
		checked++
		if !neutralizes {
			t.Errorf("%s sends literal nudge text without neutralizeComposerSigil: a message beginning with \"!\" would execute a shell command instead of arriving", fn.Name.Name)
		}
	}
	// The count is asserted, not just the absences. A rename or a refactor that
	// took the send out of these functions would otherwise leave this test
	// inspecting nothing and passing.
	if checked != 2 {
		t.Fatalf("inspected %d nudge entry points that send literal text, want 2 (NudgeSession, NudgePane) -- update this count deliberately", checked)
	}
}
