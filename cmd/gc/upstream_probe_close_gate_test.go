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

func TestEvaluateUpstreamProbeCloseGateDoesNotAskForkOnlyBranchForAProbe(t *testing.T) {
	repoDir := newUpstreamProbeCloseGateRepo(t)
	runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "main")
	runUpstreamProbeCloseGateGit(t, repoDir, "checkout", "-q", "-b", "fix/fork-only")
	if err := os.WriteFile(filepath.Join(repoDir, "fork_only.txt"), []byte("fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUpstreamProbeCloseGateGit(t, repoDir, "add", "fork_only.txt")
	runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "-m", "fork-only work")

	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ci-probe"}}, nil)
	var stderr strings.Builder
	if blocked := evaluateUpstreamProbeCloseGate([]string{"close", "ci-probe"}, store, nil, repoDir, &stderr); blocked {
		t.Fatalf("fork-only branch was blocked: %s", stderr.String())
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
