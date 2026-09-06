package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/gastownhall/gascity/internal/execenv"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/supervisor"
)

// No test in this file calls os.Setenv or os.Unsetenv. GC_FORMULA_REF is a
// leak vector (internal/testenv), so every test binary starts with it
// scrubbed -- that scrubbed absence is the precondition these tests need, and
// each one asserts it rather than assuming it. Restoring the key by hand
// would also be counted debt against the cmd/gc environment ratchet in
// test/test-resources.toml.
func requireFormulaRefUnset(t *testing.T) {
	t.Helper()
	if val, present := os.LookupEnv(supervisorFormulaRefEnv); present {
		t.Fatalf("%s = %q at test start; internal/testenv should have scrubbed it", supervisorFormulaRefEnv, val)
	}
}

// stubSupervisorFormulaRefConfig points supervisorLoadConfig at a config
// carrying ref. Follows the package convention for that test double:
// replacing it is process-global, so no test using this may run in parallel.
func stubSupervisorFormulaRefConfig(t *testing.T, ref string) {
	t.Helper()
	orig := supervisorLoadConfig
	supervisorLoadConfig = func(string) (supervisor.Config, error) {
		var cfg supervisor.Config
		cfg.Supervisor.FormulaRef = ref
		return cfg, nil
	}
	t.Cleanup(func() { supervisorLoadConfig = orig })
}

// captureSupervisorFormulaRefSetenv replaces the pin's setter and returns a
// pointer to the values it received, so a test can see what the supervisor
// would export without actually exporting it.
func captureSupervisorFormulaRefSetenv(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	orig := supervisorFormulaRefSetenv
	supervisorFormulaRefSetenv = func(key, val string) error {
		seen = append(seen, key+"="+val)
		return nil
	}
	t.Cleanup(func() { supervisorFormulaRefSetenv = orig })
	return &seen
}

// TestApplySupervisorFormulaRefExportsConfiguredRef verifies that the
// supervisor exports its configured formula_ref verbatim, and that the value
// it exports is one that actually moves formula.SourceFromEnv off the live
// working tree.
//
// The second half is a separate assertion for a reason: SourceFromEnv maps
// "", "HEAD" and "working-tree" all back to FSSource, so a pin can be
// delivered perfectly and still resolve from the working tree. That failure
// is indistinguishable from success until the next mid-merge order firing,
// which is the whole hazard this change exists to remove.
func TestApplySupervisorFormulaRefExportsConfiguredRef(t *testing.T) {
	t.Run("exports the configured ref", func(t *testing.T) {
		requireFormulaRefUnset(t)
		stubSupervisorFormulaRefConfig(t, "main")
		seen := captureSupervisorFormulaRefSetenv(t)

		applySupervisorFormulaRef()

		if want := []string{supervisorFormulaRefEnv + "=main"}; !slices.Equal(*seen, want) {
			t.Fatalf("setenv calls = %#v, want %#v", *seen, want)
		}
	})

	t.Run("that ref leaves the working tree", func(t *testing.T) {
		t.Setenv(supervisorFormulaRefEnv, "main")
		if _, isFS := formula.SourceFromEnv().(formula.FSSource); isFS {
			t.Fatal("SourceFromEnv() is FSSource under the pinned ref; formulas would still resolve from the working tree")
		}
	})

	t.Run("HEAD would not have", func(t *testing.T) {
		t.Setenv(supervisorFormulaRefEnv, "HEAD")
		if _, isFS := formula.SourceFromEnv().(formula.FSSource); !isFS {
			t.Fatal("SourceFromEnv() left FSSource for HEAD; the no-middle-option premise this pin was chosen under no longer holds")
		}
	})
}

// TestApplySupervisorFormulaRefEnvWins verifies the escape hatch: a
// GC_FORMULA_REF already in the supervisor's environment beats the config
// file, so the operator can start a supervisor on the working tree without
// editing supervisor.toml.
//
// Presence is the test, not non-emptiness. An operator who exports the key at
// all has expressed an intent, and SourceFromEnv reads the empty value as a
// real choice (the working tree) rather than as an omission.
func TestApplySupervisorFormulaRefEnvWins(t *testing.T) {
	for _, ambient := range []string{"working-tree", "", "some-other-ref"} {
		t.Run("ambient="+ambient, func(t *testing.T) {
			t.Setenv(supervisorFormulaRefEnv, ambient)
			stubSupervisorFormulaRefConfig(t, "main")
			seen := captureSupervisorFormulaRefSetenv(t)

			applySupervisorFormulaRef()

			if len(*seen) != 0 {
				t.Fatalf("setenv calls = %#v, want none: the environment must beat the config file", *seen)
			}
		})
	}
}

// TestApplySupervisorFormulaRefUnconfigured verifies that a supervisor.toml
// with no formula_ref exports nothing, so an existing install keeps resolving
// formulas from the working tree until its operator opts in.
func TestApplySupervisorFormulaRefUnconfigured(t *testing.T) {
	requireFormulaRefUnset(t)
	stubSupervisorFormulaRefConfig(t, "")
	seen := captureSupervisorFormulaRefSetenv(t)

	applySupervisorFormulaRef()

	if len(*seen) != 0 {
		t.Fatalf("setenv calls = %#v, want none from an unconfigured supervisor.toml", *seen)
	}
}

// TestDoSupervisorRunAppliesFormulaRefBeforeTheLoop covers the call site. The
// pin has to land before the run loop starts, because the loop is what
// dispatches orders and any order dispatched ahead of it would parse its
// formula off the working tree.
//
// runSupervisor blocks until shutdown, so the runSupervisorFunc indirection
// substitutes a no-op loop and samples from inside it. Sampling after
// doSupervisorRun returned would not distinguish "applied before the loop"
// from "applied after".
func TestDoSupervisorRunAppliesFormulaRefBeforeTheLoop(t *testing.T) {
	requireFormulaRefUnset(t)
	// doSupervisorRun also runs defaultSupervisorBeadsActor, which os.Setenvs
	// BEADS_ACTOR when it reads as unset. Pinning it to its own default here
	// keeps that write from happening at all: an escaped BEADS_ACTOR stops
	// productmetrics recording for every later test in the package, which is
	// how this test first broke TestProductMetricsDirectChildEnvPerf.
	t.Setenv("BEADS_ACTOR", "controller")
	stubSupervisorFormulaRefConfig(t, "main")
	seen := captureSupervisorFormulaRefSetenv(t)

	origRun := runSupervisorFunc
	var atLoopEntry []string
	runSupervisorFunc = func(io.Writer, io.Writer) int {
		atLoopEntry = slices.Clone(*seen)
		return 0
	}
	t.Cleanup(func() { runSupervisorFunc = origRun })

	if rc := doSupervisorRun(io.Discard, io.Discard); rc != 0 {
		t.Fatalf("doSupervisorRun = %d, want 0", rc)
	}
	if want := []string{supervisorFormulaRefEnv + "=main"}; !slices.Equal(atLoopEntry, want) {
		t.Fatalf("setenv calls at loop entry = %#v, want %#v", atLoopEntry, want)
	}
}

// TestSupervisorChildEnvCarriesFormulaRef pins the fork-side half of the
// delivery.
//
// It exists because os.Setenv is invisible in /proc/<pid>/environ: Go keeps
// its own copy and the kernel's block is a snapshot taken at exec. Measured
// on this host -- a process that Setenvs a key sees it in os.Environ() and
// not in its own /proc environ. So a run-side pin alone would make the
// operator's natural verification of a live supervisor ("does the
// controller's environment carry it?") read as a silent failure. Seeding the
// forked child's env is what makes that check truthful on the
// `gc supervisor start` path.
func TestSupervisorChildEnvCarriesFormulaRef(t *testing.T) {
	t.Run("appends when absent", func(t *testing.T) {
		got := supervisorChildEnv([]string{"HOME=/home/x"}, "main")
		if !slices.Contains(got, supervisorFormulaRefEnv+"=main") {
			t.Fatalf("child env = %#v, want it to contain %s=main", got, supervisorFormulaRefEnv)
		}
	})

	t.Run("inherited value wins", func(t *testing.T) {
		base := []string{"HOME=/home/x", supervisorFormulaRefEnv + "=working-tree"}
		got := supervisorChildEnv(base, "main")
		if !slices.Equal(got, base) {
			t.Fatalf("child env = %#v, want it unchanged from %#v", got, base)
		}
	})

	t.Run("inherited empty value wins", func(t *testing.T) {
		base := []string{supervisorFormulaRefEnv + "="}
		got := supervisorChildEnv(base, "main")
		if !slices.Equal(got, base) {
			t.Fatalf("child env = %#v, want it unchanged from %#v", got, base)
		}
	})

	t.Run("unconfigured leaves env alone", func(t *testing.T) {
		base := []string{"HOME=/home/x"}
		got := supervisorChildEnv(base, "")
		if !slices.Equal(got, base) {
			t.Fatalf("child env = %#v, want it unchanged from %#v", got, base)
		}
	})
}

// TestSupervisorStartForkCarriesFormulaRef closes the hole the unit test
// above cannot: supervisorChildEnv could be correct and simply not called. A
// helper whose result no production path consumes is invisible to a suite
// that tests each end separately, so this drives the real
// `gc supervisor start` fork and reads the environment the spawned child
// actually received.
//
// It reuses the product-metrics child-env spy (the test binary re-executed as
// `supervisor run`, snapshotting its own environ) because that spy is the
// only observer in this package of a real fork's env. The spy child runs
// internal/testenv's scrub before it samples, so GC_FORMULA_REF -- a leak
// vector -- has to be declared as passthrough; without that declaration the
// snapshot reads empty and the test would mistake the scrub for a missing
// seed.
func TestSupervisorStartForkCarriesFormulaRef(t *testing.T) {
	// Mirrors TestProductMetricsServiceChildEnvSupervisorStart: clearing the
	// systemd delegation env keeps the start path non-delegated, so it
	// reaches the fork rather than handing off to systemctl.
	pinRealHome(t)
	t.Setenv("GC_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv(supervisorSystemdUnitEnv, "")
	t.Setenv(supervisorSystemdScopeEnv, "")
	requireFormulaRefUnset(t)
	stubSupervisorFormulaRefConfig(t, "main")

	previousAlive := supervisorAliveHook
	supervisorAliveHook = func() int { return 4242 }
	t.Cleanup(func() { supervisorAliveHook = previousAlive })

	entries := captureProductMetricsDirectChildEnv(t, func() error {
		t.Setenv("GC_TESTENV_PASSTHROUGH", execenv.UsageMetricsDisableEnv+","+supervisorFormulaRefEnv)
		var stdout, stderr bytes.Buffer
		if code := doSupervisorStartJSON(&stdout, &stderr, true); code != 0 {
			return fmt.Errorf("gc supervisor start code %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		return nil
	})

	if got := valuesForProductMetricsDirectChildKey(entries, supervisorFormulaRefEnv); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("forked supervisor %s values = %#v, want [main]; env=%#v", supervisorFormulaRefEnv, got, entries)
	}
}

// TestPassthroughEnvCarriesFormulaRef pins the reach of the pin: the
// supervisor's GC_FORMULA_REF is swept into every session it spawns by
// passthroughEnv's GC_ prefix rule, so a pinned supervisor does not leave
// agent sessions resolving formulas from the working tree.
//
// This is asserted rather than assumed because it is a consequence of a rule
// written for other variables, not a decision taken for this one: a future
// narrowing of that sweep (an allowlist, or adding the key to
// processenv.ControllerOnlyEnvKeys) would split the fleet in half silently,
// with the controller ref-stable and every agent on the live tree.
func TestPassthroughEnvCarriesFormulaRef(t *testing.T) {
	t.Setenv(supervisorFormulaRefEnv, "main")
	if got := passthroughEnv()[supervisorFormulaRefEnv]; got != "main" {
		t.Fatalf("passthroughEnv()[%s] = %q, want %q", supervisorFormulaRefEnv, got, "main")
	}
}
