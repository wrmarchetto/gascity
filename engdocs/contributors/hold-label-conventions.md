---
title: Hold and Blocked Label Conventions
description: The canonical hold/blocked label taxonomy for this project's own bd tracker — which label to use, when to use status or a dependency edge instead, and what happened to the old ad hoc labels.
---

## Why this exists

Before 2026-07-14 this repo's bd tracker had accumulated at least 8 overlapping
ad hoc "hold"-family labels with unclear, possibly inconsistent semantics
(`arch-hold`, `blocked`, `blocked-by-operator`, `blocked-on-external`,
`blocked-on-upstream`, `blocked-prereq`, `human-hold`/`human`, `on-hold`),
alongside the one label that already followed the sanctioned convention,
`hold:mayor`. `ga-tug8ry` audited and consolidated them down to two canonical
values; `ga-tug8ry.2` migrated every live bead onto the result. This page is
the durable reference so nobody reinvents another ad hoc hold label — if
you're about to pause a bead and reach for a new label name, stop and use one
of the two values below instead.

Full rationale and the live census this decision was based on:
`bd show ga-tug8ry.1` (the decision) and `bd show ga-tug8ry.2` (the
migration record, including before/after counts).

## Three orthogonal "not ready" mechanisms

A bead can be "not simply ready to work" for three structurally different
reasons. Pick the mechanism that matches *why* you're pausing it, not just
"it's blocked":

| Mechanism | How to set it | Meaning |
|---|---|---|
| Dependency edge | `bd dep add <a> <b>` | Bead A cannot start until bd-tracked bead B closes. Gates `bd ready`. Computed from real edges, not a manual claim. |
| Bead status | `bd update <id> --status blocked` | "I cannot currently proceed," with no further structure about why or who must act. |
| `hold:<value>` label | `bd set-state <id> hold=<value> --reason "..."` | "I am paused pending a specific actor or condition." Structured, audited (files an event bead), and names the *who*. |

These are orthogonal and combine freely — a bead can be `status=blocked`
**and** `hold:external` at the same time. Use a dependency edge when the
blocker is itself a bd bead; use `status=blocked` when nothing more specific
applies; use `hold:<value>` only when a specific actor or external condition
is the actual reason you're paused.

## Canonical `hold:<value>` values

Only two values are canonical. Don't introduce a third without a new
architecture decision — see `ga-tug8ry.1` for the reasoning that narrowed
the taxonomy to these two.

- **`hold:mayor`** — the required next actor is the mayor. Covers both
  mayor-initiated pauses and automation-escalated-to-mayor cases; both are
  the same operational state ("nothing proceeds until the mayor acts") and
  share one value rather than being split in two.
- **`hold:external`** — the required next actor or condition is outside this
  bd instance's control (an external repo's maintainers, an upstream PR
  merge, etc.). Established by `ga-h7hnpt`.

Set either with the sanctioned command — never with a plain `bd label add`:

```bash
bd set-state <id> hold=mayor --reason "why, and who/what unblocks it"
bd set-state <id> hold=external --reason "why, and who/what unblocks it"
```

`bd set-state` removes any existing label in the `hold:` dimension, adds the
new one, and files an audit event bead. It does **not** touch `status`,
`owner`, or `metadata` — update those separately (or add a dependency edge)
if they also need to change.

## What a hold label does to dispatch

Reach for this section when the need is "record who should eventually own this
bead, without that record being an instruction to start it now." A hold label
is the only one of the three mechanisms above that expresses it: it removes the
bead from automatic dispatch while leaving `assignee`, `gc.routed_to` and the
audit trail exactly as written.

That distinction matters because for everything else in this system, addressing
a bead IS starting it. The reconciler sizes a pool from the same `assignee` and
`gc.routed_to` fields a filer uses to say who the work belongs to, and the
`unclaimable-work` doctor check reports a ready bead carrying neither. So a
decomposition that proposes beads for a verdict has no unlabelled way to sit
still: filed unaddressed it is reported as stranded, filed addressed it is
picked up before the verdict lands. `ci-t98zgv` was opened on the belief that
no lever existed at all.

```bash
bd set-state <id> hold=mayor --reason "awaiting DECOMP verdict; mayor clears"
```

Both directions of the guarantee are enforced, and both are pinned by
`TestRecordedOwnerWithHoldRaisesNoSpawn` in
`cmd/gc/build_desired_state_recorded_owner_test.go`, which runs each held case
beside an identical unheld control so a change that suppressed *all* demand
fails there rather than in production:

- A held bead raises no pool demand and produces no desired session, whether it
  records its owner by assignee or by route. Enforced in the in-process
  controller reader (`hasDispatchHoldLabel`, `cmd/gc/pool_alias_demand.go`) and
  independently in every generated shell predicate
  (`excludeHoldLabelsShellArgs` / `excludeHoldLabelsJQClause`,
  `internal/config/workquery.go`). Both halves are required: the reconciler
  counts in Go for a default probe and shells out for a custom `scale_check`,
  and dispatch.md invariant 11 forbids the two disagreeing.
- An ordinary addressed bead still sizes its pool. Nothing about the hold path
  narrows what an unlabelled assignee means.

**The limit, which is deliberate and must not be read as a gap.** The
assignee-scoped work tiers — Tier 1 crash recovery and Tier 2 assigned-ready —
are hold-transparent and must never filter on this list. A hold names the actor
who has to move next, so that actor reaching its own held work is the mechanism
working, not leaking. The consequence to plan around: for an agent whose own
identity IS the recorded owner (a singleton pool, or a `[[named_session]]`
holder addressed by its bare name), a hold stops a session from being *spawned*
for the bead but not a live one from *claiming* it. `poolDesiredRequestIdentity`
(`cmd/gc/build_desired_state.go`) is where that turns: it hands a singleton pool
its bare qualified name as `GC_ALIAS`, and above one slot the same call returns
a suffixed slot name instead, after which the bare pool name is reachable only
through the route-scoped tier, which excludes holds. So raising
`max_active_sessions` past 1 silently changes this behavior.

**The sibling lever, and when to prefer it.** bd's status-based indefinite
deferral (`bd update <id> --status deferred`, no `defer_until`) also holds a
bead without touching its assignee, and `Ready()` drops it outright — see the
`StatusDeferred` branch in `internal/beads/native_dolt_store.go`, which
resurfaces only a deferral whose `defer_until` has expired. It is the blunter
of the two: the bead leaves every ready and queue view rather than staying
visible as paused-on-a-named-actor, and nothing records who is expected to
clear it. Prefer a hold label when the pause has an owner; prefer deferral when
the bead should be out of sight until someone deliberately goes looking.

## Retired labels

These labels are legacy. If you see one on a live bead, treat it as drift
worth a bug report, not a pattern to follow.

| Legacy label | Replace with | Notes |
|---|---|---|
| `blocked-by-operator` | `hold:mayor` | "Operator" meant the human operator/mayor seat. |
| `blocked-on-upstream` | `hold:mayor` | Means "next step in our own merge pipeline," not an external repo — despite the name, this is not a `hold:external` synonym. |
| `human-hold`, bare `human` | `hold:mayor` | Both named the same "next actor is mayor" state as a bare label. Caution: a bare `human` label can also appear alone for an unrelated reason (a human merge/PR action needed) that is not a hold state at all — check the bead's own context before assuming `human` implies a hold. |
| `blocked-on-external` | `hold:external` | Direct predecessor of `hold:external`; carry forward any `blocker_scope`/`external_blocker`/`external_pr`/`pr`/`repo` metadata unchanged. |
| `blocked` | none — use native `status=blocked` | Redundant with the bead's own `Status` field; keeping both invites drift between them. |
| `arch-hold` | none — owned by the `maintainer-pr-review` pack | Not a generic bd hold; it's that pack's own gate, cleared via `gc maintainer-pr-review clear-hold`. It only looked like one of ours because it lacks the `mpr-` prefix its sibling `mpr-human-hold` carries. |
| `blocked-prereq` | none today; if it recurs, use a dependency edge (prerequisite is a bd bead) or `hold:external` with PR numbers recorded in metadata (prerequisite is bare GitHub PR numbers) | Historical: blocked on specific GitHub PRs merging first, with no corresponding bd bead. |
| `on-hold` | none — already superseded | Any bead needing this should already carry the canonical `hold:mayor`/`hold:external` in its place. |

**Explicitly out of scope — do not migrate these, they mean something
different:**

- `mpr-human-hold` and other `mpr-*` labels — owned end-to-end by the
  `maintainer-pr-review` pack, with its own metadata namespace and its own
  clearing tool. Not a generic bd hold label.
- `build-blocker`, `ci-blocker`, `pre-push-blocker`, `push-blocking`,
  `test-blocker` — a different semantic axis ("pipeline stage X is red
  because of me"), not "I am waiting on decision-maker Y."
- `needs-mayor` / `needs-mayor-decision` — a routing/queue-placement label
  (parallel to `needs-architecture`, `needs-design`, `needs-pm`,
  `ready-to-build`), not a pause-state label. It may legitimately co-occur
  with `hold:mayor`.

## This is a data convention, and the SDK reads it role-neutrally

Nothing on this page requires special-casing any role name in Go. Which value
to reach for, and what counts as cleared, are settled by this document, PR
review, and `bd set-state`'s dimension semantics — never by SDK code.

The two literals do appear in Go, in exactly one place:
`internal/beadmeta/hold_labels.go` declares them and `DispatchHoldLabels`
collects them, and every enforcement point named in the section above reads
that list rather than a literal of its own. Gas City's "ZERO hardcoded roles"
invariant is unaffected, because the dispatcher tests for the label's
*presence* and never for who "mayor" is — it cannot tell the two values apart
and does not try (ga-5736js). Adding a third value would be a data change plus
one line in `beadmeta`; it would not teach any dispatch path a new role.

## See also

- `bd show ga-tug8ry.1` — the architecture decision: full live census,
  per-label disposition rationale, and a label-flow diagram.
- `bd show ga-tug8ry.2` — the migration record: before/after counts and the
  beads intentionally skipped (bare `human` used for an unrelated reason).
- [Beads architecture](../architecture/beads.md) — the generic `Label` and
  `Store` mechanism this convention is built on.
- `cmd/gc/build_desired_state_recorded_owner_test.go` — the both-directions
  gate behind "What a hold label does to dispatch", and the pin on the
  singleton-pool limit that section warns about.
