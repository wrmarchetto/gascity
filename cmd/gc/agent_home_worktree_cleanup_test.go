package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// errFakeResolver is a sentinel error returned by the fake DefaultBranch
// resolver to exercise the error-fallback path.
var errFakeResolver = errors.New("fake resolver failure")

// fakeAgentWorktreeGit is a configurable fake for the agentWorktreeGitProbe interface.
type fakeAgentWorktreeGit struct {
	isRepo           bool
	currentBranch    string
	currentBranchErr error
	hasUncommitted   bool
	// uncommittedExclusions records the paths the last
	// HasUncommittedWorkExcluding call was asked to ignore.
	uncommittedExclusions []string
	checkoutDetachErr     error
	checkoutDetachRef     string
	defaultBranch         string
	defaultBranchErr      error
}

func (f *fakeAgentWorktreeGit) IsRepo() bool { return f.isRepo }

func (f *fakeAgentWorktreeGit) CurrentBranch() (string, error) {
	return f.currentBranch, f.currentBranchErr
}

// HasUncommittedWorkExcluding records the exclusions and answers from a bool.
// It cannot model the marker-as-its-own-dirt defect -- see
// agent_home_worktree_cleanup_realgit_test.go, which pins that against real
// git -- but it does catch a caller that stops passing the marker name.
func (f *fakeAgentWorktreeGit) HasUncommittedWorkExcluding(paths ...string) bool {
	f.uncommittedExclusions = append([]string(nil), paths...)
	return f.hasUncommitted
}

func (f *fakeAgentWorktreeGit) CheckoutDetach(ref string) error {
	f.checkoutDetachRef = ref
	return f.checkoutDetachErr
}

func (f *fakeAgentWorktreeGit) DefaultBranch() (string, error) {
	return f.defaultBranch, f.defaultBranchErr
}

func setupAgentHomeWorktreeCleanupTest(t *testing.T) (cityPath, builderWTPath string, store beads.Store) {
	t.Helper()
	cityPath = t.TempDir()
	rigWTDir := filepath.Join(cityPath, ".gc", "worktrees", "ga-rig")
	builderWTPath = filepath.Join(rigWTDir, "builder")
	if err := os.MkdirAll(builderWTPath, 0o755); err != nil {
		t.Fatalf("creating builder worktree: %v", err)
	}
	store = beads.NewMemStore()
	return
}

func agentHomeConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
		Agents:    []config.Agent{{Name: "builder", Dir: "ga-rig"}},
	}
}

// TestBeadIDFromBranch_BareID: "ga-frmdxd" → "ga-frmdxd".
func TestBeadIDFromBranch_BareID(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "ga-frmdxd")
	if got != "ga-frmdxd" {
		t.Errorf("got %q, want %q", got, "ga-frmdxd")
	}
}

// TestBeadIDFromBranch_WithAgentPrefix: "builder/ga-frmdxd.3" → "ga-frmdxd.3"
// (child bead IDs are returned as-is; the caller resolves them in the store).
func TestBeadIDFromBranch_WithAgentPrefix(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "builder/ga-frmdxd.3")
	if got != "ga-frmdxd.3" {
		t.Errorf("got %q, want %q", got, "ga-frmdxd.3")
	}
}

// TestBeadIDFromBranch_WithDescriptiveSuffix: "builder/ga-abc123-some-feature" → "ga-abc123".
func TestBeadIDFromBranch_WithDescriptiveSuffix(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "builder/ga-abc123-some-feature")
	if got != "ga-abc123" {
		t.Errorf("got %q, want %q", got, "ga-abc123")
	}
}

// TestBeadIDFromBranch_Detached: "HEAD" → "".
func TestBeadIDFromBranch_Detached(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "HEAD")
	if got != "" {
		t.Errorf("got %q, want empty for HEAD", got)
	}
}

// TestBeadIDFromBranch_NoBeadID: no valid bead ID in branch name → "".
func TestBeadIDFromBranch_NoBeadID(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "main")
	if got != "" {
		t.Errorf("got %q, want empty for non-bead branch", got)
	}
}

// TestBeadIDFromBranch_Empty: "" → "".
func TestBeadIDFromBranch_Empty(t *testing.T) {
	cfg := gaConfig()
	got := beadIDFromBranch(cfg, "")
	if got != "" {
		t.Errorf("got %q, want empty for empty branch", got)
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_SkipsWithoutMarker verifies that
// worktrees without a .worktree-stale marker are left untouched.
func TestCleanupClosedBeadAgentHomeWorktrees_SkipsWithoutMarker(t *testing.T) {
	cityPath, builderWTPath, store := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()

	var fakeGit *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fakeGit = &fakeAgentWorktreeGit{isRepo: true, currentBranch: "HEAD"}
		return fakeGit
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 when no marker present", cleaned)
	}
	// Confirm no marker was created.
	if _, err := os.Stat(filepath.Join(builderWTPath, worktreeStaleFileName)); !os.IsNotExist(err) {
		t.Error("marker appeared unexpectedly")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_SkipsNonSessionHomes verifies that
// non-session-home directories (per-bead worktrees) are not touched.
func TestCleanupClosedBeadAgentHomeWorktrees_SkipsNonSessionHomes(t *testing.T) {
	cityPath := t.TempDir()
	// "ga-abc123" is a per-bead worktree, not a session home.
	perBeadWT := filepath.Join(cityPath, ".gc", "worktrees", "ga-rig", "ga-abc123")
	if err := os.MkdirAll(perBeadWT, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stalePath := filepath.Join(perBeadWT, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\n"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	cfg := agentHomeConfig() // builder is the session home, not ga-abc123
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)

	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		return &fakeAgentWorktreeGit{isRepo: true, currentBranch: "builder/ga-abc123"}
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0: per-bead worktrees must be skipped", cleaned)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("stale marker was removed from per-bead worktree, want untouched")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_CaseA_DetachedHeadRemovesMarker verifies
// that a .worktree-stale marker is removed when the worktree is already detached
// (currentBranch == "HEAD"), regardless of the marker's recorded ahead count.
func TestCleanupClosedBeadAgentHomeWorktrees_CaseA_DetachedHeadRemovesMarker(t *testing.T) {
	cityPath, builderWTPath, store := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-frmdxd.3\nbase=origin/main\nahead=0\nreason=rebase-onto-main-conflicted\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		return &fakeAgentWorktreeGit{isRepo: true, currentBranch: "HEAD"}
	}

	var stderr bytes.Buffer
	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, &stderr)

	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1 when detached HEAD and marker present", cleaned)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale marker not removed for detached HEAD worktree")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_CaseB_ClosedBeadResetsAndRemovesMarker
// verifies that the worktree is reset to detached origin/main and the marker
// is removed when the current branch corresponds to a confirmed-closed bead.
func TestCleanupClosedBeadAgentHomeWorktrees_CaseB_ClosedBeadResetsAndRemovesMarker(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\nbase=origin/main\nahead=3\nreason=rebase-onto-main-conflicted\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{
			isRepo:        true,
			currentBranch: "builder/ga-abc123",
		}
		return fake
	}

	var stderr bytes.Buffer
	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, &stderr)

	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1 when bead closed", cleaned)
	}
	if fake.checkoutDetachRef != "origin/main" {
		t.Errorf("CheckoutDetach(%q), want %q", fake.checkoutDetachRef, "origin/main")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale marker not removed after reset for closed bead")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_TrackedCityBeadBranchResets verifies
// that a marker brought back by an old city-bead branch is resolved by detaching
// the slot to main. The branch's bead lives in the city store, not the rig
// store whose directory contains the agent-home worktree.
func TestCleanupClosedBeadAgentHomeWorktrees_TrackedCityBeadBranchResets(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	cfg.Workspace.Prefix = "ci"
	cityStore := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ci-1p6a", Status: "closed"}}, nil)
	rigStore := beads.NewMemStore()

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=fix/ci-1p6a-cache-applyevent-through-wrappers\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write tracked-branch stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{
			isRepo:        true,
			currentBranch: "fix/ci-1p6a-cache-applyevent-through-wrappers",
		}
		return fake
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, cityStore, map[string]beads.Store{"ga-rig": rigStore}, nil)
	if cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1 for a resolved tracked city-bead branch", cleaned)
	}
	if fake.checkoutDetachRef != "origin/main" {
		t.Errorf("CheckoutDetach(%q), want %q", fake.checkoutDetachRef, "origin/main")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale marker remains after detaching old branch: %v", err)
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_ClosedRigBeadBranchResets verifies
// that an agent home below a rig is recovered when its branch names a closed
// rig-store bead. The city's configured bead prefix does not describe every
// registered rig, so routing this lookup only through the city store leaves a
// resolved rig slot permanently quarantined.
func TestCleanupClosedBeadAgentHomeWorktrees_ClosedRigBeadBranchResets(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	cfg.Workspace.Prefix = "ci"
	cityStore := beads.NewMemStore()
	rigStore := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "az-52m", Status: "closed"}}, nil)

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=docs/az-52m-cold-boot-residency-pre-rebase\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{
			isRepo:        true,
			currentBranch: "docs/az-52m-cold-boot-residency-pre-rebase",
		}
		return fake
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, cityStore, map[string]beads.Store{"ga-rig": rigStore}, nil)
	if cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1 for a closed rig-store branch", cleaned)
	}
	if fake.checkoutDetachRef != "origin/main" {
		t.Errorf("CheckoutDetach(%q), want %q", fake.checkoutDetachRef, "origin/main")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale marker remains after detaching closed rig branch: %v", err)
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_CaseB_OpenBeadSkips verifies that
// the worktree is left untouched when the bead is not closed.
func TestCleanupClosedBeadAgentHomeWorktrees_CaseB_OpenBeadSkips(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "open"}}, nil)

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		return &fakeAgentWorktreeGit{isRepo: true, currentBranch: "builder/ga-abc123"}
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 for open bead", cleaned)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("stale marker removed for open bead, want untouched")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_CaseB_UncommittedWorkSkips verifies
// that a worktree with uncommitted changes is never reset even if the bead is closed.
func TestCleanupClosedBeadAgentHomeWorktrees_CaseB_UncommittedWorkSkips(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{
			isRepo:         true,
			currentBranch:  "builder/ga-abc123",
			hasUncommitted: true,
		}
		return fake
	}

	var stderr bytes.Buffer
	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, &stderr)

	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 when uncommitted work present", cleaned)
	}
	if fake.checkoutDetachRef != "" {
		t.Error("CheckoutDetach was called, want skipped when uncommitted work present")
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("stale marker removed despite uncommitted work, want untouched")
	}
	if !strings.Contains(stderr.String(), "uncommitted") {
		t.Errorf("stderr = %q, want mention of uncommitted work", stderr.String())
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_NilConfig returns 0 gracefully.
func TestCleanupClosedBeadAgentHomeWorktrees_NilConfig(t *testing.T) {
	cleaned := cleanupClosedBeadAgentHomeWorktrees(t.TempDir(), nil, nil, map[string]beads.Store{}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 for nil config", cleaned)
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_EmptyStores returns 0 gracefully.
func TestCleanupClosedBeadAgentHomeWorktrees_EmptyStores(t *testing.T) {
	cfg := agentHomeConfig()
	cleaned := cleanupClosedBeadAgentHomeWorktrees(t.TempDir(), cfg, nil, nil, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 for empty stores", cleaned)
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_DefaultBranch verifies that Case B
// uses the probed default branch for the detach reset ref.
func TestCleanupClosedBeadAgentHomeWorktrees_DefaultBranch(t *testing.T) {
	cases := []struct {
		name             string
		defaultBranch    string
		defaultBranchErr error
		wantRef          string
	}{
		{name: "non-main default branch", defaultBranch: "master", wantRef: "origin/master"},
		{name: "custom default branch", defaultBranch: "develop", wantRef: "origin/develop"},
		{name: "resolver returns empty, fallback to main", defaultBranch: "", wantRef: "origin/main"},
		{name: "resolver error, fallback to main", defaultBranchErr: errFakeResolver, wantRef: "origin/main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
			cfg := agentHomeConfig()
			store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)

			stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
			if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\n"), 0o644); err != nil {
				t.Fatalf("write stale marker: %v", err)
			}

			var fake *fakeAgentWorktreeGit
			orig := newAgentWorktreeGitProbe
			defer func() { newAgentWorktreeGitProbe = orig }()
			newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
				fake = &fakeAgentWorktreeGit{
					isRepo:           true,
					currentBranch:    "builder/ga-abc123",
					defaultBranch:    tc.defaultBranch,
					defaultBranchErr: tc.defaultBranchErr,
				}
				return fake
			}

			cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
			if cleaned != 1 {
				t.Errorf("cleaned = %d, want 1", cleaned)
			}
			if fake.checkoutDetachRef != tc.wantRef {
				t.Errorf("CheckoutDetach(%q), want %q", fake.checkoutDetachRef, tc.wantRef)
			}
		})
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_DetachesToMainNotCurrentBranch is a
// regression test for the origin/HEAD-unset case. The mainline resolver
// (DefaultBranch) must never return the current (closed bead) branch, so the
// reset ref must be origin/main — never origin/<current branch>. This guards
// against reintroducing the registration-time ProbeDefaultBranch resolver,
// which falls back to the current branch when origin/HEAD is unset.
func TestCleanupClosedBeadAgentHomeWorktrees_DetachesToMainNotCurrentBranch(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)

	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{
			isRepo:        true,
			currentBranch: "builder/ga-abc123",
			// DefaultBranch resolves origin/HEAD → origin/main → origin/master
			// → "main"; it never returns the current branch. Simulate the
			// origin/HEAD-unset case where it resolves to "main".
			defaultBranch: "main",
		}
		return fake
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1", cleaned)
	}
	if fake.checkoutDetachRef != "origin/main" {
		t.Errorf("CheckoutDetach(%q), want %q", fake.checkoutDetachRef, "origin/main")
	}
	if fake.checkoutDetachRef == "origin/builder/ga-abc123" {
		t.Error("reset detached to the closed bead branch; must reset to origin/main")
	}
}

// --- pool-slot home scope ---
//
// A numbered pool slot is a runtime-only identity: deepCopyAgent
// (cmd/gc/pool.go) builds it during reconcile and never appends it to
// cfg.Agents. The scan below used to exact-match cfg.Agents names, so every
// slot home was out of scope and NOTHING in the city ever cleared a marker
// from one -- a slot marked once stayed marked for the life of the city, which
// is what turned each false-marker bug into a permanently lost slot rather
// than a transient one (bead ci-ciu63).

// newPoolSlotHome creates a worktree home directory named dirName under a rig,
// carrying a marker, and returns the city path and marker path.
func newPoolSlotHome(t *testing.T, dirName string) (cityPath, stalePath string) {
	t.Helper()
	cityPath = t.TempDir()
	home := filepath.Join(cityPath, ".gc", "worktrees", "ga-rig", dirName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}
	stalePath = filepath.Join(home, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=HEAD\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}
	return cityPath, stalePath
}

// stubDetachedAgentWorktreeGit points the probe factory at a detached-HEAD fake
// for the duration of the test, so every case below exercises Case A.
func stubDetachedAgentWorktreeGit(t *testing.T) {
	t.Helper()
	orig := newAgentWorktreeGitProbe
	t.Cleanup(func() { newAgentWorktreeGitProbe = orig })
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		return &fakeAgentWorktreeGit{isRepo: true, currentBranch: "HEAD"}
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_ClearsFalseMarkerInNumberedPoolSlot is
// the regression this scope widening exists for: Case A is an unconditional
// remove, so a false branch=HEAD marker in "builder-2" should have been cleared
// on the controller's first pass. It never was, because "builder-2" is not a
// name in cfg.Agents. Measured against the live city on 2026-08-12: toolsmith-3
// carried branch=HEAD since 08:09 with the supervisor up and events flowing.
func TestCleanupClosedBeadAgentHomeWorktrees_ClearsFalseMarkerInNumberedPoolSlot(t *testing.T) {
	cityPath, stalePath := newPoolSlotHome(t, "builder-2")
	stubDetachedAgentWorktreeGit(t)
	store := beads.NewMemStore()

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, agentHomeConfig(), store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1 for a numbered pool slot of a configured agent", cleaned)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("false marker not removed from numbered pool slot home")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_ClearsFalseMarkerInNamepoolSlot covers
// the other slot-naming shape. A themed namepool name REPLACES the base name
// rather than suffixing it (poolInstanceName, cmd/gc/build_desired_state.go), so
// "furiosa" shares no prefix with "polecat" and a suffix pattern alone would
// leave every namepool city with the original bug.
func TestCleanupClosedBeadAgentHomeWorktrees_ClearsFalseMarkerInNamepoolSlot(t *testing.T) {
	cityPath, stalePath := newPoolSlotHome(t, "furiosa")
	stubDetachedAgentWorktreeGit(t)
	store := beads.NewMemStore()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
		Agents: []config.Agent{{
			Name:          "polecat",
			Dir:           "ga-rig",
			NamepoolNames: []string{"furiosa", "nux"},
		}},
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1 for a namepool slot of a configured agent", cleaned)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("false marker not removed from namepool slot home")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_SkipsUnrelatedNumericDir bounds the
// widening. "unrelated-2" matches the numeric slot shape but names no
// configured agent, so it must stay out of scope: the pass may only touch
// directories it can attribute to an agent, and a scan that removed markers
// from anything ending in a number would be a different bug with the same
// symptom.
func TestCleanupClosedBeadAgentHomeWorktrees_SkipsUnrelatedNumericDir(t *testing.T) {
	cityPath, stalePath := newPoolSlotHome(t, "unrelated-2")
	stubDetachedAgentWorktreeGit(t)
	store := beads.NewMemStore()

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, agentHomeConfig(), store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 for a directory naming no configured agent", cleaned)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("marker removed from a directory naming no configured agent")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_SkipsSlotOfNonExpandingAgent keeps the
// widening tied to the mechanism that creates slot homes. A named-session agent
// (max_active_sessions=1, no pool controls) never gets a "-N" identity
// synthesized for it -- SupportsExpandedSessionIdentities is false -- so
// "builder-2" beside such an agent is some other directory, not its slot.
func TestCleanupClosedBeadAgentHomeWorktrees_SkipsSlotOfNonExpandingAgent(t *testing.T) {
	cityPath, stalePath := newPoolSlotHome(t, "builder-2")
	stubDetachedAgentWorktreeGit(t)
	store := beads.NewMemStore()
	one := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
		Agents:    []config.Agent{{Name: "builder", Dir: "ga-rig", MaxActiveSessions: &one}},
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0: a named-session agent has no numbered slot homes", cleaned)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("marker removed from a directory that cannot be a slot home")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_SkipsBeadIDShapedDir pins the
// collision the numeric pattern would otherwise open. "ga-123456" is a legal
// bead ID and also matches "<agent>-<digits>" for an agent named "ga", so the
// per-bead worktrees this pass must never touch would become candidates. Those
// belong to the reaper, which applies liveness and orphan-commit gates this
// pass does not have.
func TestCleanupClosedBeadAgentHomeWorktrees_SkipsBeadIDShapedDir(t *testing.T) {
	cityPath, stalePath := newPoolSlotHome(t, "ga-123456")
	stubDetachedAgentWorktreeGit(t)
	store := beads.NewMemStore()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
		Agents:    []config.Agent{{Name: "ga", Dir: "ga-rig"}},
	}

	cleaned := cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0: a bead-ID-shaped directory is the reaper's, not this pass's", cleaned)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Error("marker removed from a per-bead worktree")
	}
}

// TestCleanupClosedBeadAgentHomeWorktrees_ExcludesItsOwnMarkerFromTheGate is the
// fast-suite plumbing pin for Case B's uncommitted gate. The behavior it guards
// -- Case B firing on a worktree whose only dirt is the marker that put it in
// scope -- is pinned against real git in
// agent_home_worktree_cleanup_realgit_test.go; this catches a refactor that
// drops the exclusion argument without needing a real worktree.
func TestCleanupClosedBeadAgentHomeWorktrees_ExcludesItsOwnMarkerFromTheGate(t *testing.T) {
	cityPath, builderWTPath, _ := setupAgentHomeWorktreeCleanupTest(t)
	cfg := agentHomeConfig()
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-abc123", Status: "closed"}}, nil)
	stalePath := filepath.Join(builderWTPath, worktreeStaleFileName)
	if err := os.WriteFile(stalePath, []byte("branch=builder/ga-abc123\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	var fake *fakeAgentWorktreeGit
	orig := newAgentWorktreeGitProbe
	defer func() { newAgentWorktreeGitProbe = orig }()
	newAgentWorktreeGitProbe = func(_ string) agentWorktreeGitProbe {
		fake = &fakeAgentWorktreeGit{isRepo: true, currentBranch: "builder/ga-abc123", defaultBranch: "main"}
		return fake
	}

	cleanupClosedBeadAgentHomeWorktrees(cityPath, cfg, store, map[string]beads.Store{"ga-rig": store}, nil)

	if len(fake.uncommittedExclusions) != 1 || fake.uncommittedExclusions[0] != worktreeStaleFileName {
		t.Errorf("uncommitted probe exclusions = %v, want exactly [%q]", fake.uncommittedExclusions, worktreeStaleFileName)
	}
}
