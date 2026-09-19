package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// TestOrderDispatchMarksBackupOrderFailedWhenADatabaseIsNotBackedUp drives the
// REAL mol-dog-backup.sh, through the REAL shellExecRunner, under the real
// dispatcher, and requires the order to go red.
//
// It exists because the script's own suite and the dispatcher's own suite are
// each blind to the seam between them. The script suite asserts an exit status
// and stops there; TestOrderDispatchExecFailure hands the dispatcher a fake
// ExecRunner returning a fabricated error, so it never runs a process and
// cannot observe a real exit status at all. Both were green throughout the
// 27.5-day outage this pins (ci-liz6kl, from ci-ux4wo7), which is what two
// passing suites and one missing test look like from the inside.
//
// Two verdicts are asserted, not one. That a failing tick is recorded
// exec-failed is the fix; that the diagnostic naming the uncovered database
// survives on the tracking bead is why the fix is worth anything, because a
// zero-exit exec run retains no output at all and the escalation mail sent
// instead was auto-closed 96 times out of 97.
func TestOrderDispatchMarksBackupOrderFailedWhenADatabaseIsNotBackedUp(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not found: %v", err)
	}
	root := repoRootForLint(t)
	packDir := filepath.Join(root, "examples", "bd", "dolt")

	data, err := os.ReadFile(filepath.Join(packDir, "orders", "mol-dog-backup.toml"))
	if err != nil {
		t.Fatalf("read backup order: %v", err)
	}
	order, err := orders.Parse(data)
	if err != nil {
		t.Fatalf("parse backup order: %v", err)
	}
	order.Name = "mol-dog-backup"
	// Standing in for the scanner's one substitution, so everything downstream
	// of it is the production path.
	order.Exec = strings.ReplaceAll(order.Exec, "$PACK_DIR", packDir)

	// The city is stood up the way the dispatcher expects to find one rather
	// than by exporting GC_DOLT_* into the child. mergeRuntimeEnv strips every
	// controller-owned key out of the inherited environment precisely so a
	// caller cannot reach past that resolution, and a test that bypassed it
	// would stop covering the env path the order actually runs under.
	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	writeFile(t, filepath.Join(cityPath, ".beads", "config.yaml"), strings.Join([]string{
		"issue_prefix: ct",
		"gc.endpoint_origin: city_canonical",
		"gc.endpoint_status: verified",
		"dolt.host: 127.0.0.1",
		"dolt.port: 4406",
		"dolt.user: root",
		"",
	}, "\n"))

	target := execStoreTarget{ScopeRoot: cityPath, ScopeKind: "city"}
	t.Setenv("GC_BACKUP_DATABASES", "prod")
	t.Setenv("GC_BACKUP_OFFSITE_PATH", "")

	// The fixture database goes wherever the projected env says the data dir
	// is, asked of the same function the dispatcher calls. Hardcoding the
	// layout here would pin a runtime path this test has no stake in, and
	// would go quietly green-then-wrong the first time that layout moved.
	projected, err := orderExecEnvWithError(cityPath, nil, target, order, nil)
	if err != nil {
		t.Fatalf("project order exec env: %v", err)
	}
	dataDir := envValue(projected, "GC_DOLT_DATA_DIR")
	if dataDir == "" {
		t.Fatalf("projected order env has no GC_DOLT_DATA_DIR: %v", projected)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "prod", ".dolt"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}

	// The stand-in refuses to be a sync that worked: `dolt backup sync` is the
	// one subcommand that fails. Every other call answers narrowly and an
	// unscripted one exits 64, so a script that reached a zero exit could only
	// have done so by ignoring the failure -- not by wandering into a
	// blanket-success stub.
	binDir := t.TempDir()
	writeExecutableStub(t, filepath.Join(binDir, "dolt"), `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "version" ]; then
  printf 'dolt version 2.1.0\n'
  exit 0
fi
if [ "${1:-}" = "backup" ] && [ "$#" -eq 1 ]; then
  printf 'prod-backup file:///backups/prod\n'
  exit 0
fi
if [ "${1:-} ${2:-}" = "backup sync" ]; then
  printf 'simulated backup sync failure\n' >&2
  exit 1
fi
printf 'unscripted dolt call: %s\n' "$*" >&2
exit 64
`)
	writeExecutableStub(t, filepath.Join(binDir, "gc"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store := beads.NewMemStore()
	tracking, err := store.Create(beads.Bead{
		Title:  "order:mol-dog-backup",
		Labels: []string{"order-run:mol-dog-backup", labelOrderTracking},
	})
	if err != nil {
		t.Fatal(err)
	}

	var rec memRecorder
	var stderr bytes.Buffer
	ad := buildOrderDispatcherFromListExec([]orders.Order{order}, store, nil, shellExecRunner, &rec)
	mad := ad.(*memoryOrderDispatcher)
	mad.stderr = &stderr

	captureCmdOrderLogs(t, func() {
		mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}),
			target, order, cityPath, tracking.ID, nil)
	})

	all := trackingBeads(t, store, "order-run:mol-dog-backup")
	if len(all) == 0 {
		t.Fatal("no tracking bead found")
	}
	if !slices.Contains(all[0].Labels, "exec-failed") {
		t.Fatalf("tracking bead labels = %v, want exec-failed; stderr: %s", all[0].Labels, stderr.String())
	}
	if !rec.hasType(events.OrderFailed) {
		t.Fatalf("missing order.failed event; stderr: %s", stderr.String())
	}
	got := all[0].Metadata[beadmeta.OrderExecFailureOutputMetadataKey]
	for _, want := range []string{
		"prod(sync failed: simulated backup sync failure)",
		"NOT backed up",
		"dolt backup sync prod-backup",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tracking bead failure output missing %q:\n%s", want, got)
		}
	}
}

// envValue returns the value of key in a KEY=VALUE environment slice, or "".
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// writeExecutableStub writes a stand-in binary onto a test's PATH.
func writeExecutableStub(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
