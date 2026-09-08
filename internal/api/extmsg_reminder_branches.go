package api

// ExtMsgNotifyReminderBranches renders the inbound-notify system reminder for
// one conversation, one entry per conditional block in
// formatExtmsgNotifyReminder. It exists so the gate in
// cmd/gc/extmsg_reminder_command_gate_test.go can resolve every gc command the
// reminder names against the real cobra tree: that tree is built by package
// main and cannot be imported, so the reminder text has to travel the other
// way instead.
//
// Exporting extmsgNotifyReminder itself was the obvious alternative and is
// rejected. It carries session-routing fields (RecipientSessionID,
// ExplicitTargetSessionID) that no gate needs, and publishing them invites
// callers outside this package to assemble reminders by hand, which is how the
// sanitization in formatExtmsgNotifyReminder would come to be bypassed.
//
// The free-text fields are filled with values that contain no "gc" token on
// purpose. The gate scrapes commands out of the whole rendered block rather
// than out of the instruction lines alone, because a future hint may be added
// anywhere in it -- so an actor name or message body carrying "gc something"
// would read as a command the reminder names.
//
// This function does NOT get to decide on its own which branches exist. The
// gate cross-checks the set rendered here against every command literal inside
// formatExtmsgNotifyReminder's own body, so a hint added under a condition
// this function never drives fails the gate rather than passing unseen.
func ExtMsgNotifyReminderBranches(provider, conversationID, handle string) []string {
	base := extmsgNotifyReminder{
		Provider:       provider,
		ConversationID: conversationID,
		ActorDisplay:   "operator",
		ActorKind:      "human",
		Text:           "ping",
		Handle:         handle,
	}
	addressedElsewhere := base
	addressedElsewhere.ExplicitTarget = handle + "-peer"
	return []string{
		formatExtmsgNotifyReminder(base),
		formatExtmsgNotifyReminder(addressedElsewhere),
	}
}
