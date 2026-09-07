package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/doctor"
)

// worktreeStaleCheck reports agent-home worktree markers recording work the
// prune path refused to destroy. The marker still refuses any session pointed
// at that worktree from elsewhere, but since bead ci-4btflb it admits the slot
// whose home it is -- otherwise no actor could ever commit the work, and the
// slot respawned into the refusal on a loop. So this check is the standing
// signal that a tree holds unadjudicated work: an admitted adoption raises no
// operator mail, because no start was refused.
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
		result.Message = "no worktree markers record unadjudicated work"
		return result
	}

	slots := make([]string, 0, len(markers))
	for _, marker := range markers {
		slots = append(slots, marker.slot)
	}
	result.Status = doctor.StatusWarning
	result.Message = fmt.Sprintf("%d worktree marker(s) record unadjudicated work in agent slot(s): %s", len(markers), strings.Join(slots, ", "))
	result.Details = make([]string, 0, len(markers))
	for _, marker := range markers {
		result.Details = append(result.Details, marker.path)
	}
	result.FixHint = "inspect the named worktree: its own slot is admitted and can commit or discard the work, and the controller clears the marker only after its fail-closed recovery checks prove the tree resolved"
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
