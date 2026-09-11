package tmux

import (
	"errors"
	"regexp"
	"strings"
)

// Overlay recognition for the nudge submit loop.
//
// submitEnterAndConfirm re-sends its submit key only while the pane shows no
// busy indicator, which means the states it re-sends into are exactly the ones
// that produce no busy indicator -- and a provider OVERLAY is one. An overlay
// gives Enter a DIFFERENT meaning: accept the highlighted entry, advance to the
// next picker, confirm a prompt. Re-sending there does not deliver a nudge, it
// operates the TUI.
//
// MEASURED 2026-09-11 against the binaries the city actually runs, on detached
// panes, and the captures are checked in under testdata/panes/ rather than
// described:
//
//	codex-cli 0.153.4   a composer holding "/" opens the command palette and
//	                    paneContainsBusyIndicator reads 0. Three Enters walked
//	                    /model -> model picker -> reasoning picker and
//	                    COMMITTED the change (ci-gqvu9q's closing note).
//	codex-cli 0.153.4   the directory-trust prompt and the unauthenticated
//	                    sign-in screen are the same hazard at session START:
//	                    no busy indicator, "Press enter to continue", and the
//	                    highlighted entry is "1. Yes, continue".
//	Claude Code 2.1.268 a composer holding "/" opens the same shape of palette.
//
// WHY THIS SPANS BOTH FAMILIES rather than being scoped to codex. Claude was
// measured IMMUNE to the re-send walk -- its first Enter runs the highlighted
// command and the pane goes busy, so submitEnterAndConfirm stops before any
// re-send -- so a codex-only guard would be sufficient today. It is written for
// both anyway because the two TUIs render the palette in the SAME shape (a
// two-space-indented "/command" followed by its description), so one predicate
// covers both at no extra cost, and because claude's immunity rests on a TUI
// behavior no contract pins. A guard that is correct only while a vendor keeps
// committing on the first Enter is a guard that rots silently.
//
// BOTH FAILURE DIRECTIONS ARE SAFE, which is what licenses a text matcher here.
// A false positive skips a re-send, so the nudge is reported delivered but
// unconfirmed -- the outcome the non-verify families have always had. A false
// negative leaves today's behavior exactly as it is. Neither direction can
// press Enter more often than the code already does, so transcript text that
// happens to look like a palette row costs nothing.
//
// NOT KEYED ON THE MESSAGE. "Skip the re-send when the nudge starts with /"
// tests the message, and the hazard is in the PANE: it is silent on the trust
// prompt, on the sign-in screen, and on every other overlay a TUI may be
// showing for reasons that have nothing to do with what was pasted.

// enterOverlayPromptMarkers are strings a TUI renders when an overlay, not the
// composer, will consume the next Enter. Each is quoted from a checked-in
// capture; do not add one that has not been seen in a real pane.
var enterOverlayPromptMarkers = []string{
	// codex's directory-trust prompt and its sign-in screen both end with this
	// line, above a numbered list whose first entry is preselected.
	"Press enter to continue",
}

// paletteRowRe matches a rendered command-palette row: indentation, a slash
// command, then a run of whitespace and its description.
//
// The two-space indent and the description column are both load-bearing. A
// composer holding a draft sits at column zero behind its own prompt glyph
// (U+276F for claude, U+203A for codex), so an indented row is never the
// composer, and requiring a description separated by two or more spaces is what
// keeps an ordinary transcript line such as "  /usr/bin/foo" or a lone "  /tmp"
// from matching. The command token stops at the first "/" it does not start
// with, so a path never reaches the description test at all.
var paletteRowRe = regexp.MustCompile(`^\s{2,}/[a-z][a-z0-9-]*\s{2,}\S`)

// paneShowsEnterOverlay reports whether the pane is in a state where Enter
// means "accept this overlay" rather than "submit the composer".
//
// Callers gate a RE-send on it, never the first send: the first send is
// today's behavior for every family and changing it is a separate argument
// (it is also the half that already commits a command on claude, which is
// recorded on ci-vkadhb rather than fixed here).
func paneShowsEnterOverlay(lines []string) bool {
	for _, line := range lines {
		for _, marker := range enterOverlayPromptMarkers {
			if strings.Contains(line, marker) {
				return true
			}
		}
		if paletteRowRe.MatchString(line) {
			return true
		}
	}
	return false
}

// errSubmitOverlayPresent ends the confirm loop because the pane is showing an
// overlay rather than because delivery failed.
//
// It travels as an error rather than as a third return value so the thirteen
// existing loop tests keep their shape, and NudgeSession maps it onto the
// unconfirmed path: the keys reached tmux, so this is not a send failure.
var errSubmitOverlayPresent = errors.New("submit not re-sent: the pane is showing an overlay that consumes Enter")

// paneShowsOverlay reports whether target's visible pane is in a state where
// Enter accepts an overlay instead of submitting the composer.
func (t *Tmux) paneShowsOverlay(target string) (bool, error) {
	lines, err := t.CapturePaneLines(target, promptObservationLines)
	if err != nil {
		return false, err
	}
	return paneShowsEnterOverlay(lines), nil
}
