// Gate on the verbs `gc bd` handles itself: none of them may shadow a bd
// subcommand without a human having said so.
//
// The suite exists because the failure it catches is not a design error but a
// dependency bump. `bd heartbeat` arrived under an intercept of the same name
// on 2026-08-05 and went unnoticed until 2026-08-09, with every test green
// throughout -- each asserted the intercept did what its author intended, and
// none asked whether bd had since claimed the name (ci-ctkz, ci-mosn). So the
// tests here assert about the RELATIONSHIP between two name sets rather than
// about gc's behavior, and one of them exercises the verdict against the bd
// name sets of two real beads versions so the collision arm is not left
// unproven by whichever version go.mod happens to pin today.
//
// Delegated elsewhere: whether the bd name set itself is current, which is
// internal/bdflags/commands_source_test.go's job.
//
//	go test ./cmd/gc/ -run 'BdIntercept'
package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/bdflags"
)

// TestBdInterceptRegistryHoldsEveryKnownVerb keeps the gate below from passing
// on an empty or partial registry. Every other test here iterates
// bdInterceptedVerbs, so a verb that failed to register -- or a registry the
// linker dropped -- would make all of them vacuously green, which is the same
// silence the gate exists to break.
func TestBdInterceptRegistryHoldsEveryKnownVerb(t *testing.T) {
	got := make(map[string]bdInterceptKind, len(bdInterceptedVerbs))
	for _, v := range bdInterceptedVerbs {
		if _, dup := got[v.name]; dup {
			t.Errorf("verb %q is registered twice; the gate would check it twice and dispatch would take the first", v.name)
		}
		got[v.name] = v.kind
	}
	want := map[string]bdInterceptKind{
		"heartbeat":          bdVerbWrapsBd,
		"release-if-current": bdVerbIsGcOwn,
	}
	for name, kind := range want {
		gotKind, ok := got[name]
		if !ok {
			t.Errorf("bdInterceptedVerbs does not include %q; every verb gc bd handles itself must be registered to be checked", name)
			continue
		}
		if gotKind != kind {
			t.Errorf("verb %q is registered as kind %v, want %v", name, gotKind, kind)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("verb %q is registered but this test does not know it. Add it here with its kind, having first checked that kind is right.", name)
		}
	}
}

// TestBdInterceptsDoNotShadowBdCommands is the gate. It fails when a beads
// bump gives bd a command by a name gc claims as its own, and when a wrap
// declaration outlives the bd command it wraps.
func TestBdInterceptsDoNotShadowBdCommands(t *testing.T) {
	for _, msg := range bdInterceptCollisions(bdInterceptedVerbs, bdflags.AliasGroup) {
		t.Error(msg)
	}
}

// TestBdInterceptCollisionsVerdictAcrossBeadsVersions exercises the verdict
// against the two beads versions that bracket the original bug.
//
// The pairing is the point and neither half stands alone. The v1.1.0 case
// alone would pass just as well against a lookup that was never consulted or a
// name misspelled in the fixture; the v1.1.1 case proves the same declaration
// and the same spelling do produce a verdict, so silence in the first case is
// evidence about bd v1.1.0 rather than about the test.
//
// The fixtures model the slice of each version's name set these declarations
// can touch, not the whole of it -- v1.1.0 root-registers 115 names and
// v1.1.1-0.20260805093327 registers 124. Only the difference between them
// decides a verdict. Both fixtures were checked against the real module source
// at both versions under ci-mosn: heartbeat, hb and reclaim are the names the
// bump added, and release-if-current is in neither.
func TestBdInterceptCollisionsVerdictAcrossBeadsVersions(t *testing.T) {
	// bd v1.1.0 (2026-07-13): no lease concept, so no heartbeat command and
	// nothing named hb.
	beadsV110 := bdNameSet(map[string][]string{
		"close": {"done"},
		"show":  {"view"},
	})
	// bd v1.1.1-0.20260805093327: claim leases, and with them heartbeat/hb.
	beadsV111 := bdNameSet(map[string][]string{
		"close":     {"done"},
		"show":      {"view"},
		"heartbeat": {"hb"},
	})

	heartbeatAsGcOwn := []bdInterceptedVerb{{name: "heartbeat", kind: bdVerbIsGcOwn}}

	if got := bdInterceptCollisions(heartbeatAsGcOwn, beadsV110); len(got) != 0 {
		t.Errorf("intercepting %q as gc's own reports %v against bd v1.1.0, which had no such command", "heartbeat", got)
	}
	got := bdInterceptCollisions(heartbeatAsGcOwn, beadsV111)
	if len(got) != 1 {
		t.Fatalf("intercepting %q as gc's own against bd v1.1.1 reports %d collisions, want exactly 1: this is the shadowing that shipped for two months", "heartbeat", len(got))
	}
	// The report has to name the alias too. An operator told only that
	// "heartbeat" collides fixes one spelling and leaves `gc bd hb` on the
	// other path, which is the half of ci-ctkz that outlived its own fix.
	for _, want := range []string{"heartbeat", "hb"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("collision report %q does not name the spelling %q", got[0], want)
		}
	}
}

// TestBdInterceptCollisionsCatchesAnAliasOnlyCollision pins the case a
// canonical-name-only check would wave through: bd claiming a gc verb as an
// ALIAS of some command with an unrelated name. The name gc dispatches on is
// taken either way.
func TestBdInterceptCollisionsCatchesAnAliasOnlyCollision(t *testing.T) {
	bd := bdNameSet(map[string][]string{"reassign": {"release-if-current"}})
	got := bdInterceptCollisions([]bdInterceptedVerb{bdReleaseIfCurrentVerb}, bd)
	if len(got) != 1 {
		t.Fatalf("bd claiming %q as an alias of `reassign` reports %d collisions, want 1", "release-if-current", len(got))
	}
	if !strings.Contains(got[0], "reassign") {
		t.Errorf("collision report %q does not name the bd command that owns the alias", got[0])
	}
}

// TestBdInterceptCollisionsCatchesAStaleWrap pins the other direction: an
// acknowledged wrap of a bd command bd no longer has. Left unreported, gc
// would keep intercepting a name for the sake of a command that is gone, and
// the acknowledgement would read as though someone had checked recently.
func TestBdInterceptCollisionsCatchesAStaleWrap(t *testing.T) {
	bd := bdNameSet(map[string][]string{"close": {"done"}})
	got := bdInterceptCollisions([]bdInterceptedVerb{bdHeartbeatVerb}, bd)
	if len(got) != 1 {
		t.Fatalf("a wrap of %q against a bd with no such command reports %d collisions, want 1", bdHeartbeatVerb.name, len(got))
	}
}

// TestBdInterceptCollisionsReportsAnUnknownKind pins the branch that keeps a
// future third kind from arriving as silence. A kind with no case would leave
// every verb declared with it unchecked while the suite stayed green, which is
// the shape of the original bug rather than a new one.
func TestBdInterceptCollisionsReportsAnUnknownKind(t *testing.T) {
	unknown := bdInterceptedVerb{name: "somelater-verb", kind: bdInterceptKind(99)}
	got := bdInterceptCollisions([]bdInterceptedVerb{unknown}, bdNameSet(nil))
	if len(got) != 1 {
		t.Fatalf("a verb of an unhandled kind reports %d collisions, want 1", len(got))
	}
	if !strings.Contains(got[0], "somelater-verb") {
		t.Errorf("report %q does not name the verb it could not check", got[0])
	}
}

// TestBdInterceptSpellingsFollowTheWrappedCommand pins what dispatch actually
// matches on. A wrap must claim every spelling of the bd command it wraps --
// derived, so an alias a later bump adds is claimed too -- while a verb of
// gc's own claims exactly its own name and does not start swallowing traffic
// because bd happens to use that word as an alias elsewhere.
func TestBdInterceptSpellingsFollowTheWrappedCommand(t *testing.T) {
	bd := bdNameSet(map[string][]string{"heartbeat": {"hb", "beat"}})

	if got, want := bdHeartbeatVerb.spellings(bd), []string{"heartbeat", "hb", "beat"}; !sameStrings(got, want) {
		t.Errorf("wrap of heartbeat claims %v, want %v", got, want)
	}
	if got, want := bdReleaseIfCurrentVerb.spellings(bd), []string{"release-if-current"}; !sameStrings(got, want) {
		t.Errorf("gc's own verb claims %v, want %v", got, want)
	}
	// bd having dropped the command does not silently unclaim the verb.
	if got, want := bdHeartbeatVerb.spellings(bdNameSet(nil)), []string{"heartbeat"}; !sameStrings(got, want) {
		t.Errorf("wrap of a command bd no longer has claims %v, want %v", got, want)
	}
}

// TestBdInterceptRegistryDrivesArgParsing proves the registry is load-bearing
// rather than a manifest kept beside the real decision. If the parse functions
// still matched their own literals, every test above could pass while dispatch
// went on doing something else -- a gate on a table nothing reads.
func TestBdInterceptRegistryDrivesArgParsing(t *testing.T) {
	for _, spelling := range bdHeartbeatVerb.spellings(bdflags.AliasGroup) {
		if _, ok, err := parseBdHeartbeatArgs([]string{spelling, "ci-abc"}); !ok || err != nil {
			t.Errorf("parseBdHeartbeatArgs does not claim registered spelling %q (ok=%v, err=%v)", spelling, ok, err)
		}
	}
	for _, spelling := range bdReleaseIfCurrentVerb.spellings(bdflags.AliasGroup) {
		if _, _, ok, err := parseBdReleaseIfCurrentArgs([]string{spelling, "ci-abc", "worker-1"}); !ok || err != nil {
			t.Errorf("parseBdReleaseIfCurrentArgs does not claim registered spelling %q (ok=%v, err=%v)", spelling, ok, err)
		}
	}
	// A bd command gc does NOT intercept must still reach bd untouched.
	for _, passthrough := range []string{"close", "done", "update", "list"} {
		if _, ok, _ := parseBdHeartbeatArgs([]string{passthrough, "ci-abc"}); ok {
			t.Errorf("parseBdHeartbeatArgs claims %q, which gc does not intercept", passthrough)
		}
		if _, _, ok, _ := parseBdReleaseIfCurrentArgs([]string{passthrough, "ci-abc", "worker-1"}); ok {
			t.Errorf("parseBdReleaseIfCurrentArgs claims %q, which gc does not intercept", passthrough)
		}
	}
}

// bdNameSet builds an alias-group lookup over a canonical-name-to-aliases
// table, with the same by-alias reachability bdflags.AliasGroup has.
//
// It answers nil for a name absent from the table, which is not a permissive
// default but the real meaning of absence: bd has no command by that name. The
// risk that carries -- a fixture typo reading as "bd does not have it" -- is
// why every no-collision case above is paired with a collision case over the
// same spelling.
func bdNameSet(commands map[string][]string) func(string) []string {
	return func(name string) []string {
		for canonical, aliases := range commands {
			if canonical == name {
				return append([]string{canonical}, aliases...)
			}
			for _, alias := range aliases {
				if alias == name {
					return append([]string{canonical}, aliases...)
				}
			}
		}
		return nil
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
