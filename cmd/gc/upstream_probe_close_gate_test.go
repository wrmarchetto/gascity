package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestEvaluateUpstreamProbeCloseGateRejectsPresentButWrongProbe(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "ci-probe",
		Metadata: map[string]string{
			upstreamProbeMetadataKey: "test -f upstream_owned.txt",
		},
	}}, nil)

	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); !blocked {
		t.Fatalf("wrong probe was accepted; stderr=%s", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "still passes") {
		t.Fatalf("refusal does not explain that the probe stayed green: %q", got)
	}
}

func TestEvaluateUpstreamProbeCloseGateRejectsBugWithoutProbe(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:   "ci-probe",
		Type: "bug",
	}}, nil)

	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); !blocked {
		t.Fatalf("bug close without a probe was accepted; stderr=%s", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "set gc.upstream_probe") {
		t.Fatalf("refusal does not request the missing probe: %q", got)
	}
}

func TestEvaluateUpstreamProbeCloseGateDoesNotGateControlBead(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:       "ci-probe",
		Type:     "task",
		Metadata: map[string]string{"gc.kind": "step"},
	}}, nil)

	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); blocked {
		t.Fatalf("control bead close was blocked: %s", stderr.String())
	}
}

func TestEvaluateUpstreamProbeCloseGateAcceptsProbeThatFailsWithoutProductionChange(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "ci-probe",
		Metadata: map[string]string{
			upstreamProbeMetadataKey: "test \"$(cat upstream_owned.txt)\" = fixed",
		},
	}}, nil)

	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); blocked {
		t.Fatalf("valid probe was rejected: %s", stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("valid probe wrote a warning: %q", got)
	}
}

func TestEvaluateUpstreamProbeCloseGateUsesProbeSetByAtomicClose(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ci-probe"}}, nil)
	args := []string{
		"update", "ci-probe", "--set-metadata",
		upstreamProbeMetadataKey + "=test \"$(cat upstream_owned.txt)\" = fixed",
		"--status=closed",
	}

	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate(args, store, nil, repoDir, &stderr); blocked {
		t.Fatalf("valid atomic close was rejected: %s", stderr.String())
	}
}

// TestEvaluateUpstreamProbeCloseGateScopesProbeByBranchShape pins what each
// branch shape owes. Fork-only owes no probe. Upstream code owes a kept test
// and a probe that runs a test both before and after the revert. Only
// upstream-owned tests owe a probe that runs them green, as the merge sweep does.
func TestEvaluateUpstreamProbeCloseGateScopesProbeByBranchShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		paths    []string // files the branch writes; upstream_owned.txt is upstream's
		upstream bool     // paths are committed to upstream/main first
		probe    string
		wantMsg  string // empty when the close must be accepted
	}{
		{name: "fork-only/no-probe", paths: []string{"fork_only.txt"}},
		// Green on upstream's tree too, as a flaky-test fix's own test run is.
		{name: "upstream-test-only/passing-probe", paths: []string{"owned_test.go"}, upstream: true, probe: "test -f owned_test.go"},
		{name: "upstream-test-only/no-probe", paths: []string{"owned_test.go"}, upstream: true, wantMsg: "edits only test paths"},
		{name: "upstream-test-only/red-probe", paths: []string{"owned_test.go"}, upstream: true, probe: "grep -q never-written owned_test.go", wantMsg: "fails on the branch tip"},
		{name: "upstream-test-only/vacuous-probe", paths: []string{"owned_test.go"}, upstream: true, probe: "echo 'ok pkg [no test files]'", wantMsg: "ran no test"},
		{name: "upstream-code-without-test/probe", paths: []string{"upstream_owned.txt"}, probe: "true", wantMsg: "adds no test path"},
		{
			name:    "upstream-code-and-test/vacuous-after-revert",
			paths:   []string{"upstream_owned.txt", "new_test.go"},
			probe:   "grep -q branch-edit upstream_owned.txt && echo ok || echo 'ok pkg [no test files]'",
			wantMsg: "ran no test after",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := newUpstreamProbeCloseGateRepo(t)
			runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "main")
			writeAndCommit := func(content, msg string) {
				t.Helper()
				for _, path := range tc.paths {
					if err := os.WriteFile(filepath.Join(repoDir, path), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				runUpstreamProbeCloseGateGit(t, repoDir, append([]string{"add"}, tc.paths...)...)
				runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", msg)
			}
			if tc.upstream {
				writeAndCommit("upstream\n", "upstream files")
				runUpstreamProbeCloseGateGit(t, repoDir, "update-ref", "refs/remotes/upstream/main", "HEAD")
			}
			runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "-b", "fix/shape")
			writeAndCommit("branch-edit\n", "branch work")

			bead := beads.Bead{ID: "ci-probe", Type: "bug"}
			if tc.probe != "" {
				bead.Metadata = map[string]string{upstreamProbeMetadataKey: tc.probe}
			}
			store := beads.NewMemStoreFrom(1, []beads.Bead{bead}, nil)
			var stderr strings.Builder
			blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr)
			if tc.wantMsg == "" {
				if blocked {
					t.Fatalf("close was blocked: %s", stderr.String())
				}
				return
			}
			if !blocked {
				t.Fatalf("close was accepted, want refusal containing %q", tc.wantMsg)
			}
			if got := stderr.String(); !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("refusal = %q, want it to contain %q", got, tc.wantMsg)
			}
		})
	}
}

func newUpstreamProbeCloseGateRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runUpstreamProbeCloseGateGit(t, repoDir, "init", "-q", "-b", "main")
	runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.email", "fixture@example.invalid")
	runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.name", "fixture")
	if err := os.WriteFile(filepath.Join(repoDir, "upstream_owned.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUpstreamProbeCloseGateGit(t, repoDir, "add", "upstream_owned.txt")
	runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", "upstream base")
	runUpstreamProbeCloseGateGit(t, repoDir, "update-ref", "refs/remotes/upstream/main", "HEAD")
	runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "-b", "fix/ci-probe")
	if err := os.WriteFile(filepath.Join(repoDir, "upstream_owned.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "upstream_probe_test.go"), []byte("test fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUpstreamProbeCloseGateGit(t, repoDir, "add", "upstream_owned.txt", "upstream_probe_test.go")
	runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", "fork fix and regression test")
	return repoDir
}

func runUpstreamProbeCloseGateGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestEvaluateUpstreamProbeCloseGateKeepsNodeTestModules pins that a branch
// whose only tests are Node ES/CommonJS test modules keeps them through the
// revert. The probe mimics `node --test <file>...`, which silently skips a
// named file that does not exist: classified as production, the modified
// test goes back to its pre-patch body and the new one is deleted, so the
// probe stays green on the old suite and the gate refuses a valid probe as
// "still passes" (measured on ci-06lx7u). Each suffix gets its own fixture
// so dropping either one from the classifier fails its own subtest. The
// go-seam variant also carries a kept _test.go, the ci-06lx7u workaround:
// without it a misclassification trips the cheaper "no test path" refusal
// and never reaches the revert that makes the probe silently stay green.
func TestEvaluateUpstreamProbeCloseGateKeepsNodeTestModules(t *testing.T) {
	for _, tc := range []struct {
		suffix string
		goSeam bool
	}{
		{".test.mjs", false},
		{".test.mjs", true},
		{".test.cjs", false},
		{".test.cjs", true},
	} {
		suffix := tc.suffix
		name := suffix
		if tc.goSeam {
			name += "/go-seam"
		}
		t.Run(name, func(t *testing.T) {
			repoDir := newUpstreamProbeCloseGateRepo(t)
			testDir := filepath.Join("contrib", "bridge", "test")
			existing := filepath.Join(testDir, "bridge"+suffix)
			added := filepath.Join(testDir, "regression"+suffix)

			// The pre-patch suite passes on any tree, as the old 18-test
			// openclaw-bridge suite did, so only the branch's tests can
			// tell the patch apart.
			runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "main")
			if err := os.MkdirAll(filepath.Join(repoDir, testDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, existing), []byte("exit 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runUpstreamProbeCloseGateGit(t, repoDir, "add", existing)
			runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", "pre-patch suite")
			runUpstreamProbeCloseGateGit(t, repoDir, "update-ref", "refs/remotes/upstream/main", "HEAD")

			runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "-B", "fix/ci-probe")
			if err := os.WriteFile(filepath.Join(repoDir, "upstream_owned.txt"), []byte("fixed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			assertFixed := []byte("test \"$(cat upstream_owned.txt)\" = fixed\n")
			for _, path := range []string{existing, added} {
				if err := os.WriteFile(filepath.Join(repoDir, path), assertFixed, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			staged := []string{"add", "upstream_owned.txt", existing, added}
			if tc.goSeam {
				if err := os.WriteFile(filepath.Join(repoDir, "seam_test.go"), []byte("test fixture\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				staged = append(staged, "seam_test.go")
			}
			runUpstreamProbeCloseGateGit(t, repoDir, staged...)
			runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", "fork fix and node regression tests")

			probe := "for f in " + existing + " " + added + "; do [ -f \"$f\" ] || continue; sh \"$f\" || exit 1; done"
			store := beads.NewMemStoreFrom(1, []beads.Bead{{
				ID:       "ci-probe",
				Metadata: map[string]string{upstreamProbeMetadataKey: probe},
			}}, nil)

			var stderr strings.Builder
			if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); blocked {
				t.Fatalf("%s tests were reverted as production: %s", suffix, stderr.String())
			}
		})
	}
}

// TestEvaluateUpstreamProbeCloseGateTreatsBehaviorFreeBranchAsTestOnly pins
// the gs-g0m7 shape: a branch whose reverted paths are all comment-only Go or
// unembedded engdocs owes only a probe green on the tip. One behavioral path
// restores the full experiment, with the behavior-free paths reverted too.
func TestEvaluateUpstreamProbeCloseGateTreatsBehaviorFreeBranchAsTestOnly(t *testing.T) {
	const ownedGo = "package p\n\n// F returns one.\nfunc F() int { return 1 }\n"
	const commentGo = "package p\n\n// F returns one, as the fork documents.\nfunc F() int { return 1 }\n"
	upstream := map[string]string{
		"owned.go":                 ownedGo,
		"engdocs/arch/dispatch.md": "upstream prose\n",
		"upstream_owned.txt":       "base\n",
	}
	behaviorFree := map[string]string{
		"owned.go":                 commentGo,
		"engdocs/arch/dispatch.md": "fork prose\n",
		"fork_test.go":             "test fixture\n",
	}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range behaviorFree {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		upstream map[string]string // committed and pinned as upstream/main and main
		branch   map[string]string // committed on the work branch
		probe    string
		wantMsg  string // empty when the close must be accepted
	}{
		{name: "comment-docs-and-test/green-probe", branch: behaviorFree, probe: "test -f fork_test.go"},
		{name: "comment-docs-and-test/red-probe", branch: behaviorFree, probe: "grep -q never-written fork_test.go", wantMsg: "fails on the branch tip"},
		{name: "comment-docs-and-test/vacuous-probe", branch: behaviorFree, probe: "echo 'ok pkg [no test files]'", wantMsg: "ran no test"},
		{name: "comment-docs-and-test/no-probe", branch: behaviorFree, wantMsg: "edits only test paths or behavior-free paths"},
		{
			name:   "comment-only-without-test/green-probe",
			branch: map[string]string{"owned.go": commentGo},
			probe:  "test -f owned.go",
		},
		{
			name:    "behavioral-path-keeps-full-experiment",
			branch:  with(map[string]string{"upstream_owned.txt": "fixed\n"}),
			probe:   "test -f fork_test.go",
			wantMsg: "still passes after this branch's production changes are reverted (behavioral: upstream_owned.txt)",
		},
		{
			name:   "behavioral-path-reverts-comment-paths-alongside",
			branch: with(map[string]string{"upstream_owned.txt": "fixed\n"}),
			probe:  "grep -q 'as the fork documents' owned.go",
		},
		{
			name:    "behavioral-path-without-probe-still-owes-one",
			branch:  with(map[string]string{"upstream_owned.txt": "fixed\n"}),
			wantMsg: "edits upstream-owned code; set gc.upstream_probe",
		},
		{
			name: "embedded-markdown-keeps-full-experiment",
			upstream: map[string]string{
				"owned.go":                 ownedGo,
				"engdocs/arch/dispatch.md": "upstream prose\n",
				"embed.go":                 "package p\n\nimport \"embed\"\n\n//go:embed engdocs\nvar Docs embed.FS\n",
			},
			branch:  behaviorFree,
			probe:   "test -f fork_test.go",
			wantMsg: "(behavioral: engdocs/arch/dispatch.md)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := t.TempDir()
			runUpstreamProbeCloseGateGit(t, repoDir, "init", "-q", "-b", "main")
			runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.email", "fixture@example.invalid")
			runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.name", "fixture")
			commit := func(files map[string]string, msg string) {
				t.Helper()
				for path, content := range files {
					full := filepath.Join(repoDir, path)
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				runUpstreamProbeCloseGateGit(t, repoDir, "add", "-A")
				runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", msg)
			}
			base := tc.upstream
			if base == nil {
				base = upstream
			}
			commit(base, "upstream base")
			runUpstreamProbeCloseGateGit(t, repoDir, "update-ref", "refs/remotes/upstream/main", "HEAD")
			runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "-b", "fix/ci-probe")
			commit(tc.branch, "branch work")

			bead := beads.Bead{ID: "ci-probe", Type: "task"}
			if tc.probe != "" {
				bead.Metadata = map[string]string{upstreamProbeMetadataKey: tc.probe}
			}
			store := beads.NewMemStoreFrom(1, []beads.Bead{bead}, nil)
			var stderr strings.Builder
			blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr)
			if tc.wantMsg == "" {
				if blocked {
					t.Fatalf("close was blocked: %s", stderr.String())
				}
				return
			}
			if !blocked {
				t.Fatalf("close was accepted, want refusal containing %q", tc.wantMsg)
			}
			if got := stderr.String(); !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("refusal = %q, want it to contain %q", got, tc.wantMsg)
			}
		})
	}
}
