package main

import (
	"sync"

	"github.com/gastownhall/gascity/internal/builtinpacks"
)

// Full synthetic-cache validation is owed once per (cache dir, commit) per
// process on the builtin-readiness path.
//
// The readiness pass runs during root command construction, before gc knows
// which subcommand it is running, and its two predicates
// (requiredBuiltinSourcesUsable, lockedBundledImportsUsable) name overlapping
// source sets that resolve to the same cache directory. Unmemoized that is
// four whole-tree reads per config load, and a command loads config several
// times: measured on dbz1 against the city at /home/willie/projects/city,
// `gc version` opened 8,764 paths under ~/.gc/cache/repos and `gc prime`
// 25,474, for 1,481 distinct paths (bead ci-4iozy). With the memo the same
// two commands open 2,670 and 4,140, and what is left is pack composition
// reading manifests, not revalidation.
//
// Why once per process is the honest boundary. A cache directory's name folds
// both the pin commit and the running binary's embedded-content hash
// (builtinpacks.SyntheticCacheKeyComponent), so the tuple this memo is keyed
// on names immutable content -- a different binary or pin resolves to a
// different directory. The materializer in internal/builtinpacks writes the
// cache marker LAST, so an interrupted or partial materialization leaves no
// valid marker and the marker-only fast check below rejects it on every call.
// What the memo gives up is in-place mutation of a COMPLETED cache being
// noticed mid-process rather than by the next invocation.
//
// Rejected: dropping full validation from this path entirely and calling only
// ValidateSyntheticRepoFast, which its docstring invites. Then no gc
// invocation would ever re-read cached pack content, and drift would sit
// until someone ran `gc doctor` (the packv2-import-state check reaches
// packman.CheckInstalled, which still validates in full). Keeping one full
// pass per process keeps the automatic self-heal on every fresh process,
// which is what the CLI actually is.
//
// Rejected: persisting the result across processes so a warm host validates
// once per boot. A stamp inside the cache directory is itself an unexpected
// file that validateSyntheticRepoFileSet rejects; a stamp outside it is a
// status file that goes stale exactly when the cache is repaired out of band.
//
// Rejected: keeping per-call detection by memoizing against a stat-only
// fingerprint of the tree instead of the tuple. It would keep
// TestEnsureBuiltinRuntimeAssetsRehydratesCorruptedCache green while no
// longer comparing content -- a size-and-mtime-preserving substitution passes
// -- so the test would stop proving what it says.
//
// Absence worth naming: a long-lived process (supervisor, API server) that
// calls EnsureBuiltinRuntimeAssets repeatedly now gets one full validation per
// cache per LIFETIME, not per config load. Its remedies are `gc doctor` and a
// restart. That is the same trade the sibling remote-cache memo in
// internal/config/pack_include.go already makes for gc-managed pinned
// checkouts.
//
// Pinned by TestFullSyntheticValidationRunsAtMostOncePerCachePerProcess and
// TestEnsureBuiltinRuntimeAssetsRehydratesCorruptedCache.

// validateSyntheticRepoFull is the injection seam for the expensive whole-tree
// validation. Production binds it to the real one; the readiness-cost test
// swaps in a counting wrapper so the invariant above is asserted rather than
// merely benchmarked.
var validateSyntheticRepoFull = builtinpacks.ValidateSyntheticRepo

// syntheticRepoFullyValidated records (cache dir, commit) tuples this process
// has already validated in full. Only successes are recorded, so a cache that
// failed is re-checked and a repaired one is picked up immediately.
var syntheticRepoFullyValidated sync.Map

// validateSyntheticRepoForReadiness reports whether a bundled-pack cache is
// usable, doing the whole-tree read at most once per (dir, commit) per
// process. The marker-only check runs every time: it is the change detector
// for an evicted, replaced, or re-pinned cache, and its checks are a strict
// prefix of the full one's, so the error it returns is the error the full
// validation would have returned.
func validateSyntheticRepoForReadiness(dir, commit string) error {
	if err := builtinpacks.ValidateSyntheticRepoFast(dir, commit); err != nil {
		return err
	}
	key := dir + "\x00" + commit
	if _, done := syntheticRepoFullyValidated.Load(key); done {
		return nil
	}
	if err := validateSyntheticRepoFull(dir, commit); err != nil {
		return err
	}
	syntheticRepoFullyValidated.Store(key, struct{}{})
	return nil
}
