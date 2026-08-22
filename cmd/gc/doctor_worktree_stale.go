package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/doctor"
)

// worktreeStaleCheck reports agent-home worktree markers that prevent the
// reconciler from assigning a replacement session. The marker remains a
// fail-closed handoff boundary; this check only makes its impact visible.
type worktreeStaleCheck struct {
	cityPath string
}

func newWorktreeStaleCheck(cityPath string) *worktreeStaleCheck {
	return &worktreeStaleCheck{cityPath: cityPath}
}

func (c *worktreeStaleCheck) Name() string { return "worktree-stale" }

func (c *worktreeStaleCheck) CanFix() bool { return false }

func (c *worktreeStaleCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *worktreeStaleCheck) WarmupEligible() bool { return false }

func (c *worktreeStaleCheck) Run(ctx *doctor.CheckContext) *doctor.CheckResult {
	cityPath := c.cityPath
	if strings.TrimSpace(cityPath) == "" && ctx != nil {
		cityPath = ctx.CityPath
	}
	result := &doctor.CheckResult{Name: c.Name(), Severity: doctor.SeverityAdvisory}
	if strings.TrimSpace(cityPath) == "" {
		result.Status = doctor.StatusWarning
		result.Message = "stale worktree marker visibility unknown: city path is empty"
		return result
	}

	markers, err := staleWorktreeMarkers(cityPath)
	if err != nil {
		result.Status = doctor.StatusWarning
		result.Message = fmt.Sprintf("stale worktree marker visibility unknown: %v", err)
		return result
	}
	if len(markers) == 0 {
		result.Status = doctor.StatusOK
		result.Message = "no stale worktree markers block agent slots"
		return result
	}

	slots := make([]string, 0, len(markers))
	for _, marker := range markers {
		slots = append(slots, marker.slot)
	}
	result.Status = doctor.StatusWarning
	result.Message = fmt.Sprintf("%d stale worktree marker(s) block agent slot(s): %s", len(markers), strings.Join(slots, ", "))
	result.Details = make([]string, 0, len(markers))
	for _, marker := range markers {
		result.Details = append(result.Details, marker.path)
	}
	result.FixHint = "inspect the named worktree; the controller clears markers only after its fail-closed recovery checks prove the worktree is resolved"
	return result
}

type staleWorktreeMarker struct {
	slot string
	path string
}

func staleWorktreeMarkers(cityPath string) ([]staleWorktreeMarker, error) {
	root := filepath.Join(cityPath, ".gc", "worktrees")
	rigs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var markers []staleWorktreeMarker
	for _, rig := range rigs {
		if !rig.IsDir() {
			continue
		}
		rigPath := filepath.Join(root, rig.Name())
		slots, err := os.ReadDir(rigPath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rigPath, err)
		}
		for _, slot := range slots {
			if !slot.IsDir() {
				continue
			}
			markerPath := filepath.Join(rigPath, slot.Name(), worktreeStaleFileName)
			if _, err := os.Lstat(markerPath); err == nil {
				markers = append(markers, staleWorktreeMarker{
					slot: filepath.Join(rig.Name(), slot.Name()),
					path: markerPath,
				})
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("checking %s: %w", markerPath, err)
			}
		}
	}
	sort.Slice(markers, func(i, j int) bool { return markers[i].slot < markers[j].slot })
	return markers, nil
}
