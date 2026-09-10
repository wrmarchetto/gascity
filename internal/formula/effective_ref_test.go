// Scope: EffectiveRef, and specifically that it and SourceFromEnv cannot
// disagree about which GC_FORMULA_REF values mean "the live working tree".
//
// Why this suite exists: the alias set ("", "working-tree", "HEAD") is now
// read by two consumers -- the resolver that picks a Source, and the
// supervisor's report of its own effective formula source (cmd/gc's
// "formularef" control-socket command, ci-38p2ky). A second hand-written
// copy of that set would rot on the next alias added, and the failure is
// silent in the expensive direction: the report would call a working-tree
// supervisor "pinned", and an operator would commit a formula edit that was
// already live.
//
// So the agreement is DERIVED rather than restated. Every case asserts
// EffectiveRef's pinned flag against what SourceFromEnv actually returns
// for the same raw value, which is the behavior the report is a claim
// about. A table of expected pinned flags alone would agree with a wrong
// alias list as readily as with a right one.
//
// Run: go test ./internal/formula/ -run EffectiveRef

package formula

import "testing"

// formulaRefCases covers each alias plus values on both sides of it. The
// whitespace and case rows are the ones a restated copy gets wrong:
// SourceFromEnv trims, and it does NOT fold case, so "head" is a real ref
// and "  HEAD  " is not.
var formulaRefCases = []string{
	"", "working-tree", "HEAD",
	"  ", "  HEAD  ", "\tworking-tree\n",
	"main", "head", "Working-Tree", "release/1.4", "origin/main",
	"-x", "HEADs", "working-tree-2",
}

func TestEffectiveRefAgreesWithSourceFromEnvOnEveryValue(t *testing.T) {
	for _, raw := range formulaRefCases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("GC_FORMULA_REF", raw)
			_, pinned := EffectiveRef(raw)
			// FSSource is the working tree. Type identity rather than a
			// name comparison: a Source that merely printed as FSSource
			// would satisfy a string check while resolving through a ref.
			_, isWorkingTree := SourceFromEnv().(FSSource)
			if pinned == isWorkingTree {
				t.Fatalf("EffectiveRef(%q) pinned=%v but SourceFromEnv gave working-tree=%v; the report and the resolver disagree",
					raw, pinned, isWorkingTree)
			}
		})
	}
}

// TestEffectiveRefReportsTheTrimmedRef pins the value itself, not only the
// flag. The report names the ref an operator is expected to `git log`, so
// surrounding whitespace passed through would name a ref that does not
// resolve -- and GitRefSource is constructed from the trimmed form.
func TestEffectiveRefReportsTheTrimmedRef(t *testing.T) {
	for raw, want := range map[string]string{
		"main":          "main",
		"  main  ":      "main",
		"\trelease/1\n": "release/1",
	} {
		ref, pinned := EffectiveRef(raw)
		if !pinned || ref != want {
			t.Fatalf("EffectiveRef(%q) = (%q, %v), want (%q, true)", raw, ref, pinned, want)
		}
	}
}

// TestEffectiveRefFromEnvReadsTheProcessEnvironment covers the seam the
// supervisor actually calls: the report must reflect the pin the process
// holds, which os.Setenv changes and /proc/<pid>/environ does not.
func TestEffectiveRefFromEnvReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("GC_FORMULA_REF", "main")
	if ref, pinned := EffectiveRefFromEnv(); !pinned || ref != "main" {
		t.Fatalf("EffectiveRefFromEnv() = (%q, %v), want (main, true)", ref, pinned)
	}
	t.Setenv("GC_FORMULA_REF", "working-tree")
	if ref, pinned := EffectiveRefFromEnv(); pinned {
		t.Fatalf("EffectiveRefFromEnv() = (%q, %v), want unpinned", ref, pinned)
	}
}
