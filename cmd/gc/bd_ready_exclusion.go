package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beads"
)

// The flags `gc bd ready` adds to the forwarded argv so the CLI's ready view
// is the same ready view every store computes.
//
// The defect this closes: `gc bd ready` execs bd, bd knows nothing about gc's
// ready-exclusion set, and bd caps at 100 rows by default. On 2026-09-10 the
// city store held 101 external-messaging fabric rows, so the cap was spent
// entirely on infrastructure bookkeeping -- 100 rows shown, 4 of them real,
// with 6 real ready beads (two naming a live agent as assignee) below the cut.
// The command exited 0. An agent reading it to see what was outstanding got a
// confidently wrong answer, which is the failure shape the city's own
// CLAUDE.md already warns about for bare `bd` (ci-6wwggu).
//
// REJECTED, and it is the option a future editor reaches for first:
// intercepting `ready` in gc and rendering the filtered rows ourselves. That
// is a second copy of bd's output format and bd's ready semantics, and it is
// the exact shape of ci-ctkz -- gc shadowed `heartbeat` for two months after
// bd claimed the name, with the whole suite green. Pushing the exclusion into
// bd's own query keeps ONE renderer and ONE ready implementation: gc
// contributes the set, bd contributes everything else. It also fixes the cap
// as a side effect rather than by raising it, because the 100 rows are then
// spent on real work.
//
// DELIBERATELY NOT SUPPRESSIBLE. No gc flag turns the filter off, because a
// switch that reaches the filter before the filter runs is a switch a caller
// sets once and then reads a truncated view through forever. The escape hatch
// is the bd binary itself, which `gc bd`'s help already names for the other
// behavior gc forces on the passthrough (BD_EXPORT_AUTO).
const (
	bdExcludeLabelFlag = "--exclude-label"
	bdExcludeTypeFlag  = "--exclude-type"
)

// augmentBdReadyArgs returns args with gc's ready-exclusion sets inserted when
// args invoke bd's `ready` command, and unchanged otherwise. The input slice
// is never written through: doBd hands the same slice to guards that run after
// this point.
//
// The verb is located with bdflags.SplitGlobalFlags rather than read from
// args[0]. The naive read takes "bob" out of `--actor bob ready` and the
// filter then stops applying for exactly the callers who pass an explicit
// actor -- with the suite still green, because it asserts the argv gc builds
// and is simply never handed that one. Same trap, same fix, as
// bdPreWriteMutation.
//
// Spellings come from bdflags.AliasGroup, not the literal "ready". bd
// registers no alias for it today; derived, one a later beads bump adds joins
// the filter instead of quietly opening an unfiltered second door.
//
// ABSENT: `bd list --ready`, which reaches the same ready semantics by another
// route. Filtering it would mean parsing --ready out of an argv gc otherwise
// forwards whole, and deciding what `gc bd list` shows without it -- an
// ordinary list, where the fabric rows legitimately belong. An operator
// chasing a fabric row keeps that view.
func augmentBdReadyArgs(args []string) []string {
	verb, rest := bdflags.SplitGlobalFlags(args)
	if verb == "" || !isBdReadyVerb(verb) {
		return args
	}
	// SplitGlobalFlags returns rest as the suffix after the verb, so the
	// verb's own index is fixed by the two lengths. Computed rather than
	// re-scanned: a second scan is a second copy of the global-flag rules,
	// and the two would disagree the first time bd adds a global flag.
	verbIdx := len(args) - len(rest) - 1

	out := make([]string, 0, len(args)+4)
	out = append(out, args[:verbIdx+1]...)
	out = append(out,
		bdExcludeLabelFlag, strings.Join(beads.ReadyExcludedLabels(), ","),
		bdExcludeTypeFlag, strings.Join(beads.ReadyExcludedTypes(), ","),
	)
	out = append(out, rest...)
	return out
}

// isBdReadyVerb reports whether argv0 selects bd's ready command under any of
// its published spellings.
func isBdReadyVerb(argv0 string) bool {
	for _, spelling := range bdflags.AliasGroup("ready") {
		if spelling == argv0 {
			return true
		}
	}
	return false
}
