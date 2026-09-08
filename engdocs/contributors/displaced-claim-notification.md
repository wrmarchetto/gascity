# Who may interrupt a running session, and how a displaced holder is told

Decision record for ci-32fp1p, the deferred half of ci-q5spdz item 1. Read
this before adding mail, a nudge, or an event to any bead-write path.

## The situation

`POST /v0/city/{cityName}/bead/{id}/assign` writes the assignee
**unconditionally**. That is deliberate and settled: the handler has no
earlier read to compare against, so an operator's intent is current by
construction and a compare-and-swap would invent a race. Taking work back
from a stuck agent is what an operator tool is for
(`humaHandleBeadAssign`, `internal/api/huma_handlers_beads.go`).

ci-q5spdz made that visible **to the caller** -- the reply and the server log
name the holder displaced. It left the other half open: the displaced agent
is mid-turn, believes it holds the bead, and nothing tells it.

## The decision

**The displaced holder learns at the boundary it already crosses. Nothing
interrupts a running session, and no bead-write path emits mail, a nudge, or
an event.**

Concretely: `gc bd release-if-current` -- the hand-back every worker prompt
routes a blocked bead through -- now names the holder that took the claim,
instead of printing `skipped` and exiting 0.

The exit status stays 0. The agent's intent was *do not release a bead
someone else holds*, and that intent was honored; a nonzero exit would turn
every correct refusal into a failed step for callers that check it.

## Why not a nudge

It interrupts a turn, and the project's own principles say the agent finds
out by reading persistent state:

- *The system converges because work persists.* Sessions come and go; the
  work survives. A displaced agent's next read is the notification.
- *If you find work on your hook, you run it.* The hook is already the
  channel through which a worker learns what it owns.

A nudge also arrives at an arbitrary point in the agent's reasoning, which is
the worst moment to tell it that a premise changed.

## Why not mail from the bead handler

Three reasons, and each is independently sufficient:

1. **`internal/api` emits no messaging side effects from any bead write, and
   no bead handler publishes even an event.** Making this the first would
   invent a pattern for a single case. Every existing bead-claim anomaly
   event -- `bead.claim_rejected`, `bead.dead_assignee_reopened`,
   `session.drain_acked_with_assigned_work` -- is emitted from `cmd/gc/`
   (the hook, the reconciler), never from the API layer. Verified by grep:
   `internal/api/` contains only the `RegisterPayload` calls.
2. **Mail resolution needs a live session.** `gc mail send` resolves one
   before storing anything, so a holder whose session has already gone
   returns `session not found` and nothing is saved. A handler that mails on
   displacement would have a failure mode it cannot act on, on the exact path
   where the displacement matters most.
3. **Layering.** Side effects are confined to Layer 0; the API is a
   projection over the object model. A bead write is not the place.

## Why not a new event on its own

It would satisfy observability and not the stated gap. An agent does not
watch the event bus, so `bead.claim_displaced` would tell dashboards and tell
the displaced holder nothing. It is also not free: every constant in
`events.KnownEventTypes` needs a registered payload
(`TestEveryKnownEventTypeHasRegisteredPayload`).

Not ruled out as a **later** addition for the dashboard. Ruled out as the
answer to "the displaced agent is not told."

## The CLI half is out of gc's scope, and the seam already exists

`gc bd update <id> --assignee X` reaches the same effect. It needs no gc
change, because `update` is already in `bdPreWriteVerbs`
(`cmd/gc/bd_prewrite.go`): every such write is handed to the city's
configured `pre_write_command` as the exact bd argv, with `GC_CITY` and
`GC_STORE_ROOT` in the environment, and a non-zero validator exit refuses the
write before bd runs.

So a city that wants to refuse or report an assignee steal on the CLI writes
a validator. That is city-local policy, not SDK behavior -- which is the
correct side of the *keep judgment out of Go* line.

**What that seam does NOT cover**, stated so it is not trusted past its
reach: `assign`, `label`, `set-state`, `edit` and `batch` are deliberately
absent from `bdPreWriteVerbs` (they carry no `internal/bdflags` manifest, so
their argv cannot be scanned safely), and `create -f/--file` builds beads
from a file no argv scan opens. Intercepting bd's own verbs is a larger
question about how much of bd's surface gc wraps, and this decision does not
open it.

## What is still not covered

`gc bd close` is a pass-through -- gc intercepts only `heartbeat` and
`release-if-current` (`registerBdIntercept`, `cmd/gc/bd_intercepts.go`) -- so
an agent that closes a displaced bead without releasing it first is still not
told. That is the same "intercept bd's own verb" question as above. The
release path was chosen because it is the boundary the worker prompts already
route a blocked bead through, and because it is gc-owned today.
