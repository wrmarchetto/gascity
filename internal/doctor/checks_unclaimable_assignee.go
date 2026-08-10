package doctor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

// UnclaimableAssigneeCheck reports open work parked on an assignee that no
// session in this city can hold work under.
//
// It exists because the failure is silent from every other angle. bd accepts
// any string as an assignee, so `bd update --assignee toolsmth` succeeds; the
// work query then matches on exact assignee, finds nothing, and reports
// nothing. The bead is not blocked, not held, not stale -- it simply never
// appears in any queue again. ci-c000 measured an entire historical queue lost
// this way before anyone noticed, and fixing the claim path for the one shape
// it found (work hand-assigned to a bare pool name) left the general class
// just as invisible.
//
// Two shapes produce it. A typo or a stale name, which was never an identity;
// and a name that WAS one and stopped being one -- work left on toolsmith-3
// after max_active_sessions drops to 2. Nothing writes anything when the
// second happens, which is why config and store have to be reconciled here
// rather than at the moment of the change.
//
// Why doctor and not `gc hook`: the hook sees only its own store scope and
// only runs when a session exists, so by construction it cannot report the
// beads nobody will ever claim. Doctor already reconciles bd store facts
// against gc config (checks_custom_types.go) and is the established home.
//
// Documented absences, each a deliberate limit on scope:
//   - Rig stores are not scanned. Only the city store is read. Rig work
//     assigned to a dead rig-agent name is the same defect and would need
//     per-rig registration like NewCustomTypesCheck; nobody has measured it
//     stranding a queue yet.
//   - Ephemeral (wisp) beads are not scanned. The default TierMode reads
//     durable rows only, and wisps are TTL-collected rather than stranded.
//   - No fix is offered. See CanFix.
type UnclaimableAssigneeCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(cityPath string) (beads.Store, error)
}

// NewUnclaimableAssigneeCheck creates the check. newStore is a factory that
// opens the city bead store, injected so tests drive a real in-memory store
// rather than a scripted stand-in.
func NewUnclaimableAssigneeCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *UnclaimableAssigneeCheck {
	return &UnclaimableAssigneeCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

// Name returns the check identifier.
func (c *UnclaimableAssigneeCheck) Name() string { return "unclaimable-assignee" }

// Run reconciles every non-closed assigned bead in the city store against the
// set of identities the city's config and live sessions can produce.
func (c *UnclaimableAssigneeCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory}

	// Without config there is no identity set to resolve against, so every
	// assignee would look unclaimable. Standing down beats reporting the
	// whole store; the core config check already names the parse failure.
	if c.cfg == nil {
		r.Status = StatusOK
		r.Message = "no config; nothing to check"
		return r
	}
	if c.newStore == nil {
		r.Status = StatusOK
		r.Message = "no store factory; nothing to check"
		return r
	}

	store, err := c.newStore(c.cityPath)
	if err != nil {
		// Reporting OK here would make an unreachable store look identical
		// to a clean city -- the check would go permanently green at the
		// moment it stopped running.
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("store open failed: %v", err)
		return r
	}

	sessionBeads, err := session.ListAllSessionBeads(store, beads.ListQuery{})
	if err != nil && !beads.IsPartialResult(err) {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("listing live sessions: %v", err)
		return r
	}
	claimable := newClaimIdentitySet(c.cfg, sessionBeads)

	// AllowScan because the question is about every assigned bead; there is
	// no narrower filter for "assignee matches none of a computed set".
	// IncludeClosed stays false: a closed bead's assignee is history.
	candidates, err := store.List(beads.ListQuery{AllowScan: true})
	if err != nil && !beads.IsPartialResult(err) {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("listing beads: %v", err)
		return r
	}

	var details []string
	for _, b := range candidates {
		if b.Assignee == "" || !scannedForClaimability(b) {
			continue
		}
		if claimable.covers(b.Assignee) {
			continue
		}
		details = append(details, fmt.Sprintf("%s (%s) assigned to %q, which is not an agent, a pool slot, a named session or a live session",
			b.ID, b.Status, b.Assignee))
	}

	if len(details) == 0 {
		r.Status = StatusOK
		r.Message = "every assigned bead resolves to a claimable identity"
		return r
	}
	sort.Strings(details)
	r.Status = StatusWarning
	r.Message = fmt.Sprintf("%d bead(s) assigned to a name no session can carry", len(details))
	r.Details = details
	r.FixHint = "reassign each to a live identity (bd update <id> --assignee <name>), " +
		"or unassign it and stamp gc.routed_to=<pool> so any slot can claim it; " +
		"names that are deliberately not agents belong in [doctor] external_assignees"
	return r
}

// CanFix returns false. Both remedies -- reassign, or unassign and route --
// change who does the work, and a bead addressed to a name a person chose is
// exactly the case where guessing is wrong. FixHint names both instead.
func (c *UnclaimableAssigneeCheck) CanFix() bool { return false }

// Fix is a no-op.
func (c *UnclaimableAssigneeCheck) Fix(_ *CheckContext) error { return nil }

// WarmupEligible returns false. The scan reads the whole store, and stranded
// work is a standing condition rather than a start-blocking one.
func (c *UnclaimableAssigneeCheck) WarmupEligible() bool { return false }

// scannedForClaimability reports whether a bead's assignee is subject to the
// claim ladder at all.
//
// Only mail is excluded, and it is excluded because EVERY claim tier carries
// --exclude-type=message (config.ExcludeMessageTypeArg): mail is delivered to
// a live session, never claimed, so a message addressed to a non-identity is a
// different defect and reporting it here would be a permanent false positive
// on every city that mails an operator.
//
// beadmail.IsMessageBead is the exclusion rather than a local type compare
// because it is the same bare-Type predicate bd's --exclude-type applies. A
// looser local test (case-folded, trimmed) would exclude beads the claim path
// still serves, hiding the stranded ones among them.
//
// Deliberately NOT excluded: molecule, step and gate. The in_progress
// crash-recovery tier serves those by exact assignee, so one parked on a name
// nothing carries is stranded in precisely the way this check exists to find.
func scannedForClaimability(b beads.Bead) bool {
	return !beadmail.IsMessageBead(b)
}

// claimIdentitySet is the set of assignee values that some session in this
// city can hold work under -- the union of what config can spawn and what is
// currently live.
//
// Membership is deliberately generous. A false positive here is a check the
// operator turns off, which costs the detection entirely; a false negative is
// one stranded bead that stays invisible, which is the status quo. Where the
// two trade off, this errs toward covering the name.
type claimIdentitySet struct {
	// exact holds identities that can be enumerated: agent and named-session
	// qualified names, bounded pool slots, live session identities, and the
	// operator's declared external assignees.
	exact map[string]struct{}
	// slotPrefixes holds "<qualified-name>-" for pools with no session cap,
	// where the reachable slot names are unbounded and cannot be listed. A
	// positive integer suffix is required, so polecat-47 is covered and
	// polecat-typo is still reported.
	slotPrefixes []string
}

// newClaimIdentitySet resolves the city's claimable assignee values from
// config plus the live session beads.
//
// Rejected alternative: calling agentutil.ExpandAgents, which computes pool
// members already. It answers a narrower question -- it returns nothing at all
// for an uncapped pool unless handed a live session lister, which is exactly
// the case that must stay permissive here, and it emits only the Dir-prefixed
// spelling of a member name while cmd/gc assigns the binding-prefixed
// QualifiedInstanceName spelling. Both spellings are added below. The naming
// rule itself is NOT reimplemented: agentutil.PoolInstanceName is called, so a
// change to namepool naming cannot drift this set away from the real one.
func newClaimIdentitySet(cfg *config.City, sessionBeads []beads.Bead) claimIdentitySet {
	s := claimIdentitySet{exact: make(map[string]struct{}, 32)}
	if cfg == nil {
		return s
	}

	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		// The qualified name is both the agent's own identity and the pool
		// route target (config.Agent.poolDemandTarget), so it covers work
		// hand-assigned to a bare pool name -- the tier ci-c000 added.
		s.add(a.QualifiedName())
		s.add(a.PoolName)
		s.addPoolSlots(a)
	}

	// A named session's public identity can differ from the agent template
	// backing it; that difference is the whole point of the name field.
	for i := range cfg.NamedSessions {
		ns := &cfg.NamedSessions[i]
		s.add(ns.QualifiedName())
		s.add(ns.TemplateQualifiedName())
	}

	// Live sessions are the own-identity tier. A session's alias is not
	// always derivable from config -- an adopted or ad-hoc session carries
	// one config never declared -- so the store is read rather than inferred.
	for _, b := range sessionBeads {
		for _, id := range session.ClaimIdentities(b) {
			s.add(id)
		}
	}

	for _, name := range cfg.Doctor.ExternalAssignees {
		s.add(name)
	}
	return s
}

// add records a trimmed, non-empty identity.
func (s *claimIdentitySet) add(identity string) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return
	}
	s.exact[identity] = struct{}{}
}

// addSlotPrefix records an uncapped pool's slot prefix, skipping the duplicate
// an agent with no import binding produces (both spellings collapse).
func (s *claimIdentitySet) addSlotPrefix(prefix string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "-" {
		return
	}
	for _, existing := range s.slotPrefixes {
		if existing == prefix {
			return
		}
	}
	s.slotPrefixes = append(s.slotPrefixes, prefix)
}

// addPoolSlots records the concrete slot identities an agent's session cap
// allows. An uncapped agent contributes a prefix rule instead, because its
// reachable slot names are unbounded.
func (s *claimIdentitySet) addPoolSlots(a *config.Agent) {
	if !a.SupportsInstanceExpansion() || a.UsesCanonicalSingletonPoolIdentity() {
		// A singleton's identity is its bare qualified name, already added.
		return
	}

	// A cap above enumerationCap is treated as uncapped rather than expanded.
	// Nothing rejects max_active_sessions = 1000000 at config load, and a
	// doctor check must not answer one with a million-entry allocation. The
	// prefix rule it falls back to is strictly more permissive, so the
	// degradation can only under-report, never invent a stranded bead.
	if a.HasUnlimitedSessionCapacity() || exceedsEnumerationCap(a) {
		// The prefix is built from the agent's own name, matching how
		// agentutil.discoverUnlimitedPool scans for running members.
		s.addSlotPrefix(qualifyInstance(a, a.Name) + "-")
		s.addSlotPrefix(a.QualifiedInstanceName(a.Name) + "-")
		// A namepool draws slot names from a file rather than synthesizing
		// them, so the prefix rule alone would miss every one of them.
		for _, name := range a.NamepoolNames {
			s.add(qualifyInstance(a, name))
			s.add(a.QualifiedInstanceName(name))
		}
		return
	}

	maxSessions := a.EffectiveMaxActiveSessions()
	if maxSessions == nil || *maxSessions < 1 {
		return
	}
	for slot := 1; slot <= *maxSessions; slot++ {
		member := agentutil.PoolInstanceName(a.Name, slot, *a)
		s.add(qualifyInstance(a, member))
		s.add(a.QualifiedInstanceName(member))
	}
}

// enumerationCap bounds how many pool slots are listed individually. The value
// is arbitrary and only has to sit far above any real pool; the largest in
// service is single digits.
const enumerationCap = 1024

// exceedsEnumerationCap reports whether an agent's session cap is too large to
// enumerate slot by slot.
func exceedsEnumerationCap(a *config.Agent) bool {
	m := a.EffectiveMaxActiveSessions()
	return m != nil && *m > enumerationCap
}

// qualifyInstance renders a member name the way agentutil.ExpandAgents does:
// rig-qualified, without the import binding prefix that
// Agent.QualifiedInstanceName adds. Both spellings are in circulation, so both
// are treated as claimable.
func qualifyInstance(a *config.Agent, member string) string {
	if a.Dir == "" {
		return member
	}
	return a.Dir + "/" + member
}

// covers reports whether some session can hold work assigned to this name.
//
// The assignee is matched verbatim, never trimmed. bd matches --assignee
// exactly, so an assignee with a stray trailing space really is claimable by
// nobody, and %q in the report makes it visible.
func (s claimIdentitySet) covers(assignee string) bool {
	if _, ok := s.exact[assignee]; ok {
		return true
	}
	for _, prefix := range s.slotPrefixes {
		if slot, found := strings.CutPrefix(assignee, prefix); found && isPositiveSlotNumber(slot) {
			return true
		}
	}
	return false
}

// isPositiveSlotNumber reports whether a suffix is a slot index an uncapped
// pool could synthesize (agentutil.PoolInstanceName renders "%s-%d").
func isPositiveSlotNumber(suffix string) bool {
	n, err := strconv.Atoi(suffix)
	return err == nil && n > 0
}
