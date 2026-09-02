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
// store for a city-scoped custom scale check. The command's working directory
// selects the store; its city-scoped runtime environment is shared unchanged.
func cityScopedFanOutProbes(cityPath string, cfg *config.City, _ *config.Agent, ownDir string, ownEnv map[string]string, suspendedRigPaths map[string]bool) []poolStoreProbe {
	probes := []poolStoreProbe{{ref: "city", dir: ownDir, env: ownEnv}}
	if cfg == nil {
		return probes
	}
	for _, rig := range cfg.Rigs {
		if suspendedRigPaths[filepath.Clean(rig.Path)] {
			continue
		}
		probes = append(probes, poolStoreProbe{ref: rig.Name, dir: resolveAgentDirPath(cityPath, rig.Path), env: ownEnv})
	}
	return probes
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
