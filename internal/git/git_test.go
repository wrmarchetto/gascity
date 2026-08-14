package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/testutil"
)

// initTestRepo creates a git repo with one commit in a temp directory.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	return dir
}

// runGit runs a git command in dir and fails the test on error.
// Strips git env vars to prevent interference from pre-commit hooks.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if gitEnvBlacklist[k] {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
}

func TestSanitizeGitEnvStripsGitLocatingVariables(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/poison/.git",
		"GIT_WORK_TREE=/poison",
		"GIT_INDEX_FILE=/poison/.git/index",
		"GIT_OBJECT_DIRECTORY=/poison/.git/objects",
		"HOME=/home/user",
		"GIT_AUTHOR_NAME=keep-me",
	}
	got := sanitizeGitEnv(in)
	for _, e := range got {
		if k, _, _ := strings.Cut(e, "="); gitEnvBlacklist[k] {
			t.Errorf("sanitizeGitEnv kept blacklisted var %q", k)
		}
	}
	want := map[string]bool{"PATH": true, "HOME": true, "GIT_AUTHOR_NAME": true}
	if len(got) != len(want) {
		t.Fatalf("sanitizeGitEnv returned %d vars %v, want %d", len(got), got, len(want))
	}
	for _, e := range got {
		k, _, _ := strings.Cut(e, "=")
		if !want[k] {
			t.Errorf("sanitizeGitEnv dropped expected var %q", k)
		}
	}
}

func TestSanitizedEnvStripsPoisonedProcessEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/poison/.git")
	t.Setenv("GIT_WORK_TREE", "/poison")
	t.Setenv("GIT_INDEX_FILE", "/poison/.git/index")
	for _, e := range SanitizedEnv() {
		switch k, _, _ := strings.Cut(e, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			t.Errorf("SanitizedEnv leaked git-locating var %q", k)
		}
	}
}

func TestHermeticEnvStripsDiscoveryAndConfigVarsAndPinsHermeticConfig(t *testing.T) {
	// SanitizedEnv-covered locating vars plus HermeticEnv-only discovery and
	// config-location vars must all be removed; the hermetic config pins must be
	// appended.
	t.Setenv("GIT_DIR", "/poison/.git")
	t.Setenv("GIT_CEILING_DIRECTORIES", "/poison")
	t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", "1")
	t.Setenv("GIT_NAMESPACE", "poison")
	t.Setenv("GIT_CONFIG_SYSTEM", "/poison/system")
	t.Setenv("GIT_EXEC_PATH", "/poison/exec")
	t.Setenv("GIT_PAGER", "poison-pager")
	t.Setenv("PATH", "/usr/bin")

	got := HermeticEnv()
	stripped := map[string]bool{
		"GIT_DIR": true, "GIT_CEILING_DIRECTORIES": true,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM": true, "GIT_NAMESPACE": true,
		"GIT_CONFIG_SYSTEM": true, "GIT_EXEC_PATH": true, "GIT_PAGER": true,
	}
	seen := map[string]string{}
	for _, e := range got {
		k, v, _ := strings.Cut(e, "=")
		if stripped[k] {
			t.Errorf("HermeticEnv leaked %q", k)
		}
		seen[k] = v
	}
	if seen["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Errorf("HermeticEnv GIT_CONFIG_NOSYSTEM = %q, want 1", seen["GIT_CONFIG_NOSYSTEM"])
	}
	if seen["GIT_CONFIG_GLOBAL"] != "/dev/null" {
		t.Errorf("HermeticEnv GIT_CONFIG_GLOBAL = %q, want /dev/null", seen["GIT_CONFIG_GLOBAL"])
	}
	if _, ok := seen["PATH"]; !ok {
		t.Error("HermeticEnv dropped non-git var PATH")
	}
}

func TestIsRepo(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	if !g.IsRepo() {
		t.Error("IsRepo() = false, want true")
	}

	notRepo := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notRepo))
	g2 := New(notRepo)
	if g2.IsRepo() {
		t.Error("IsRepo() = true for non-repo, want false")
	}
}

func TestCurrentBranch(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	// Default branch is typically "master" or "main" depending on git config.
	if branch == "" {
		t.Error("CurrentBranch returned empty string")
	}
}

func TestDefaultBranch_NoRemote(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	branch, err := g.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch() = %q, want %q (fallback)", branch, "main")
	}
}

// TestDefaultBranch_FromOriginHEAD exercises the symref parsing path and
// verifies that branch names containing slashes round-trip correctly.
// Regression test for the bug where strings.LastIndex(ref, "/") truncated
// "refs/remotes/origin/user/feature" to "feature".
func TestDefaultBranch_FromOriginHEAD(t *testing.T) {
	tests := []struct {
		name   string
		branch string
	}{
		{"plain branch", "main"},
		{"single slash", "boylec/develop"},
		{"nested slashes", "team/feature/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a bare remote and a clone that tracks it, so
			// refs/remotes/origin/HEAD can be wired to the target ref.
			bare := t.TempDir()
			runGit(t, bare, "init", "--bare")

			clone := t.TempDir()
			runGit(t, clone, "clone", bare, ".")
			runGit(t, clone, "config", "user.email", "test@test.com")
			runGit(t, clone, "config", "user.name", "Test")
			runGit(t, clone, "commit", "--allow-empty", "-m", "init")

			// Create the target ref under refs/remotes/origin/ and point
			// origin/HEAD at it. symbolic-ref is permissive about its
			// target so we don't need to push the branch first.
			target := "refs/remotes/origin/" + tt.branch
			runGit(t, clone, "update-ref", target, "HEAD")
			runGit(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", target)

			g := New(clone)
			got, err := g.DefaultBranch()
			if err != nil {
				t.Fatalf("DefaultBranch: %v", err)
			}
			if got != tt.branch {
				t.Errorf("DefaultBranch() = %q, want %q", got, tt.branch)
			}
		})
	}
}

// TestDefaultBranch_OriginHEADUnsetWithMasterRef covers the master-default
// rig case from gc-8cowk: a clone where refs/remotes/origin/HEAD is not set
// but refs/remotes/origin/master exists. The hardcoded "main" fallback
// strands polecats on master-default rigs (added before PR#1554) with
// metadata.target=main, causing refinery rejection loops.
func TestDefaultBranch_OriginHEADUnsetWithMasterRef(t *testing.T) {
	tests := []struct {
		name        string
		remoteRefs  []string
		wantBranch  string
		description string
	}{
		{
			name:        "origin/master only",
			remoteRefs:  []string{"master"},
			wantBranch:  "master",
			description: "master-default rig: must detect master without origin/HEAD",
		},
		{
			name:        "origin/main only",
			remoteRefs:  []string{"main"},
			wantBranch:  "main",
			description: "main-default rig with unset origin/HEAD must still detect main",
		},
		{
			name:        "both main and master",
			remoteRefs:  []string{"main", "master"},
			wantBranch:  "main",
			description: "when ambiguous, prefer main (matches the hardcoded historical default)",
		},
		{
			name:        "neither candidate exists",
			remoteRefs:  []string{"develop"},
			wantBranch:  "main",
			description: "last-resort fallback remains main when no known candidate is on origin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bare := t.TempDir()
			runGit(t, bare, "init", "--bare")

			clone := t.TempDir()
			runGit(t, clone, "clone", bare, ".")
			runGit(t, clone, "config", "user.email", "test@test.com")
			runGit(t, clone, "config", "user.name", "Test")
			runGit(t, clone, "commit", "--allow-empty", "-m", "init")

			// Populate refs/remotes/origin/<name> for each requested ref
			// but DO NOT wire refs/remotes/origin/HEAD. This mirrors the
			// state of rig clones added before gc rig add auto-detected
			// the default branch.
			for _, ref := range tt.remoteRefs {
				runGit(t, clone, "update-ref", "refs/remotes/origin/"+ref, "HEAD")
			}
			// Defensive: ensure no origin/HEAD symref lingers from clone.
			_ = exec.Command("git", "-C", clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD").Run()

			g := New(clone)
			got, err := g.DefaultBranch()
			if err != nil {
				t.Fatalf("DefaultBranch: %v", err)
			}
			if got != tt.wantBranch {
				t.Errorf("%s: DefaultBranch() = %q, want %q",
					tt.description, got, tt.wantBranch)
			}
		})
	}
}

func TestProbeDefaultBranch_FromOriginHEAD(t *testing.T) {
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")

	target := "refs/remotes/origin/master"
	runGit(t, clone, "update-ref", target, "HEAD")
	runGit(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", target)

	g := New(clone)
	got, src := g.ProbeDefaultBranch()
	if got != "master" {
		t.Errorf("ProbeDefaultBranch() = %q, want %q", got, "master")
	}
	if src != DefaultBranchFromOriginHEAD {
		t.Errorf("source = %q, want %q", src, DefaultBranchFromOriginHEAD)
	}
}

// cloneWithUnsetOriginHEAD builds the ci-6m97 fixture: a bare repo whose HEAD
// names mainline, and a clone of it whose refs/remotes/origin/HEAD has been
// deleted and whose working tree is parked on a feature branch. That is the
// normal state of a shared multi-agent checkout -- clone wrote origin/HEAD, a
// later `git remote set-head --delete` or a clone made before git 2.28 left it
// unset, and some session's branch is checked out.
//
// mainline is a parameter, and callers pass something that is neither "main"
// nor "master", so a candidate-ref guess cannot pass a test that means to
// prove the answer came from the remote.
func cloneWithUnsetOriginHEAD(t *testing.T, mainline string) string {
	t.Helper()
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seed := t.TempDir()
	runGit(t, seed, "clone", bare, ".")
	runGit(t, seed, "config", "user.email", "test@test.com")
	runGit(t, seed, "config", "user.name", "Test")
	runGit(t, seed, "checkout", "-b", mainline)
	runGit(t, seed, "commit", "--allow-empty", "-m", "init")
	runGit(t, seed, "push", "origin", mainline)
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+mainline)

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	// The clone wired origin/HEAD from the remote; drop it so the probe has to
	// go ask. Not defensive -- without this the test would pass on step 1 and
	// never reach the ls-remote leg it exists to pin.
	runGit(t, clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	runGit(t, clone, "checkout", "-b", "fix/ci-yxpd-stop-gate")
	return clone
}

// TestProbeDefaultBranch_PrefersRemoteHEADOverCheckedOutBranch pins ci-6m97:
// registering a rig from a shared checkout that happens to sit on a feature
// branch must not record that branch as the rig's mainline. The remote answers
// authoritatively via `git ls-remote --symref origin HEAD` even when the local
// refs/remotes/origin/HEAD was never set, so the checked-out-branch guess must
// not be reached while that answer is available.
//
// default_branch is what polecats and the refinery target, so the failure mode
// this guards is work slung at a branch that gets deleted.
func TestProbeDefaultBranch_PrefersRemoteHEADOverCheckedOutBranch(t *testing.T) {
	clone := cloneWithUnsetOriginHEAD(t, "trunk")
	g := New(clone)
	got, src := g.ProbeDefaultBranch()
	if got != "trunk" {
		t.Errorf("ProbeDefaultBranch() = %q, want %q (remote HEAD, not the checked-out branch)", got, "trunk")
	}
	// The branch assertion alone cannot distinguish "the remote answered" from
	// "some later leg happened to guess trunk", and the source is what drives
	// the rig-add warning, so both are pinned.
	if src != DefaultBranchFromRemoteHEAD {
		t.Errorf("source = %q, want %q", src, DefaultBranchFromRemoteHEAD)
	}
}

// TestProbeDefaultBranch_RejectsRemoteHEADOutsideRefsHeads pins the guard in
// parseRemoteHeadSymref. git accepts `symbolic-ref HEAD refs/foo/bar` in a bare
// repo, and ls-remote reports it verbatim, so a parser that trimmed to the last
// path component would record "bar" as the rig's default_branch -- a ref no
// merge could ever target. The probe must decline and fall through to the
// checked-out branch, which at least exists as a branch.
func TestProbeDefaultBranch_RejectsRemoteHEADOutsideRefsHeads(t *testing.T) {
	clone := cloneWithUnsetOriginHEAD(t, "trunk")
	remote, err := New(clone).run("remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("remote get-url: %v", err)
	}
	bare := strings.TrimSpace(remote)
	runGit(t, bare, "update-ref", "refs/foo/bar", "refs/heads/trunk")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/foo/bar")

	got, src := New(clone).ProbeDefaultBranch()
	if got != "fix/ci-yxpd-stop-gate" || src != DefaultBranchFromCheckedOut {
		t.Errorf("ProbeDefaultBranch() = (%q, %q), want (%q, %q)",
			got, src, "fix/ci-yxpd-stop-gate", DefaultBranchFromCheckedOut)
	}
}

// TestProbeDefaultBranch_FallsBackWhenRemoteUnreachable covers the offline
// case, which is the only one where the checked-out-branch guess still gets
// recorded. It must be reported as a guess so gc rig add can say so; a probe
// that returned the branch with an authoritative source would silently
// reintroduce ci-6m97 for anyone adding a rig without network.
func TestProbeDefaultBranch_FallsBackWhenRemoteUnreachable(t *testing.T) {
	repo := initTestRepo(t)
	runGit(t, repo, "checkout", "-b", "develop")
	runGit(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "absent.git"))

	got, src := New(repo).ProbeDefaultBranch()
	if got != "develop" || src != DefaultBranchFromCheckedOut {
		t.Errorf("ProbeDefaultBranch() = (%q, %q), want (%q, %q)",
			got, src, "develop", DefaultBranchFromCheckedOut)
	}
}

func TestProbeDefaultBranch_FallsBackToCurrentBranch(t *testing.T) {
	repo := initTestRepo(t)
	// Force a known branch name; the test repo's default may be "main"
	// or "master" depending on the host's git init.defaultBranch.
	runGit(t, repo, "checkout", "-b", "develop")
	g := New(repo)
	got, src := g.ProbeDefaultBranch()
	if got != "develop" {
		t.Errorf("ProbeDefaultBranch() = %q, want %q (current branch fallback)", got, "develop")
	}
	if src != DefaultBranchFromCheckedOut {
		t.Errorf("source = %q, want %q", src, DefaultBranchFromCheckedOut)
	}
}

func TestProbeDefaultBranch_NoRepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	g := New(dir)
	got, src := g.ProbeDefaultBranch()
	if got != "" {
		t.Errorf("ProbeDefaultBranch() = %q, want empty (no repo)", got)
	}
	if src != DefaultBranchUnresolved {
		t.Errorf("source = %q, want %q", src, DefaultBranchUnresolved)
	}
}

// TestParseRemoteHeadSymref covers the output shapes the network leg cannot
// reach from a local fixture: the unborn-HEAD empty response, and a symref line
// for a ref other than HEAD (git prints one per requested pattern, so a future
// caller adding patterns must not pick up the wrong one).
func TestParseRemoteHeadSymref(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "symref then sha",
			out:  "ref: refs/heads/main\tHEAD\n9187121b3\tHEAD\n",
			want: "main",
		},
		{
			name: "empty bare repo answers with no symref line",
			out:  "",
			want: "",
		},
		{
			name: "sha only, origin/HEAD unset on the remote too",
			out:  "9187121b3\tHEAD\n",
			want: "",
		},
		{
			name: "symref for a ref other than HEAD is not the answer",
			out:  "ref: refs/heads/main\trefs/remotes/upstream/HEAD\n",
			want: "",
		},
		{
			name: "branch name containing a slash survives intact",
			out:  "ref: refs/heads/release/2.1\tHEAD\n",
			want: "release/2.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRemoteHeadSymref(tt.out); got != tt.want {
				t.Errorf("parseRemoteHeadSymref(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestWorktreeRemove(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)

	wtPath := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "to-remove", wtPath)

	if err := g.WorktreeRemove(wtPath, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	// Directory should be gone.
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after remove")
	}
}

func TestWorktreeRemoveForce(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)

	wtPath := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "dirty-wt", wtPath)

	// Create an uncommitted file to make the worktree dirty.
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Force remove should succeed even with dirty worktree.
	if err := g.WorktreeRemove(wtPath, true); err != nil {
		t.Fatalf("WorktreeRemove(force): %v", err)
	}
}

func TestWorktreeList(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)

	wtPath := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "listed", wtPath)

	worktrees, err := g.WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}

	// Should have at least 2: the main repo and the worktree.
	if len(worktrees) < 2 {
		t.Fatalf("len(worktrees) = %d, want >= 2", len(worktrees))
	}

	// Find our worktree.
	var found bool
	for _, wt := range worktrees {
		if testutil.CanonicalPath(wt.Path) == testutil.CanonicalPath(wtPath) {
			found = true
			if wt.Branch != "listed" {
				t.Errorf("worktree branch = %q, want %q", wt.Branch, "listed")
			}
		}
	}
	if !found {
		t.Errorf("worktree at %q not found in list", wtPath)
	}
}

// TestWorktreeList_NestedSiblings verifies the algorithmic assumption used
// by NestedWorktreePruneCheck: when worktree B is created at a path that
// lies inside worktree A's working tree, git treats them as siblings in
// the same admin dir. WorktreeList() from any of A, B, or the main repo
// returns all three entries with each entry's true on-disk path.
//
// This is the foundation for "find nested worktrees" — we walk per-agent
// homes, list siblings, and filter by path containment to identify nested
// entries.
func TestWorktreeList_NestedSiblings(t *testing.T) {
	repo := initTestRepo(t)

	// Outer worktree (the "agent home").
	home := filepath.Join(t.TempDir(), "home")
	runGit(t, repo, "worktree", "add", "-b", "home-branch", home)

	// Nested worktree, path lies inside `home`. Equivalent to the polecat
	// "$(pwd)/worktrees/<issue>" pattern from mol-polecat-work.toml.
	nested := filepath.Join(home, "worktrees", "task-x")
	runGit(t, home, "worktree", "add", "-b", "task-x-branch", nested)

	// Listing from the home worktree returns all three siblings.
	gHome := New(home)
	wts, err := gHome.WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList from home: %v", err)
	}
	gotPaths := make(map[string]string)
	for _, wt := range wts {
		gotPaths[testutil.CanonicalPath(wt.Path)] = wt.Branch
	}

	wantHome := testutil.CanonicalPath(home)
	wantNested := testutil.CanonicalPath(nested)
	wantRepo := testutil.CanonicalPath(repo)

	if _, ok := gotPaths[wantHome]; !ok {
		t.Errorf("home worktree %q missing from list; got %v", wantHome, gotPaths)
	}
	if br := gotPaths[wantNested]; br != "task-x-branch" {
		t.Errorf("nested worktree branch = %q (path %q), want task-x-branch; full list: %v",
			br, wantNested, gotPaths)
	}
	if _, ok := gotPaths[wantRepo]; !ok {
		t.Errorf("main repo %q missing from list; got %v", wantRepo, gotPaths)
	}

	// Listing from inside the nested worktree must produce the same set.
	gNested := New(nested)
	wts2, err := gNested.WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList from nested: %v", err)
	}
	if len(wts2) != len(wts) {
		t.Errorf("WorktreeList from nested returned %d entries; from home returned %d (must match)",
			len(wts2), len(wts))
	}

	// Path containment is the discriminator the doctor check uses to
	// classify "nested" vs "agent home" vs "main repo". Verify it works
	// on canonical paths.
	if !strings.HasPrefix(wantNested+string(filepath.Separator), wantHome+string(filepath.Separator)) {
		t.Errorf("nested path %q is not a strict subpath of home %q", wantNested, wantHome)
	}
	if strings.HasPrefix(wantHome+string(filepath.Separator), wantNested+string(filepath.Separator)) {
		t.Errorf("home %q must not be classified as inside nested %q", wantHome, wantNested)
	}
}

func TestHasUncommittedWork_Clean(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	if g.HasUncommittedWork() {
		t.Error("HasUncommittedWork() = true for clean repo, want false")
	}
}

func TestHasUncommittedWork_Dirty(t *testing.T) {
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(repo)
	if !g.HasUncommittedWork() {
		t.Error("HasUncommittedWork() = false for dirty repo, want true")
	}
}

func TestHasUnpushedCommits_NoneWhenClean(t *testing.T) {
	// Create a bare remote and clone it so there's a tracking branch.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")
	runGit(t, clone, "push", "origin", "HEAD")

	g := New(clone)
	if g.HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = true for fully-pushed repo, want false")
	}
}

func TestHasUnpushedCommits_DetectsLocal(t *testing.T) {
	// Create a bare remote and clone it.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")
	runGit(t, clone, "push", "origin", "HEAD")

	// Create a worktree with a local-only commit.
	wtPath := filepath.Join(t.TempDir(), "wt")
	runGit(t, clone, "worktree", "add", "-b", "feature", wtPath)
	runGit(t, wtPath, "config", "user.email", "test@test.com")
	runGit(t, wtPath, "config", "user.name", "Test")
	runGit(t, wtPath, "commit", "--allow-empty", "-m", "local work")

	g := New(wtPath)
	if !g.HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = false for worktree with local commit, want true")
	}
}

func TestHasUnpushedCommits_NoRemote(t *testing.T) {
	// A repo with no remote has no remote branches → all commits are "unpushed".
	repo := initTestRepo(t)
	g := New(repo)
	if !g.HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = false for repo with no remote, want true")
	}
}

func TestHasUnpushedCommitsResult_ReturnsProbeError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	g := New(dir)
	if _, err := g.HasUnpushedCommitsResult(); err == nil {
		t.Fatal("HasUnpushedCommitsResult() error = nil, want probe error")
	}
	if !g.HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() should fail closed on probe errors")
	}
}

func TestHasStashes_NoneWhenClean(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	if g.HasStashes() {
		t.Error("HasStashes() = true for clean repo, want false")
	}
}

func TestHasStashes_DetectsStash(t *testing.T) {
	repo := initTestRepo(t)
	// Create a file and stash it.
	if err := os.WriteFile(filepath.Join(repo, "stash-me.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "stash-me.txt")
	runGit(t, repo, "stash")

	g := New(repo)
	if !g.HasStashes() {
		t.Error("HasStashes() = false for repo with stash, want true")
	}
}

func TestHasStashesResult_ReturnsProbeError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	g := New(dir)
	if _, err := g.HasStashesResult(); err == nil {
		t.Fatal("HasStashesResult() error = nil, want probe error")
	}
	if !g.HasStashes() {
		t.Error("HasStashes() should fail closed on probe errors")
	}
}

func TestFetch(t *testing.T) {
	// Create a bare remote and clone it.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")
	runGit(t, clone, "push", "origin", "HEAD")

	g := New(clone)
	if err := g.Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestFetch_NoRemote(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	if err := g.Fetch(); err == nil {
		t.Error("expected error fetching repo with no remote")
	}
}

func TestStashAndPop(t *testing.T) {
	repo := initTestRepo(t)

	// Create a dirty file.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := New(repo)
	if !g.HasUncommittedWork() {
		t.Fatal("expected dirty repo")
	}

	// Stash the changes.
	if err := g.Stash("test-stash"); err != nil {
		t.Fatalf("Stash: %v", err)
	}
	if g.HasUncommittedWork() {
		t.Error("repo still dirty after stash")
	}
	if !g.HasStashes() {
		t.Error("expected stash after Stash()")
	}

	// Pop the stash.
	if err := g.StashPop(); err != nil {
		t.Fatalf("StashPop: %v", err)
	}
	if !g.HasUncommittedWork() {
		t.Error("repo should be dirty after stash pop")
	}
}

func TestStash_CleanRepo(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	// Stashing a clean repo: behavior varies by git version.
	// Some return exit 1 ("No local changes to save"), some return 0.
	// Just verify it doesn't create a stash entry.
	_ = g.Stash("empty")
	// A clean repo should have no stash entries regardless.
	if g.HasStashes() {
		t.Error("clean repo should have no stashes after stash attempt")
	}
}

func TestStashPop_NoStash(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	if err := g.StashPop(); err == nil {
		t.Error("expected error popping empty stash")
	}
}

func TestPullRebase(t *testing.T) {
	// Create a bare remote and clone it.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")
	runGit(t, clone, "push", "origin", "HEAD")

	// Make an upstream change.
	clone2 := t.TempDir()
	runGit(t, clone2, "clone", bare, ".")
	runGit(t, clone2, "config", "user.email", "test@test.com")
	runGit(t, clone2, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone2, "upstream.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone2, "add", "upstream.txt")
	runGit(t, clone2, "commit", "-m", "upstream change")
	runGit(t, clone2, "push", "origin", "HEAD")

	// Fetch and pull --rebase in original clone.
	g := New(clone)
	if err := g.Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Get the current branch name.
	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	if err := g.PullRebase("origin", branch); err != nil {
		t.Fatalf("PullRebase: %v", err)
	}

	// Verify the upstream file now exists.
	if _, err := os.Stat(filepath.Join(clone, "upstream.txt")); err != nil {
		t.Errorf("upstream.txt not found after pull --rebase: %v", err)
	}
}

func TestWorktreePrune(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)

	// Prune on a clean repo should not fail.
	if err := g.WorktreePrune(); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}
}

func TestParseWorktreeList(t *testing.T) {
	output := `worktree /home/user/repo
HEAD abc123
branch refs/heads/main

worktree /home/user/repo-wt
HEAD def456
branch refs/heads/feature-1

`
	wts := parseWorktreeList(output)
	if len(wts) != 2 {
		t.Fatalf("len(worktrees) = %d, want 2", len(wts))
	}
	if wts[0].Path != "/home/user/repo" {
		t.Errorf("wts[0].Path = %q, want %q", wts[0].Path, "/home/user/repo")
	}
	if wts[0].Branch != "main" {
		t.Errorf("wts[0].Branch = %q, want %q", wts[0].Branch, "main")
	}
	if wts[1].Path != "/home/user/repo-wt" {
		t.Errorf("wts[1].Path = %q, want %q", wts[1].Path, "/home/user/repo-wt")
	}
	if wts[1].Branch != "feature-1" {
		t.Errorf("wts[1].Branch = %q, want %q", wts[1].Branch, "feature-1")
	}
	if wts[1].Head != "def456" {
		t.Errorf("wts[1].Head = %q, want %q", wts[1].Head, "def456")
	}
}

func TestParseWorktreeList_Empty(t *testing.T) {
	wts := parseWorktreeList("")
	if len(wts) != 0 {
		t.Errorf("len(worktrees) = %d, want 0", len(wts))
	}
}

// TestUntrustedRemoteGitConfigArgs pins the hardening applied to git invocations
// whose remote URL is attacker-influenced (the pack-import add path). Redirect
// following must be disabled and the transport allowlist constrained, so a
// fenced public host cannot 30x to an internal target and a crafted URL cannot
// escalate to a dangerous transport such as ext::.
func TestUntrustedRemoteGitConfigArgs(t *testing.T) {
	args := UntrustedRemoteGitConfigArgs()

	// Every override is passed as a leading "-c key=value" pair.
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c http.followRedirects=false",
		"-c protocol.allow=never",
		"-c protocol.https.allow=always",
		"-c protocol.http.allow=always",
		"-c protocol.ssh.allow=always",
		"-c protocol.git.allow=always",
		"-c protocol.file.allow=always",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("UntrustedRemoteGitConfigArgs missing %q; got %v", want, args)
		}
	}

	// The args must be well-formed -c pairs so they can be prepended before a git
	// subcommand.
	if len(args)%2 != 0 {
		t.Fatalf("expected an even number of args (-c pairs), got %d: %v", len(args), args)
	}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatalf("arg %d = %q, want -c; full: %v", i, args[i], args)
		}
	}
}

// --- HasUncommittedWorkExcluding ---
//
// The exclusion has to cover both states a gc-written bookkeeping file can be
// in. Untracked is the normal one; TRACKED is what bead ci-2uh5p was, when a
// marker got committed onto a branch and every worktree that checked that
// branch out inherited it.

func TestHasUncommittedWorkExcluding_UntrackedExcludedPathIsNotWork(t *testing.T) {
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".worktree-stale"), []byte("branch=HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(repo)
	// State the premise: the unfiltered probe must see it, or the exclusion
	// below is being credited for a repo that was clean anyway.
	if !g.HasUncommittedWork() {
		t.Fatal("HasUncommittedWork() = false with an untracked file present; fixture is wrong")
	}
	if g.HasUncommittedWorkExcluding(".worktree-stale") {
		t.Error("HasUncommittedWorkExcluding() = true when the only change is the excluded path")
	}
}

func TestHasUncommittedWorkExcluding_TrackedModifiedExcludedPathIsNotWork(t *testing.T) {
	repo := initTestRepo(t)
	marker := filepath.Join(repo, ".worktree-stale")
	if err := os.WriteFile(marker, []byte("branch=HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".worktree-stale")
	runGit(t, repo, "commit", "-m", "marker committed by mistake")
	if err := os.WriteFile(marker, []byte("branch=HEAD\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(repo)
	if !g.HasUncommittedWork() {
		t.Fatal("HasUncommittedWork() = false with a modified tracked file; fixture is wrong")
	}
	if g.HasUncommittedWorkExcluding(".worktree-stale") {
		t.Error("HasUncommittedWorkExcluding() = true for a modified TRACKED excluded path; the exclusion must cover both states")
	}
}

func TestHasUncommittedWorkExcluding_OtherChangesStillCount(t *testing.T) {
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".worktree-stale"), []byte("branch=HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "agent-wip.txt"), []byte("real work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !New(repo).HasUncommittedWorkExcluding(".worktree-stale") {
		t.Error("HasUncommittedWorkExcluding() = false while real work sits beside the excluded path")
	}
}

// TestHasUncommittedWorkExcluding_NoPathsMatchesPlainProbe pins the degenerate
// call, because ":(exclude)" with no positive pathspec left would change the
// meaning of the query rather than widen it.
func TestHasUncommittedWorkExcluding_NoPathsMatchesPlainProbe(t *testing.T) {
	repo := initTestRepo(t)
	if New(repo).HasUncommittedWorkExcluding() {
		t.Error("HasUncommittedWorkExcluding() = true for a clean repo with no exclusions")
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !New(repo).HasUncommittedWorkExcluding() {
		t.Error("HasUncommittedWorkExcluding() = false for a dirty repo with no exclusions")
	}
}
