package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/telemetry"
)

// poolStoreProbe identifies one store leg in a city-scoped custom scale-check
// fan-out.
type poolStoreProbe struct {
	ref string
	dir string
	env map[string]string
}

// cityScopedFanOutProbes returns the city store plus every non-suspended rig
// store for a city-scoped custom scale check. Each leg carries the working
// directory AND the runtime environment of the store it is meant to count.
//
// The env is what selects the store, not the directory, and this function
// said the opposite until ci-efjuzz. controllerAgentCommandEnv puts BEADS_DIR
// in the caller's env, and the gc-beads-bd provider reads it ahead of cwd --
// bd_env.go says so where it sets the key: "cwd-based discovery is not
// sufficient". For a city-scoped agent that key names the CITY store, so
// sharing one env across the legs gave five working directories and one
// store. Measured on the host that found it: the city leg and a rig leg as gc
// built it both reported db=hq ready=113, while the same rig leg with a
// rig-scoped env reported db=as ready=1. Every bench technician in that city
// is city-scoped with a custom scale check, so no rig bead could raise demand
// for one, whatever it was labeled or routed.
//
// The rig-scoped agent view is the shape appendOneRigHookStore already uses
// for the same problem on the claim path (hook_cross_store.go): copy the
// agent, point its Dir at the rig, and let the ordinary env builder resolve
// the coordinates. Rebuilding the env by hand here would be a second place
// that has to know which keys carry a store.
//
// A rig whose env cannot be built keeps the caller's env rather than being
// dropped from the fan-out. Dropping it loses that store's demand silently,
// which is the failure this fan-out exists to prevent; keeping it is at worst
// the pre-fix behavior for that one leg.
func cityScopedFanOutProbes(cityPath string, cfg *config.City, agentCfg *config.Agent, ownDir string, ownEnv map[string]string, suspendedRigPaths map[string]bool) []poolStoreProbe {
	probes := []poolStoreProbe{{ref: "city", dir: ownDir, env: ownEnv}}
	if cfg == nil {
		return probes
	}
	for _, rig := range cfg.Rigs {
		if suspendedRigPaths[filepath.Clean(rig.Path)] {
			continue
		}
		probes = append(probes, poolStoreProbe{
			ref: rig.Name,
			dir: resolveAgentDirPath(cityPath, rig.Path),
			env: rigScopedProbeEnv(cityPath, cfg, agentCfg, rig.Name, ownEnv),
		})
	}
	return probes
}

// rigScopedProbeEnv builds the runtime environment a fan-out leg needs to
// count rigName's store, falling back to the caller's env when it cannot.
func rigScopedProbeEnv(cityPath string, cfg *config.City, agentCfg *config.Agent, rigName string, ownEnv map[string]string) map[string]string {
	if agentCfg == nil {
		return ownEnv
	}
	view := *agentCfg
	view.Dir = rigName
	env, err := controllerAgentCommandEnv(cityPath, cfg, &view)
	// The test is BEADS_DIR, not emptiness. controllerAgentCommandEnv can
	// return a non-empty env carrying only GC_ROUTE_TARGETS when the
	// coordinates behind it do not resolve, and handing a leg an env with no
	// store key is worse than the fallback: it points the command at whatever
	// the controller process inherited instead of at a named store. Found by
	// TestRigScopedProbeEnvFallsBackToTheCallersEnv, which a len(env) == 0
	// guard failed.
	if err != nil || env["BEADS_DIR"] == "" {
		return ownEnv
	}
	return env
}

// evaluatePoolFanOutSum executes every probe through the caller's shared
// concurrency bound, preserving healthy-store demand when an individual probe
// fails. The aggregate is clamped once in desired-state mode.
func evaluatePoolFanOutSum(agentName string, sp scaleParams, probes []poolStoreProbe, runner ScaleCheckRunner, sem chan struct{}, newDemand bool) (int, []error) {
	counts := make([]int, len(probes))
	errs := make([]error, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func(i int, probe poolStoreProbe) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			started := time.Now()
			out, err := runner(sp.Check, probe.dir, probe.env)
			durationMS := float64(time.Since(started).Milliseconds())
			if err != nil {
				telemetry.RecordPoolCheck(context.Background(), agentName, durationMS, 0, err)
				errs[i] = fmt.Errorf("%s: %w", probe.ref, err)
				return
			}
			n, err := parseScaleCheckCount(agentName, sp.Check, out)
			if err != nil {
				telemetry.RecordPoolCheck(context.Background(), agentName, durationMS, 0, err)
				errs[i] = fmt.Errorf("%s: %w", probe.ref, err)
				return
			}
			telemetry.RecordPoolCheck(context.Background(), agentName, durationMS, n, nil)
			counts[i] = n
		}(i, probe)
	}
	wg.Wait()

	sum := 0
	var outErrs []error
	for i, n := range counts {
		sum += n
		if errs[i] != nil {
			outErrs = append(outErrs, errs[i])
		}
	}
	if !newDemand {
		if sum < sp.Min {
			sum = sp.Min
		}
		if sp.Max >= 0 && sum > sp.Max {
			sum = sp.Max
		}
	}
	return sum, outErrs
}
