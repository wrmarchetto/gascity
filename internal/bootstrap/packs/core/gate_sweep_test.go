package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// gateSweepTestscriptParams builds the testscript environment for one
// gate-sweep scenario. Each txtar ships its own bin/gc stand-in, so the shared
// setup only materializes the real script out of PackFS -- running the embedded
// asset rather than a copy is the point, since a copy would let the pack and
// the suite drift apart silently.
//
// No GC_PACK_STATE_DIR here, unlike renudgeTestscriptParams. gate-sweep keeps
// no state between passes and builds its scope list in memory, so a state dir
// would be setup that proves nothing.
func gateSweepTestscriptParams(txtar string) testscript.Params {
	return testscript.Params{
		Files: []string{filepath.Join("testdata", txtar)},
		Setup: func(env *testscript.Env) error {
			scriptDir := filepath.Join(env.WorkDir, "scripts")
			if err := os.MkdirAll(scriptDir, 0o755); err != nil {
				return err
			}
			for _, path := range []string{"assets/scripts/gate-sweep.sh", "assets/scripts/_bd_trace.sh"} {
				data, err := PackFS.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(scriptDir, filepath.Base(path)), data, 0o755); err != nil {
					return err
				}
			}
			if err := os.Chmod(filepath.Join(env.WorkDir, "bin", "gc"), 0o755); err != nil {
				return err
			}
			env.Setenv("PATH", filepath.Join(env.WorkDir, "bin")+":"+env.Getenv("PATH"))
			return nil
		},
	}
}

// TestGateSweepChecksTimerGatesInEveryRigStore pins that the sweep evaluates
// timer gates in every rig store and not only in HQ.
//
// This is the invariant whose absence cost 2h37m on as-oavp (ci-q4fyku): the
// order ran two bare `gc bd gate check` calls, a bare gate query is HQ-scoped,
// and a rig-store timer gate therefore had no closer at all. Established live
// by a two-arm experiment -- a city-store timer gate closed 134s past expiry
// while an otherwise identical rig-store gate was still open 15 minutes later.
//
// The txtar asserts one invocation PER STORE by recording what the stand-in was
// called with, rather than asserting that some gate closed. A closed-gate
// assertion would pass against a sweep that reached one store and happened to
// find the gate there, which is exactly the bug.
func TestGateSweepChecksTimerGatesInEveryRigStore(t *testing.T) {
	testscript.Run(t, gateSweepTestscriptParams("gate-sweep-every-rig-store.txtar"))
}

// TestGateSweepSkipsStoresItCannotAddress pins that the hq pseudo-rig and any
// rig with no resolved path are left out of the per-store walk.
//
// Both would be errors rather than extra coverage. HQ is already swept by the
// bare call, so addressing it again would double-escalate every HQ gate; and a
// rig declared in city.toml but never cloned reports an empty path, which as a
// `-C` argument means the CURRENT directory -- so the sweep would silently
// re-check whatever store it happens to be standing in and report success.
func TestGateSweepSkipsStoresItCannotAddress(t *testing.T) {
	testscript.Run(t, gateSweepTestscriptParams("gate-sweep-unaddressable-stores.txtar"))
}

// TestGateSweepContinuesPastAFailingStoreAndStillExitsNonzero pins the two
// properties that pull against each other once the sweep becomes a loop.
//
// #1734 requires a timer-gate failure to reach the controller log, which it
// does only through a non-zero exit (the `if err != nil` branch of dispatchOne
// in cmd/gc/order_dispatch.go). But an unreachable rig must not stop the sweep
// for the stores after it -- that would turn one broken rig into a fleet-wide
// gate outage, the same shape of failure this bead exists to fix. So the loop
// records the failure and exits non-zero at the END, and this test requires
// BOTH: the later store is still swept, and the exit is non-zero.
//
// A `|| true` on the timer line would satisfy the first half and silently
// discard the second, which is why the bare form was there to begin with.
func TestGateSweepContinuesPastAFailingStoreAndStillExitsNonzero(t *testing.T) {
	testscript.Run(t, gateSweepTestscriptParams("gate-sweep-failing-store.txtar"))
}

// TestGateSweepToleratesGhGateFailureInEveryStore pins that the gh-gate line
// stays best-effort in the per-store walk, not just in HQ.
//
// bd shells out to `gh` for gh:run and gh:pr gates, so a city without
// `gh auth` fails that line on every 30s cooldown. That tolerance existed
// before the walk and has to survive it: without it, adding rig scoping would
// turn one unauthenticated city into a permanently failing order.
func TestGateSweepToleratesGhGateFailureInEveryStore(t *testing.T) {
	testscript.Run(t, gateSweepTestscriptParams("gate-sweep-gh-failure.txtar"))
}
