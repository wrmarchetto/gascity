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
// Three shapes produce it. A typo or a stale name, which was never an
// identity; a name that WAS one and stopped being one -- work left on
// toolsmith-3 after max_active_sessions drops to 2; and, since ci-1ztzgt, a
// live pool's OWN bare name holding in_progress work. Nothing writes anything
// when the second happens, which is why config and store have to be reconciled
// here rather than at the moment of the change.
//
// The third is why claimability is asked per status rather than per name. Only
// one tier reaches a bare pool name -- the route-scoped
// bdReadyPoolAliasDemandShell -- and it is `bd ready`, which excludes
// in_progress by design, while the tier that does serve in_progress probes the
// session's own identity, which above max_active_sessions=1 is a suffixed slot
// name. So the same string is claimable open and claimable by nobody claimed.
// See claimIdentitySet.covers and addRouteTarget.
//
// Why doctor and not `gc hook`: the hook sees only its own store scope and
// only runs when a session exists, so by construction it cannot report the
// beads nobody will ever claim. Doctor already reconciles bd store facts
// against gc config (checks_custom_types.go) and is the established home.
//
// One instance is registered per store scope: the city store, and one per
// non-suspended rig. Rig scopes were added by ci-tuy2u9, which measured the
// same defect the city scope was built for happening in a rig store: as-d5nv
// sat ready and assigned for 9h25m on "lab.engineer" -- the pool name with its
// rig qualifier stripped -- while gc matched every claim tier against
// "astoria-sel4/lab.engineer". The identity set is city-wide in both scopes,
// because a rig agent is declared in city.toml and a rig session's bead is
// written to the city store.
//
// Documented absences, each a deliberate limit on scope:
//   - Suspended rigs are not scanned. Registration skips them so a check
//     cannot bd-auto-start an orphan Dolt server (ga-wzk); work stranded in a
//     suspended rig is reported when the rig resumes.
//   - Ephemeral (wisp) beads are not scanned. The default TierMode reads
//     durable rows only, and wisps are TTL-collected rather than stranded.
//   - An uncapped pool's slot names get no qualifier suggestion. They are
//     covered by a prefix rule rather than enumerated, so there is no
//     qualified string to name; such a bead still reports, with the generic
//     FixHint.
//   - No fix is offered. See CanFix.
type UnclaimableAssigneeCheck struct {
	cfg      *config.City
	cityPath string
	// scanDir holds the store whose beads are scanned. It equals cityPath for
	// the city scope and the rig path for a rig scope. The identity set is
	// resolved from cityPath either way.
	scanDir string
	// label distinguishes the registered instances in one flat result list.
	label    string
	newStore func(dir string) (beads.Store, error)
}

// NewUnclaimableAssigneeCheck creates the city-store scope. newStore is a
// factory that opens the bead store at a directory, injected so tests drive a
// real in-memory store rather than a scripted stand-in.
func NewUnclaimableAssigneeCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *UnclaimableAssigneeCheck {
	return &UnclaimableAssigneeCheck{cfg: cfg, cityPath: cityPath, scanDir: cityPath, label: "city", newStore: newStore}
}

// NewUnclaimableAssigneeCheckForRig creates a scope that scans one rig's
// store. cityPath is still required and is NOT interchangeable with rigPath:
// session beads and the agent config both live at the city, so a rig scope
// opens two stores.
func NewUnclaimableAssigneeCheckForRig(cfg *config.City, cityPath, rigPath, rigName string, newStore func(string) (beads.Store, error)) *UnclaimableAssigneeCheck {
	return &UnclaimableAssigneeCheck{cfg: cfg, cityPath: cityPath, scanDir: rigPath, label: rigName, newStore: newStore}
}

// Name returns the check identifier, scoped by store label so a finding names
// the store it came from.
func (c *UnclaimableAssigneeCheck) Name() string { return "unclaimable-assignee:" + c.label }

// Run reconciles every non-closed assigned bead in this scope's store against
// the set of identities the city's config and live sessions can produce.
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

	// The city store carries every session bead in the city, including the
	// sessions serving a rig, so the identity set is resolved from it in both
	// scopes. Reading sessions from the rig store instead would resolve none
	// and report every bead a live rig session legitimately holds.
	cityStore, err := c.newStore(c.cityPath)
	if err != nil {
		// Reporting OK here would make an unreachable store look identical
		// to a clean city -- the check would go permanently green at the
		// moment it stopped running.
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("city store open failed: %v", err)
		return r
	}

	sessionBeads, err := session.ListAllSessionBeads(cityStore, beads.ListQuery{})
	if err != nil && !beads.IsPartialResult(err) {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("listing live sessions: %v", err)
		return r
	}
	claimable := newClaimIdentitySet(c.cfg, sessionBeads)

	scanStore := cityStore
	if c.scanDir != c.cityPath {
		scanStore, err = c.newStore(c.scanDir)
		if err != nil {
			r.Status = StatusWarning
			r.Message = fmt.Sprintf("%s store open failed: %v", c.label, err)
			return r
		}
	}

	// AllowScan because the question is about every assigned bead; there is
	// no narrower filter for "assignee matches none of a computed set".
	// IncludeClosed stays false: a closed bead's assignee is history.
	candidates, err := scanStore.List(beads.ListQuery{AllowScan: true})
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
		if claimable.covers(b.Assignee, b.Status) {
			continue
		}
		details = append(details, unclaimableAssigneeDetail(b, claimable))
	}

	if len(details) == 0 {
		r.Status = StatusOK
		r.Message = "every assigned bead resolves to a claimable identity"
		return r
	}
	sort.Strings(details)
	r.Status = StatusWarning
	r.Message = fmt.Sprintf("%d %s bead(s) assigned to a name no session can carry", len(details), c.label)
	r.Details = details
	r.FixHint = "reassign each to a live identity (bd update <id> --assignee <name>), " +
		"or unassign it and stamp gc.routed_to=<pool> so any slot can claim it; " +
		"names that are deliberately not agents belong in [doctor] external_assignees"
	return r
}

// unclaimableAssigneeDetail renders one finding.
//
// The missing-qualifier case gets its own sentence and its own command because
// its remedy is the one a reader cannot derive from the generic line: the name
// is not a typo and was never retired, it is the right pool spelled without
// its rig, and the fix is a string the check already knows. A finding that
// only said "not an identity" is what let as-d5nv read as a routing question
// rather than a spelling one (ci-tuy2u9).
//
// An ambiguous bare name -- two rigs both declaring the pool -- deliberately
// gets NO single command. Reassigning rig work to the wrong rig's pool strands
// it again under a name that now looks correct, so the candidates are listed
// and the choice is left with the reader.
//
// The in_progress pool-name case is checked FIRST and takes both sentences'
// place, because for it the other two are wrong rather than merely vague. The
// generic line says the name "is not an agent, a pool slot, a named session or
// a live session" when the name is in fact the pool's own, and the generic
// FixHint's reassign remedy is refused by bd on a bead another assignee holds
// in_progress (measured on the live city store 2026-09-14:
// `cannot reassign ci-j6nevy: held by "toolsmith" (in_progress)`).
//
// Documented absence: a bead that is in_progress AND on an unqualified rig
// pool name still gets the qualifier sentence, whose command bd will refuse
// for the same lease reason. It is left alone because its remedy is two steps
// -- release, then reassign -- and inventing that wording here would be
// untested against any shape anyone has observed.
func unclaimableAssigneeDetail(b beads.Bead, claimable claimIdentitySet) string {
	if isInProgressStatus(b.Status) && claimable.isOpenOnlyRouteTarget(b.Assignee) {
		return fmt.Sprintf("%s (%s) assigned to %q, a pool whose slots carry suffixed names, so no session answers to that name and the pool-alias tier cannot serve claimed work: run gc bd release-if-current %s %s to return it to open",
			b.ID, b.Status, b.Assignee, b.ID, b.Assignee)
	}
	forms := claimable.qualifiedFormsOf(b.Assignee)
	switch len(forms) {
	case 0:
		return fmt.Sprintf("%s (%s) assigned to %q, which is not an agent, a pool slot, a named session or a live session",
			b.ID, b.Status, b.Assignee)
	case 1:
		return fmt.Sprintf("%s (%s) assigned to %q, which is %s without its rig qualifier: run gc bd update %s --assignee %s",
			b.ID, b.Status, b.Assignee, forms[0], b.ID, forms[0])
	default:
		return fmt.Sprintf("%s (%s) assigned to %q, which is a pool name without its rig qualifier and %d rigs declare it (%s): reassign it to the one that owns the work",
			b.ID, b.Status, b.Assignee, len(forms), strings.Join(forms, ", "))
	}
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
	// operator's declared external assignees. Membership here is claimable in
	// every status.
	exact map[string]struct{}
	// openOnly holds names reachable ONLY through the route-scoped pool-alias
	// tier (config.bdReadyPoolAliasDemandShell), which is `bd ready` and so
	// cannot return in_progress work. A name lands here instead of `exact`
	// when it is the bare name of a pool that mints suffixed slot identities;
	// `exact` still wins, so a live session or a declared external assignee
	// carrying the same string restores full coverage.
	openOnly map[string]struct{}
	// slotPrefixes holds "<qualified-name>-" for pools with no session cap,
	// where the reachable slot names are unbounded and cannot be listed. A
	// positive integer suffix is required, so polecat-47 is covered and
	// polecat-typo is still reported.
	slotPrefixes []string
	// qualifiedForms maps an identity's unqualified spelling to every
	// rig-qualified identity ending in it, so a finding can name the string to
	// use rather than only the string that failed. Populated from `exact`
	// alone: an uncapped pool's slot names live in slotPrefixes and have no
	// enumerable qualified form to suggest.
	qualifiedForms map[string][]string
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
	s := claimIdentitySet{
		exact:          make(map[string]struct{}, 32),
		openOnly:       make(map[string]struct{}, 8),
		qualifiedForms: make(map[string][]string, 16),
	}
	if cfg == nil {
		return s
	}

	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		// The qualified name is both the agent's own identity and the pool
		// route target (config.Agent.poolDemandTarget), so it covers work
		// hand-assigned to a bare pool name -- the tier ci-c000 added. That
		// coverage is status-dependent; addRouteTarget carries the split.
		s.addRouteTarget(a, a.QualifiedName())
		s.addRouteTarget(a, a.PoolName)
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

// add records a trimmed, non-empty identity as claimable in every status, and
// indexes its unqualified spelling when it carries a rig qualifier.
func (s *claimIdentitySet) add(identity string) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return
	}
	if _, exists := s.exact[identity]; exists {
		return
	}
	s.exact[identity] = struct{}{}
	s.indexQualifiedForm(identity)
}

// addOpenOnly records a name the claim ladder reaches only while the work is
// still open. It does NOT touch `exact`, so an identity added by both routes
// keeps full coverage whichever order the two calls happen in.
//
// The qualified form is indexed here as well as in add. Skipping it was the
// bug this split would otherwise introduce: every rig pool above
// max_active_sessions=1 leaves `exact` through this path, and the ci-tuy2u9
// missing-qualifier remedy is built from that index, so the suggestion would
// vanish for exactly the pools that have one.
func (s *claimIdentitySet) addOpenOnly(identity string) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return
	}
	if _, exists := s.openOnly[identity]; exists {
		return
	}
	s.openOnly[identity] = struct{}{}
	s.indexQualifiedForm(identity)
}

// indexQualifiedForm records a rig-qualified identity under its bare spelling.
//
// config.ParseQualifiedName splits on the LAST "/", the same rule
// Agent.QualifiedName composes with, so the bare form indexed here is exactly
// the string an operator gets by dropping the rig prefix.
func (s *claimIdentitySet) indexQualifiedForm(identity string) {
	dir, bare := config.ParseQualifiedName(identity)
	if dir == "" || bare == "" {
		return
	}
	for _, existing := range s.qualifiedForms[bare] {
		if existing == identity {
			return
		}
	}
	s.qualifiedForms[bare] = append(s.qualifiedForms[bare], identity)
}

// addRouteTarget records a pool route target, splitting its coverage by the
// status the ladder can serve it in.
//
// Agent.SupportsExpandedSessionIdentities is the same predicate ci-45nrw8 used
// to stop the pool door presenting the bare name as an identity, and it has to
// stay the same one: it is true exactly when the pool mints suffixed slot
// names, so nothing carries the bare name in $GC_ALIAS and the own-identity
// tier -- the only tier that serves in_progress -- cannot reach it. At
// max_active_sessions=1 it is false and GC_ALIAS IS the bare name
// (Agent.UsesCanonicalSingletonPoolIdentity), so the mayor's held bead stays
// fully covered. It is also false at max_active_sessions=0, where no session
// runs at all: reporting a deliberately disabled agent's whole queue is the
// false-positive flood, not a finding.
//
// Rejected alternative: dropping the bare name from the set outright. That
// re-reports every legitimately hand-assigned OPEN bead the ci-c000 tier
// exists to serve -- the shape TestUnclaimableAssigneeAcceptsTheBarePoolName
// pins.
//
// Documented constraint: this reads an agent as a TEMPLATE, which is what
// loadCityConfig hands the check. Pool expansion (cmd/gc/pool.go) rewrites
// Name to the slot spelling and moves the template name into PoolName; hand
// this an expanded list and a slot identity would be classified open-only.
// Nothing constructs the check that way today.
func (s *claimIdentitySet) addRouteTarget(a *config.Agent, identity string) {
	if a.SupportsExpandedSessionIdentities() {
		s.addOpenOnly(identity)
		return
	}
	s.add(identity)
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

// covers reports whether some session can hold work assigned to this name in
// this status.
//
// The assignee is matched verbatim, never trimmed. bd matches --assignee
// exactly, so an assignee with a stray trailing space really is claimable by
// nobody, and %q in the report makes it visible.
//
// Only in_progress narrows the set, and only for the openOnly tier. Documented
// absence: "blocked" is deliberately NOT narrowed even though `bd ready`
// excludes it too. A blocked bead is waiting, not stranded -- it returns to
// open when its blocker closes and the pool-alias tier serves it then. An
// in_progress one never transitions back on its own, which is what makes it
// claimable by nobody rather than claimable later.
func (s claimIdentitySet) covers(assignee, status string) bool {
	if _, ok := s.exact[assignee]; ok {
		return true
	}
	if !isInProgressStatus(status) {
		if _, ok := s.openOnly[assignee]; ok {
			return true
		}
	}
	for _, prefix := range s.slotPrefixes {
		if slot, found := strings.CutPrefix(assignee, prefix); found && isPositiveSlotNumber(slot) {
			return true
		}
	}
	return false
}

// isOpenOnlyRouteTarget reports whether this name is a pool route target the
// claim ladder reaches only while the work is still open -- the shape that
// earns its own finding sentence.
//
// It is called only for an assignee covers already rejected, so a name sitting
// in both tiers can never arrive here. A defensive `exact` lookup is
// deliberately ABSENT: no caller can reach it, so no mutation can prove it,
// and an unprovable guard reads as tested when it is not.
func (s claimIdentitySet) isOpenOnlyRouteTarget(assignee string) bool {
	_, ok := s.openOnly[assignee]
	return ok
}

// inProgressStatus is bd's spelling of a claimed bead. Both store backends
// normalize through beads.mapBdStatus, whose switch is an exact, case-
// sensitive match emitting only "closed", "in_progress" and "open", so a
// verbatim compare here cannot miss a casing bd never produces.
const inProgressStatus = "in_progress"

// isInProgressStatus reports whether a bead is claimed.
func isInProgressStatus(status string) bool { return status == inProgressStatus }

// qualifiedFormsOf returns the rig-qualified identities whose unqualified
// spelling is exactly assignee, sorted so a report is stable across runs. An
// assignee that is already qualified, or that matches no identity under any
// rig, yields none -- a suggestion invented for a typo would send the reader
// to a pool that does not exist.
func (s claimIdentitySet) qualifiedFormsOf(assignee string) []string {
	forms := s.qualifiedForms[strings.TrimSpace(assignee)]
	if len(forms) == 0 {
		return nil
	}
	out := make([]string, len(forms))
	copy(out, forms)
	sort.Strings(out)
	return out
}

// isPositiveSlotNumber reports whether a suffix is a slot index an uncapped
// pool could synthesize (agentutil.PoolInstanceName renders "%s-%d").
func isPositiveSlotNumber(suffix string) bool {
	n, err := strconv.Atoi(suffix)
	return err == nil && n > 0
}
