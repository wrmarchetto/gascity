# Hook Event Vocabulary

Gas City wires per-provider hook configs into a small set of coordination
commands (`gc prime --hook`, `gc handoff --auto`, and prompt-submit
`gc hook run --timeout 15s --timeout-exit-code 0 -- ...` wrappers around
`gc nudge drain --inject` / `gc mail check --inject`). Each provider names its hook events differently;
this document maps Gas City's canonical events to the provider's native
name for each, plus where the wiring lives on disk.

The mapping exists primarily so future contributors can audit coverage
gaps at a glance — see [`gastownhall/gascity#672`](https://github.com/gastownhall/gascity/issues/672)
("non-Claude provider parity") for the audit that motivated it.

## File layout

Provider hook configs live in two places:

- `internal/hooks/config/claude.json` — Claude-specific settings (this directory).
- `internal/bootstrap/packs/core/overlay/per-provider/<provider>/…` — every other
  provider, scoped under that provider's expected dotfile path
  (e.g. `codex/.codex/hooks.json`, `cursor/.cursor/hooks.json`).

Installation walks the pack overlay during `gc start` / `gc rig boot`,
materializing the per-provider files into each agent's working directory.

## Event mapping

✓ = wired today. — = not wired (either the provider does not expose
the event, or it does but Gas City has not opted in yet).

| Canonical event | claude | codex | cursor | copilot | gemini | antigravity | opencode | omp | pi | kimi |
|---|---|---|---|---|---|---|---|---|---|---|
| session start    | `SessionStart` ✓ | `SessionStart` ✓ | `sessionStart` ✓ | `sessionStart` ✓ | `SessionStart` ✓ | `PreInvocation` ✓ | `session.created` ✓ | `session_start` ✓ | `session_start` ✓ | `SessionStart` ✓ |
| pre-compaction   | `PreCompact` ✓   | `PreCompact` ✓   | `preCompact` ✓   | `preCompact` ✓   | `PreCompress` ✓  | — | `session.compacted` ✓ | `session_compact` ✓ | `session_compact` ✓ | — |
| user prompt submit | `UserPromptSubmit` ✓ | `UserPromptSubmit` ✓ | `beforeSubmitPrompt` ✓ | `userPromptSubmitted` ✓ | — | — | — | — | — | — |
| before agent run | —                | —                | —                | —                | `BeforeAgent` ✓  | `PreInvocation` ✓ | —                | `before_agent_start` ✓ | `before_agent_start` ✓ | — |
| turn end (stop gate) | `Stop` ✓ | — | — | — | — | — | — | — | — | — |

### Gas City command bindings

For each provider where a row above is ✓, the wired command is one of:

- **session start** → `gc prime --hook` (loads context, drains hooks).
- **pre-compaction** → `gc handoff --auto "context cycle"` (capture state
  before the provider compacts the conversation).
- **user prompt submit** / **before agent run** → bounded `gc hook run`
  wrappers around `gc nudge drain --inject` and/or `gc mail check --inject`
  (inject pending agent-to-agent messages into the upcoming prompt without
  letting a wedged data-plane command block the provider hook).
- **turn end** → a bounded `gc hook run` wrapper around `gc hook stop`
  (refuse to end a turn while the session's closing contract is unfinished).

## Why a Stop hook is wired when other Stop hooks deliberately are not

Operators commonly keep their own `Stop` hooks — a test-suite gate, a
formatter, a review gate — deliberately UNWIRED for agent sessions, because
firing a test suite or blocking a commit at an agent's turn boundary breaks
work the operator is supervising. `gc hook stop` is not that kind of hook and
the distinction is what keeps it from being reverted as drift:

- It **reads**. One work-query per turn end, no writes, no events, no
  processes spawned beyond the query itself.
- It **blocks nothing that was going to succeed**. Its only blocking verdict
  is "this session claimed a bead and is ending its turn without closing it",
  which is a defect in every case — including the case where the operator is
  supervising.
- It is **bounded and fails open**. The `gc hook run` wrapper caps it at 15s
  with `--timeout-exit-code 0`, and every fact the gate cannot establish
  resolves to "let the turn end". A store outage cannot wedge the fleet.
- It is **self-limiting**. The provider re-enters with `stop_hook_active`
  set, which the gate honors first and unconditionally, so a genuinely stuck
  agent is blocked at most once per stop sequence.

The bug it exists for: an agent finishes its engineering work, writes a
summary, and ends the turn with the rest of its contract — set result
metadata, close the bead, `gc runtime drain-ack` — never run. Nothing
recovered that state. The nudge machinery is wired to `UserPromptSubmit`,
which fires only when a prompt IS submitted, so the sole recovery path
structurally cannot reach a session that has stopped submitting, and
`gc status` reports the session running, which is true and useless. For an
agent at `max_active_sessions = 1` the stall blocks its whole queue.

Adding prompt text was the reflex to resist: the prompts already state the
contract plainly and give the closing block verbatim. A rule with no gate
behind it rots. See `cmd/gc/cmd_hook_stop.go` for the four editing
constraints and `cmd/gc/cmd_hook_stop_test.go` for the tests that pin them.

Some providers fold both injection commands into a single hook entry;
others split them. The exact wiring lives in the per-provider config —
this README only documents the event vocabulary, not the command shape.

Antigravity currently exposes `PreInvocation` for before-model-call
injection. Gas City wires prime, nudge-drain, and mail-check through
separate named `PreInvocation` hooks in `.agents/hooks.json`; no
pre-compaction hook is installed because Antigravity does not expose one.

## Adding a new provider hook

1. Find the provider's native event name in its documentation. Do not
   guess — wiring a non-existent event silently no-ops and looks fine in
   review.
2. Add an entry to the provider's hook config file under the right path
   (see "File layout" above). For new providers, create the directory
   under `internal/bootstrap/packs/core/overlay/per-provider/<provider>/`.
3. Update the event table above with the new row or column.
4. If the provider supports `BuiltinProviderSpec.SupportsHooks` in
   `internal/worker/builtin/profiles.go`, flip it to `true` for that
   provider.

## Known gaps

- **kiro pre-compaction** — Kiro's hook config (under
  `.kiro/agents/gascity.json`) wires `agentSpawn` and `userPromptSubmit`
  but has no pre-compaction event. Kiro does not currently document a
  hook fired before context compaction; add a row here and wire
  `gc handoff --auto` if/when Kiro exposes one. Tracked under the parent
  audit (#672 gap 3).
