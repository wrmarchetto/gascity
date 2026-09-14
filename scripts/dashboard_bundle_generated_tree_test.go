package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// distPath is the committed Vite bundle embedded by
// internal/api/dashboardspa/embed.go.
const distPath = "internal/api/dashboardspa/dist"

// upstreamMergeDoc records how this fork merges upstream through distPath.
// Its procedure -- discard the tree, regenerate, stage -- is only valid while
// the three facts below hold, which is why the assertions name it.
const upstreamMergeDoc = "engdocs/contributors/upstream-merge-dashboard-bundle.md"

// TestDashboardBundleIsATrackedGeneratedTree pins the three facts that make a
// merge conflict inside the committed dashboard bundle resolvable by
// regeneration instead of by reading hunks: the tree is tracked, it is
// declared generated output, and `make dashboard-build` is what rebuilds it.
//
// Rollup folds each chunk's content hash into the names of every chunk that
// imports it, so any source edit reaching the entry chunk renames the whole
// asset tree. Git then pairs the base file with both sides' renames and
// reports rename/rename -- measured on ci-zspx at 66 such paths plus
// dist/index.html against 24 upstream commits, 96% of that merge's 70
// conflicts. scripts/rebase-resolve-lib.sh refuses those XY codes (DD, AU,
// UA) by design, so no auto-resolver will ever help here; the resolution is
// to rebuild, and that resolution is what these assertions protect.
//
// This is deliberately NOT a check that the committed bundle matches the
// committed sources. That is scripts/check-artifact-drift.sh's job, it needs
// an npm build to answer, and duplicating it here would make a hermetic test
// depend on Node. What is asserted here is only the shape the procedure reads.
func TestDashboardBundleIsATrackedGeneratedTree(t *testing.T) {
	root := repoRoot(t)

	if _, err := os.Stat(filepath.Join(root, upstreamMergeDoc)); err != nil {
		t.Fatalf("%s is missing (%v); it carries the procedure these assertions protect, "+
			"so removing it silently drops the only record of how to merge through %s",
			upstreamMergeDoc, err, distPath)
	}

	// Tracked, not merely present on disk. An untracked bundle cannot conflict
	// at all, which would make the procedure unnecessary rather than wrong --
	// so this failing means the doc needs deleting, not editing.
	cmd := exec.Command("git", "ls-files", "--", distPath)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files %s: %v", distPath, err)
	}
	tracked := strings.Fields(string(out))
	if len(tracked) == 0 {
		t.Fatalf("%s holds no tracked files. The fork no longer commits the built bundle, "+
			"so the upstream-merge procedure in %s no longer describes anything -- delete "+
			"that page and this test together.", distPath, upstreamMergeDoc)
	}
	// index.html is the one path in the tree with a stable name, so it is the
	// one the procedure can name; the hashed assets under assets/ cannot be.
	if !containsPath(tracked, distPath+"/index.html") {
		t.Errorf("%s/index.html is not tracked; %s names it as the app shell that "+
			"conflicts by content rather than by rename", distPath, upstreamMergeDoc)
	}

	attrs, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	attrLine := gitattributesLineFor(string(attrs), distPath+"/**")
	if attrLine == "" {
		t.Fatalf(".gitattributes has no rule for %s/**; without it git offers textual "+
			"conflict hunks over minified bundles and review UIs invite reading them, "+
			"which is exactly what %s exists to stop", distPath, upstreamMergeDoc)
	}
	if !strings.Contains(attrLine, "-diff") {
		t.Errorf(".gitattributes rule for %s/** lost `-diff`: %q", distPath, attrLine)
	}
	if !strings.Contains(attrLine, "linguist-generated") {
		t.Errorf(".gitattributes rule for %s/** lost `linguist-generated`: %q", distPath, attrLine)
	}

	// The expanded recipe, not the Makefile text, so a regenerator moved behind
	// a variable or a sibling target still shows up. `make -n` executes nothing
	// here: dashboard-build has no prerequisites and no $(MAKE) recursion or
	// `+`-prefixed line, which is the only reason a test can afford to name a
	// target whose real run is an npm build.
	recipeCmd := exec.Command("make", "-n", "dashboard-build")
	recipeCmd.Dir = root
	recipe, err := recipeCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n dashboard-build failed: %v\n%s", err, recipe)
	}
	if !strings.Contains(string(recipe), "npm run build") {
		t.Errorf("dashboard-build no longer runs `npm run build`; %s cites it as the "+
			"regenerator that replaces conflict resolution:\n%s", upstreamMergeDoc, recipe)
	}
	// Either spelling counts. The recipe writes `../dist` because it runs from
	// web/, but a future recipe running from the repo root would spell the same
	// destination in full -- and failing on that would be pedantry, not a
	// finding. What must not happen is the recipe stopping short of the bundle.
	if !strings.Contains(string(recipe), "../dist") && !strings.Contains(string(recipe), distPath) {
		t.Errorf("dashboard-build no longer writes the bundle to %s; the procedure in %s "+
			"regenerates into that exact path:\n%s", distPath, upstreamMergeDoc, recipe)
	}
}

// containsPath reports whether paths holds want exactly.
func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// gitattributesLineFor returns the first non-comment .gitattributes line whose
// pattern field equals pattern, or "" when none does. Matching the pattern
// field exactly rather than substring-searching the file keeps a rule named
// only inside a comment from satisfying the assertion -- the comment block
// above the real rule in this repository quotes the path.
func gitattributesLineFor(contents, pattern string) string {
	for _, raw := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == pattern {
			return line
		}
	}
	return ""
}
