# City bead cycle time: the non-development time to main

epic: bead-cycle-time

## what this was for

Finished work took far too long to reach main, and most of that time was
not development. The aim was to measure, in every rig of the city, where a
bead's time goes between its last code commit and its landing on main, and
then to cut it. The starting suspicion was testing: the full suite seemed
to run more than once per bead, and the gascity suite had roughly tripled
in six months. The goal Willie set was simple. Everything should land
within an hour of its last commit, and sooner is better.

## what was done

A baseline was measured first, per rig and per stage, before any change
landed. It showed that most of the waiting was in the merge queue rather
than in tests, so the epic was turned toward the queue. The merge sweep
was changed so one rig's slow gate no longer starves the others, so a
rig's finished branches are gated and landed together in one run with
bisection on failure, and so it no longer rests a fixed half hour between
passes. The full test suite now runs once per landing, inside the sweep,
over the combined result. In-session and push-time test runs in gascity,
astoria-sel4 and astoria-zephyr run only the tests a change can reach.
Each rig with a material suite had its slowest tests ranked and sped up
before anything was pruned, and gascity's whole merge gate was brought
under the time a sweep pass can give it. A close gate now refuses a bead
that adds test functions without saying why no existing test could be
extended, and the engineer and toolsmith prompts carry the same rule.
The end-to-end time was then measured twice after the changes, and each
rig still over the hour got a fix aimed at the stage carrying its miss.

## what was found

The queue, not the test suite, was where most of the time went. The
baseline puts that share per rig, and the queue-wait decomposition
breaks it down by cause. After the queue changes, the
remaining misses were specific rather than general. City work waited on
review rounds that sat finished for hours until the mayor happened to
wake. Zephyr sat behind a broken main caused by a change in another rig's
shared code. sel4's gate was being killed at a time cap its suite had
outgrown. Each figure lives in the cohort report and in the
close reason of the bead that fixed that stage. One queue change, running
rigs' gates side by side in lanes, never fired in the first
post-change window, because a full pass needs more cores than the host
has. That holds for this host and that load, as the post-change report
states. gascity and dart had too few landings in the second window to be
measured at all, so their verdicts are absent rather than passing.

## where it ended

Five of the six acceptance criteria are met, as the self-review record
lays out criterion by criterion. The one-hour target is not met. At the
last measurement it missed in city, astoria-sel4 and astoria-zephyr. The
fixes for each of those misses have since landed, but nothing has
measured a window after them, so whether they close the gap is still a
prediction. A follow-up bead places that re-measurement. gascity's own
half of the target waits on gascity landings, and none are queued because
no further gascity epic is planned. Whether it can be judged on a small
sample is an open question for Willie. The test-growth gate's runtime
half reads as unmeasured on most closes, since only a full local run
writes the record it reads. sel4's affected-test table is stale enough
that its affected runs still select most of the suite until it is
re-recorded. Zephyr's affected runs execute their gates one at a time,
which a separate zephyr bead covers outside this epic.

## references

- docs/self-review/bead-cycle-time.md -- the criterion-by-criterion pass,
  findings and bounds
- city docs/bead-cycle-time-baseline.md -- the baseline (ci-npfgao)
- city docs/merge-queue-wait.md -- queue wait decomposed before (ci-bq7qvz)
- city docs/bead-cycle-time-post-change.md -- first post-change window
  (ci-r81svr)
- city docs/bead-cycle-time-cohort.md -- second window, per-rig misses
  (ci-cj9utx)
- city docs/merge-sweep-batching.md -- lanes, batching, bisection
  (ci-ms65m7, ci-sx056l, ci-cicsvc, ci-3imbeu)
- city docs/test-growth-gate.md -- extend-before-add close gate (ci-ufxl74)
- city docs/fleet-review-gate.md -- review-round relay fix (ci-ce84ou)
- ci-5c95j4, ci-fb3uff, ci-tr56b0 -- sel4 gate cap and zephyr red-main
  stage fixes
- gascity TESTING.md, "Scoping the push gate" -- affected-only push gate
  and its fallback rate (gs-rvw5, gs-0y4d)
- gs-zle6, gs-okl8, gs-sch3, gs-gln1, gs-46s4, gs-sodh -- gascity suite and
  merge-gate speed work
- as-8384y, as-hq04p, az-8ujz, az-r41s -- astoria affected runs and suite
  speed
- ci-71d5yl -- the open re-measurement of the target
- az-20v1f -- zephyr serial affected runs, outside this epic
- pm-log #148 to #195 -- the rulings that shaped the criteria
