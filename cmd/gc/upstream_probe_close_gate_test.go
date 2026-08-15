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
