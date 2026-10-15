// Guards the seam ci-06lx7u added between this package's inbound reminder and
// the openclaw assistant-turn mirror. The mirror publishes a session's turn
// only when that turn answers a human message, and one of its two pieces of
// evidence is this package's reminder appearing in the turn's prompt. No
// Go-only or JS-only test sees the two halves disagree: a reworded reminder
// leaves both suites green while the mirror silently stops recognizing the
// delivery, and a reply held for the next prompt is then dropped as answered.
//
// The mirror's lead literal is read out of its own source rather than restated
// here, for the reason city_name_seam_test.go gives: a Go copy agrees with
// itself forever while the consumer drifts.
//
// Run: go test ./internal/api/ -run TestExtmsgReminderCarriesTheMirrorDeliveryMarker
package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/extmsg"
)

const assistantTurnMirrorPath = "../../contrib/openclaw-bridge/lib/assistant-turn-mirror.mjs"

func mirrorReminderLead(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(assistantTurnMirrorPath)
	if err != nil {
		t.Fatalf("read assistant-turn mirror: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^const INBOUND_REMINDER_LEAD = '([^']+)'$`).FindSubmatch(source)
	if decl == nil {
		t.Fatalf("no INBOUND_REMINDER_LEAD declaration in %s; the mirror no longer recognizes a delivered reminder by its lead, so this guard is measuring nothing", assistantTurnMirrorPath)
	}
	return string(decl[1])
}

func TestExtmsgReminderCarriesTheMirrorDeliveryMarker(t *testing.T) {
	lead := mirrorReminderLead(t)
	// Distinct values, none a substring of another, so a layout that swapped
	// two fields could not still match.
	const provider, conversationID, actor = "slack", "C0SEAMROOM", "U0SEAMACTOR"
	reminder := formatExtmsgNotifyReminder(extmsgNotifyReminder{
		Provider:       provider,
		ConversationID: conversationID,
		ActorDisplay:   actor,
		ActorKind:      extmsgInboundActorKind(extmsg.ExternalActor{DisplayName: actor}),
		Text:           "are you there?",
		Handle:         "seam-handle",
	})
	// The layout the mirror's inboundDeliveryPattern matches: lead, the
	// conversation, a blank line, then the sender marked as a person.
	want := lead + provider + "/" + conversationID + ":\n\n- " + actor + " (human): "
	if !strings.Contains(reminder, want) {
		t.Fatalf("inbound reminder no longer carries the mirror's delivery marker %q; the mirror will drop replies held for the next prompt.\nreminder:\n%s", want, reminder)
	}
}

func TestExtmsgInboundActorKindMarksOnlyAPersonHuman(t *testing.T) {
	// The mirror opens a turn on "(human)" and must not on a bot's post, so
	// the kind has to separate the two rather than merely produce the word.
	if got := extmsgInboundActorKind(extmsg.ExternalActor{IsBot: true}); got == "human" {
		t.Fatalf("a bot actor is reported as %q; the mirror would publish a turn answering another agent", got)
	}
}
