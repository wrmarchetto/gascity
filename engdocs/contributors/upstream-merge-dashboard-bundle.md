---
title: Merging upstream through the committed dashboard bundle
description: Why an upstream merge conflicts inside internal/api/dashboardspa/dist, why those conflicts are never read, and the three commands that resolve them.
---

A merge from `upstream/main` into this fork conflicts overwhelmingly inside
`internal/api/dashboardspa/dist`, the committed Vite bundle that
`internal/api/dashboardspa/embed.go` embeds into the `gc` binary. Those
conflicts are **never resolved by reading them**. The tree is generated output,
so the resolution is to throw it away and rebuild it from the merged sources.

This page exists because the cost lands on whoever next merges rather than on
whoever wrote the dashboard change, and nothing else in the repository records
that the cheap resolution exists. Without it the reader sees dozens of
rename/rename conflicts over minified JavaScript and reasonably concludes
something is wrong with the toolchain -- the same wrong conclusion that cost
about an agent-hour on ci-c425.

## The procedure

Resolve every other conflicted path first, so the sources you rebuild from are
the merged sources. Then, from the repository root:

```bash
git rm -r --cached --ignore-unmatch internal/api/dashboardspa/dist
make dashboard-build
git add internal/api/dashboardspa/dist
```

`git rm --cached` is what clears the unmerged index entries, and
`--ignore-unmatch` is there so the step is safe to re-run after a partial
resolution. Do **not** add an `rm -rf` of the directory: `dashboard-build`
already ends in `rm -rf ../dist && cp -rf frontend/dist ../dist`, so it wipes
and recreates the tree itself. Verified on the merge measured below -- the
staged result was 23 renames and one modified `index.html`, with no stray
additions, which is what a leftover stale chunk would have shown up as.

Run `make dashboard-ci` before committing. It rebuilds and then judges the
result through `scripts/check-artifact-drift.sh`, which is the gate that
actually proves the committed bundle matches the committed sources -- the
rebuild above proves only that a bundle builds.

## Why the conflicts look the way they do

Rollup folds each chunk's content hash into the filenames of every chunk that
imports it, so one source edit reaching the entry chunk renames the entire
asset tree above it. Both sides of the merge therefore rename the same base
file to two different names, git pairs the base against both, and the result is
`rename/rename`.

Measured 2026-08-10, merging `upstream/main` (`f08858b8a`) into `origin/main`
(`9187121b3`) across merge base `76123b700`, with upstream 24 commits ahead and
2 of those touching `internal/api/dashboardspa/`:

| Conflicted paths | Where |
| --- | --- |
| 66 | `dist/assets/**`, as 22 components x 3 paths (`DD` + `AU` + `UA`) |
| 1 | `dist/index.html`, a content conflict (`UU`) over the entry `<script>` |
| 3 | three files under `cmd/gc/`, named below |
| **70** | total |

Dropping `dist` from the index takes that 70 to 3. The three that remain --
`pool_session_name.go`, `work_assignment.go`, and
`init_from_hosted_dolt_test.go`, all under `cmd/gc/` -- are ordinary
fork-vs-upstream source conflicts with nothing to do with the dashboard.

The rebuild that replaces the other 67 took 10.5 s and 12.8 s wall clock across
two runs in a fresh worktree, and produced 35 assets including the fork's
`Accounts` chunk, with `index.html` pointing at an entry chunk that exists.
Treat those seconds as a floor rather than an estimate: both runs hit a warm
npm cache, and `dashboard-build` starts with `npm ci`, so a cold cache pays the
install too.

The bundle is declared generated in `.gitattributes` (`-diff
linguist-generated`), which suppresses textual diffing and collapses the tree
in review UIs. It does not and cannot prevent the conflicts: `-diff` governs
content presentation, while `rename/rename` is a tree-level verdict reached
before any content is examined.

## What is not the fix

- **Resolving the hunks.** Nothing in a minified chunk is authored, so a
  hand-merged bundle is at best a bundle no source produces. The drift gate
  then fails it.
- **A `merge=ours` driver on the path.** Merge drivers run per file on content
  conflicts. `rename/rename` never reaches one, so 66 of the 70 paths above
  would be untouched by it.
- **`scripts/rebase-resolve-lib.sh`.** The deployer's bounded self-rebase
  auto-resolver handles only `UU` and `AA` and refuses `DD`, `AU`, and `UA`
  explicitly. That refusal is correct -- a wrong auto-resolve silently corrupts
  the branch -- so it will bail on every upstream dashboard merge by design,
  not by omission.
- **Untracking the bundle.** It would remove the whole conflict surface, and it
  is a real option, but it is a decision rather than a cleanup: `//go:embed
  all:dist` fails to compile when the directory is absent, so a `go build` in
  any checkout that has not run `make dashboard-build` would stop working. The
  bundle is committed precisely so a Node-less build still yields a working
  dashboard.

## What this costs, and what would retire the page

The toll is flat per merge, not proportional to the fork's dashboard changes.
Commit `c5c9856e6` on `fix/ci-aigx-dashboard-branding` changed two string
literals and rotated all 23 hashed asset filenames -- a one-word rebrand and a
whole new tab price identically at merge time.

The source side is the opposite, and is the reason this stays cheap. The fork's
Accounts tab (ci-gpxg, ci-c0cu) is 1567 added lines over 13 files, of which
1335 sit in 5 fork-owned new files, and the 8 upstream-owned files it edits
take 232 insertions against 6 deletions. Nearly-pure additions rebase cleanly,
and in the merge measured above **none of those 8 files conflicted**.

Delete this page if the fork stops committing the bundle, which would remove
the conflicts rather than resolve them.
`scripts/dashboard_bundle_generated_tree_test.go` fails and names this page
when that happens, so the procedure cannot outlive its premise silently.
