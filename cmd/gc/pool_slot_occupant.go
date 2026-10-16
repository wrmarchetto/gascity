// cmd/gc/pool_slot_occupant.go
//
// The last-line refusal that keeps a second pool session out of a slot another
// session is still running in.
//
// A pool slot is not just a name. work_dir is <worktrees>/<rig>/<AgentBase>,
// and AgentBase carries the slot number, so the slot IS a git working tree,
// and two sessions at one slot share an index and a HEAD. The allocator is meant
// to prevent that and failed open (ci-1tv1cr: above-bound numbers were
// invisible to it), and the alias check that runs at create cannot catch it,
// because an alias refusal is a naming fallback the create is designed to
// survive, and an occupant whose own reservation was refused holds no alias at
// all. This file is the check that does not depend on either.
//
// Editing constraint: the refusal must stay keyed on a RUNNING runtime. A bead
// whose state says awake but whose pane is gone is a crash the sweep heals --
// refusing on state alone wedges that slot until a human notices. Pinned case by
// case in pool_slot_occupant_test.go.
package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// errPoolSlotOccupied refuses a pool session create whose (agent_name, slot) a
// running session already holds.
var errPoolSlotOccupied = errors.New("pool session create refused: slot is occupied by a running session")

// refusePoolSlotOccupied returns errPoolSlotOccupied, naming the occupant, when
// an open pool-managed session bead other than the one being created holds
// qualifiedInstance at slot and its runtime is running.
//
// It reads the STORE, not bp.sessionBeads. The snapshot is taken at the start
// of the tick, and the caller holds the identifier lock for qualifiedInstance,
// so only a store read sees a same-instance create that committed after the
// snapshot -- the concurrent-create half of this race.
//
// Documented absence: a probe ERROR is not an occupant. A bead the runtime
// cannot answer for has no session_name or no reachable provider, and refusing
// on that would wedge the slot on a bead nobody may ever repair. The alias
// handover takes the opposite choice on error, deliberately: declining an alias
// costs one alias-less pass, declining a create here costs the slot.
func refusePoolSlotOccupied(bp *agentBuildParams, cfgAgent *config.Agent, qualifiedInstance string, slot int) error {
	if bp == nil || bp.beadStore == nil || cfgAgent == nil || slot <= 0 {
		return nil
	}
	open, err := loadSessionBeads(bp.beadStore)
	if err != nil {
		return fmt.Errorf("checking slot %d of %q for a running occupant: %w", slot, qualifiedInstance, err)
	}
	var processNames []string
	processNamesKnown := false
	for _, b := range open {
		if strings.TrimSpace(b.Metadata["pool_managed"]) != "true" ||
			strings.TrimSpace(b.Metadata["agent_name"]) != qualifiedInstance ||
			poolSlotMetadataInt(b) != slot {
			continue
		}
		if !processNamesKnown {
			processNames = config.AgentProcessNames(bp.city, *cfgAgent, bp.lookPath)
			processNamesKnown = true
		}
		running, probeErr := poolSessionBeadRuntimeRunning(b, bp.sp, processNames)
		if probeErr != nil || !running {
			continue
		}
		return fmt.Errorf("%w: %s (%s) is running at %q slot %d, and a second session there would share its worktree",
			errPoolSlotOccupied, b.ID, strings.TrimSpace(b.Metadata["session_name"]), qualifiedInstance, slot)
	}
	return nil
}
