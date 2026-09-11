---
title: Test-Policy Waiver Review
description: The per-entry decision record behind the shared expiry dates on the two test-policy catalogs, and the machine-checked table that keeps it honest.
---

# Test-policy waiver review

Two catalogs park a whole set of entries behind ONE shared expiry date:

- `internal/testpolicy/resourcecensus` — 40 dated rows in `bootstrapPolicy`,
  mirrored exactly in `test/test-resources.toml`.
- `internal/testutil/providerledger` — 9 waived `runtime.Provider` contract
  claims behind the single date literal in `waivedRuntime`.

Both packages are listed in `scripts/push-gate-always-run.manifest`, so the
first push of any branch runs them. On the expiry date every entry in the set
fails at once and the push gate closes for everybody. That cliff is deliberate
— one date means the remaining set is reviewed in one sitting rather than
forty-nine — and this file is the other half of the bargain: the per-entry
decision that justifies each renewal.

## How to use this file

You are most likely here because
`doctor/test-policy-expiry-horizon` in the city went yellow, or because a guard
told you an entry has no recorded decision. In either case the work is the
same, and it is NOT moving the date:

1. For each entry, establish whether the gap it covers can be closed NOW. For
   `providerledger` that is a literal question — does a full
   `internal/runtime/runtimetest.RunProviderTests` proof against the production
   composition exist, one that `ValidateProofRefs` accepts? For
   `resourcecensus` it is whether the declared migration has landed, or the
   resource has become replaceable in the tests that own it.
2. Where it can, retire the entry: delete it from its catalog, and change its
   row below to `retire` with the expiry it carried.
3. Where it cannot, say why the gap is STRUCTURAL rather than merely
   outstanding, and what would close it. That sentence is the row's `Basis`.
4. Only then choose the next shared date, and update the `Expiry` column of
   every retained row to match.

Step 4 cannot be skipped or done first: `waiverreview.Check` requires each
retained row's `Expiry` to equal the expiry its entry currently carries, so a
date moved without a re-recorded decision is a red gate rather than a silent
extension of entries nobody re-read. That equality is the entire mechanical
content of this file.

Gates, and the exact commands:

```bash
go test ./internal/testpolicy/waiverreview/
go test ./internal/testpolicy/resourcecensus/ -run TestEveryDatedPolicyRowHasARecordedDecision
go test ./internal/testutil/providerledger/ -run TestEveryWaiverHasARecordedDecision
go test ./internal/testpolicy/resourcecensus/ -run TestRepositoryLedgerMatchesCensusAndDocumentation
go test ./internal/testutil/providerledger/ -run TestCatalogMatchesProductionWiringAndDocumentation
```

Moving a census date means editing `census.go`, `test/test-resources.toml` and
the rendered block in `TESTING.md` — regenerate the last with
`go test ./internal/testpolicy/resourcecensus -run TestRepositoryLedgerMatchesCensusAndDocumentation -update`
and review the diff.

## Review of 2026-09-11 (bead gs-xujz)

The first review of the `resourcecensus` catalog since it was created, and the
second of `providerledger` after the 2026-08-12 renewal recorded in the
`waivedRuntime` comment. Both catalogs were reviewed per entry; 49 of 49 were
retained, 0 retired.

### What was measured

Direction of travel per ratchet row, from the full history of
`test/test-resources.toml` (125 commits, 2026-07-13 through 2026-09-10), read
at `main` `7cdbb6999`. These figures are tree state and are true only at that
commit — the live numbers are in the `TESTING.md` block, which is generated and
gated. Re-derive rather than quote:

```bash
git log --format=%H --reverse -- test/test-resources.toml |
  while read -r sha; do git show "$sha:test/test-resources.toml"; done
```

| Row | At creation | At `7cdbb6999` | Ever lowered |
| --- | --- | --- | --- |
| `debt cmd/gc+untagged environment` | 4092 calls / 180 files | 129 / 14 | yes |
| `debt cmd/gc+untagged cwd` | 208 / 40 | 174 / 16 | yes |
| `debt cmd/gc+untagged slow_process_gate` | 77 / 26 | 59 / 25 | yes |
| `debt untagged subprocess` | 374 / 97 | 426 / 125 | yes |
| `debt untagged http_test_server` | 255 / 56 | 317 / 66 | yes |
| `debt untagged fixed_sleep` | 291 / 113 | 296 / 119 | yes |
| `debt untagged net_listen` | 92 / 34 | 97 / 37 | yes |
| `debt untagged tmux` | 6 / 2 | 9 / 3 | yes |
| `debt untagged listener_helper` | 38 / 13 | 38 / 13 | no |
| `debt untagged net_listen_config` | 1 / 1 | 1 / 1 | no |
| `debt untagged net_listen_packet` | 3 / 2 | 3 / 2 | no |
| `debt untagged syscall_listen` | 1 / 1 | 1 / 1 | no |
| `small_debt untagged tmux` | 0 / 0 | 0 / 0 | no |

The `small_debt` rows track their `debt` siblings and are not repeated. The
`audit_baseline` rows are all-source counts and are not migration targets.

Three populations separate cleanly, and they want different verdicts:

- **Outstanding and progressing.** The three `cmd/gc+untagged` rows. The
  environment row fell from 4092 calls to 129 — a real migration, still live.
  These are debt in the ordinary sense and will close by reaching zero.
- **A budget, not a migration.** `subprocess`, `http_test_server`,
  `fixed_sleep`, `net_listen` and `tmux` under `untagged` have all GROWN. Each
  increment was argued for at the commit that raised it, and reading the
  justifications, they are proofs that cannot be hermetic — a real SIGTERM, a
  real tmux server, a real child process's environment block. The ratchet is
  doing its job by forcing that argument, not by trending to zero.
- **Flat since creation.** The four single-boundary rows never moved. Nothing
  about them became retirable; each still guards against a SECOND boundary
  appearing.

### Finding: a ratchet at zero is the opposite of retirable

`small_debt untagged tmux` sits at 0 calls / 0 files and has never moved. It
is not spent debt — it is a terminal invariant that forbids any untagged tmux
call from appearing. Deleting it would silently re-open the door it closed.

The same holds in miniature for every flat row: as a migration succeeds, its
ratchet gets MORE load-bearing, not less. So the expiry on a `resourcecensus`
row is shaped for a migration target and applied to rows that are permanent
guards, and a review of those rows can only ever conclude "retain". That is
worth knowing before budgeting the next sitting: the `resourcecensus` half of
this review is cheap and will stay cheap, and the expensive half is
`providerledger`, where the question really is per-entry.

This is a finding, not a proposal to drop the dates. The re-justification the
expiry forces is still the only thing that would catch a row whose owning test
was deleted or rewritten.

### Finding: `ValidateProofRefs` does not see a build constraint

`ValidateProofRefs` rejects `t.Skip`, `t.Skipf`, `t.SkipNow` and
`testing.Short` inside a proof, so that "pre-run gates and silent skips" are
visible. It parses the proof file with `parser.ParseFile(..., 0)` and never
reads its build constraints, so a `//go:build` tag is invisible to it.

`TestSubprocessSeamConformance` and `TestACPConformance` — two of the three
proved production compositions — are both `//go:build integration`. They are
real proofs and they do run, but only under the integration suite; the
untagged fast suite proves nothing about them while the ledger records them as
proved without qualification. This does not affect any decision below (both
entries' WAIVED siblings are retained for unrelated reasons), and it is not
fixed here because tightening `ValidateProofRefs` would turn two proved claims
red and needs its own review of what "proved" should mean for a tagged suite.

### Finding: two constructors are unprovable in the current proof shape

`k8s.NewSeamBacked` and `cmd/gc.newHybridProvider` return
`(runtime.Provider, error)`. A proof factory's body must be exactly one return
whose FIRST result is the constructor call, and Go rejects a two-value call in
that position. So those two are blocked by the proof shape before the cluster
question is even reached. Worth knowing because "get a test cluster" is the
obvious plan for the k8s waiver and it would not be sufficient.

### The date

Both catalogs move to, or stay at, **2026-11-09**.

`providerledger` already carried 2026-11-09, set by the 2026-08-12 renewal at
the 90-day `maxWaiverHorizon` ceiling. It is NOT extended by this review: not
one waiver gets an extra day. `resourcecensus` moves from 2026-10-01 to the
same date, which aligns the two cliffs and is the smallest change that does.

Alignment is the point, and it is the same argument `waivedRuntime` already
makes for sharing one date WITHIN a catalog, applied across the two. Before,
the fleet took two review sittings per cycle on unrelated dates and the
`resourcecensus` sitting was due first; after, one sitting covers both and the
`providerledger` half — the expensive half — happens on exactly the schedule
the previous reviewer chose.

The closures stay one day apart and that is correct, not sloppy:
`resourcecensus` compares `expiry.Before(day(now))` so the expiry date is
still a good day and the gate closes on 2026-11-10, while `providerledger`
compares `!Expires.After(now)` against a midnight-UTC `time.Date` and closes
on 2026-11-09 itself. `doctor/test-policy-expiry-horizon` reports the earlier
of the two, so the summons arrives once.

A longer date was rejected. The `providerledger` gaps are structural and would
survive any horizon, so a longer window buys the same answer later while
`maxWaiverHorizon` caps that catalog at 90 days anyway — and a `resourcecensus`
date beyond the `providerledger` one would put the cheap half of the review
after the expensive half and split the sitting again.

## Decisions

Machine-checked: `waiverreview.Parse` requires every line between the markers
to parse, and each catalog's guard requires this table to match its live
entries exactly, in both directions. Do not hand-edit a key — it is generated
from the catalog and a typo reads as an entry nobody reviewed.

<!-- BEGIN TEST POLICY WAIVER DECISIONS -->
| Catalog | Entry | Expiry | Decision | Reviewed | Basis |
| --- | --- | --- | --- | --- | --- |
| providerledger | `runtime.builtin.subprocess internal/runtime/subprocess.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | the no-cityPath branch hard-codes `os.TempDir()/gc-subprocess` and takes no directory, and a proof factory may call only the constructor plus `t.Name`/`t.TempDir`, so the contract's session discovery would observe every concurrent run on the host; closed by resolving a default directory in `cmd/gc` so that branch calls `NewSeamBackedWithDir` and this constructor leaves production wiring |
| providerledger | `runtime.builtin.acp internal/runtime/acp.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | identical shape to the subprocess waiver against `os.TempDir()/gc-acp`; closed the same way, and the two should be retired together because one fix pattern covers both |
| providerledger | `runtime.builtin.t3bridge internal/runtime/t3bridge.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | `NewProvider` calls `cleanupLegacyStateDir`, which `os.RemoveAll`s `$HOME/.t3/gc-bridge` unless `GC_EXEC_STATE_DIR` is set, and a factory cannot set it -- an untagged proof would delete the operator's live T3 state on every run, before `Start` even reaches for the T3 websocket; closed by moving that cleanup out of the constructor and taking the state root as an argument |
| providerledger | `runtime.builtin.exec internal/runtime/t3bridge.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | the legacy `gc-session-t3` prefix branch selects the same constructor as the `t3bridge` entry and carries the same two blockers; it retires with that entry, or earlier if the legacy prefix branch is deleted |
| providerledger | `runtime.builtin.k8s internal/runtime/k8s.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | returns `(runtime.Provider, error)`, and a proof factory must return the constructor call as the first of three results -- Go rejects a two-value call there, so the shape is unprovable before the cluster question arises; closed by a single-value constructor plus a conformance environment that guarantees a cluster |
| providerledger | `runtime.builtin.hybrid cmd/gc.newHybridProvider` | 2026-11-09 | retain | 2026-09-11 | the same two-value return shape as k8s, and it composes tmux with k8s so it inherits both of their gaps; it cannot retire before both halves do |
| providerledger | `runtime.builtin.ssh internal/runtime/ssh.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | the contract's `Start` needs a reachable host running tmux and `RunProviderTests` cannot skip; the only live-host coverage is `TestConn_ExecOverRealLocalhost`, which skips when passwordless localhost ssh is unavailable; closed by a conformance environment that guarantees the host |
| providerledger | `runtime.builtin.herdr internal/runtime/herdr.New` | 2026-11-09 | retain | 2026-09-11 | `TestHerdrConformance` exists but skips on `-short` and on a missing herdr binary, and `ValidateProofRefs` rejects any `t.Skip` or `testing.Short` inside a proof; closed by making herdr a guaranteed test dependency so both skips can be deleted |
| providerledger | `runtime.builtin.tmux internal/runtime/tmux.NewSeamBackedWithConfig` | 2026-11-09 | retain | 2026-09-11 | `TestTmuxConformance` skips on a missing tmux, is `//go:build integration`, and calls `RunProviderTestsWithOptions` rather than the declared `RunProviderTests` runner; closed by guaranteeing tmux, deleting the skip, and moving it onto the declared runner |
| resourcecensus | `audit_baseline scope=all resource=fixed_sleep` | 2026-11-09 | retain | 2026-09-11 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `audit_baseline scope=all resource=listener_helper` | 2026-11-09 | retain | 2026-09-11 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `audit_baseline scope=all resource=subprocess` | 2026-11-09 | retain | 2026-09-11 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `debt scope=cmd/gc+untagged resource=cwd` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `debt scope=cmd/gc+untagged resource=environment` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `debt scope=cmd/gc+untagged resource=slow_process_gate` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=cwd` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=environment` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=slow_process_gate` | 2026-11-09 | retain | 2026-09-11 | the migration is outstanding rather than structural and is measurably progressing (see the direction-of-travel table above); it closes when the count reaches zero |
| resourcecensus | `debt scope=untagged resource=fixed_sleep` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=http_test_server` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=net_listen` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=subprocess` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=tmux` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=fixed_sleep` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=http_test_server` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=net_listen` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=subprocess` | 2026-11-09 | retain | 2026-09-11 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=listener_helper` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `debt scope=untagged resource=net_listen_config` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `debt scope=untagged resource=net_listen_packet` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `debt scope=untagged resource=syscall_listen` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=listener_helper` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=net_listen_config` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=net_listen_packet` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=syscall_listen` | 2026-11-09 | retain | 2026-09-11 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=tmux` | 2026-11-09 | retain | 2026-09-11 | already at 0 calls / 0 files, which makes it a terminal invariant rather than debt: retiring it would silently re-open the door it closed, so this row gets MORE load-bearing as the migration succeeds, not less |
| resourcecensus | `medium package_dir=cmd/gc package_name=main owner=TestMain` | 2026-11-09 | retain | 2026-09-11 | package-level setup of process environment and the tmux namespace for the whole `cmd/gc` suite; a stand-in cannot set state the tests under it read from the real process, and the row closes only when no `cmd/gc` test needs process-global setup |
| resourcecensus | `medium package_dir=cmd/gc package_name=main owner=TestPassthroughEnvWithholdsControllerTokenFromChildProcess` | 2026-11-09 | retain | 2026-09-11 | the session env is an overlay, so only a real child process can show `GC_CONTROLLER_TOKEN` is ABSENT rather than merely missing from a map; a stand-in would be asked the question the test exists to put to the kernel |
| resourcecensus | `medium package_dir=internal/api package_name=api owner=TestEveryEmittedErrorCodeIsRegistered` | 2026-11-09 | retain | 2026-09-11 | spawns `git ls-files` because a `WalkDir` over the root descends into the nested worktrees under `.gc/`, whose historical copies carry pre-migration URN literals; git's tracked set is the definition of shipped source, so the subprocess IS the specification |
| resourcecensus | `medium package_dir=internal/doctor package_name=doctor owner=TestCustomTypesCheck_TableDrift` | 2026-11-09 | retain | 2026-09-11 | manufactures and heals real config-CSV-vs-table drift against a throwaway store, so the bd and dolt subprocesses are the artifact under test; closed only if drift becomes observable without a store |
| resourcecensus | `medium package_dir=internal/doctor package_name=doctor owner=TestCustomTypesCheck_TableDriftUsesTestOwnedDoltContext` | 2026-11-09 | retain | 2026-09-11 | the invariant is that bd routes to a test-owned embedded store rather than the machine's shared server, which is a claim about a real process's resolution and cannot be posed to a stand-in |
| resourcecensus | `medium package_dir=internal/runtime/herdr package_name=herdr owner=TestServerAliveDetectsLiveServer` | 2026-11-09 | retain | 2026-09-11 | needs a real Unix socket inode with a live acceptor; the distinction it pins is made by the kernel on connect, so a stand-in listener would answer from the stand-in |
| resourcecensus | `medium package_dir=internal/runtime/herdr package_name=herdr owner=TestServerAliveRejectsStaleSocket` | 2026-11-09 | retain | 2026-09-11 | the same kernel-level distinction from the other side: a socket file whose listener was closed with `SetUnlinkOnClose(false)`, which is exactly what an uncleanly-exited server leaves and cannot be simulated above the socket |
| resourcecensus | `medium package_dir=internal/runtime/tmux package_name=tmux owner=TestMain` | 2026-11-09 | retain | 2026-09-11 | owns the isolated tmux socket and its cleanup for the whole package; the suite's subject IS tmux, so the setup cannot be replaced by a fake executor without deleting what the suite measures |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestDashboardCheckRunsFrontendVitestSuite` | 2026-11-09 | retain | 2026-09-11 | must read the EXPANDED `make -n` recipe, because a step reached through a prerequisite or a variable is invisible to a Makefile grep; only make can expand its own recipe |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestDockerSessionProtocol` | 2026-11-09 | retain | 2026-09-11 | the artifact under test is a shell adapter, so it must be executed; docker itself is already a strict PATH-injected fake, which is the hermetic half of the boundary |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestProviderOverridesAndSuiteContractsCrossMakeIsolation` | 2026-11-09 | retain | 2026-09-11 | six isolated make invocations are the subject: the property is that provider overrides do not leak ACROSS make processes, which no single-process test can represent |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestWithGoTmpRemovesOwnedDirectoryOnTermination` | 2026-11-09 | retain | 2026-09-11 | must observe a real SIGTERM and verify the wrapper removes its private `GOTMPDIR`; signal delivery and the cleanup that races it exist only in a real process |
| resourcecensus | `medium package_dir=test/tmuxtest package_name=tmuxtest owner=TestSweepOrphanKillsTmuxServerBeforeRemovingItsSocketDir` | 2026-11-09 | retain | 2026-09-11 | starts one real tmux server, orphans it, and asserts the PROCESS is gone; a fake executor cannot demonstrate that deleting a socket leaves a live server behind, which is the whole defect |
<!-- END TEST POLICY WAIVER DECISIONS -->
