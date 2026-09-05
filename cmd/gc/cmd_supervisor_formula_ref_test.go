package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/supervisor"
)

// stubSupervisorFormulaRefConfig points supervisorLoadConfig at a config
// carrying ref, for the duration of the test. Follows the package
// convention for the supervisorLoadConfig test double: replacing it is
// process-global, so no test using this helper may run in parallel.
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

// TestApplySupervisorFormulaRefPinsFormulaSource verifies the invariant
// the whole change exists for: after the supervisor applies its
// configured formula_ref, formula.SourceFromEnv -- the function every
// formula parse in this process goes through -- no longer returns the
// live working tree.
//
// The assertion is on the resolved Source rather than on the env string
// because the env var is only the transport. A test that checked
// GC_FORMULA_REF alone would still pass if SourceFromEnv's own mapping
// changed underneath it (it already maps "HEAD" and "working-tree" back
// to FSSource), and a pin that silently resolves to the working tree is
// indistinguishable from success until the next mid-merge order firing.
func TestApplySupervisorFormulaRefPinsFormulaSource(t *testing.T) {
	t.Setenv("GC_FORMULA_REF", "")
	_ = os.Unsetenv("GC_FORMULA_REF")

	if _, isFS := formula.SourceFromEnv().(formula.FSSource); !isFS {
		t.Fatal("SourceFromEnv() is not FSSource before the pin (parent-RED baseline)")
	}

	stubSupervisorFormulaRefConfig(t, "main")
	applySupervisorFormulaRef()

	if got := os.Getenv("GC_FORMULA_REF"); got != "main" {
		t.Fatalf("GC_FORMULA_REF = %q, want %q", got, "main")
	}
	if _, isFS := formula.SourceFromEnv().(formula.FSSource); isFS {
		t.Fatal("SourceFromEnv() is still FSSource after the pin; formulas would resolve from the working tree")
	}
}

// TestApplySupervisorFormulaRefEnvWins verifies the escape hatch: a
// GC_FORMULA_REF present in the supervisor's environment beats the
// config file, so the operator can start a supervisor on the working
// tree without editing supervisor.toml.
//
// Presence is the test, not non-emptiness. An operator who exports the
// key at all has expressed an intent, and SourceFromEnv reads the empty
// value as a real choice (FSSource) rather than as an omission.
func TestApplySupervisorFormulaRefEnvWins(t *testing.T) {
	stubSupervisorFormulaRefConfig(t, "main")

	t.Run("explicit ref survives", func(t *testing.T) {
		t.Setenv("GC_FORMULA_REF", "working-tree")
		applySupervisorFormulaRef()
		if got := os.Getenv("GC_FORMULA_REF"); got != "working-tree" {
			t.Fatalf("GC_FORMULA_REF = %q, want %q", got, "working-tree")
		}
	})

	t.Run("explicit empty survives", func(t *testing.T) {
		t.Setenv("GC_FORMULA_REF", "")
		applySupervisorFormulaRef()
		if got, present := os.LookupEnv("GC_FORMULA_REF"); !present || got != "" {
			t.Fatalf("GC_FORMULA_REF = %q present=%v, want empty and present", got, present)
		}
	})
}

// TestApplySupervisorFormulaRefUnconfigured verifies that a
// supervisor.toml with no formula_ref leaves the process env untouched,
// so an existing install keeps resolving formulas from the working tree
// until its operator opts in.
func TestApplySupervisorFormulaRefUnconfigured(t *testing.T) {
	t.Setenv("GC_FORMULA_REF", "")
	_ = os.Unsetenv("GC_FORMULA_REF")

	stubSupervisorFormulaRefConfig(t, "")
	applySupervisorFormulaRef()

	if _, present := os.LookupEnv("GC_FORMULA_REF"); present {
		t.Fatal("GC_FORMULA_REF was set from an unconfigured supervisor.toml")
	}
}

// TestDoSupervisorRunAppliesFormulaRef covers the call site: the pin has
// to be applied before the run loop starts, because the loop is what
// dispatches orders and every order dispatched before it would resolve
// from the working tree.
//
// runSupervisor blocks until shutdown, so the runSupervisorFunc
// indirection substitutes a no-op loop and samples the env from inside
// it -- sampling after doSupervisorRun returns would not distinguish
// "applied before the loop" from "applied after".
func TestDoSupervisorRunAppliesFormulaRef(t *testing.T) {
	t.Setenv("GC_FORMULA_REF", "")
	_ = os.Unsetenv("GC_FORMULA_REF")
	// doSupervisorRun also runs defaultSupervisorBeadsActor, which os.Setenvs
	// BEADS_ACTOR=controller in this process. Registering the key with
	// t.Setenv is what scopes that write to this test: without it the value
	// escapes into every later test in the package, and productmetrics
	// recording -- which reads the ambient actor -- silently stops recording.
	// Observed as TestProductMetricsDirectChildEnvPerf failing only when this
	// test ran first.
	t.Setenv("BEADS_ACTOR", "")
	_ = os.Unsetenv("BEADS_ACTOR")
	stubSupervisorFormulaRefConfig(t, "main")

	origRun := runSupervisorFunc
	seen := "<loop never ran>"
	runSupervisorFunc = func(io.Writer, io.Writer) int {
		seen = os.Getenv("GC_FORMULA_REF")
		return 0
	}
	t.Cleanup(func() { runSupervisorFunc = origRun })

	if rc := doSupervisorRun(io.Discard, io.Discard); rc != 0 {
		t.Fatalf("doSupervisorRun = %d, want 0", rc)
	}
	if seen != "main" {
		t.Fatalf("GC_FORMULA_REF inside the run loop = %q, want %q", seen, "main")
	}
}

// TestSupervisorChildEnvCarriesFormulaRef pins the fork-side half of the
// delivery.
//
// It exists because os.Setenv is invisible in /proc/<pid>/environ: Go
// keeps its own copy and the kernel's environ block is a snapshot taken
// at exec. Measured on this host -- a process that Setenvs a key sees it
// in os.Environ() and not in its own /proc environ. So a run-side pin
// alone would make the operator's natural verification of a live
// supervisor ("does the controller's environment carry it?") read as a
// silent failure. Seeding the forked child's env is what makes that
// check truthful on the `gc supervisor start` path.
func TestSupervisorChildEnvCarriesFormulaRef(t *testing.T) {
	t.Run("appends when absent", func(t *testing.T) {
		got := supervisorChildEnv([]string{"HOME=/home/x"}, "main")
		if !slices.Contains(got, "GC_FORMULA_REF=main") {
			t.Fatalf("child env = %#v, want it to contain GC_FORMULA_REF=main", got)
		}
	})

	t.Run("inherited value wins", func(t *testing.T) {
		base := []string{"HOME=/home/x", "GC_FORMULA_REF=working-tree"}
		got := supervisorChildEnv(base, "main")
		if !slices.Equal(got, base) {
			t.Fatalf("child env = %#v, want it unchanged from %#v", got, base)
		}
	})

	t.Run("inherited empty value wins", func(t *testing.T) {
		base := []string{"GC_FORMULA_REF="}
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
// above cannot: supervisorChildEnv could be correct and simply not called.
// A helper whose result no production path consumes is invisible to a suite
// that tests each end separately, so this drives the real
// `gc supervisor start` fork and reads the environment the spawned child
// actually received.
//
// It reuses the product-metrics child-env spy (the test binary re-executed
// as `supervisor run`, snapshotting its own environ) because that spy is the
// only place in this package that observes a real fork's env. GC_FORMULA_REF
// is deliberately absent from internal/testenv's LeakVectorVars, so no
// GC_TESTENV_PASSTHROUGH declaration is needed here -- if it is ever added
// to that list, this test goes red rather than quietly reading an empty
// value.
func TestSupervisorStartForkCarriesFormulaRef(t *testing.T) {
	// Mirrors TestProductMetricsServiceChildEnvSupervisorStart: clearing the
	// systemd delegation env keeps the start path non-delegated so it reaches
	// the fork rather than handing off to systemctl.
	pinRealHome(t)
	t.Setenv("GC_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv(supervisorSystemdUnitEnv, "")
	t.Setenv(supervisorSystemdScopeEnv, "")
	t.Setenv("GC_FORMULA_REF", "")
	_ = os.Unsetenv("GC_FORMULA_REF")
	stubSupervisorFormulaRefConfig(t, "main")

	previousAlive := supervisorAliveHook
	supervisorAliveHook = func() int { return 4242 }
	t.Cleanup(func() { supervisorAliveHook = previousAlive })

	entries := captureProductMetricsDirectChildEnv(t, func() error {
		var stdout, stderr bytes.Buffer
		if code := doSupervisorStartJSON(&stdout, &stderr, true); code != 0 {
			return fmt.Errorf("gc supervisor start code %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		return nil
	})

	if got := valuesForProductMetricsDirectChildKey(entries, "GC_FORMULA_REF"); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("forked supervisor GC_FORMULA_REF values = %#v, want [main]; env=%#v", got, entries)
	}
}

// TestPassthroughEnvCarriesFormulaRef pins the reach of the pin: the
// supervisor's GC_FORMULA_REF is swept into every session it spawns by
// passthroughEnv's GC_ prefix rule, so a pinned supervisor does not leave
// agent sessions resolving formulas from the working tree.
//
// This is asserted rather than assumed because it is a consequence of a
// rule written for other variables, not a decision taken for this one: a
// future narrowing of that sweep (an allowlist, or adding the key to
// processenv.ControllerOnlyEnvKeys) would split the fleet in half silently,
// with the controller ref-stable and every agent on the live tree.
func TestPassthroughEnvCarriesFormulaRef(t *testing.T) {
	t.Setenv("GC_FORMULA_REF", "main")
	if got := passthroughEnv()["GC_FORMULA_REF"]; got != "main" {
		t.Fatalf("passthroughEnv()[GC_FORMULA_REF] = %q, want %q", got, "main")
	}
}
