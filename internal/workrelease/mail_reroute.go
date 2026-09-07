package workrelease

import (
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

// This file is the OTHER half of the question this package owns. The work sweep
// asks which beads addressed to an ending session may be taken back; that sweep
// deliberately excludes mail beads, because a message bead's assignee is its
// only route to an inbox and clearing it destroys the message silently
// (ra-59207). The exclusion was right and it left the other half unbuilt.
//
// WHAT WAS UNBUILT, measured in the pilot city 2026-09-07. Eleven open messages
// addressed to eight session beads that had since closed. Each was stored with
// the recipient session's ephemeral address as its assignee -- `gc mail send
// <session-id>` writes the bead id verbatim, and a session with no alias has
// nothing else -- so when the session bead closed the message pointed at an
// identifier nobody bears again. It was not re-routed, appeared in no inbox for
// anyone, and stayed open forever. The only thing naming it was one
// `closed-bead-owner` advisory among a dozen `gc doctor` findings. Three of the
// eleven were live operational warnings written for a bench sitting that was
// running at the time, and one named the exact grader refusal that sitting then
// hit (ci-cw9wsk).
//
// Beadmail's own reader does try a closed-session pass, which is why this looks
// like it should already work. That fallback fires only while NO live session
// claims the address: the moment a successor occupies the seat, the live pass
// wins, the dead session's bead id drops out of the route set, and the message
// is filtered from every view. A pool slot always gets a successor.
//
// Here rather than in each door for this package's whole reason for existing:
// four session-ending paths (the reconciler close, the named-session retire,
// `gc session close`, and the HTTP close handlers) already share the work rule,
// and the mail rule has the same four callers and the same way of going wrong
// -- three implementations that drift, with the quiet one delivering nothing.

// RerouteMailFromEndedSession moves the ending session's still-undelivered mail
// off the addresses that die with it and onto one a future reader will look at.
// It returns how many messages moved and how many failed to move.
//
// FROM is the ending session's EPHEMERAL identities -- its bead id and its
// runtime session_name -- because those are the addresses nobody bears again.
// Durable seat identities are left alone while the seat survives, for the same
// reason Targets refuses to release an OPEN bead addressed to one: the next
// occupant answers for them, so a message sitting there is addressed, not
// stranded. When the seat RETIRES every identity it held becomes unbearable and
// Targets already says so, which is why the target list decides and no second
// rule here does.
//
// TO is the surviving seat address (session.SeatMailboxAddress: the alias, else
// the seat identity in agent_name), and when the seat is gone the owning
// pool/agent route the seat was cut from (FallbackRoute). Both are read off the
// ending session's own metadata, so no role name appears here.
//
// An empty destination is a no-op rather than a move. A message left on a dead
// address is at least still findable by id and reported by `gc doctor`; one
// moved to an address nothing ever resolves is findable by nothing.
//
// Best-effort, like the work sweep beside it: errors are reported to stderr and
// never returned, because the session IS ended by the time this runs and no
// failure here can undo that. Idempotent, because all four doors can re-run and
// the reconciler's orphan pass repeats every tick.
//
// SCOPE THIS DOES NOT COVER, and it is an absence rather than an oversight.
// store is the caller's own session/work store, so a city that relocates
// message beads with [beads.classes.mail] has its mail swept from the wrong
// place -- Reroute finds nothing there and reports no error, because an empty
// store legitimately has no stranded mail. Closing it needs the mail
// coordination-class store threaded through all four session-ending doors
// (cmd/gc/class_store.go resolveMailMessagesStore is where it is resolved
// today), which is a wider change than this defect warrants and shares the
// shape of the cross-store gap the HTTP close handler already documents. Until
// then a relocated-mail city keeps the pre-fix behavior and `gc doctor`'s
// closed-bead-owner advisory stays its only signal.
func RerouteMailFromEndedSession(
	store beads.Store,
	sessionBead beads.Bead,
	identities []string,
	retired SeatRetirement,
	stderr io.Writer,
) (moved, failed int) {
	if store == nil || strings.TrimSpace(sessionBead.ID) == "" {
		return 0, 0
	}
	if stderr == nil {
		stderr = io.Discard
	}

	// Targets carries each identity once per status; Reroute dedupes by message
	// id, so a duplicate address costs one extra list and no correctness. Folded
	// to unique addresses anyway, because the log line below counts messages and
	// a reader should not have to know the target list's shape to trust it.
	from := make([]string, 0, 4)
	for _, target := range Targets(SeatFromBead(sessionBead), identities, retired) {
		address := strings.TrimSpace(target.Assignee)
		if address == "" || containsAddress(from, address) {
			continue
		}
		from = append(from, address)
	}

	to := session.SeatMailboxAddress(sessionBead)
	if retired == SeatRetired {
		to = FallbackRoute(sessionBead)
	}

	movedIDs, moveErrs, listErr := beadmail.Reroute(beads.MailStore{Store: store}, from, to)
	if listErr != nil {
		fmt.Fprintf(stderr, "mail reroute: reading mail addressed to ended session %s: %v\n", sessionBead.ID, listErr) //nolint:errcheck
	}
	for _, err := range moveErrs {
		fmt.Fprintf(stderr, "mail reroute: moving mail off ended session %s: %v\n", sessionBead.ID, err) //nolint:errcheck
	}
	if len(movedIDs) > 0 {
		fmt.Fprintf(stderr, "mail reroute: moved %d undelivered message(s) off ended session %s to %q\n", len(movedIDs), sessionBead.ID, to) //nolint:errcheck
	}
	return len(movedIDs), len(moveErrs)
}

func containsAddress(addresses []string, address string) bool {
	for _, existing := range addresses {
		if existing == address {
			return true
		}
	}
	return false
}
