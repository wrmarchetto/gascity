// cmd/gc/pool_alias_handover.go
//
// The handover exception that lets a pool slot's replacement session bead
// reclaim its alias from the slot's own outgoing incarnation.
//
// The file exists because the reconciler creates a slot's replacement BEFORE
// the outgoing incarnation is closed -- measured at +7.06s and +7.16s for two
// governor sessions swept for want of work, and at +1s for a toolsmith slot
// retired by city-stop. Those are two different retirement paths reaching the
// same overlap, which is why the fix is here at the reservation seam and not in
// the ordering of either one. Inside that window the alias reservation is
// refused, the bead is created alias-less, and the launch environment is frozen
// from it: the session then spells every claim it ever writes as its session
// name, and a later back-fill of the alias onto the bead does not reach the
// running process.
//
// Editing constraint: the exception must never widen into "a dormant pool bead
// may be stripped of its alias". Two conditions carry that, and both are
// load-bearing -- the holder must be THIS identity at THIS slot, and it must be
// observably not running. TestPoolSessionAliasHandover* in
// build_desired_state_alias_handover_test.go pins each refusal separately from
// the handover.
package main

import (
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// liveSessionStates are the metadata states that mean a session bead is a pool
// slot's CURRENT occupant rather than its outgoing one. The set is a denylist,
// not an allowlist of retiring states, because the state field carries
// free-form values that never became session.State constants -- "city-stop" is
// the one this defect was measured on -- and an allowlist would silently stop
// covering the next such value.
//
// Absent from the set: "asleep", "draining", "drained", "suspended",
// "archived", "quarantined", "failed-create". Each is a bead the reconciler has
// already decided is not the slot's live occupant; selectOrPlanPoolSessionBead
// reaches a fresh create only after refusing to reuse every one of them.
var liveSessionStates = map[string]bool{
	string(session.StateActive):       true,
	string(session.StateAwake):        true,
	string(session.StateCreating):     true,
	string(session.StateStartPending): true,
}

// poolAliasHandover decides, for one pool session create, which identifier
// holders that create supersedes, and remembers the ones it waved past so the
// create site can say whose alias it took.
//
// The handover leaves no metadata on the created bead, deliberately. The
// sibling refusal keys (alias_reservation_refused/_reason, ci-yfuh3a) exist
// because a refused reservation produces a DEGRADED session that a store query
// days later has to explain; a successful handover produces an ordinary session
// holding its ordinary alias, with nothing to explain. The city-side measure of
// whether this fix fires is the disappearance of those refusal keys from
// same-slot overlaps, which is already published on the session-list row.
type poolAliasHandover struct {
	qualifiedInstance string
	slot              int
	sp                runtime.Provider
	// resolveProcessNames is memoized rather than resolved in the constructor
	// because it walks PATH through config.ResolveProvider, and a handover is
	// built for every pool session create while the identifier scan only
	// reaches a candidate bead when something already holds the alias.
	resolveProcessNames func() []string
	processNames        []string
	processNamesKnown   bool
	reclaimed           []string
}

// newPoolAliasHandover builds the handover for a pool session create at
// qualifiedInstance/slot. It returns nil when the handover cannot be proven
// safe -- no runtime provider to ask, or no identity to compare -- and a nil
// handover supersedes nothing, so every holder blocks exactly as before.
//
// The rejected alternative is to pass selfOwner to
// EnsureAliasAvailableWithConfigForOwner, which the singleton-pool normalizer
// already does. It changes nothing here: both blockers measured in the city
// carried alias=<name>, and the alias branch of ensureSessionAliasAvailable has
// no self-owner exception -- only the agent_name branch does.
func newPoolAliasHandover(bp *agentBuildParams, cfgAgent *config.Agent, qualifiedInstance string, slot int) *poolAliasHandover {
	qualifiedInstance = strings.TrimSpace(qualifiedInstance)
	if bp == nil || bp.sp == nil || cfgAgent == nil || qualifiedInstance == "" {
		return nil
	}
	city, agent, lookPath := bp.city, *cfgAgent, bp.lookPath
	return &poolAliasHandover{
		qualifiedInstance:   qualifiedInstance,
		slot:                slot,
		sp:                  bp.sp,
		resolveProcessNames: func() []string { return config.AgentProcessNames(city, agent, lookPath) },
	}
}

// supersedes answers the one question the reservation scan asks of a blocking
// holder: is this the same pool identity the create is replacing, with nothing
// left running under it?
//
// Identity is agent_name plus pool slot on a pool-managed bead. The alias alone
// is not enough -- an unrelated dormant session that merely persisted the name
// would satisfy it, and stripping that session's alias renames a slot that is
// only asleep between assignments. Liveness is the runtime probe the sweep
// already trusts to CLOSE a pool session bead, which is a strictly stronger
// decision than moving its alias.
func (h *poolAliasHandover) supersedes(b beads.Bead) bool {
	if h == nil {
		return false
	}
	if strings.TrimSpace(b.Metadata["pool_managed"]) != "true" {
		return false
	}
	if strings.TrimSpace(b.Metadata["agent_name"]) != h.qualifiedInstance {
		return false
	}
	if poolSlotMetadataInt(b) != h.slot {
		return false
	}
	if liveSessionStates[strings.TrimSpace(b.Metadata["state"])] {
		return false
	}
	// A probe error is not evidence of absence -- a bead with no session_name,
	// or a provider that cannot answer, leaves the holder's process
	// unaccounted for, and the alias stays where it is.
	running, err := poolSessionBeadRuntimeRunning(b, h.sp, h.agentProcessNames())
	if err != nil || running {
		return false
	}
	h.reclaimed = append(h.reclaimed, b.ID)
	return true
}

// agentProcessNames resolves the liveness probe's process-name hints once. The
// scan runs single-threaded inside one WithCitySessionIdentifierLocks call, so
// the memo needs no synchronization.
func (h *poolAliasHandover) agentProcessNames() []string {
	if !h.processNamesKnown {
		h.processNames = h.resolveProcessNames()
		h.processNamesKnown = true
	}
	return h.processNames
}

// predicate adapts the handover for the reservation scan. A nil handover still
// yields a callable that supersedes nothing, so the create site needs no nil
// branch of its own.
func (h *poolAliasHandover) predicate() session.SupersededHolderFunc {
	return h.supersedes
}

// reclaimedFrom names the holders this handover waved past, or empty when the
// alias was free all along.
func (h *poolAliasHandover) reclaimedFrom() []string {
	if h == nil {
		return nil
	}
	return h.reclaimed
}

// poolSlotMetadataInt reads a session bead's pool slot from raw metadata. An
// empty or unparseable value is slot 0, which is what the singleton-pool
// identity collapse writes and what poolDesiredRequestIdentity passes for a
// singleton, so the two agree without a special case. The sibling parsePoolSlot
// in adoption_barrier.go is NOT this function -- it recovers a slot from a
// session-name suffix.
func poolSlotMetadataInt(b beads.Bead) int {
	slot, err := strconv.Atoi(strings.TrimSpace(b.Metadata["pool_slot"]))
	if err != nil {
		return 0
	}
	return slot
}
