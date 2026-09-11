package main

// cmd/gc/claim_lease_renewal.go
//
// Controller-side renewal of the bd claim lease on work beads whose holder
// session is still open, so that an expired lease means what every consumer
// already reads it to mean: the holder is gone.
//
// Nothing else in the tree renews a lease. bd grants one at claim time and
// expects the worker to refresh it faster than the TTL; `gc bd heartbeat <id>`
// is the only writer gc has, and no hook, prompt template, order or code path
// calls it. So before this file every claim's lease expired exactly one TTL
// after the claim -- on every provider, for a working holder and a dead one
// alike -- and `lease_expires_at` was a claim-age timer wearing a liveness
// name. That is not a latent hazard: `bd reclaim` applies the bare lease with
// no liveness check, and bd's own close refusal ("reclaim or use --force to
// override") routes any agent that meets a foreign assignee straight into it.
// 13 lease_reclaimed events had already landed across four stores by
// 2026-09-06, four of them by neither a human nor the mayor (ci-pzejlf).
//
// Why the controller and not the worker. The claims that outlive the TTL are
// precisely the ones where the worker runs no gc command for the whole window
// -- a container build, a 16-minute test gate, a long authoring turn. A prompt
// instruction, a UserPromptSubmit hook and a per-tool-call hook all renew at
// turn or call boundaries, which is the granularity that already fails. The
// controller is awake on its own patrol timer and already computes the
// liveness join this needs.
//
// The predicate is deliberately the ORPHAN-RELEASE predicate: a claim's lease
// is renewed exactly while releaseOrphanedPoolAssignments would not reopen the
// claim, both through openSessionOwnership. That is what makes the lease agree
// with the controller rather than offer a second opinion.
//
// Rejected: keying renewal on session activity (`gc session list`'s LAST
// ACTIVE), which is the discriminator an operator reaches for. A holder parked
// on a human gate for an hour is inactive and still holds live work, so an
// activity-keyed lease lapses under it and re-opens the theft this closes.
//
// NOT renewed here: gc.last_heartbeat_at, the committed cross-node stamp
// `gc bd heartbeat` writes for the dashboard (gastownhall/gascity#1855). That
// stamp asserts the WORKER said it was alive, which the controller cannot say
// on its behalf, and writing it per claim per interval would commit to Dolt
// forever for a signal the session beads already carry. A dashboard that wants
// controller-observed liveness should read the session bead.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

const (
	// bdClaimLeaseTTLMeasured is bd's claim-lease TTL as measured, not as
	// configured: bd keeps leases in an ephemeral node-local table, beads.Store
	// exposes no lease surface at all, and no bd flag or config key reports the
	// figure. Measured 2026-09-09 on the city store -- claim stamped
	// heartbeat_at 05:53:18Z and lease_expires_at 05:58:18Z, exactly 5m.
	// Consumed only by TestClaimLeaseRenewIntervalFitsTwiceInTheClaimLeaseTTL;
	// nothing schedules against it, so a bd change moves the test, not the
	// behavior.
	bdClaimLeaseTTLMeasured = 5 * time.Minute

	// claimLeaseRenewInterval is how often one claim's lease is pushed forward.
	// Sized to fit twice inside the measured TTL so a slow or skipped patrol
	// tick cannot lapse a live holder's lease, and no tighter: each renewal is
	// a bd subprocess, and the patrol tick defaults to 30s.
	//
	// Renewals are not staggered. Claims made minutes apart drift apart on
	// their own, but every claim already held when a controller starts comes
	// due on the same tick, so the first tick after a restart pays one bd
	// subprocess per live claim at once. Left alone: at this city's scale that
	// is a handful of sub-second calls on one tick every two minutes, and a
	// stagger would be a scheduling heuristic bought with nothing measured.
	claimLeaseRenewInterval = 2 * time.Minute

	// claimLeaseRenewTimeout bounds one bd heartbeat. A lease refresh writes no
	// Dolt commit, so anything approaching this is a wedged store rather than a
	// slow write; the sweep runs on the reconciler goroutine and must not hold
	// the tick for a store that has stopped answering.
	claimLeaseRenewTimeout = 20 * time.Second

	// cityStoreRef is the store-ref label the assigned-work snapshot gives the
	// city store. Rig stores carry their rig name.
	cityStoreRef = "city"
)

// claimLeaseRenewal names one claim whose lease the controller will push
// forward on this tick.
type claimLeaseRenewal struct {
	BeadID string
	// Assignee is presented to bd as --actor. bd matches a lease on
	// holder = actor, and gc hook --claim takes the lease under the bead's own
	// assignee, so this is the only value bd will accept for the refresh.
	Assignee string
	// StoreRef is "city" or a rig name, and selects the scope the bd
	// subprocess runs in.
	StoreRef string
}

// claimLeaseRenewer selects the claims due for a lease refresh and remembers
// when each was last attempted.
//
// The memory is per-controller and in-process on purpose. It is a cost
// throttle, not state anyone must recover: a controller restart re-renews
// every live claim one tick later, which is the correct behavior and cheaper
// than a file that can go stale against the ephemeral table it mirrors.
type claimLeaseRenewer struct {
	// renewedAt records the last ATTEMPT, not the last success. A bd refusal
	// means the caller no longer holds the lease, and retrying that every
	// patrol tick would spend a subprocess per tick to be told so again.
	renewedAt map[string]time.Time
	interval  time.Duration
}

// newClaimLeaseRenewer builds a renewer on the package's own interval. The
// interval is a field rather than a bare constant read at the use site so the
// throttle comparison in due() names one value; it takes no argument because
// no caller, test included, has a reason to run a different cadence than the
// one the TTL fixes.
func newClaimLeaseRenewer() *claimLeaseRenewer {
	return &claimLeaseRenewer{renewedAt: make(map[string]time.Time), interval: claimLeaseRenewInterval}
}

// due returns the claims whose lease must be refreshed now, and records the
// attempt against each. Claims absent from this tick's snapshot are forgotten,
// so the map tracks live claims rather than every bead the city ever claimed.
//
// Callers must skip the sweep entirely on a partial snapshot: a session
// snapshot missing a live holder makes its claim look unheld here, exactly as
// it does to the release path.
func (r *claimLeaseRenewer) due(
	cityPath string,
	cfg *config.City,
	cityStore beads.Store,
	openSessionInfos []sessionpkg.Info,
	assignedWorkBeads []beads.Bead,
	assignedWorkStoreRefs []string,
	now time.Time,
) []claimLeaseRenewal {
	storeRefAware := len(assignedWorkStoreRefs) == len(assignedWorkBeads) && len(assignedWorkBeads) > 0
	ownership := newOpenSessionOwnership(cityPath, cfg, openSessionInfos, storeRefAware)

	var targets []claimLeaseRenewal
	seen := make(map[string]struct{}, len(assignedWorkBeads))
	for i, wb := range assignedWorkBeads {
		if wb.Status != "in_progress" {
			continue
		}
		// A short-circuit, not a safety check, and it survives mutation for
		// that reason: claimHolderIsPresent refuses an empty assignee through
		// both of its sources, so deleting these three lines changes no
		// selection this suite can observe. Kept because an unassigned
		// in_progress bead is a normal snapshot member (the release path's own
		// stranded-work case) and there is no sense resolving ownership for it.
		assignee := strings.TrimSpace(wb.Assignee)
		if assignee == "" {
			continue
		}
		seen[wb.ID] = struct{}{}
		storeRef := ""
		if storeRefAware {
			storeRef = assignedWorkStoreRefs[i]
		}
		if !claimHolderIsPresent(ownership, cityStore, assignee, storeRef) {
			continue
		}
		if last, ok := r.renewedAt[wb.ID]; ok && now.Sub(last) < r.interval {
			continue
		}
		r.renewedAt[wb.ID] = now
		targets = append(targets, claimLeaseRenewal{BeadID: wb.ID, Assignee: assignee, StoreRef: storeRef})
	}
	for id := range r.renewedAt {
		if _, ok := seen[id]; !ok {
			delete(r.renewedAt, id)
		}
	}
	return targets
}

// claimHolderIsPresent reports whether a claim's assignee still names a
// session the controller can see. It is the renewal half of the release path's
// keep/reopen decision, and reads the same two liveness sources in the same
// order: this tick's open-session snapshot, then a live store query for the
// snapshot's own misses.
//
// The store query is not redundant with the snapshot. A live session absent
// from a tick's snapshot is the case
// TestReleaseOrphanedPoolAssignments_SkipsLiveSessionMissingFromSnapshot
// exists for, and without the same fallback here that holder's lease would
// lapse under it -- the precise failure this whole file closes. It costs one
// query per claim whose assignee missed the snapshot, which on a healthy tick
// is none.
//
// Renewal deliberately does NOT mirror the release path's other two keep
// clauses, so the two are not symmetric and the asymmetry is the point.
// assigneeNamesConfiguredPool keeps a bare pool ADDRESS, which is not a
// session and holds no lease -- bd would refuse the refresh. And
// assigneePreservesNamedSessionRoute keeps a claim to protect a named
// session's ROUTE whether or not that session still exists, which is a routing
// statement, not a liveness one; renewing on it would pin a dead holder's
// lease. Both cases therefore keep the claim assigned while letting its lease
// lapse, which is the correct reading: nobody is working it.
func claimHolderIsPresent(ownership openSessionOwnership, cityStore beads.Store, assignee, workStoreRef string) bool {
	if ownership.ownsWork(assignee, workStoreRef) {
		return true
	}
	return liveOpenSessionAssignmentExists(cityStore, assignee)
}

// renewClaimLeaseViaBd pushes one claim's lease forward through the bd binary.
//
// It shells out because there is no other way: bd keeps leases in a node-local
// table no store method exposes, so the beads.Store the controller already
// holds cannot reach one. The scope resolution, binary pin and environment are
// the same three the gc bd passthrough applies, for the same reason -- a scope
// bound to a storage binding pins the bd build that speaks it.
//
// The --actor is joined with "=" deliberately. A two-token `--actor <id>` puts
// a bead id where resolveBdScopeTarget's argument scan reads it as the
// command's subject, which re-scopes a rig bead's refresh to the city store
// (see doBdHeartbeat, and TestResolveBdScopeTargetReadsATwoTokenFlagValueAsTheSubject).
//
// Presenting another identity's actor is exactly what bdHeartbeatLeaseActor
// refuses for `gc bd heartbeat`, on the grounds that it would let any caller
// refresh any other agent's lease and would delete bd's refusal -- the only
// signal that tells a worker its claim is gone. Neither applies here. The
// controller is the component that owns the release decision rather than an
// arbitrary caller, and it renews only what it would refuse to release; and a
// worker whose claim HAS been reclaimed is no longer the bead's assignee, so
// this path stops renewing it and the worker's own heartbeat is still refused.
func renewClaimLeaseViaBd(ctx context.Context, cityPath string, cfg *config.City, target claimLeaseRenewal) error {
	rigName := ""
	if target.StoreRef != "" && target.StoreRef != cityStoreRef {
		rigName = target.StoreRef
	}
	scope, err := resolveBdScopeTarget(cfg, cityPath, rigName, []string{"heartbeat", target.BeadID}, false, io.Discard)
	if err != nil {
		return fmt.Errorf("resolving scope for %s: %w", target.BeadID, err)
	}
	bdPath, err := resolveBdBinaryForScope(cityPath, scope.ScopeRoot)
	if err != nil {
		return fmt.Errorf("resolving bd for %s: %w", target.BeadID, err)
	}
	env, err := bdCommandEnv(cityPath, cfg, scope)
	if err != nil {
		return fmt.Errorf("building bd env for %s: %w", target.BeadID, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, claimLeaseRenewTimeout)
	defer cancel()
	args := []string{"heartbeat", target.BeadID, "--actor=" + target.Assignee}
	cmd := exec.CommandContext(runCtx, bdPath, args...)
	cmd.Dir = scope.ScopeRoot
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	start := time.Now()
	cmd.Env = workQueryEnvForDir(env, cmd.Dir)
	runErr := cmd.Run()
	beads.TraceBDCall("go:claim-lease-renewal", scope.ScopeRoot, args, start, bdExitCode(runErr), runErr)
	if runErr != nil {
		return fmt.Errorf("bd heartbeat %s: %w", target.BeadID, runErr)
	}
	return nil
}

// bdExitCode maps an exec error onto the exit code TraceBDCall records: 0 for
// success, the process code for an ordinary refusal, -1 for a failure that
// never produced one (timeout, missing binary).
func bdExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// renewLiveClaimLeases pushes the bd claim lease forward for every in-progress
// claim this tick still considers held, and returns how many it refreshed.
//
// It runs immediately after releaseOrphanedPoolAssignments and over that
// function's own post-release snapshot, so a claim reopened this tick is never
// renewed on the same tick.
//
// A partial snapshot skips the sweep whole. A session snapshot missing a live
// holder makes its claim look unheld, and renewing nothing is the recoverable
// direction: the next complete tick renews, and one skipped tick cannot lapse
// a lease that is refreshed twice per TTL.
//
// Failures are logged and dropped. A refusal is the ordinary case (the lease
// was reclaimed, the bead closed) and carries no work for the controller; a
// store fault will re-present itself on the next tick. Nothing about the tick
// depends on the outcome.
func (cr *CityRuntime) renewLiveClaimLeases(
	ctx context.Context,
	openSessionInfos []sessionpkg.Info,
	assignedWorkBeads []beads.Bead,
	assignedWorkStoreRefs []string,
	result DesiredStateResult,
) int {
	if cr.cfg == nil || len(assignedWorkBeads) == 0 || result.snapshotQueryPartial() {
		return 0
	}
	if cr.clr == nil {
		cr.clr = newClaimLeaseRenewer()
		cr.warnIfPatrolOutrunsClaimLeaseTTL()
	}
	targets := cr.clr.due(cr.cityPath, cr.cfg, cr.cityBeadStore(), openSessionInfos, assignedWorkBeads, assignedWorkStoreRefs, time.Now())
	renewed := 0
	for _, target := range targets {
		if err := renewClaimLeaseViaBd(ctx, cr.cityPath, cr.cfg, target); err != nil {
			fmt.Fprintf(cr.stderr, "claim-lease renewal: %v\n", err) //nolint:errcheck // best-effort stderr
			continue
		}
		renewed++
	}
	return renewed
}

// warnIfPatrolOutrunsClaimLeaseTTL reports a patrol cadence that cannot sustain
// renewal, once per controller.
//
// The renewal interval is a floor, not a schedule: renewal happens on patrol
// ticks, so the effective cadence is whichever is slower. A city configured
// with a patrol interval at or above the lease TTL renews strictly later than
// the lease expires and this whole file silently does nothing -- the failure
// mode it was written to remove, restored by a config key nobody would connect
// to it. There is no gate that can refuse it, because bd's TTL is not
// observable from here (bdClaimLeaseTTLMeasured is a measurement, not a read),
// so the remedy is named on stderr rather than enforced.
func (cr *CityRuntime) warnIfPatrolOutrunsClaimLeaseTTL() {
	patrol := cr.cfg.Daemon.PatrolIntervalDuration()
	if patrol < bdClaimLeaseTTLMeasured {
		return
	}
	fmt.Fprintf(cr.stderr, //nolint:errcheck // best-effort stderr
		"claim-lease renewal: patrol interval %s is not shorter than the %s bd claim-lease TTL, so a live holder's lease will lapse between ticks and bd reclaim can re-serve work in progress. Remedy: set [daemon] patrol_interval below %s in city.toml and run `gc reload`.\n",
		patrol, bdClaimLeaseTTLMeasured, claimLeaseRenewInterval)
}
