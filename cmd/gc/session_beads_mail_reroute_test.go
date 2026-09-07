package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

// ci-cw9wsk: mail addressed to a session that then closes was never delivered
// and no mail command surfaced it. `gc mail send` resolves the recipient to a
// live session and stores the message under that session's PRIMARY mailbox
// address, which for a session with no alias is its BEAD ID
// (session.MailboxAddress). When the session bead closes, the message keeps
// pointing at a dead ephemeral address: it is not re-routed, it appears in no
// inbox, and it stays open forever. Only `gc doctor`'s session-model check
// names it, as one advisory `closed-bead-owner` line among a dozen findings.
//
// Measured in the pilot city 2026-09-07: eleven such messages, addressed to
// eight closed session beads. Three were live operational warnings written for
// an overnight bench sitting -- one of them naming the exact grader refusal
// that sitting then hit -- and all three reached nobody. Seven of the eight
// dead recipients had a durable alias; the eighth (gascity/lab.engineer-1) had
// none and carried its seat identity in `agent_name` alone.
//
// WHY THE ASSERTIONS READ AN INBOX AND NOT AN ASSIGNEE FIELD. A test that
// checks the re-assignment landed passes while the message is still filtered
// out of every inbox view, which is the half that made this invisible for a
// day: the assignee was always set to SOMETHING. So each case here closes the
// recipient, stands a successor session up on the same seat, and requires the
// message to come back from beadmail's own reader through that successor's
// address -- the same reader `gc mail inbox` and the API both use.
//
// Each case MUST fail on unpatched source. Before the fix the message stays
// assigned to the closed session's bead ID, which no live session's mailbox
// address set contains, so every Inbox call below returns nothing.

// mailRerouteSeat is one pool seat's session-bead metadata. Written as a
// fixture rather than three literal maps because the whole point of the
// durable-address ladder is which KEY carries the seat, so the cases differ
// only in which of these is populated.
type mailRerouteSeat struct {
	alias     string
	agentName string
	template  string
}

func (s mailRerouteSeat) metadata(sessionName string) map[string]string {
	md := map[string]string{
		"session_name": sessionName,
		"state":        "active",
	}
	if s.alias != "" {
		md["alias"] = s.alias
	}
	if s.agentName != "" {
		md["agent_name"] = s.agentName
	}
	if s.template != "" {
		md["template"] = s.template
	}
	return md
}

func createMailRerouteSession(t *testing.T, store beads.Store, title, sessionName string, seat mailRerouteSeat) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:    title,
		Type:     sessionBeadType,
		Labels:   []string{sessionBeadLabel},
		Metadata: seat.metadata(sessionName),
	})
	if err != nil {
		t.Fatalf("create session bead %q: %v", title, err)
	}
	return b
}

// sendMailToLiveSessionAddress stores a message addressed to the recipient's
// BEAD ID, through the real Send path.
//
// The bead ID rather than the session's durable alias, because that is what
// production stores and it is not a fixture shortcut:
// resolveMailRecipientIdentityCached returns the resolved session ID verbatim
// whenever the recipient string is an exact bead ID, so `gc mail send
// ci-2vp1f2 ...` addresses the message to an identifier that dies with the
// session even though that session publishes under a durable alias. All three
// of the incident's engineer mails were addressed this way. It is also the only
// address a session with no alias has at all -- session.MailboxAddress falls
// back to the bead ID -- so the two cases below share one fixture.
func sendMailToLiveSessionAddress(t *testing.T, store beads.Store, recipient beads.Bead, subject string) beads.Bead {
	t.Helper()
	mp := beadmail.NewWithStores(store, store)
	msg, err := mp.Send("sender", recipient.ID, subject, "body")
	if err != nil {
		t.Fatalf("send mail: %v", err)
	}
	b, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get sent mail bead: %v", err)
	}
	return b
}

// closeSessionBead ends the recipient the way every session-ending door does
// before its sweeps run: the bead is already closed when the reroute executes.
// The order matters to the assertions, not to the reroute -- with the recipient
// still live its bead id is a route beadmail resolves, so a pre-check taken
// before the close finds the message and reports no defect.
func closeSessionBead(t *testing.T, store beads.Store, id string) {
	t.Helper()
	if err := store.Close(id); err != nil {
		t.Fatalf("close session bead %s: %v", id, err)
	}
}

func inboxSubjects(t *testing.T, store beads.Store, recipient string) []string {
	t.Helper()
	msgs, err := beadmail.NewWithStores(store, store).Inbox(recipient)
	if err != nil {
		t.Fatalf("inbox %q: %v", recipient, err)
	}
	subjects := make([]string, 0, len(msgs))
	for _, m := range msgs {
		subjects = append(subjects, m.Subject)
	}
	return subjects
}

// inboxSubjectsAsTheCLIWouldRead drives the reader the way `gc mail inbox`
// does: it hands beadmail the recipient set the SESSION publishes, rather than
// an address the test chose. doMailInboxTarget builds that set with
// session.MailboxAddresses and passes it to InboxRecipients, so this is the
// path an agent checking its own mail actually takes -- and the only one that
// can tell whether the successor KNOWS to look at its seat address.
func inboxSubjectsAsTheCLIWouldRead(t *testing.T, store beads.Store, sessionBead beads.Bead) []string {
	t.Helper()
	msgs, err := beadmail.NewWithStores(store, store).InboxRecipients(session.MailboxAddresses(sessionBead))
	if err != nil {
		t.Fatalf("inbox for session %s: %v", sessionBead.ID, err)
	}
	subjects := make([]string, 0, len(msgs))
	for _, m := range msgs {
		subjects = append(subjects, m.Subject)
	}
	return subjects
}

func containsSubject(subjects []string, want string) bool {
	for _, s := range subjects {
		if s == want {
			return true
		}
	}
	return false
}

// TestMailSurvivesRecipientSessionCloseViaAlias is the incident's majority
// case: the recipient publishes under a durable alias (astoria-sel4/lab.pm,
// toolsmith-1, governor -- seven of the eight dead recipients measured), the
// message was nonetheless stored under the ephemeral bead ID, and the successor
// session on that same alias must find it.
func TestMailSurvivesRecipientSessionCloseViaAlias(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{
		alias:     "rig/lab.pm",
		agentName: "rig/lab.pm",
		template:  "rig/lab.pm",
	}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "LIVE BLOCKER: precondition 5 fails")
	if msg.Assignee != recipient.ID {
		t.Fatalf("fixture did not reproduce the defect: mail Assignee = %q, want the ephemeral bead ID %q", msg.Assignee, recipient.ID)
	}
	closeSessionBead(t, store, recipient.ID)

	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	// The successor is stood up AFTER the reroute, which is the production
	// sequence: the session closes, the slot sits empty, the next occupant
	// spawns minutes later. A reroute that resolved its destination through a
	// live successor would pass a test that created one first and still strand
	// every message in production.
	createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-second", seat)

	if got := inboxSubjects(t, store, seat.alias); !containsSubject(got, msg.Title) {
		t.Fatalf("inbox for the surviving alias %q = %v, want it to carry %q\nstderr: %s", seat.alias, got, msg.Title, stderr.String())
	}
}

// TestMailToAClosedSessionIsUnreachableWithoutTheReroute is the control, and it
// is what makes every case above mean something. It is byte-identical to the
// alias case except that it never calls the reroute, and it requires the
// message to be UNREACHABLE -- proving the delivery those cases assert is not
// something the address resolver was doing anyway.
//
// It also records exactly why the eleven measured messages were invisible while
// beadmail's own recipientRoutes does try a closed-session pass: that fallback
// only fires when NO live session claims the address. The moment a successor
// occupies the seat the live pass wins, the dead session's bead id drops out of
// the route set, and the message addressed to it is filtered from every view.
func TestMailToAClosedSessionIsUnreachableWithoutTheReroute(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{alias: "rig/lab.pm", agentName: "rig/lab.pm"}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "warning nobody read")
	closeSessionBead(t, store, recipient.ID)
	createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-second", seat)

	if got := inboxSubjects(t, store, seat.alias); containsSubject(got, msg.Title) {
		t.Fatalf("inbox for %q = %v, want it NOT to carry %q: the control has stopped reproducing the defect, so the reroute cases prove nothing", seat.alias, got, msg.Title)
	}
}

// TestMailSurvivesRecipientSessionCloseViaAgentName is the aliasless case, and
// the one the alias ladder alone cannot reach: gascity/lab.engineer-1 carried
// its seat identity in `agent_name` and nothing else, so the durable address
// has to come from there. Kept as its own case rather than folded into the one
// above because a ladder that reads only `alias` passes that one and strands
// this seat exactly as production did.
func TestMailSurvivesRecipientSessionCloseViaAgentName(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{
		agentName: "rig/lab.engineer-1",
		template:  "rig/lab.engineer",
	}

	recipient := createMailRerouteSession(t, store, "rig/lab.engineer-1", "lab__engineer-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "gs-c6f: the salvage half is ALREADY DONE")
	closeSessionBead(t, store, recipient.ID)

	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	createMailRerouteSession(t, store, "rig/lab.engineer-1", "lab__engineer-second", seat)

	if got := inboxSubjects(t, store, seat.agentName); !containsSubject(got, msg.Title) {
		t.Fatalf("inbox for the surviving seat %q = %v, want it to carry %q\nstderr: %s", seat.agentName, got, msg.Title, stderr.String())
	}
}

// TestMailFromRetiredSeatGoesToTheOwningRoute covers the one caller that passes
// seatRetired: retireRemovedConfiguredNamedSessionBead, where the named session
// was DELETED from config so its alias and seat identity are borne by nobody
// ever again. Re-addressing to either would strand the message on an address no
// future session claims, so the destination has to fall back to the owning
// pool/agent route the seat came from.
func TestMailFromRetiredSeatGoesToTheOwningRoute(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{
		alias:     "rig/removed.agent",
		agentName: "rig/removed.agent",
		template:  "rig/pool.template",
	}

	recipient := createMailRerouteSession(t, store, "rig/removed.agent", "removed-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "ruling you asked for")

	closeSessionBead(t, store, recipient.ID)
	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatRetired, &stderr)

	got, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Assignee != seat.template {
		t.Fatalf("retired-seat mail Assignee = %q, want the owning route %q (the alias and seat identity are borne by nobody)\nstderr: %s", got.Assignee, seat.template, stderr.String())
	}
}

// TestMailRerouteRecordsWhereItCameFrom pins the provenance. A message that
// silently changes recipient is worse than one that is stranded: the sender is
// told nothing either way, and without the original address in the record
// nobody reading the thread later can tell a re-route from a mis-addressed
// send. The sender's own view of what happened is the absence this names --
// there is still no acknowledgement path back, which is a separate defect.
func TestMailRerouteRecordsWhereItCameFrom(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{alias: "rig/lab.pm", agentName: "rig/lab.pm"}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "CORRECTION to my last")

	closeSessionBead(t, store, recipient.ID)
	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	got, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Metadata[beadmail.RerouteFromMetadataKey] != recipient.ID {
		t.Fatalf("%s = %q, want the dead address %q", beadmail.RerouteFromMetadataKey, got.Metadata[beadmail.RerouteFromMetadataKey], recipient.ID)
	}
	if got.Metadata[beadmail.RerouteReasonMetadataKey] != beadmail.RerouteReasonRecipientClosed {
		t.Fatalf("%s = %q, want %q", beadmail.RerouteReasonMetadataKey, got.Metadata[beadmail.RerouteReasonMetadataKey], beadmail.RerouteReasonRecipientClosed)
	}
}

// TestMailRerouteLeavesAReadMessageAlone is the control on "undelivered". A
// message the recipient already read is delivered: moving it re-opens it in a
// successor's inbox, which pages a second agent about a thing that was handled
// and teaches the pool to ignore the channel. Without this case the reroute is
// satisfied by one that sweeps every message the session ever received.
func TestMailRerouteLeavesAReadMessageAlone(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{alias: "rig/lab.pm", agentName: "rig/lab.pm"}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "already handled")
	if err := beadmail.NewWithStores(store, store).MarkRead(msg.ID); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	closeSessionBead(t, store, recipient.ID)
	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	got, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Assignee != recipient.ID {
		t.Fatalf("read mail Assignee = %q, want unchanged %q", got.Assignee, recipient.ID)
	}
}

// TestMailRerouteLeavesWorkAlone is the class boundary. The sibling sweep on
// this same close (releaseWorkFromClosedSessionBead) owns WORK beads and
// deliberately skips message beads since ra-59207; this one owns message beads
// and must not touch WORK. A reroute that reassigned work would hand a claim to
// a pool template and let two sessions run it.
func TestMailRerouteLeavesWorkAlone(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{alias: "rig/lab.pm", agentName: "rig/lab.pm"}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	work, err := store.Create(beads.Bead{
		Title:    "real work",
		Status:   "open",
		Assignee: recipient.ID,
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}

	closeSessionBead(t, store, recipient.ID)
	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if got.Assignee != recipient.ID {
		t.Fatalf("work bead Assignee = %q, want unchanged %q (the mail reroute must not touch WORK)", got.Assignee, recipient.ID)
	}
}

// TestMailRerouteIsIdempotent pins the reconciler-safety property every other
// sweep on this path has: closeBead is reached from three doors and the orphan
// pass re-runs on every tick, so a second reroute of an already-moved message
// must be a no-op rather than a fresh move that rewrites the provenance to
// point at the destination instead of the dead address.
func TestMailRerouteIsIdempotent(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{alias: "rig/lab.pm", agentName: "rig/lab.pm"}

	recipient := createMailRerouteSession(t, store, "rig/lab.pm", "lab__pm-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "twice")

	closeSessionBead(t, store, recipient.ID)
	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	got, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Assignee != seat.alias {
		t.Fatalf("mail Assignee = %q, want %q after two reroutes", got.Assignee, seat.alias)
	}
	if got.Metadata[beadmail.RerouteFromMetadataKey] != recipient.ID {
		t.Fatalf("%s = %q, want the dead address %q preserved across a second reroute", beadmail.RerouteFromMetadataKey, got.Metadata[beadmail.RerouteFromMetadataKey], recipient.ID)
	}
	// Matched against the ERROR prefix, not the word "reroute": the success
	// line says "rerouted N undelivered message(s)" and a substring test on the
	// stem would fail on a healthy run, which is a test that has to be edited
	// rather than one that catches anything.
	if n := strings.Count(stderr.String(), "mail reroute: moving mail off ended session"); n != 0 {
		t.Fatalf("stderr reported %d reroute failure(s), want none:\n%s", n, stderr.String())
	}
	// A second pass must not report a second move either. Without this the
	// idempotence assertion is satisfied by a reroute that moves the message to
	// the same address again on every reconcile tick, writing a bead and firing
	// an on_update event each time.
	if n := strings.Count(stderr.String(), "mail reroute: moved "); n != 1 {
		t.Fatalf("stderr reported %d moves, want exactly 1 across two passes:\n%s", n, stderr.String())
	}
}

// TestRerouteReachesTheSuccessorsOwnInboxCheck is the assertion the two
// delivery cases above cannot make. They ask for an address by name, which
// tells us a message re-routed there is matchable; it does not tell us the
// successor ever ASKS. An agent checking its own mail hands beadmail the
// recipient set its session publishes (session.MailboxAddresses, via
// doMailInboxTarget), so a seat address missing from that set is a message
// re-routed correctly and read by nobody -- invisible in exactly the way the
// incident was.
//
// The aliasless seat is the case that turns on it: with an alias the set
// already contains the destination, so this only bites the sessions that have
// nothing else durable, which is the eighth measured recipient.
func TestRerouteReachesTheSuccessorsOwnInboxCheck(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{
		agentName: "rig/lab.engineer-1",
		template:  "rig/lab.engineer",
	}

	recipient := createMailRerouteSession(t, store, "rig/lab.engineer-1", "lab__engineer-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "time-critical: precondition fails now")
	closeSessionBead(t, store, recipient.ID)

	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)

	successor := createMailRerouteSession(t, store, "rig/lab.engineer-1", "lab__engineer-second", seat)
	if got := inboxSubjectsAsTheCLIWouldRead(t, store, successor); !containsSubject(got, msg.Title) {
		t.Fatalf("the successor's own inbox check = %v, want it to carry %q\nstderr: %s", got, msg.Title, stderr.String())
	}
}

// TestMailReroutedTwiceStillNamesTheFirstDeadAddress covers the second hop,
// which is the only way the provenance write is reachable twice: a message is
// re-routed off a dead session onto its seat, and later that seat is REMOVED
// from config and the message moves again onto the owning pool route. What a
// reader needs then is the address the sender actually chose, not the
// way-station -- an intermediate hop tells them nothing about who the message
// was for.
func TestMailReroutedTwiceStillNamesTheFirstDeadAddress(t *testing.T) {
	store := beads.NewMemStore()
	seat := mailRerouteSeat{
		alias:     "rig/removed.agent",
		agentName: "rig/removed.agent",
		template:  "rig/pool.template",
	}

	recipient := createMailRerouteSession(t, store, "rig/removed.agent", "removed-first", seat)
	msg := sendMailToLiveSessionAddress(t, store, recipient, "ruling you asked for")
	closeSessionBead(t, store, recipient.ID)

	var stderr bytes.Buffer
	rerouteMailFromEndingSession(store, recipient, seatSurvives, &stderr)
	if got, err := store.Get(msg.ID); err != nil {
		t.Fatalf("get mail bead: %v", err)
	} else if got.Assignee != seat.alias {
		t.Fatalf("first hop left Assignee = %q, want %q", got.Assignee, seat.alias)
	}

	// The seat is now deleted from config, so the alias it just landed on is
	// borne by nobody either.
	rerouteMailFromEndingSession(store, recipient, seatRetired, &stderr)

	got, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Assignee != seat.template {
		t.Fatalf("second hop left Assignee = %q, want the owning route %q", got.Assignee, seat.template)
	}
	if got.Metadata[beadmail.RerouteFromMetadataKey] != recipient.ID {
		t.Fatalf("%s = %q, want the FIRST dead address %q preserved across the second hop", beadmail.RerouteFromMetadataKey, got.Metadata[beadmail.RerouteFromMetadataKey], recipient.ID)
	}
}
