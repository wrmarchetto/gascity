package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/convergence"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/molecule"
	"github.com/gastownhall/gascity/internal/pathutil"
)

func runRalphCheck(store beads.Store, bead, subject beads.Bead, attempt int, opts ProcessOptions) (convergence.GateResult, error) {
	if subject.Metadata[beadmeta.OutcomeMetadataKey] == beadmeta.OutcomeFail {
		exitCode := 1
		return convergence.GateResult{
			Outcome:   convergence.GateFail,
			ExitCode:  &exitCode,
			Stderr:    fmt.Sprintf("attempt subject %s already failed", subject.ID),
			Truncated: false,
		}, nil
	}

	checkPath := bead.Metadata[beadmeta.CheckPathMetadataKey]
	if checkPath == "" {
		return convergence.GateResult{}, fmt.Errorf("%s: missing gc.check_path", bead.ID)
	}
	cityPath := opts.CityPath
	if cityPath == "" {
		cityPath = resolveInheritedMetadata(store, bead, beadmeta.CityPathMetadataKey)
	}
	if cityPath == "" {
		return convergence.GateResult{}, fmt.Errorf("%s: missing city path for exec check", bead.ID)
	}
	storePath := opts.StorePath
	if storePath == "" {
		storePath = cityPath
	}

	workDir := resolveInheritedMetadata(store, bead, beadmeta.LegacyWorkDirMetadataKey, beadmeta.WorkDirMetadataKey)
	resolvedWorkDir := ""
	if workDir != "" {
		if filepath.IsAbs(workDir) {
			resolvedWorkDir = filepath.Clean(workDir)
		} else {
			resolvedWorkDir = filepath.Clean(filepath.Join(storePath, workDir))
		}
		// work_dir is inherited from bead metadata. For relative check paths
		// it becomes the script resolution base, so it must remain under an
		// operator-controlled tree. Absolute check paths are validated against
		// trusted roots below; for those, work_dir is only the process cwd.
		if !filepath.IsAbs(checkPath) && !pathutil.PathWithin(cityPath, resolvedWorkDir) && !pathutil.PathWithin(storePath, resolvedWorkDir) {
			return convergence.GateResult{}, fmt.Errorf("%s: work_dir %q escapes both city and store roots", bead.ID, workDir)
		}
	}
	scriptBase := storePath
	if resolvedWorkDir != "" {
		scriptBase = resolvedWorkDir
	}
	// Pass cityPath and scriptBase as distinct envelope/base roles: in
	// gastownhall/gascity#2320 storePath (a rig subtree) was passed as both,
	// causing relative gc.check_path values to be looked up under the rig
	// tree even when the script lives in the city tree.
	trustedAbsRoots := ralphCheckTrustedAbsoluteRoots(cityPath, storePath, opts.FormulaSearchPaths)
	if filepath.IsAbs(checkPath) && !pathWithinAny(checkPath, trustedAbsRoots) {
		return convergence.GateResult{}, fmt.Errorf("%s: absolute gc.check_path %q escapes trusted roots", bead.ID, checkPath)
	}
	scriptPath, err := convergence.ResolveConditionPath(cityPath, scriptBase, checkPath)
	if err != nil && scriptBase != storePath && !filepath.IsAbs(checkPath) && errors.Is(err, fs.ErrNotExist) {
		// Pack-shipped check scripts live in the pack/city tree, not the
		// per-task gc.work_dir worktree, so a relative gc.check_path joined
		// against a work_dir worktree that lacks the pack tree resolves to a
		// nonexistent path (gastownhall/gascity#3008). Fall back to the
		// store/city root — exactly the base used when work_dir is empty, so
		// it introduces no new trusted root and stays subject to
		// ResolveConditionPath's containment checks. Only on a not-exist miss,
		// so a check that does exist under the worktree keeps precedence; the
		// original work_dir error is preserved when the fallback also misses.
		if fallbackPath, fallbackErr := convergence.ResolveConditionPath(cityPath, storePath, checkPath); fallbackErr == nil {
			scriptPath, err = fallbackPath, nil
		}
	}
	if err != nil {
		return convergence.GateResult{}, fmt.Errorf("%s: resolving check path: %w", bead.ID, err)
	}
	if filepath.IsAbs(checkPath) && !pathWithinAny(scriptPath, trustedAbsRoots) {
		return convergence.GateResult{}, fmt.Errorf("%s: resolved gc.check_path %q escapes trusted roots", bead.ID, scriptPath)
	}

	timeout := convergence.DefaultGateTimeout
	// Per-step timeout (from formula step.timeout) applies first as a
	// general override. The check-specific gc.check_timeout (from
	// ralph.check.timeout) takes precedence if also set.
	if raw := bead.Metadata[beadmeta.StepTimeoutMetadataKey]; raw != "" {
		parsed, parseErr := parsePositiveRalphTimeout(bead.ID, beadmeta.StepTimeoutMetadataKey, raw)
		if parseErr != nil {
			return convergence.GateResult{}, parseErr
		}
		timeout = parsed
	}
	if raw := bead.Metadata[beadmeta.CheckTimeoutMetadataKey]; raw != "" {
		parsed, parseErr := parsePositiveRalphTimeout(bead.ID, beadmeta.CheckTimeoutMetadataKey, raw)
		if parseErr != nil {
			return convergence.GateResult{}, parseErr
		}
		timeout = parsed
	}

	conditionBeadID := subject.ID
	pathBead := subject
	if conditionBeadID == "" {
		conditionBeadID = bead.ID
		pathBead = bead
	}
	// gastownhall/gascity#2522: ralph.check scripts read $GC_MOLECULE_DIR and
	// $GC_ARTIFACT_DIR to access the molecule-scoped working storage where
	// the per-attempt agent wrote its verdict. Resolve both from the same
	// bead we expose as GC_BEAD_ID (the subject/attempt, falling back to the
	// control bead) so the per-step artifact dir matches where that agent
	// wrote — using the bead's gc.root_bead_id metadata that
	// molecule.Instantiate stamps onto every member. Best-effort: when the
	// bead is not a molecule member (no root stamped) both stay empty and
	// the env vars are omitted, matching the sling-time GC_ARTIFACT_DIR
	// contract that pack scripts already handle.
	moleculeDir, artifactDir := resolveRalphCheckMoleculePaths(pathBead, cityPath)
	opts.tracef("ralph check-start bead=%s script=%s timeout=%s", bead.ID, scriptPath, timeout)
	result := convergence.RunCondition(context.Background(), scriptPath, convergence.ConditionEnv{
		BeadID:      conditionBeadID,
		Iteration:   attempt,
		CityPath:    cityPath,
		StorePath:   storePath,
		WorkDir:     resolvedWorkDir,
		MoleculeDir: moleculeDir,
		ArtifactDir: artifactDir,
	}, timeout, 0)
	opts.tracef("ralph check-done bead=%s outcome=%s dur=%s", bead.ID, result.Outcome, result.Duration)
	return result, nil
}

func ralphCheckTrustedAbsoluteRoots(cityPath, storePath string, formulaSearchPaths []string) []string {
	roots := make([]string, 0, 2+3*len(formulaSearchPaths))
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		normalized := pathutil.NormalizePathForCompare(root)
		for _, existing := range roots {
			if pathutil.SamePath(existing, normalized) {
				return
			}
		}
		roots = append(roots, normalized)
	}
	add(cityPath)
	add(storePath)
	// Pack-authored checks may live beside a formula layer's formulas/ dir.
	for _, formulaPath := range formulaSearchPaths {
		formulaPath = strings.TrimSpace(formulaPath)
		if formulaPath == "" {
			continue
		}
		clean := filepath.Clean(formulaPath)
		add(clean)
		// formula.winningAssetPath resolves a step's "../assets/..." check path
		// to the layer's sibling assets/ tree, regardless of the layer dir's
		// name, so trust that sibling for every layer — a formula layer need not
		// be named "formulas" (e.g. a custom or absolute formulas_dir).
		add(filepath.Join(filepath.Dir(clean), "assets"))
		if filepath.Base(clean) == "formulas" {
			add(filepath.Dir(clean))
		}
	}
	return roots
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		if pathutil.PathWithin(root, path) {
			return true
		}
	}
	return false
}

// resolveRalphCheckMoleculePaths derives the molecule root directory and the
// per-step artifact directory for a ralph bead. Both paths are derived from
// the bead's gc.root_bead_id metadata (stamped by molecule.Instantiate on
// every formula-scaffolded member). Returns empty strings when the bead is
// not a molecule member, when gc.root_bead_id is path-unsafe, or when the
// artifact dir cannot be created; the caller treats empty as "omit the env
// var", which matches the sling-time GC_ARTIFACT_DIR contract.
func resolveRalphCheckMoleculePaths(bead beads.Bead, cityPath string) (string, string) {
	if strings.TrimSpace(cityPath) == "" {
		return "", ""
	}
	rootID := strings.TrimSpace(bead.Metadata[beadmeta.RootBeadIDMetadataKey])
	if rootID == "" {
		return "", ""
	}
	// Reject a path-traversing/unsafe gc.root_bead_id before joining it so
	// an unsafe root cannot surface a path-escaping GC_MOLECULE_DIR. This
	// mirrors the rejection molecule.EnsureArtifactDir applies to rootID and
	// keeps the omit-on-unsafe contract used by the sling env path.
	if molecule.ValidateMemberID(rootID) != nil {
		return "", ""
	}
	moleculeDir := molecule.Dir(cityPath, rootID)
	artifactDir, err := molecule.EnsureArtifactDir(fsys.OSFS{}, cityPath, rootID, bead.ID)
	if err != nil {
		// rootID is already validated, so EnsureArtifactDir failed either
		// on the per-step bead ID or on mkdir (e.g. permissions). Surface
		// the (safe) molecule root so check scripts that only need
		// GC_MOLECULE_DIR still work; the artifact-dir omission mirrors the
		// sling-time best-effort contract.
		return moleculeDir, ""
	}
	return moleculeDir, artifactDir
}

func parsePositiveRalphTimeout(beadID, key, raw string) (time.Duration, error) {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: parsing %s %q: %w", beadID, key, raw, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s: %s must be positive, got %v", beadID, key, parsed)
	}
	return parsed, nil
}

// gateObservationMetadata renders what a check script did -- exit status, both
// output streams, wall time, truncation flag -- as bead metadata.
//
// gc.outcome is deliberately NOT in this set, and no caller may add it. The
// disposition owns that key: processAttemptControl writes it at close, and a
// gate outcome stamped here would publish "fail" on a control bead that still
// has attempts left. A second writer used to fold gc.outcome into this same
// batch for gc.kind=check beads, where the bead's outcome and the gate's were
// one thing; that kind no longer exists (ci-zg0l), so the omission is now
// unconditional.
//
// gc.exit_code is written as an empty string rather than omitted when the gate
// produced no code (manual mode, or a timeout that killed the script): the
// key's presence is what separates "the check ran and reported no status" from
// "nothing was ever recorded here". Omitting it collapses those two, which is
// the ambiguity that made ci-kki3 cost a root-cause investigation.
func gateObservationMetadata(result convergence.GateResult) map[string]string {
	batch := map[string]string{
		beadmeta.StdoutMetadataKey:     result.Stdout,
		beadmeta.StderrMetadataKey:     result.Stderr,
		beadmeta.DurationMsMetadataKey: strconv.FormatInt(result.Duration.Milliseconds(), 10),
		beadmeta.TruncatedMetadataKey:  strconv.FormatBool(result.Truncated),
	}
	if result.ExitCode != nil {
		batch[beadmeta.ExitCodeMetadataKey] = strconv.Itoa(*result.ExitCode)
	} else {
		batch[beadmeta.ExitCodeMetadataKey] = ""
	}
	return batch
}

// persistGateObservation records what a check observed on the control bead
// without touching that bead's disposition.
//
// The keys stay under gc.stdout / gc.stderr / gc.exit_code rather than moving
// to the gc.check_* prefix that holds the check's CONFIGURATION
// (gc.check_mode, gc.check_path, gc.check_timeout). Sharing one prefix between
// config and results would make clearRetryEphemera's delete list a trap: it
// must scrub a previous attempt's results off a cloned bead while leaving the
// config intact, and prefix-adjacent names invite clearing one with the other.
func persistGateObservation(store beads.Store, beadID string, result convergence.GateResult) error {
	return store.SetMetadataBatch(beadID, gateObservationMetadata(result))
}

func retryPreservedAssigneeWithConfig(bead beads.Bead, cfg *config.City) string {
	if bead.Assignee == "" {
		return ""
	}
	if beadUsesMetadataPoolRouteWithConfig(bead, cfg) {
		return ""
	}
	return bead.Assignee
}

func collectRalphAttemptBeads(store beads.Store, subject beads.Bead) (map[string]beads.Bead, error) {
	if subject.Metadata[beadmeta.KindMetadataKey] != beadmeta.KindScope {
		return map[string]beads.Bead{subject.ID: subject}, nil
	}
	rootID := subject.Metadata[beadmeta.RootBeadIDMetadataKey]
	if rootID == "" {
		return nil, fmt.Errorf("%s: missing gc.root_bead_id", subject.ID)
	}
	all, err := listByWorkflowRoot(store, rootID)
	if err != nil {
		return nil, err
	}
	return collectRalphAttemptBeadsFromBeads(all, subject)
}

func collectRalphAttemptBeadsFromBeads(all []beads.Bead, subject beads.Bead) (map[string]beads.Bead, error) {
	out := map[string]beads.Bead{
		subject.ID: subject,
	}
	if subject.Metadata[beadmeta.KindMetadataKey] != beadmeta.KindScope {
		return out, nil
	}
	scopeRef := subject.Metadata[beadmeta.StepRefMetadataKey]
	if scopeRef == "" {
		scopeRef = subject.ID
	}
	for _, bead := range all {
		if bead.Metadata[beadmeta.DynamicFragmentMetadataKey] == "true" {
			continue
		}
		if matchesRalphRetryScope(bead.Metadata[beadmeta.ScopeRefMetadataKey], scopeRef, subject.ID) {
			out[bead.ID] = bead
		}
	}
	return out, nil
}

func matchesRalphRetryScope(beadScopeRef, scopeRef, subjectID string) bool {
	beadScopeRef = strings.TrimSpace(beadScopeRef)
	if beadScopeRef == "" {
		return false
	}
	if beadScopeRef == scopeRef || beadScopeRef == subjectID {
		return true
	}
	return scopeRef != "" && strings.HasSuffix(scopeRef, "."+beadScopeRef)
}

func copyRetryDeps(store beads.Store, oldID, newID string, mapping map[string]string) error {
	deps, err := store.DepList(oldID, "down")
	if err != nil {
		return err
	}
	for _, dep := range deps {
		if dep.Type != "blocks" && dep.Type != "waits-for" && dep.Type != "conditional-blocks" {
			continue
		}
		targetID := dep.DependsOnID
		if mapped, ok := mapping[targetID]; ok {
			targetID = mapped
		} else {
			target, err := store.Get(dep.DependsOnID)
			if err != nil {
				return err
			}
			if target.Metadata[beadmeta.DynamicFragmentMetadataKey] == "true" {
				continue
			}
		}
		if err := store.DepAdd(newID, targetID, dep.Type); err != nil {
			return fmt.Errorf("copying dep %s->%s (%s): %w", newID, targetID, dep.Type, err)
		}
	}
	return nil
}

func resolveLogicalBeadID(store beads.Store, bead beads.Bead) string {
	if bead.Metadata[beadmeta.LogicalBeadIDMetadataKey] != "" {
		return bead.Metadata[beadmeta.LogicalBeadIDMetadataKey]
	}

	deps, err := store.DepList(bead.ID, "up")
	if err == nil {
		for _, dep := range deps {
			if dep.Type != "blocks" {
				continue
			}
			candidate, getErr := store.Get(dep.IssueID)
			if getErr != nil {
				continue
			}
			switch candidate.Metadata[beadmeta.KindMetadataKey] {
			case "ralph", "retry":
				return candidate.ID
			}
		}
	}
	if rootID := bead.Metadata[beadmeta.RootBeadIDMetadataKey]; rootID != "" {
		// Build candidate refs: scope-check controlled ref first (most specific),
		// then logicalStepRefForAttemptBead (may trim attempt patterns).
		var candidates []string
		if controlledRef := scopeCheckControlledStepRef(bead); controlledRef != "" {
			candidates = append(candidates, controlledRef)
		}
		if logicalRef := logicalStepRefForAttemptBead(bead); logicalRef != "" {
			alreadyHave := false
			for _, c := range candidates {
				if c == logicalRef {
					alreadyHave = true
					break
				}
			}
			if !alreadyHave {
				candidates = append(candidates, logicalRef)
			}
		}
		if len(candidates) > 0 {
			all, listErr := listByWorkflowRoot(store, rootID)
			if listErr == nil {
				for _, ref := range candidates {
					for _, candidate := range all {
						switch candidate.Metadata[beadmeta.KindMetadataKey] {
						case "ralph", "retry":
						default:
							continue
						}
						candidateRef := strings.TrimSpace(candidate.Metadata[beadmeta.StepRefMetadataKey])
						if candidateRef == "" {
							candidateRef = strings.TrimSpace(candidate.Ref)
						}
						if candidateRef == ref {
							return candidate.ID
						}
					}
				}
			}
		}
	}
	return ""
}

func logicalStepRefForAttemptBead(bead beads.Bead) string {
	stepRef := strings.TrimSpace(bead.Metadata[beadmeta.StepRefMetadataKey])
	if stepRef == "" {
		stepRef = strings.TrimSpace(bead.Ref)
	}
	if stepRef == "" {
		return ""
	}
	kind := strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey])
	normalized := stepRef
	if kind == beadmeta.KindScopeCheck && strings.HasSuffix(normalized, "-scope-check") {
		normalized = strings.TrimSuffix(normalized, "-scope-check")
	}
	attempt := strings.TrimSpace(bead.Metadata[beadmeta.AttemptMetadataKey])
	if trimmed, ok := trimAttemptStepRefForKind(normalized, kind, attempt); ok {
		return trimmed
	}
	// For scope-check beads, prefer trimming attempt patterns from the
	// normalized ref (e.g., .eval.1 from a nested retry scope-check) to
	// resolve to the logical retry/ralph step. Fall back to normalized ref
	// for flat scope-checks that don't have attempt patterns.
	if kind == beadmeta.KindScopeCheck && normalized != stepRef {
		if trimmed, ok := trimRightmostAttemptStepRef(normalized); ok {
			return trimmed
		}
		return normalized
	}
	if trimmed, ok := trimRightmostAttemptStepRef(normalized); ok {
		return trimmed
	}
	return ""
}

func scopeCheckControlledStepRef(bead beads.Bead) string {
	if strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey]) != beadmeta.KindScopeCheck {
		return ""
	}
	stepRef := strings.TrimSpace(bead.Metadata[beadmeta.StepRefMetadataKey])
	if stepRef == "" {
		stepRef = strings.TrimSpace(bead.Ref)
	}
	if stepRef == "" || !strings.HasSuffix(stepRef, "-scope-check") {
		return ""
	}
	return strings.TrimSuffix(stepRef, "-scope-check")
}

func trimAttemptStepRefForKind(stepRef, kind, attempt string) (string, bool) {
	if attempt == "" {
		return "", false
	}
	switch kind {
	case "run", "scope", "retry-run":
		return trimAttemptStepRefSuffix(stepRef, ".run."+attempt)
	case "check":
		return trimAttemptStepRefSuffix(stepRef, ".check."+attempt)
	case "retry-eval":
		return trimAttemptStepRefSuffix(stepRef, ".eval."+attempt)
	default:
		return "", false
	}
}

func trimRightmostAttemptStepRef(stepRef string) (string, bool) {
	best := -1
	for _, prefix := range []string{".run.", ".check.", ".eval.", ".iteration.", ".attempt."} {
		if idx := strings.LastIndex(stepRef, prefix); idx > best {
			best = idx
		}
	}
	if best <= 0 {
		return "", false
	}
	return stepRef[:best], true
}

func trimAttemptStepRefSuffix(stepRef, suffix string) (string, bool) {
	if suffix == "" || !strings.HasSuffix(stepRef, suffix) {
		return "", false
	}
	return strings.TrimSuffix(stepRef, suffix), true
}

func resolveInheritedMetadata(store beads.Store, bead beads.Bead, keys ...string) string {
	current := bead
	visited := map[string]struct{}{}
	for {
		for _, key := range keys {
			if value := current.Metadata[key]; value != "" {
				return value
			}
		}
		if parentID := current.ParentID; parentID != "" {
			if _, seen := visited[parentID]; !seen {
				parent, err := store.Get(parentID)
				if err == nil {
					visited[parentID] = struct{}{}
					current = parent
					continue
				}
			}
		}
		rootID := current.Metadata[beadmeta.RootBeadIDMetadataKey]
		if rootID != "" && current.ID != rootID {
			if _, seen := visited[rootID]; !seen {
				parent, err := store.Get(rootID)
				if err == nil {
					visited[rootID] = struct{}{}
					current = parent
					continue
				}
			}
		}
		return ""
	}
}

func cloneMetadata(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	return out
}

func clearRetryEphemera(meta map[string]string) {
	if meta == nil {
		return
	}
	for _, key := range []string{
		beadmeta.OutcomeMetadataKey,
		beadmeta.ExitCodeMetadataKey,
		beadmeta.StdoutMetadataKey,
		beadmeta.StderrMetadataKey,
		beadmeta.OutputJSONMetadataKey,
		beadmeta.DurationMsMetadataKey,
		beadmeta.TruncatedMetadataKey,
		beadmeta.TerminalMetadataKey,
		beadmeta.FailedAttemptMetadataKey,
		beadmeta.FanoutStateMetadataKey,
		beadmeta.SpawnedCountMetadataKey,
		beadmeta.RetryStateMetadataKey,
		beadmeta.NextAttemptMetadataKey,
		beadmeta.PartialRetryMetadataKey,
		beadmeta.FailureClassMetadataKey,
		beadmeta.FailureReasonMetadataKey,
		beadmeta.FinalDispositionMetadataKey,
		beadmeta.ClosedByAttemptMetadataKey,
		beadmeta.LastFailureClassMetadataKey,
		beadmeta.RetrySessionRecycledMetadataKey,
		"review.verdict",
		"design_review.verdict",
		"code_review.verdict",
	} {
		delete(meta, key)
	}
}

func clearSessionAffinityMetadata(meta map[string]string) {
	if meta == nil {
		return
	}
	// Delete (rather than empty-string clear, as cmd/gc does) because this map
	// is handed to store.Create on the cloned attempt, where an absent key is
	// the natural representation of "no affinity".
	for _, key := range beadmeta.SessionAffinityMetadataKeys {
		delete(meta, key)
	}
}

// remappedControlForBeadID returns the new bead ID for a bead-ID-valued
// gc.control_for pointer that referenced a bead re-minted in this retry clone
// (i.e. the old value is a mapping key). It returns "" for step-ref-valued
// pointers and for bead IDs outside the clone set — those keep the value
// produced by rewriteRetryControlFor at clone time. This mirrors the
// gc.logical_bead_id remap so cloned attempt roots point at the cloned
// nested control's NEW bead ID (S38 W6).
func remappedControlForBeadID(mapping map[string]string, raw string) string {
	controlFor := strings.TrimSpace(raw)
	if controlFor == "" {
		return ""
	}
	return mapping[controlFor]
}

func copiedDepsPresent(store beads.Store, oldID, newID string, mapping map[string]string) (bool, error) {
	oldDeps, err := store.DepList(oldID, "down")
	if err != nil {
		return false, err
	}
	newDeps, err := store.DepList(newID, "down")
	if err != nil {
		return false, err
	}
	for _, oldDep := range oldDeps {
		if oldDep.Type != "blocks" && oldDep.Type != "waits-for" && oldDep.Type != "conditional-blocks" {
			continue
		}
		targetID := oldDep.DependsOnID
		if mapped, ok := mapping[targetID]; ok {
			targetID = mapped
		} else {
			target, err := store.Get(oldDep.DependsOnID)
			if err != nil {
				return false, err
			}
			if target.Metadata[beadmeta.DynamicFragmentMetadataKey] == "true" {
				continue
			}
		}
		found := false
		for _, newDep := range newDeps {
			if newDep.Type == oldDep.Type && newDep.DependsOnID == targetID {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func discardPartialRalphRetry(store beads.Store, partial map[string]beads.Bead) error {
	if len(partial) == 0 {
		return nil
	}

	pending := make(map[string]beads.Bead, len(partial))
	for id, bead := range partial {
		pending[id] = bead
	}

	for len(pending) > 0 {
		progress := false
		for _, id := range sortedPendingFragmentIDs(pending) {
			if !canDiscardPartialFragmentBead(store, id, pending) {
				continue
			}
			bead := pending[id]
			if err := detachIncomingDeps(store, id); err != nil {
				return err
			}
			if err := store.SetMetadataBatch(id, map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomeSkipped,
				beadmeta.PartialRetryMetadataKey: "true",
			}); err != nil {
				return err
			}
			if bead.Status != "closed" {
				if err := store.Close(id); err != nil {
					return fmt.Errorf("closing partial retry bead %s: %w", id, err)
				}
			}
			delete(pending, id)
			progress = true
		}
		if progress {
			continue
		}
		return fmt.Errorf("unable to discard partial retry beads: %v", sortedPendingFragmentIDs(pending))
	}

	return nil
}

func rewriteRalphAttemptRef(ref string, oldAttempt, nextAttempt int) string {
	if ref == "" || oldAttempt < 1 || nextAttempt < 1 {
		return ref
	}
	if rewritten, ok := rewriteAttemptSegment(ref, "run", oldAttempt, nextAttempt); ok {
		return rewritten
	}
	if rewritten, ok := rewriteAttemptSegment(ref, "check", oldAttempt, nextAttempt); ok {
		return rewritten
	}
	if rewritten, ok := rewriteAttemptSegment(ref, "iteration", oldAttempt, nextAttempt); ok {
		return rewritten
	}
	return ref
}

func rewriteAttemptSegment(ref, kind string, oldAttempt, nextAttempt int) (string, bool) {
	needle := "." + kind + "." + strconv.Itoa(oldAttempt)
	index := strings.LastIndex(ref, needle)
	if index < 0 {
		return "", false
	}
	end := index + len(needle)
	if end < len(ref) && ref[end] != '.' {
		return "", false
	}
	replacement := "." + kind + "." + strconv.Itoa(nextAttempt)
	return ref[:index] + replacement + ref[end:], true
}

// traceCheckOutputCap bounds stderr/stdout in the ralph check-result trace
// line so a noisy script does not produce an unreadable log entry.
// GateResult already truncates each stream to convergence.MaxOutputBytes
// (4 KiB); this further clips for tracing.
const traceCheckOutputCap = 512

// traceClipString returns s truncated to at most limit bytes, appending an
// ellipsis marker when truncation occurred. Used to keep ralph check-result
// trace lines bounded.
func traceClipString(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...[clipped]"
}

// formatGateExitCode renders a GateResult.ExitCode pointer for tracing.
// Avoids leaking the *int address (the prior trace line emitted %v against
// the pointer, producing `exit=0x...` instead of the numeric exit code).
func formatGateExitCode(code *int) string {
	if code == nil {
		return "<nil>"
	}
	return strconv.Itoa(*code)
}
