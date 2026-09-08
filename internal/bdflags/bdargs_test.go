package bdflags

import (
	"reflect"
	"testing"
)

// TestSplitGlobalFlagsSkipsGlobalFlagValues pins that a global flag's VALUE is
// never mistaken for the subcommand. Taking the first non-dash token reads
// "bob" out of `bd --actor bob update <id> ...`, so anything keyed off the
// subcommand is bypassed by a token the caller never meant as a verb.
func TestSplitGlobalFlagsSkipsGlobalFlagValues(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantVerb string
		wantRest []string
	}{
		{"plain", []string{"update", "bd-1"}, "update", []string{"bd-1"}},
		{"--actor", []string{"--actor", "bob", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"-C dir", []string{"-C", "/some/dir", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"--db", []string{"--db", "/x/y.db", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"--directory", []string{"--directory", "/d", "close", "bd-1"}, "close", []string{"bd-1"}},
		{"inline form consumes nothing", []string{"--actor=bob", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"bool global", []string{"--json", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"stacked", []string{"--actor", "bob", "--json", "-C", "/d", "update", "bd-1"}, "update", []string{"bd-1"}},
		{"no verb", []string{"--actor", "bob"}, "", nil},
		{"empty", nil, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, rest := SplitGlobalFlags(tc.args)
			if verb != tc.wantVerb {
				t.Errorf("verb = %q, want %q", verb, tc.wantVerb)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// TestSplitGlobalFlagsSkipsEveryGlobalValue pins that SplitGlobalFlags
// consumes the value of every flag the manifest declares value-consuming, so
// none of them can be read as the verb. Getting that wrong reopens the bypass
// TestRefusalFiresBehindAGlobalFlag covers: every guard keyed off the verb
// stops firing, with nothing failing.
//
// Driven from GlobalValueFlags() rather than a hand-written expectation. This
// test previously carried its own copy of the table and asserted bd declares
// "exactly four" persistent value flags. bd declares seven -- --database,
// --format and --mem-profile were missing from both the copy and the manifest
// -- and the copy is what made the omission look deliberate (gs-9zu).
//
// Completeness of the set itself is NOT this test's job and cannot be: a flag
// deleted from the manifest disappears from the loop below instead of failing
// it. TestBdGlobalFlagManifestMatchesModuleSource in flags_source_test.go is
// the authority, deriving the set from bd's own persistent-flag registrations.
// What is pinned here is the pair of flags whose bypass was actually observed,
// so an emptied manifest cannot pass this file either.
func TestSplitGlobalFlagsSkipsEveryGlobalValue(t *testing.T) {
	globals := GlobalValueFlags()
	if len(globals) == 0 {
		t.Fatal("GlobalValueFlags() is empty; SplitGlobalFlags would read any global flag's value as the verb")
	}
	for _, required := range []string{"--actor", "-C"} {
		if !globals[required] {
			t.Errorf("GlobalValueFlags() is missing %s, whose value was read as the verb in the observed bypass", required)
		}
	}

	for flag := range globals {
		t.Run(flag, func(t *testing.T) {
			// The value is spelled like a bd verb on purpose: a scanner that
			// fails to skip it returns "update" and looks correct on any
			// assertion weaker than an exact verb match.
			verb, rest := SplitGlobalFlags([]string{flag, "update", "close", "gcy-dv7"})
			if verb != "close" {
				t.Errorf("SplitGlobalFlags with %s: verb = %q, want %q -- the flag's value was read as the verb", flag, verb, "close")
			}
			if !reflect.DeepEqual(rest, []string{"gcy-dv7"}) {
				t.Errorf("SplitGlobalFlags with %s: rest = %v, want [gcy-dv7]", flag, rest)
			}
		})
	}
}

// TestRefusalFiresBehindAGlobalFlag composes the two halves of the guard the way
// the caller does — locate the verb, then judge its args — and pins that a
// global value-flag before the verb does not disarm it.
//
// Testing SplitGlobalFlags and DroppedMetadataRefusal only in isolation leaves
// the composition untested, and the composition is where the bypass lived:
// `bd --actor bob update <id> --set-metadata a=1 b=2` yielded verb "bob", the
// refusal is scoped to "update", so it never fired and bd performed the silent
// 1-of-N write the guard exists to prevent.
func TestRefusalFiresBehindAGlobalFlag(t *testing.T) {
	prefixes := [][]string{
		{"--actor", "bob"},
		{"-C", "/some/dir"},
		{"--db", "/x/y.db"},
		{"--directory", "/d"},
		{"--dolt-auto-commit", "off"},
		{"--actor", "bob", "--json", "-C", "/d"},
	}
	for _, prefix := range prefixes {
		args := append(append([]string{}, prefix...), "update", "bd-1", "--set-metadata", "a=1", "b=2")
		verb, rest := SplitGlobalFlags(args)
		msg, ok := DroppedMetadataRefusal("gc bd", verb, rest)
		if !ok {
			t.Errorf("prefix %v: refusal did not fire (verb=%q); the silent 1-of-N write survives", prefix, verb)
			continue
		}
		if !contains(msg, "b=2") {
			t.Errorf("prefix %v: message %q does not name the dropped pair", prefix, msg)
		}
	}
}

// TestPositionalsKnowsEveryValueTakingFlag is the drift guard: positional
// detection must consume the value of EVERY value-taking flag for the
// subcommand. With a partial set, the value of any omitted flag is read as a
// positional — which is how `update <id> --add-label role=worker` came to look
// like a stray key=value token.
func TestPositionalsKnowsEveryValueTakingFlag(t *testing.T) {
	for flag := range ValueFlags("update") {
		got := Positionals("update", []string{"bd-1", flag, "role=worker"})
		if len(got) != 1 || got[0] != "bd-1" {
			t.Errorf("Positionals(update, bd-1 %s role=worker) = %v; the flag's value was read as an id", flag, got)
		}
	}
}

// TestDroppedMetadataPairs pins detection in both directions.
func TestDroppedMetadataPairs(t *testing.T) {
	dropped := [][]string{
		{"bd-1", "--set-metadata", "a=1", "b=2"},
		{"bd-1", "--set-metadata", "a=1", "b=2", "c=3"},
		{"bd-1", "--set-metadata=a=1", "b=2"},
	}
	for _, args := range dropped {
		if len(DroppedMetadataPairs(args)) == 0 {
			t.Errorf("DroppedMetadataPairs(%v) = none; want the dropped pair caught", args)
		}
	}
	valid := [][]string{
		{"bd-1", "--set-metadata", "a=1"},
		{"bd-1", "--set-metadata", "a=1", "--set-metadata", "b=2"},
		{"bd-1", "--add-label", "role=worker"},
		{"bd-1", "--set-labels", "a=b"},
		{"bd-1", "--external-ref", "https://example.test/i/ABC-1?tab=activity"},
		{"bd-1", "--metadata", `{"url":"https://x?a=b"}`},
		{"bd-1", "bd-2", "--set-metadata", "a=1"},
	}
	for _, args := range valid {
		if got := DroppedMetadataPairs(args); len(got) != 0 {
			t.Errorf("DroppedMetadataPairs(%v) = %v; this is a valid invocation", args, got)
		}
	}
}

// TestDroppedMetadataRefusalOnlyUpdate pins that the refusal is scoped to update.
func TestDroppedMetadataRefusalOnlyUpdate(t *testing.T) {
	if _, ok := DroppedMetadataRefusal("gc bd", "create", []string{"t", "--set-metadata", "a=1", "b=2"}); ok {
		t.Error("refusal fired for create; --set-metadata is an update flag")
	}
	msg, ok := DroppedMetadataRefusal("gc bd", "update", []string{"bd-1", "--set-metadata", "a=1", "b=2"})
	if !ok {
		t.Fatal("refusal did not fire for update")
	}
	for _, want := range []string{"b=2", "--set-metadata", "exits 0"} {
		if !contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
