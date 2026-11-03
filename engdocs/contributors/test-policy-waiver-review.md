---
title: Test-Policy Waiver Review
description: The per-entry decision record behind the shared expiry dates on the two test-policy catalogs, and the machine-checked table that keeps it honest.
---

# Test-policy waiver review

Two catalogs park a whole set of entries behind ONE shared expiry date:

- `internal/testpolicy/resourcecensus` — every dated row in `bootstrapPolicy`,
  mirrored exactly in `test/test-resources.toml`.
- `internal/testutil/providerledger` — every waived `runtime.Provider`
  contract claim, behind the single date literal in `waivedRuntime`.

Both packages are listed in `scripts/push-gate-always-run.manifest`, so the
first push of any branch runs them. On the expiry date every entry in the set
fails at once and the push gate closes for everybody. That cliff is deliberate
— one date means the remaining set is reviewed in one sitting rather than
one entry at a time — and this file is the other half of the bargain: the per-entry
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

## Review of 2026-11-03 (bead gs-y6g3)

The third review of `providerledger` and the second of `resourcecensus`. All
53 entries were reviewed per entry: 9 waivers and 44 census rows. 2 were
retired and 51 retained, and the shared date moved to **2027-02-01**.

### Retired: the two default-directory constructors

`subprocess.NewSeamBacked` and `acp.NewSeamBacked` were waived only because
the empty-cityPath branches in `cmd/gc/runtime_registry.go` reached them, and
the previous review named the fix. Each was exactly its `NewSeamBackedWithDir`
applied to `os.TempDir()/gc-<name>`: same struct, same `MkdirAll`. Each
package now exports that directory as `DefaultDir()`, and the branch resolves
it and calls `NewSeamBackedWithDir`. So the registry composes only the proved
constructor, and nothing in production behaves differently. With no caller
left, `NewSeamBacked` and `NewProvider` were deleted from both packages.

Both proofs are `//go:build integration`, so these two compositions are proved
under the integration suite and not the fast one. That is the qualification
recorded in the 2026-09-11 build-constraint finding below. The retirement
inherits it and does not add to it.

### What was measured

Ratchet movement between the previous review (`7cdbb6999`) and `main` at
`4c12dcde2`, from the history of `test/test-resources.toml`. This is tree
state at that commit, so re-derive it with the command in the previous section
rather than quoting it.

| Row | Since the previous review |
| --- | --- |
| `untagged subprocess` | +5 calls over four commits, each argued at its commit |
| `untagged net_listen` | +3 calls (`10c38496a`), argued at its commit |
| `untagged listener_helper` | +1 call (`7d5f72715`, ci-6mp9hs), argued at its commit |

Every other ratchet row is unchanged. That includes the three
`cmd/gc+untagged` rows. All 17 Medium owner tests still exist under their
recorded names. Four owner files changed after the previous review:

- Two changed before their rows were first recorded, on 2026-09-15 and
  2026-09-18.
- The 2026-09-21 edits to `cmd/gc/cmd_start_test.go` left
  `TestPassthroughEnvWithholdsControllerTokenFromChildProcess` alone. Its last
  change is 2026-08-06, by `git log -L`.
- `cmd/gc/main_test.go` gained a disk-probe stub. The environment and tmux
  setup its `TestMain` row covers is unchanged.

For every owner, the resource call its basis names was confirmed still present
in the owner function.

### Finding: the `cmd/gc` migration is not progressing, and nothing here owns it

The previous review retained the six `cmd/gc+untagged` rows as "measurably
progressing", because environment had fallen from 4092 calls to 129. That fall
was not a migration. It was `6ccc7f9a2` on 2026-07-25, which redefined the
census so that `t.Setenv` and `t.Chdir`, which restore themselves, no longer
count. The same commit took cwd from 285 to 174. Since that redefinition the
environment count has grown by one. Slow-process markers fell to 57 on
2026-07-22 through real upstream migrations, and have grown back to 59 in this
fork.

The rows name migration targets D5, D6 and E6 in
`engdocs/contributors/testing-pyramid-hardening-plan.md`, whose status is
Proposed, under the epic `ga-80po0c`. That epic and its children
`ga-80po0c.2.1` and `ga-80po0c.2.3` are upstream beads, and they are in no
store this city reads.

The verdict is still `retain`, because deleting a ratchet lets its count grow
with nothing watching. But "progressing" was the wrong basis, and the rows now
say so. If the fork wants these counts to fall, the migration needs a local
owner first. Until it has one, these rows are guards, the same as the flat
rows.

### Finding: herdr and tmux are also blocked by the proof shape

A proof factory must be a single `return` that calls the constructor once,
using no test method but `t.Name` and `t.TempDir`. Before the runner, only
zero-value `var` declarations are allowed.

- `TestHerdrConformance` puts its skips before the runner, and its factory is
  three statements, one of them `t.Cleanup(p.TeardownServer)`.
- `TestTmuxConformance` builds one provider outside the factory and returns
  that same provider from every call.

The recorded bases named only the skips, so "make the binary a guaranteed
test dependency" read as sufficient. It is not. Herdr also needs a provider
that tears its own server down, or a proof shape that admits a per-instance
cleanup. Tmux also needs a constructor call inside the factory.

### The date

Both catalogs move to **2027-02-01**. That is the 90-day `maxWaiverHorizon`
ceiling measured from this review, which `waivedRuntime` cannot exceed.

The seven remaining `providerledger` gaps are structural, and each needs work
outside a review: a constructor shape, an environment guarantee, or a
destructive constructor. A shorter date buys the same answer sooner.
`2027-02-01` also keeps the lead window off the year end.
`doctor/test-policy-expiry-horizon` goes yellow 21 days out, which is
2027-01-11. A date inside the ceiling's first month would open that window
across the holidays.

The closures stay one day apart for the reason given under the previous
review: `providerledger` closes on 2027-02-01 itself and `resourcecensus` on
2027-02-02.

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
| providerledger | `runtime.builtin.subprocess internal/runtime/subprocess.NewSeamBacked` | 2026-11-09 | retire | 2026-11-03 | retired: the no-cityPath branch in `cmd/gc` now resolves `subprocess.DefaultDir()` and calls `NewSeamBackedWithDir`, so the registry composes only the proved constructor, and `NewSeamBacked` and `NewProvider` were deleted with no caller left; the directory is unchanged, and the proof inherits the integration-tag qualification recorded in the 2026-09-11 build-constraint finding |
| providerledger | `runtime.builtin.acp internal/runtime/acp.NewSeamBacked` | 2026-11-09 | retire | 2026-11-03 | retired with the subprocess entry by the same pattern: the no-cityPath branch resolves `acp.DefaultDir()` and calls `NewSeamBackedWithDir`, and `NewSeamBacked` and `NewProvider` were deleted; same directory, same integration-tag qualification |
| providerledger | `runtime.builtin.t3bridge internal/runtime/t3bridge.NewSeamBacked` | 2027-02-01 | retain | 2026-11-03 | `NewProvider` calls `cleanupLegacyStateDir`, which `os.RemoveAll`s `$HOME/.t3/gc-bridge` unless `GC_EXEC_STATE_DIR` is set, and a factory cannot set it -- an untagged proof would delete the operator's live T3 state on every run, before `Start` even reaches for the T3 websocket; closed by moving that cleanup out of the constructor and taking the state root as an argument |
| providerledger | `runtime.builtin.exec internal/runtime/t3bridge.NewSeamBacked` | 2027-02-01 | retain | 2026-11-03 | the legacy `gc-session-t3` prefix branch selects the same constructor as the `t3bridge` entry and carries the same two blockers; it retires with that entry, or earlier if the legacy prefix branch is deleted |
| providerledger | `runtime.builtin.k8s internal/runtime/k8s.NewSeamBacked` | 2027-02-01 | retain | 2026-11-03 | returns `(runtime.Provider, error)`, and a proof factory must return the constructor call as the first of three results -- Go rejects a two-value call there, so the shape is unprovable before the cluster question arises; closed by a single-value constructor plus a conformance environment that guarantees a cluster |
| providerledger | `runtime.builtin.hybrid cmd/gc.newHybridProvider` | 2027-02-01 | retain | 2026-11-03 | the same two-value return shape as k8s, and it composes tmux with k8s so it inherits both of their gaps; it cannot retire before both halves do |
| providerledger | `runtime.builtin.ssh internal/runtime/ssh.NewSeamBacked` | 2027-02-01 | retain | 2026-11-03 | the contract's `Start` needs a reachable host running tmux and `RunProviderTests` cannot skip; the only live-host coverage is `TestConn_ExecOverRealLocalhost`, which skips when passwordless localhost ssh is unavailable; closed by a conformance environment that guarantees the host |
| providerledger | `runtime.builtin.herdr internal/runtime/herdr.New` | 2027-02-01 | retain | 2026-11-03 | `TestHerdrConformance` skips on `-short` and on a missing herdr binary, and its skips precede the runner where only zero-value var declarations may; its factory is also three statements including `t.Cleanup(p.TeardownServer)`, where the proof shape allows one return and only `t.Name`/`t.TempDir`; closed by a guaranteed herdr binary AND a provider that tears its own server down, so guaranteeing the binary alone would not retire it |
| providerledger | `runtime.builtin.tmux internal/runtime/tmux.NewSeamBackedWithConfig` | 2027-02-01 | retain | 2026-11-03 | `TestTmuxConformance` (`adapter_test.go`) is `//go:build integration`, skips on a missing tmux, calls `RunProviderTestsWithOptions` rather than the declared runner, and builds one provider outside the factory and returns it from every call, where the constructor must be called inside the factory; closed by guaranteeing tmux, deleting the skip, and moving it onto the declared runner with a per-call constructor |
| resourcecensus | `audit_baseline scope=all resource=fixed_sleep` | 2027-02-01 | retain | 2026-11-03 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `audit_baseline scope=all resource=listener_helper` | 2027-02-01 | retain | 2026-11-03 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `audit_baseline scope=all resource=subprocess` | 2027-02-01 | retain | 2026-11-03 | an all-source total is audit evidence rather than a migration target: it counts tagged files too, so it can never reach zero while any suite uses the resource; it closes only by deleting the audit, which would remove the sole all-source view |
| resourcecensus | `debt scope=cmd/gc+untagged resource=cwd` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- its only large drop, on 2026-07-25, was the same census redefinition for `t.Chdir`, not a migration, and it has not moved since -- and its owner bead `ga-80po0c.2.3` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `debt scope=cmd/gc+untagged resource=environment` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- its only large drop, on 2026-07-25, was `6ccc7f9a2` redefining the census to stop counting the auto-restoring `t.Setenv`, not a migration, and the direct `os.*` count has grown by one since -- and its owner bead `ga-80po0c.2.3` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `debt scope=cmd/gc+untagged resource=slow_process_gate` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- it was last lowered on 2026-07-22 by upstream process-journey replacements and has grown by two since -- and its owner bead `ga-80po0c.2.3` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=cwd` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- its only large drop, on 2026-07-25, was the same census redefinition for `t.Chdir`, not a migration, and it has not moved since -- and its owner bead `ga-80po0c.2.1` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=environment` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- its only large drop, on 2026-07-25, was `6ccc7f9a2` redefining the census to stop counting the auto-restoring `t.Setenv`, not a migration, and the direct `os.*` count has grown by one since -- and its owner bead `ga-80po0c.2.1` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `small_debt scope=cmd/gc+untagged resource=slow_process_gate` | 2027-02-01 | retain | 2026-11-03 | the migration is not progressing -- it was last lowered on 2026-07-22 by upstream process-journey replacements and has grown by two since -- and its owner bead `ga-80po0c.2.1` belongs to the upstream epic `ga-80po0c`, which is in no store this city reads; retained because deleting a ratchet lets the count grow unobserved, and it closes when the count reaches zero, which first needs an owner (see the 2026-11-03 finding) |
| resourcecensus | `debt scope=untagged resource=fixed_sleep` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=http_test_server` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=net_listen` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=subprocess` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=tmux` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=fixed_sleep` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=http_test_server` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=net_listen` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `small_debt scope=untagged resource=subprocess` | 2027-02-01 | retain | 2026-11-03 | the resource is irreducible in the tests that own it, so the row is a spending budget rather than a migration; each increment was justified at the commit that raised it, and the row closes only if every owning test can be rewritten hermetically |
| resourcecensus | `debt scope=untagged resource=listener_helper` | 2027-02-01 | retain | 2026-11-03 | no longer flat: raised 38 to 39 on 2026-09-15 (ci-6mp9hs) for the `gc session reset` attached-session fixture, which needs a real controller socket, so the row is now a budget with its increment argued at the commit; it closes when the owning boundaries are replaced, and until then it stops another one appearing unargued |
| resourcecensus | `debt scope=untagged resource=net_listen_config` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `debt scope=untagged resource=net_listen_packet` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `debt scope=untagged resource=syscall_listen` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=listener_helper` | 2027-02-01 | retain | 2026-11-03 | no longer flat: raised 38 to 39 on 2026-09-15 (ci-6mp9hs) for the `gc session reset` attached-session fixture, which needs a real controller socket, so the row is now a budget with its increment argued at the commit; it closes when the owning boundaries are replaced, and until then it stops another one appearing unargued |
| resourcecensus | `small_debt scope=untagged resource=net_listen_config` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=net_listen_packet` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=syscall_listen` | 2027-02-01 | retain | 2026-11-03 | the count has not moved since the row was created, so nothing about it has become retirable; the row closes when the single owning boundary is replaced, and until then it is the only thing stopping a second one appearing |
| resourcecensus | `small_debt scope=untagged resource=tmux` | 2027-02-01 | retain | 2026-11-03 | already at 0 calls / 0 files, which makes it a terminal invariant rather than debt: retiring it would silently re-open the door it closed, so this row gets MORE load-bearing as the migration succeeds, not less |
| resourcecensus | `medium package_dir=cmd/gc package_name=main owner=TestMain` | 2027-02-01 | retain | 2026-11-03 | package-level setup of process environment and the tmux namespace for the whole `cmd/gc` suite; a stand-in cannot set state the tests under it read from the real process, and the row closes only when no `cmd/gc` test needs process-global setup |
| resourcecensus | `medium package_dir=cmd/gc package_name=main owner=TestPassthroughEnvWithholdsControllerTokenFromChildProcess` | 2027-02-01 | retain | 2026-11-03 | the session env is an overlay, so only a real child process can show `GC_CONTROLLER_TOKEN` is ABSENT rather than merely missing from a map; a stand-in would be asked the question the test exists to put to the kernel |
| resourcecensus | `medium package_dir=cmd/gc package_name=main owner=TestReleaseIfCurrentKeepsUnroutedWorkVisibleToPoolDemand` | 2027-02-01 | retain | 2026-11-03 | the demand predicate IS a shell pipeline over `bd` (`poolDemandCountShell`), so only running it can show a released bead is visible to the probe that decides whether a session is minted; a Go re-derivation of the same predicate would agree with itself whichever field the release wrote, which is precisely the producer/consumer mismatch the test exists to catch |
| resourcecensus | `medium package_dir=internal/api package_name=api owner=TestEveryEmittedErrorCodeIsRegistered` | 2027-02-01 | retain | 2026-11-03 | spawns `git ls-files` because a `WalkDir` over the root descends into the nested worktrees under `.gc/`, whose historical copies carry pre-migration URN literals; git's tracked set is the definition of shipped source, so the subprocess IS the specification |
| resourcecensus | `medium package_dir=internal/beads/contract package_name=contract owner=TestManagedPortOwnershipSeesARealListenerInThisProcess` | 2027-02-01 | retain | 2026-11-03 | the one case in its file that reads the REAL /proc socket table; the others read a procfs tree the test itself wrote and so agree with the fixture rather than with the kernel's formatting, which is the thing being parsed |
| resourcecensus | `medium package_dir=internal/beads/contract package_name=contract owner=TestValidManagedRuntimeStateSkipsTheDialWhenOwnershipAnswers` | 2027-02-01 | retain | 2026-11-03 | the claim is that a resolvable managed state costs the Dolt server no accept at all, and only a port this process genuinely holds can be put to the socket table that way |
| resourcecensus | `medium package_dir=internal/beads/contract package_name=contract owner=TestValidManagedRuntimeStateStillDialsWhenOwnershipCannotAnswer` | 2027-02-01 | retain | 2026-11-03 | the fallback exists for hosts that cannot attribute the socket, so the dial it makes must have a real listener to reach; retired when the dial fallback is |
| resourcecensus | `medium package_dir=internal/doctor package_name=doctor owner=TestCustomTypesCheck_TableDrift` | 2027-02-01 | retain | 2026-11-03 | manufactures and heals real config-CSV-vs-table drift against a throwaway store, so the bd and dolt subprocesses are the artifact under test; closed only if drift becomes observable without a store |
| resourcecensus | `medium package_dir=internal/doctor package_name=doctor owner=TestCustomTypesCheck_TableDriftUsesTestOwnedDoltContext` | 2027-02-01 | retain | 2026-11-03 | the invariant is that bd routes to a test-owned embedded store rather than the machine's shared server, which is a claim about a real process's resolution and cannot be posed to a stand-in |
| resourcecensus | `medium package_dir=internal/runtime/herdr package_name=herdr owner=TestServerAliveDetectsLiveServer` | 2027-02-01 | retain | 2026-11-03 | needs a real Unix socket inode with a live acceptor; the distinction it pins is made by the kernel on connect, so a stand-in listener would answer from the stand-in |
| resourcecensus | `medium package_dir=internal/runtime/herdr package_name=herdr owner=TestServerAliveRejectsStaleSocket` | 2027-02-01 | retain | 2026-11-03 | the same kernel-level distinction from the other side: a socket file whose listener was closed with `SetUnlinkOnClose(false)`, which is exactly what an uncleanly-exited server leaves and cannot be simulated above the socket |
| resourcecensus | `medium package_dir=internal/runtime/tmux package_name=tmux owner=TestMain` | 2027-02-01 | retain | 2026-11-03 | owns the isolated tmux socket and its cleanup for the whole package; the suite's subject IS tmux, so the setup cannot be replaced by a fake executor without deleting what the suite measures |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestDashboardCheckRunsFrontendVitestSuite` | 2027-02-01 | retain | 2026-11-03 | must read the EXPANDED `make -n` recipe, because a step reached through a prerequisite or a variable is invisible to a Makefile grep; only make can expand its own recipe |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestDockerSessionProtocol` | 2027-02-01 | retain | 2026-11-03 | the artifact under test is a shell adapter, so it must be executed; docker itself is already a strict PATH-injected fake, which is the hermetic half of the boundary |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestProviderOverridesAndSuiteContractsCrossMakeIsolation` | 2027-02-01 | retain | 2026-11-03 | six isolated make invocations are the subject: the property is that provider overrides do not leak ACROSS make processes, which no single-process test can represent |
| resourcecensus | `medium package_dir=scripts package_name=scripts_test owner=TestWithGoTmpRemovesOwnedDirectoryOnTermination` | 2027-02-01 | retain | 2026-11-03 | must observe a real SIGTERM and verify the wrapper removes its private `GOTMPDIR`; signal delivery and the cleanup that races it exist only in a real process |
| resourcecensus | `medium package_dir=test/tmuxtest package_name=tmuxtest owner=TestSweepOrphanKillsTmuxServerBeforeRemovingItsSocketDir` | 2027-02-01 | retain | 2026-11-03 | starts one real tmux server, orphans it, and asserts the PROCESS is gone; a fake executor cannot demonstrate that deleting a socket leaves a live server behind, which is the whole defect |
<!-- END TEST POLICY WAIVER DECISIONS -->
