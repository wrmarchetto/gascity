package main

import (
	"fmt"
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
