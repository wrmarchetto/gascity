---
title: Triaging a closed bead whose work never reached main
description: How doctor/closed-bead-unlanded attributes work to a bead, why deleting a superseded ref makes its row worse, and the verdicts already recorded for this rig.
---

`doctor/closed-bead-unlanded` starts from the **bead** and asks about the
tree: a closed bead whose commits are not on local `main` reads as FIXED to
`bd ready`, to the governor's starved-pool probe and to every other
instrument that only ever queries the bead store. The check exists because
none of those can see a tree.

Read this before acting on one of its rows. The obvious tidy-up -- delete the
stale branch -- is wrong for most of them, and it is wrong in the direction
that raises the severity.

## Attribution decides which action is available

`assets/scripts/closed-bead-landing-check.py` in the city repository
attributes a bead's work in three arms, strongest first, and **rules 2 and 3
are skipped for any bead rule 1 already spoke for in that repository**:

1. a ref whose NAME contains the bead id, local or remote-tracking
2. `gc.work_commit`, and `gc.work_branch` when it is not the integration ref
3. hex tokens in the bead's own CLOSE REASON, resolved as commits

Which arm caught a row is printed in the row itself -- `(1 via ref-name)`,
`(1 via close-reason)`. That label, not the branch, is what decides whether
any action can clear the row.

## Deleting a prose-cited ref trades a warning for an error

The severity arms follow what is being mis-scheduled, and one of them is
`unmerged AND unreferenced by any ref is an ERROR` -- those commits are one
`git gc` from pruned and no other instrument can see them.

So for a bead whose close reason names its own superseded sha, deleting the
ref that holds it does not clear the row. It moves the row off rule 1, onto
rule 3, and onto the ERROR arm. The tracker's most tempting cleanup is the
one that turns a silent warning into a blocking red.

MEASURED on `gs-dzj`, 2026-09-13, rather than read off the severity table.
`recover/gs-dzj` was the only ref holding `f2f45af63`, and the bead's close
reason names that sha alongside the landed one. Deleting the branch and
re-running the check:

| | before | after |
| --- | --- | --- |
| exit status | 1 (warning) | **2 (error)** |
| the row | `[unmerged] ... (1 via close-reason)` | `[unmerged-unreferenced] ... held by NO ref or worktree` |

The branch was restored from the recorded sha in the same minute and the
check returned to exit 1 with the row back on its warning arm. Note what the
error costs beyond the row: `gc doctor` exits nonzero, and
`orders/doctor-findings-sweep.toml` polls it every 5m and escalates an ERROR
to the mayor -- so the cleanup files a summons within five minutes.

The test is mechanical: dump the close reason's hex tokens before touching a
ref.

```bash
bd show <bead> --json | python3 -c '
import json,re,sys
row=json.load(sys.stdin)[0]
print(sorted(set(re.findall(r"\b[0-9a-f]{7,40}\b", row.get("close_reason") or ""))))'
```

A token that resolves to a commit off `main` is a row you cannot delete your
way out of.

## The check does not read `gc.merge_disposition`

`merge-closed-features.py` honors `gc.merge_disposition` (`defer` or
`superseded`) and skips such a branch, calling it an authored judgment about
the work rather than a conclusion drawn from prose.
`closed-bead-landing-check.py` never reads that key.

The two instruments therefore disagree by design: a branch can be
deliberately held out of the merge sweep and still be reported unlanded here
on every run, forever. Setting the disposition is still right -- it is the
durable record of the judgment and it is what stops the sweep -- but it is
NOT a mute, and nobody should set one expecting the row to go away.

## What is left when no action clears a row

Nothing, and that is the intended end state rather than a gap. The condition
is carried in `assets/scripts/closed-bead-unlanded.baseline.json` in the city
repository, which is what lets a newly-unlanded bead escalate while a known
permanent row stays quiet. A row triaged to `superseded` with its sha in the
close reason belongs in that baseline and should stay there.

## Verdicts recorded for this rig (gs-1soi, 2026-09-13)

Nine rows, triaged against local `main` at `967922634`. Every "superseded"
below was established by reading the landed commit, not by trusting a close
reason. The check went from 78 unlanded rows to 75 across the three actions
that had one available; the other six have none, for the reason above.

| Bead | Attributed by | Verdict |
| --- | --- | --- |
| `ci-cfu3es` | close-reason `72d12b390` | no work exists -- see below |
| `ci-m2du2o` | ref-name | superseded by `69ad854e8`; ref moved to `salvage/` |
| `ci-ui81lu` | ref-name x2 | superseded by `9b9063199` and `f9b0bcef2`; refs deleted |
| `ci-zspx` | ref-name | landed by this branch; original ref retired |
| `gs-22c` | close-reason `02c043b66` | no work of its own is unlanded |
| `gs-33z` | close-reason `a4cca6771` | superseded by `cf2ef5676` |
| `gs-8ra` | close-reason `5ba13f57b` | superseded by `9b34e5a51` and `8e8c9c644` |
| `gs-c6f` | close-reason x2 | superseded by `992fe4f33` |
| `gs-dzj` | close-reason `f2f45af63` | superseded by `9fe59f072` |

Two of the nine are **mis-attributions**, and they are the shape rule 3 warns
about rather than a defect in it. `ci-cfu3es` closed with "no branch and no
code change"; the sha in its row is prose recording that it detached from
another bead's salvage branch and left it intact. `gs-22c` is a PM placement
summons whose own work is `475790bd4`, on `main`; the sha in its row is
context cited in the same sentence. In both cases the commit's own message
names neither bead, so the tie-break that drops prose claims had nothing to
discriminate with and kept them.

Four of the nine -- `gs-33z`, `gs-8ra`, `gs-c6f`, `gs-dzj` -- are a single
pattern: an agent committed the work, refined it, landed the refinement under
a different sha, and named BOTH shas in the close reason. The superseded one
sits on a `recover/`, `gate/` or `salvage/` ref and is cited forever.

### The absence worth naming

`72d12b390` carries `reapSessionRuntimes`, an END-of-life counterpart to
`killExistingOrphans` that reaps any runtime still holding an ending
session's id. It is on `salvage/toolsmith-3-2026-09-05` and on no other ref.
What landed for `ci-sptsk3` instead (`2b6c6ab49`) reaps a managed dolt server
whose SCOPE nothing is using any more, which closes that bead's measured
symptom but leaves the general case -- any runtime outliving its session --
uncovered. Nobody owns that gap today. It is recorded here rather than filed
because the bead that would have owned it is closed and its symptom is fixed.
