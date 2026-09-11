package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestEvaluatePoolFanOutSumSumsAcrossStores(t *testing.T) {
	probes := []poolStoreProbe{{ref: "city", dir: "city"}, {ref: "riga", dir: "riga"}, {ref: "rigb", dir: "rigb"}}
	counts := map[string]string{"city": "2", "riga": "3", "rigb": "5"}
	runner := func(_ string, dir string, _ map[string]string) (string, error) {
		return counts[dir], nil
	}

	got, errs := evaluatePoolFanOutSum("worker", scaleParams{Max: 100, Check: "check"}, probes, runner, make(chan struct{}, len(probes)), true)
	if len(errs) != 0 {
		t.Fatalf("errors = %v, want none", errs)
	}
	if got != 10 {
		t.Fatalf("fan-out demand = %d, want 10 (2 + 3 + 5)", got)
	}
}

func TestEvaluatePoolFanOutSumKeepsHealthyStoreDemand(t *testing.T) {
	probes := []poolStoreProbe{{ref: "city", dir: "city"}, {ref: "riga", dir: "riga"}, {ref: "rigb", dir: "rigb"}}
	runner := func(_ string, dir string, _ map[string]string) (string, error) {
		if dir == "riga" {
			return "", fmt.Errorf("unavailable")
		}
		if dir == "city" {
			return "3", nil
		}
		return "4", nil
	}

	got, errs := evaluatePoolFanOutSum("worker", scaleParams{Max: 100, Check: "check"}, probes, runner, make(chan struct{}, len(probes)), true)
	if got != 7 {
		t.Fatalf("fan-out demand = %d, want 7 when one store fails", got)
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want one failed-store diagnostic", errs)
	}
}

func TestCityScopedFanOutProbesExcludesSuspendedRigs(t *testing.T) {
	cityPath := t.TempDir()
	rigAPath := filepath.Join(cityPath, "riga")
	rigBPath := filepath.Join(cityPath, "rigb")
	cfg := &config.City{Rigs: []config.Rig{{Name: "riga", Path: rigAPath}, {Name: "rigb", Path: rigBPath, SuspendedOnStart: true}}}

	probes := cityScopedFanOutProbes(cityPath, cfg, &config.Agent{Name: "worker"}, cityPath, nil, map[string]bool{rigBPath: true})
	if len(probes) != 2 || probes[0].ref != "city" || probes[1].ref != "riga" {
		t.Fatalf("probes = %+v, want city and non-suspended riga only", probes)
	}
}

// TestCityScopedFanOutProbesScopeEachRigLegToItsOwnStore pins the store
// coordinate of a fan-out leg, which is the whole point of the fan-out and
// was the one thing nothing asserted.
//
// The leg's working directory is not the store selector, whatever
// cityScopedFanOutProbes' own doc comment used to say. controllerAgentCommandEnv
// puts BEADS_DIR in every leg's env, the gc-beads-bd provider reads it ahead
// of cwd (bd_env.go: "cwd-based discovery is not sufficient"), and a
// city-scoped agent's env pins the CITY store -- so five legs with five
// working directories all counted the same store. Measured on the host that
// found it: the city and a rig leg as gc built it both reported db=hq
// ready=113, and the same leg with a rig-scoped env reported db=as ready=1.
// Every bench technician is city-scoped with a custom scale_check, so no rig
// bead could raise demand for one (ci-efjuzz).
//
// Asserted as three distinct BEADS_DIR values rather than as one rig's
// expected string: the defect was that they were all EQUAL, and a test naming
// only rigA's path would pass against a fan-out that gave rigB the city's.
func TestCityScopedFanOutProbesScopeEachRigLegToItsOwnStore(t *testing.T) {
	cityPath := t.TempDir()
	rigAPath := filepath.Join(cityPath, "rigs", "riga")
	rigBPath := filepath.Join(cityPath, "rigs", "rigb")
	for _, p := range []string{rigAPath, rigBPath} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "riga", Path: rigAPath}, {Name: "rigb", Path: rigBPath}},
		Agents: []config.Agent{{
			Name:       "bench-technician",
			ScaleCheck: "custom-check",
		}},
	}
	ownEnv, err := controllerAgentCommandEnv(cityPath, cfg, &cfg.Agents[0])
	if err != nil {
		t.Fatalf("controllerAgentCommandEnv() error = %v, want nil", err)
	}

	probes := cityScopedFanOutProbes(cityPath, cfg, &cfg.Agents[0], cityPath, ownEnv, nil)

	if len(probes) != 3 {
		t.Fatalf("probes = %+v, want city plus two rigs", probes)
	}
	want := map[string]string{
		"city": filepath.Join(cityPath, ".beads"),
		"riga": filepath.Join(rigAPath, ".beads"),
		"rigb": filepath.Join(rigBPath, ".beads"),
	}
	seen := map[string]struct{}{}
	for _, probe := range probes {
		got := probe.env["BEADS_DIR"]
		if got != want[probe.ref] {
			t.Errorf("probe %q BEADS_DIR = %q, want %q -- this leg counts the wrong store", probe.ref, got, want[probe.ref])
		}
		seen[got] = struct{}{}
	}
	if len(seen) != len(probes) {
		t.Errorf("the %d legs share %d store coordinates; every leg must read a different store", len(probes), len(seen))
	}
}

// TestRigScopedProbeEnvFallsBackToTheCallersEnv pins the fallback, at the
// helper rather than through cityScopedFanOutProbes.
//
// The fan-out route cannot reach it: an unbound rig still resolves a city
// env, so a fixture built that way asserts nothing -- measured, as a mutation
// that returned nil from the fallback survived it. Both guards are driven
// directly instead, each by the input that actually trips it.
//
// Returning nil would DROP the leg's environment and silently point it at
// whatever the controller process inherited. Keeping the caller's env is at
// worst the pre-fix behavior for one leg, and losing a store's demand
// entirely is the failure this fan-out exists to prevent.
func TestRigScopedProbeEnvFallsBackToTheCallersEnv(t *testing.T) {
	cityPath := t.TempDir()
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}}
	ownEnv := map[string]string{"BEADS_DIR": filepath.Join(cityPath, ".beads")}

	t.Run("no agent to build a view from", func(t *testing.T) {
		got := rigScopedProbeEnv(cityPath, cfg, nil, "riga", ownEnv)
		if got["BEADS_DIR"] != ownEnv["BEADS_DIR"] {
			t.Fatalf("env = %v, want the caller's env", got)
		}
	})

	t.Run("no city path to resolve coordinates from", func(t *testing.T) {
		got := rigScopedProbeEnv("", cfg, &config.Agent{Name: "worker"}, "riga", ownEnv)
		if got["BEADS_DIR"] != ownEnv["BEADS_DIR"] {
			t.Fatalf("env = %v, want the caller's env", got)
		}
	})
}

// TestCityScopedFanOutProbesKeepsARigWithNoPathBindingAsALeg pins that such a
// rig is not dropped from the fan-out.
//
// It does NOT reach the env fallback -- an unbound rig still resolves an env
// by the ordinary route -- so the fallback itself is
// TestRigScopedProbeEnvFallsBackToTheCallersEnv above. Dropping the leg would
// lose that store's demand silently, which is the failure the whole fan-out
// exists to prevent.
func TestCityScopedFanOutProbesKeepsARigWithNoPathBindingAsALeg(t *testing.T) {
	cityPath := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "unbound"}},
		Agents:    []config.Agent{{Name: "bench-technician", ScaleCheck: "custom-check"}},
	}
	ownEnv := map[string]string{"BEADS_DIR": filepath.Join(cityPath, ".beads")}

	probes := cityScopedFanOutProbes(cityPath, cfg, &cfg.Agents[0], cityPath, ownEnv, nil)

	if len(probes) != 2 || probes[1].ref != "unbound" {
		t.Fatalf("probes = %+v, want the unbound rig kept as a leg", probes)
	}
	if probes[1].env["BEADS_DIR"] == "" {
		t.Errorf("unbound leg env = %v, want a store coordinate rather than none", probes[1].env)
	}
}
