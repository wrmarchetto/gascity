package tmux

import "unicode/utf8"

// Composer sigil neutralization for nudge delivery.
//
// A nudge is pasted into a provider's composer and then submitted with Enter.
// If the pasted text BEGINS with a character the composer treats as a mode
// prefix, the submit does not deliver a message at all -- it runs something.
//
// MEASURED 2026-09-11 against Claude Code 2.1.268 on a detached pane, driving
// the same `send-keys -l` path production uses (tmux.go sendLiteralText):
//
//	"/status and then some prose"   ran /status and DISCARDED the prose
//	"!touch <path>  # ..."          RAN A SHELL COMMAND. The marker file was
//	                                created on disk, and the nudge was never
//	                                delivered as a message
//	"#remember this ..."            delivered as text -- "#" is NOT a sigil in
//	                                this build, so it is deliberately absent
//	                                from the set below
//
// Each of the first two then leaves the pane BUSY, so submitEnterAndConfirm
// observes a submit and reports the nudge CONFIRMED DELIVERED. Nothing retries.
// That is what makes this worth a transformation rather than a refusal: the
// failure is silent, and the caller cannot tell it happened.
//
// WHY A LEADING SPACE, and why not the alternatives. All four were measured
// rather than argued:
//
//   - Bracketed paste does NOT help. The production path already switches to
//     `paste-buffer -p` above 4096 bytes, and the same "/"-prefixed text ran
//     the same command through both mechanisms. So this is not length-scoped
//     and cannot be fixed by choosing a carrier.
//   - Escape-then-Enter has nothing to dismiss. In the failing case no overlay
//     is showing at all -- the composer simply holds a command. Escape is also
//     a SEMANTIC key for this family (providersSkippingEscapeBeforeEnter, and
//     ga-3xu is the incident where synthesizing one wedged a worker).
//   - Refusing to deliver was the conservative option. It trades a silent loss
//     for a visible non-delivery and still needs a human to clear the pane,
//     where one space delivers the message intact.
//
// A single leading space was measured to deliver the message as text on BOTH
// families, with the shell command NOT run and the marker file absent. Codex
// trims the space in its own echo; claude keeps it in the composer line, where
// it is inert.
//
// THIS IS A CLAUDE-FAMILY DEFECT AND THE GUARD IS STILL UNCONDITIONAL. Codex
// was measured to deliver "/status and then some prose" as text with NO leading
// space -- its palette only opens for a bare command prefix with no trailing
// text -- so it needs nothing here. The space is added for every family anyway
// because it is a no-op wherever the sigil is not a sigil, and scoping it to a
// family list would mean re-measuring every provider gc drives before the
// guard could protect any of them.
//
// NOT A SECURITY BOUNDARY. It removes an accident, not an attack: anything that
// can choose nudge text can also choose text whose SECOND character matters.
// The city's nudge texts are prose from agent.toml, mayor rulings, queued mail
// and continuation text, and this stops one of those beginning with "!" or "/"
// from running instead of arriving.

// composerSigils are the leading characters a provider composer reads as a mode
// prefix rather than as message text. Measured, one probe each; see the file
// comment for what each one did and for the "#" that is deliberately absent.
var composerSigils = []rune{
	'/', // slash command: runs it and discards the rest of the message
	'!', // bash mode: EXECUTES the remainder as a shell command
}

// neutralizeComposerSigil returns message in a form a composer will accept as
// text, prepending one space when it begins with a sigil.
//
// Leading whitespace already present is left alone: such a message is already
// safe, and adding a second space would change text for no reason. An empty
// message is returned unchanged -- there is nothing to submit and nothing to
// neutralize.
func neutralizeComposerSigil(message string) string {
	if message == "" {
		return message
	}
	first, _ := utf8.DecodeRuneInString(message)
	for _, sigil := range composerSigils {
		if first == sigil {
			return " " + message
		}
	}
	return message
}
