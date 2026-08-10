package main

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/bdflags"
)

// The verbs `gc bd` handles itself instead of forwarding to the bd binary,
// and the gate that keeps one of them from silently shadowing a bd subcommand.
//
// Why this is a registry and not two string literals at their comparison
// sites: gc intercepted "heartbeat" from 2026-06-01, when beads v1.0.5 had no
// command by that name and the name was free. The 2026-08-05 bump to
// v1.1.1-0.20260805093327 added claim leases and with them `bd heartbeat`,
// which gc's intercept then shadowed for two months with the whole suite green
// -- every test asserted the intercept did what its author intended, and none
// asked whether bd had since claimed the same name (ci-ctkz, ci-mosn). A name
// compared inline is invisible to any such check; a name that must be
// registered to work is not.
//
// Invariants, both pinned by cmd_bd_intercept_collision_test.go:
//   - Every intercepted verb is reachable only through registerBdIntercept.
//   - A verb declared bdVerbIsGcOwn matches no bd command name or alias, and a
//     verb declared bdVerbWrapsBd matches one that still exists.

// bdInterceptKind records whether bd already owns the name a gc intercept
// dispatches on. It is the difference between a reviewed decision and the
// collision this file exists to catch, so it is stated per verb rather than
// inferred: both look identical from inside gc.
type bdInterceptKind int

const (
	// bdVerbIsGcOwn: the name is gc's alone. bd has no command by it, and
	// `gc bd <verb>` reaching gc rather than bd costs the caller nothing.
	bdVerbIsGcOwn bdInterceptKind = iota

	// bdVerbWrapsBd: bd owns the name and gc's handler runs bd's command as
	// part of its own work, so the caller still gets bd's behavior plus gc's.
	// This is an acknowledgement, not an exemption -- it says a human compared
	// the two and chose to wrap.
	bdVerbWrapsBd
)

// bdInterceptedVerb is one argv[0] that `gc bd` handles itself.
type bdInterceptedVerb struct {
	name string
	kind bdInterceptKind
}

// bdInterceptedVerbs is every registered intercept, in declaration order. It
// is what the collision gate iterates, which is the whole reason registration
// is the only way to obtain a bdInterceptedVerb: a verb the gate cannot see is
// a verb the gate cannot check.
var bdInterceptedVerbs []bdInterceptedVerb

// registerBdIntercept declares an intercepted verb and returns it for the
// dispatch site to hold. Call it only from a package-level var initializer --
// it mutates a package-level slice and does not lock.
func registerBdIntercept(name string, kind bdInterceptKind) bdInterceptedVerb {
	v := bdInterceptedVerb{name: name, kind: kind}
	bdInterceptedVerbs = append(bdInterceptedVerbs, v)
	return v
}

var (
	// bdHeartbeatVerb wraps bd's own `heartbeat`: gc refreshes the claim lease
	// through bd and then stamps gc.last_heartbeat_at, which the dashboard
	// needs because bd holds leases in a node-local table it never commits.
	bdHeartbeatVerb = registerBdIntercept("heartbeat", bdVerbWrapsBd)

	// bdReleaseIfCurrentVerb is gc's own conditional assignment reset. bd has
	// no compare-and-swap on assignee, so nothing of bd's runs on this path.
	bdReleaseIfCurrentVerb = registerBdIntercept("release-if-current", bdVerbIsGcOwn)
)

// spellings returns every argv[0] that must reach this intercept.
//
// A wrap claims the whole alias group of the bd command it wraps, read from
// bdflags rather than listed here. Listing them is what went wrong before: gc
// claimed "heartbeat" and not bd's alias "hb", so the two spellings of one
// command reached different code and only one carried gc's extra write
// (ci-ctkz). Derived, an alias a later beads bump adds joins the intercept when
// the name list moves instead of quietly splitting it again.
//
// When the group is empty the canonical name is still claimed. That case means
// bd no longer has the command, which the collision gate reports as a stale
// declaration; until someone acts on it, `gc bd heartbeat` keeps working rather
// than falling through to a bd that would reject it.
func (v bdInterceptedVerb) spellings(aliasGroup func(string) []string) []string {
	if v.kind == bdVerbWrapsBd {
		if group := aliasGroup(v.name); len(group) > 0 {
			return group
		}
	}
	return []string{v.name}
}

// claims reports whether argv0 selects this intercept.
func (v bdInterceptedVerb) claims(argv0 string) bool {
	for _, spelling := range v.spellings(bdflags.AliasGroup) {
		if spelling == argv0 {
			return true
		}
	}
	return false
}

// bdInterceptCollisions returns one message per declared intercept whose
// relationship to bd's own command names is wrong, given a lookup of bd's
// alias groups. Empty means every intercept is either a name bd does not use
// or an acknowledged wrap of one it still has.
//
// aliasGroup is a parameter rather than a direct bdflags call so the gate's
// verdict can be exercised against the bd name sets of other beads versions --
// including the v1.1.0 shape, where "heartbeat" was genuinely free, and the
// v1.1.1 shape, where it was not. Checking the predicate only against the
// version currently in go.mod would leave the collision arm of it unproven,
// which is how the original bug passed a green suite.
func bdInterceptCollisions(verbs []bdInterceptedVerb, aliasGroup func(string) []string) []string {
	var out []string
	for _, v := range verbs {
		group := aliasGroup(v.name)
		switch v.kind {
		case bdVerbIsGcOwn:
			if len(group) == 0 {
				continue
			}
			out = append(out, fmt.Sprintf(
				"gc bd intercepts %q as a verb of its own, but beads %s registers a bd command reachable as %v. gc's handler runs and bd's never does.\n"+
					"Rename gc's verb, or -- if gc's handler is meant to run bd's command too -- make it do that and declare it bdVerbWrapsBd.",
				v.name, bdflags.SourcedBeadsVersion, group))
		case bdVerbWrapsBd:
			if len(group) > 0 {
				continue
			}
			out = append(out, fmt.Sprintf(
				"gc bd declares %q a wrap of a bd command, but beads %s registers no command by that name.\n"+
					"The acknowledgement has outlived its reason: if the verb is now gc's alone, declare it bdVerbIsGcOwn; if the command moved, follow it.",
				v.name, bdflags.SourcedBeadsVersion))
		default:
			// A kind added without a case here would leave every verb of that
			// kind unchecked, which is this gate going quiet in exactly the
			// way it exists to prevent. Report rather than fall through.
			out = append(out, fmt.Sprintf(
				"gc bd intercepts %q under kind %d, which bdInterceptCollisions does not know how to check.\n"+
					"Give the new kind a case: until it has one, no verb declared with it is checked against bd's names at all.",
				v.name, v.kind))
		}
	}
	return out
}
