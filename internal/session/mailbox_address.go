package session

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the session-class READ half of the mailbox-address codec
// consumed by the mail CLI/API. Resolving the address a session publishes mail
// under is a session-attribute read, not a mail op (mail itself is already
// front-doored via mail.Provider). Confining the codec here keeps the
// session-bead metadata vocabulary (alias / alias_history / session_name) out
// of cmd/gc and internal/api, so the mail callers speak session addresses
// instead of cracking beads.Bead.Metadata directly.

// MailboxAddress returns the primary mailbox address a session bead publishes
// under: its alias if set, else its bead id, else its session_name. It is the
// pure, side-effect-free codec for a single session bead — the canonical home
// of the logic the mail CLI previously inlined as sessionMailboxAddress.
func MailboxAddress(b beads.Bead) string {
	if alias := strings.TrimSpace(b.Metadata["alias"]); alias != "" {
		return alias
	}
	if b.ID != "" {
		return b.ID
	}
	return strings.TrimSpace(b.Metadata["session_name"])
}

// MailboxAddresses returns every address a session bead can receive mail at —
// its primary address, its bead id, and any retained alias history — deduped
// and trimmed, falling back to session_name only when nothing else resolves. It
// is the canonical home of the logic the mail CLI previously inlined as
// sessionMailboxAddresses.
//
// The seat identity in agent_name is deliberately ABSENT here even though mail
// re-routed off a closing session lands on it (see SeatMailboxAddress). It
// belongs to RecipientRoutesFromInfo instead, which is the one place beadmail
// expands a resolved session into the addresses it will answer for -- every
// caller of this function feeds its result back through that expansion, so a
// copy here is a second authority on the same set that no test can kill. A
// mutation sweep on 2026-09-07 found exactly that: removing agent_name from
// this list changed nothing observable, including on the CLI inbox path. Add it
// here only alongside a consumer that queries these addresses without route
// expansion.
func MailboxAddresses(b beads.Bead) []string {
	return mailboxAddresses(b, false)
}

// MailboxAddressesIncludingRuntimeName returns every mailbox address a session
// bead can receive mail at, always including its runtime session_name (appended
// last), even when other addresses already resolved. This is the API read
// semantics introduced by bf576b04a ("fix: include runtime session mailboxes in
// API reads"): mail persisted under a session's runtime name must stay
// reachable via API inbox/count queries, guarded by
// TestMailAPIQueriesAllResolvedSessionMailboxAddresses.
//
// It deliberately forks from MailboxAddresses, which appends session_name only
// as a last-resort fallback. That CLI/API fork is a documented product decision
// (the CLI inbox can miss mail persisted under a runtime session_name the API
// finds); reconciling the two recipient views is tracked as a follow-up.
func MailboxAddressesIncludingRuntimeName(b beads.Bead) []string {
	return mailboxAddresses(b, true)
}

// mailboxAddresses is the shared body behind MailboxAddresses (CLI fallback-only
// session_name) and MailboxAddressesIncludingRuntimeName (API unconditional
// session_name). When includeRuntimeName is true the session_name is always
// added; otherwise it is only added when nothing else resolved.
func mailboxAddresses(b beads.Bead, includeRuntimeName bool) []string {
	seen := map[string]bool{}
	var addresses []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		addresses = append(addresses, value)
	}
	add(MailboxAddress(b))
	add(b.ID)
	for _, alias := range AliasHistory(b.Metadata) {
		add(alias)
	}
	if includeRuntimeName {
		add(b.Metadata["session_name"])
	} else if len(addresses) == 0 {
		add(strings.TrimSpace(b.Metadata["session_name"]))
	}
	return addresses
}

// MailboxAddressFromInfo is the Info-taking twin of MailboxAddress: the primary
// mailbox address a session publishes under — its alias if set, else its bead
// id, else its runtime session_name. It reads Info fields that mirror the exact
// bead metadata (Alias, ID, SessionNameMetadata — the RAW session_name without
// the sessionNameFor fallback), so it is byte-identical to MailboxAddress.
func MailboxAddressFromInfo(info Info) string {
	if alias := strings.TrimSpace(info.Alias); alias != "" {
		return alias
	}
	if info.ID != "" {
		return info.ID
	}
	return strings.TrimSpace(info.SessionNameMetadata)
}

// MailboxAddressesFromInfo is the Info-taking twin of MailboxAddresses: every
// address a session can receive mail at (primary, bead id, alias history),
// falling back to session_name only when nothing else resolves.
func MailboxAddressesFromInfo(info Info) []string {
	return mailboxAddressesFromInfo(info, false)
}

// MailboxAddressesIncludingRuntimeNameFromInfo is the Info-taking twin of
// MailboxAddressesIncludingRuntimeName: the API read semantics that always
// include the runtime session_name (appended last), even when other addresses
// resolved.
func MailboxAddressesIncludingRuntimeNameFromInfo(info Info) []string {
	return mailboxAddressesFromInfo(info, true)
}

// mailboxAddressesFromInfo is the Info-taking shared body behind the two Info
// mailbox twins, byte-identical to mailboxAddresses: it reads Alias/ID
// (via MailboxAddressFromInfo), AliasHistory, and the RAW SessionNameMetadata.
func mailboxAddressesFromInfo(info Info, includeRuntimeName bool) []string {
	seen := map[string]bool{}
	var addresses []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		addresses = append(addresses, value)
	}
	add(MailboxAddressFromInfo(info))
	add(info.ID)
	for _, alias := range info.AliasHistory {
		add(alias)
	}
	if includeRuntimeName {
		add(info.SessionNameMetadata)
	} else if len(addresses) == 0 {
		add(strings.TrimSpace(info.SessionNameMetadata))
	}
	return addresses
}

// ExtmsgHandleSource returns the raw handle source for a session bead used by
// the external-messaging handle projection: alias if set, else session_name.
// Unlike MailboxAddress it does NOT fall back to the bead id — it preserves the
// exact precedence the extmsg handler relied on before routing through the
// session front door. Callers still apply their own handle-label trimming.
func ExtmsgHandleSource(b beads.Bead) string {
	if alias := strings.TrimSpace(b.Metadata["alias"]); alias != "" {
		return alias
	}
	return strings.TrimSpace(b.Metadata["session_name"])
}

// MailboxAddress loads the session bead for id and returns its primary mailbox
// address. It confines the store.Get + codec behind the typed read seam so mail
// callers stop calling store.Get(id) and reading b.Metadata themselves. The Get
// error is returned verbatim (beads.ErrNotFound-wrapped when the bead is
// absent), matching the raw mail path it replaces — the callers pass an
// already-resolved session id and surface the error to the operator.
func (s *Store) MailboxAddress(id string) (string, error) {
	b, err := s.store.Get(id)
	if err != nil {
		return "", err
	}
	return MailboxAddress(b), nil
}

// MailboxAddresses loads the session bead for id and returns all addresses it
// can receive mail at. The Get error is returned verbatim, matching the raw
// mail path it replaces.
func (s *Store) MailboxAddresses(id string) ([]string, error) {
	b, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	return MailboxAddresses(b), nil
}

// ExtmsgHandleSource loads the session bead for id and returns its extmsg
// handle source (alias, else session_name; no bead-id fallback). The (source,
// false) return signals "no session bead / load error" so the caller can fall
// back to its selector, matching the raw extmsg handler which fell back on any
// Get error.
func (s *Store) ExtmsgHandleSource(id string) (string, bool) {
	b, err := s.store.Get(id)
	if err != nil {
		return "", false
	}
	return ExtmsgHandleSource(b), true
}

// SeatMailboxAddress returns the address a session's mail should be re-routed
// to when the session itself ends but its SEAT does not: the alias the seat
// publishes under, else the seat identity in agent_name.
//
// The two addresses this deliberately does NOT consider are the bead id and the
// runtime session_name. Both are unique to one occupant -- session_name embeds
// the bead id -- so re-routing to either moves a message from one dead address
// to another, which is the failure it exists to end. An empty return means the
// session carried no durable address at all and the caller must not move the
// message: leaving it on a dead address it can still be found on by id beats
// moving it to one nothing will ever resolve.
//
// Pinned by cmd/gc/session_beads_mail_reroute_test.go, which asserts through an
// inbox read rather than the assignee field.
func SeatMailboxAddress(b beads.Bead) string {
	if alias := strings.TrimSpace(b.Metadata["alias"]); alias != "" {
		return alias
	}
	return strings.TrimSpace(b.Metadata["agent_name"])
}
