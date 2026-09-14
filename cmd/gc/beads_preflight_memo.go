package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

// Process-wide memo for the two expensive beads-preflight probes.
//
// The shape is forced by where the checker is built: every bead-store open
// constructs a fresh contract.PreflightChecker (cmd/gc/main.go and
// cmd/gc/api_state.go both call newBeadsPreflightChecker inline in the
// StoreOpenOptions literal), and internal/beads/factory.go runs Check on
// every open. A memo held on the checker value would therefore never see a
// second hit, so the cache has to outlive the checker and live here.
//
// What it buys, measured on this host 2026-09-14: one `gc doctor --json`
// process opened bead stores 38 times across the 5 registered scopes in a
// 40s window, paying 38 `bd context --json` subprocesses and 38 project-id
// dials for 5 distinct answers.
//
// Editing constraints, both load-bearing:
//   - Only SUCCESSES are memoized. A probe failure is usually transient (the
//     dolt server briefly unreachable) and caching it would pin a whole
//     supervisor process to a degraded verdict until restart.
//   - An entry is invalidated by a fingerprint of the files the probes read,
//     never by a timer. See preflightProbeFingerprint.
//
// TestPreflightProbesRunOncePerScopeWithinAProcess and its siblings in
// beads_preflight_memo_test.go verify both.

// processPreflightMemo is the memo production uses. Tests construct their own
// via newPreflightMemo so they neither share state nor have to reset a global.
var processPreflightMemo = newPreflightMemo()

type preflightMemo struct {
	mu        sync.Mutex
	bdContext map[string]preflightBDContextEntry
	projectID map[string]preflightProjectIDEntry
}

type preflightBDContextEntry struct {
	fingerprint string
	value       contract.PreflightBDContext
}

type preflightProjectIDEntry struct {
	fingerprint string
	value       string
	ok          bool
}

func newPreflightMemo() *preflightMemo {
	return &preflightMemo{
		bdContext: make(map[string]preflightBDContextEntry),
		projectID: make(map[string]preflightProjectIDEntry),
	}
}

// preflightProbeFingerprint identifies the on-disk inputs whose contents
// decide both probe answers: the scope's own .beads/metadata.json (backend,
// endpoint, project id) and the city.toml a scope inherits its endpoint from
// when it declares none. A change to either must re-probe.
//
// A TTL was the rejected alternative. Any interval is simultaneously too long
// for an operator who just edited metadata.json and too short for the
// single-shot CLI process the memo mostly serves, and it would make the
// cache's correctness a function of the wall clock rather than of the files
// the verdict was computed from.
//
// NOT covered, deliberately: a database swapped out from under a live server
// with no local file touched. That is the one case a per-open probe catches
// and this memo does not, and it is already refused where it actually
// matters -- beadslib verifies _project_id at connect and returns
// "PROJECT IDENTITY MISMATCH -- refusing to connect" rather than serving the
// wrong project (github.com/steveyegge/beads internal/storage/dolt/store.go,
// verifyProjectIdentity). The preflight probe is the early, friendlier report
// of a condition that stays fatal downstream either way.
func preflightProbeFingerprint(cityPath, scopeRoot string) string {
	return fmt.Sprintf("%s|%s",
		fileStamp(scopeMetadataJSONPath(scopeRoot)),
		fileStamp(filepath.Join(cityPath, "city.toml")))
}

// fileStamp renders a file's identity as size and modification time. A
// missing file stamps as "-" rather than erroring: absence is itself a stable
// input, and it changes the stamp the moment the file appears.
func fileStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// memoizedBDContext returns the cached bd-context answer for scopeRoot when
// one was computed from the same files, otherwise runs load and caches a
// successful result.
//
// The probe runs OUTSIDE the lock. Holding it across a subprocess spawn and a
// TCP dial would serialize every concurrent store open in the supervisor
// behind one scope's probe. The cost of releasing it is that two goroutines
// racing the same cold scope may both probe -- wasteful once, never wrong,
// since both compute the same answer from the same files.
func (m *preflightMemo) memoizedBDContext(
	cityPath, scopeRoot string,
	load func(string) (contract.PreflightBDContext, error),
) (contract.PreflightBDContext, error) {
	fingerprint := preflightProbeFingerprint(cityPath, scopeRoot)
	m.mu.Lock()
	entry, hit := m.bdContext[scopeRoot]
	m.mu.Unlock()
	if hit && entry.fingerprint == fingerprint {
		return entry.value, nil
	}

	value, err := load(scopeRoot)
	if err != nil {
		return contract.PreflightBDContext{}, err
	}
	m.mu.Lock()
	m.bdContext[scopeRoot] = preflightBDContextEntry{fingerprint: fingerprint, value: value}
	m.mu.Unlock()
	return value, nil
}

// memoizedDatabaseProjectID is memoizedBDContext's sibling for the project-id
// identity probe, which costs a dedicated dolt connection per call.
func (m *preflightMemo) memoizedDatabaseProjectID(
	cityPath, scopeRoot string,
	load func(string) (string, bool, error),
) (string, bool, error) {
	fingerprint := preflightProbeFingerprint(cityPath, scopeRoot)
	m.mu.Lock()
	entry, hit := m.projectID[scopeRoot]
	m.mu.Unlock()
	if hit && entry.fingerprint == fingerprint {
		return entry.value, entry.ok, nil
	}

	value, ok, err := load(scopeRoot)
	if err != nil {
		return "", false, err
	}
	m.mu.Lock()
	m.projectID[scopeRoot] = preflightProjectIDEntry{fingerprint: fingerprint, value: value, ok: ok}
	m.mu.Unlock()
	return value, ok, nil
}
