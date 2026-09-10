# gascity roadmap

last_reviewed: 2026-09-05

## What this rig is for

The `gc` binary and SDK this city runs on. Every other rig in the city is an
orchestrator for the "real work"; this rig is the substrate they all execute
on, so its consumers are all of them. It is a fork of
github.com/gastownhall/gascity that has diverged significantly. Upstream
pulls are not planned: if one ever happens it will be from an official
gascity release and is expected to be a difficult merge. The working stance
(Willie, init sitting 2026-09-05) is that the city molding itself to his
specific use case is the better route than tracking upstream. Note this
contradicts the "keep upstream/main easy to merge" mission recorded in
AGENTS.md, which predates this sitting; AGENTS.md is outside the PM's edit
boundary, so the drift is flagged here rather than fixed.

## Hardware and dependencies

None. This rig touches no bench hardware and no instrument; it is pure
host-side Go. No upstream rig feeds it -- it sits under every other rig in
the city as their execution substrate, so its position in the dependency
graph is downstream-of-nothing, upstream-of-everything. T3 Code and the
DoltLite beads backend are context for the integration work already in the
tree, not live dependencies: no work in this rig gates on either (Willie,
init sitting 2026-09-05).

## epic:governor -- Governor agent for autonomous city oversight

status: done

A scheduled agent, running on the Fable model, that takes over the check-in
duty Willie currently performs by hand every couple of hours: the mayor
frequently fails to notice stalled or quarantined workers, but the act of
being checked in on spurs it to notice and fix them. The governor also owns
the judged rebuild-and-restart cycle: deciding when unbuilt gascity changes
warrant rebuilding `gc` and restarting the city, and timing that restart
around active work instead of doing it blindly.

Acceptance:

- The governor is its own agent definition, separate from any rig PM
  (decided over the merge-into-PM alternative in the init sitting), and
  exists as configuration only (agent definition plus a schedule/order): no
  governor-named logic in Go, per the ZERO hardcoded roles rule.
- It wakes every 90 minutes unattended and each wake sends a "checking in"
  message to the mayor.
- Each wake it assesses city state itself -- stalled or quarantined workers,
  queue health -- and when the assessment finds a problem, it tells the
  mayor specifically what needs doing to keep the factory running.
- It detects unbuilt changes to the gascity rig and judges restart timing:
  priority of the unbuilt changes weighed against active work, restarting in
  a lull, or telling the mayor to start wrapping up so the restart lands at
  a smart time.
- Before any restart it tells the mayor to hand off, then rebuilds and
  restarts the city itself -- it has the permissions to administrate and
  restart the city.
- Every check-in posts a Slack message: a summary of the current state of
  the entire city and any action the governor took to keep things running.
- Soak evidence: stalled/quarantined workers get noticed and corrected
  without Willie checking in, such that his manual every-couple-of-hours
  ritual stops being necessary.

Depends on: ci-waw3o7

Conclusion: Delivered as configuration only -- agent definition, 90-minute
schedule, prompt templates; no governor-named Go. Criteria 1-6 verified in
the close reasons of gs-x0k, gs-xeh, gs-o9i and gs-nun (pm-log #5);
criterion 7's measure is gs-r76's 65.1-hour interval record on ci-waw3o7:
40/40 wakes claimed, closed and paged, 14 real conditions relayed to the
mayor with zero false relays, against the prediction that Willie's manual
every-couple-of-hours ritual stops being necessary. Willie ruled it met
2026-09-10T12:55Z -- ci-waw3o7 closed "Governor has indeed made regular
check-ins unnecessary" and gate ci-gbhkpa closed "Governor has retired the
manual check-in" (pm-log #132).

## epic:mayor-slack-bridge -- Direct Slack line to the mayor

status: in-progress

A second Slack channel, separate from the alerts channel, that works like
having the mayor's tmux window open in Slack: anything Willie posts there
lands in the mayor session's prompt, and the mayor's own output mirrors
back automatically. Feasibility and substrate survey are pm-log #57: the
extmsg fabric already carries inbound generically, and the new code is a
Slack Socket Mode adapter (contrib/openclaw-bridge shape) plus an
output-mirror daemon on the session SSE stream. Design decisions are
pm-log #58: assistant-turns-only mirror, Socket Mode ingress, secrets.env
credential seam.

Acceptance:

- A message Willie posts in the channel arrives in the mayor session's
  prompt with its full text via the extmsg inbound path, cold-waking the
  mayor when no session is live.
- The mayor's assistant turns -- and only those: no tool output, no other
  agents' inbound traffic -- mirror to the channel automatically, with no
  reply action required of the mayor.
- Ingress is Slack Socket Mode; no public TLS endpoint is opened.
- Adapter and mirror run as supervised out-of-process components in the
  contrib/openclaw-bridge shape, surviving controller restart (automatic
  re-registration) and mayor respawn (binding follows the named session).
- The bridge is pure configuration with respect to roles: it binds a
  named session; no role name appears in gc source (ZERO hardcoded
  roles).
- Slack credentials live in ${GC_HOME}/secrets.env like the alert seam,
  never in city.toml or the repo.
- Long turns are delivered within Slack message limits under a recorded
  chunk/truncate policy rather than dropped.
- The existing alerts channel and notify.sh seam are untouched.
- Live round trip demonstrated: Willie's channel message reaches the
  mayor and the mayor's answering turn appears in the channel with no
  manual step.

Depends on: a Slack app provisioned by Willie (bot token plus app-level
token with connections:write; the new channel created), credentials landed
in ${GC_HOME}/secrets.env. No hardware, no upstream rig.

## epic:agent-efficiency -- Agent session efficiency: time and usage headroom

status: open

Make the agent fleet cheaper in the two currencies that actually bind this
city: wall-clock time and usage-window headroom. The city runs on Claude
Code subscription usage, not metered API tokens, so no dollar figure exists
anywhere in this epic; every lever is measured in our own token counts and
latency (pm-log #133). Grounding from the spawn-path mapping (pm-log
#133-#136): sessions cold-spawn per bead as `claude --effort <level>
--model <id> --settings ... '<rendered prompt>'`; a 139-character
per-session SessionStart beacon sits ahead of ~31K tokens of byte-stable
payload (28.5KB rendered prompt, 14KB skill listing, 73KB
CLAUDE.md/AGENTS.md stack) and defeats prompt-cache reuse at every spawn,
~157 cold spawns per observed window; nearly every Claude agent runs
effort=max; six-account rotation fragments what cache could exist; and
~200KB of per-account harness memory drift violates the accounts-identical
principle (pm-log #135).

Acceptance:

- Observability: per-session input / cache-read / cache-creation / output
  token counts and wall-clock, attributable to agent type (and bead where
  one is held), extracted from our own session records and paired with the
  existing tokens-by-role analytic. A baseline over a representative
  window is recorded BEFORE any other criterion's change lands.
- Prefix hygiene, measured: the redundant SessionStart hook beacon and the
  per-prompt clock line no longer precede the stable payload on the spawn
  path, and a second consecutive spawn of the same agent slot on the same
  account shows nonzero cache_read_input_tokens in its own session record.
  The criterion is the measured read, not the removal.
- Explicit model+effort everywhere: every agent definition names model and
  effort; nothing runs effort=max. Assignments of record (pm-log #136):
  lab engineer and bench-engineer xhigh; mayor opus-5 xhigh; governor,
  analyst-fable, adversarial-reader-claude fable at high; PM fable-5 at
  high; technician class sonnet at high. An unrecognized model or effort
  value fails config load loudly instead of silently degrading to the
  provider default (today a typo degrades to max with no error).
- Canary, not harness: no retrospective bead-replay rig is built (pm-log
  #136). After the new levels land, a 1-2 week window compares per-bead
  tokens, wall-clock, and rework signals (reopened beads, failure classes,
  gate failures, skeptic rejections) against the baseline, with a named
  revert criterion per agent; results recorded.
- Account affinity: a reshuffle script, run at initial setup and whenever
  an account hits its limit, reassigns agent-type-to-account affinity from
  available 5h/7d usage paired with tokens-by-role -- highest-usage type
  gets the most-available account, types spread evenly across accounts,
  never stacking two high-usage types while an account idles, never a
  blind hash (pm-log #135). Affinity is a preference that yields at usage
  limits, and a reshuffle binds future spawns only -- it never stops or
  migrates an in-flight session. Sequenced after prefix hygiene: until the
  prefix is stable there is no cache to come back to.
- Memory-drift remediation: account-home Claude memory harvested into
  bd/docs where still valuable, the directories emptied, and a mechanical
  gate keeping the accounts-identical invariant from silently
  re-accumulating (a rule without a gate is how it reached ~200KB).
- Prompt-instruction audit: role templates, appended fragments, and the
  CLAUDE.md/AGENTS.md stack audited against the anti-pattern checklist
  (verification rituals, thoroughness boosters, mandatory scaffolds, stale
  examples, contradictory rules -- pm-log #133), each finding
  dispositioned fix or keep-with-reason (load-bearing guards stay), with
  the instructions stack's token size recorded before and after.

Depends on: mayor-slack-bridge closing (normal promotion path, pm-log
#134). A queryable per-account 5h/7d usage-availability signal for the
reshuffle script -- verify at decomposition. Edits to ~/.claude/CLAUDE.md
are Willie's own (it lives outside every rig), so audit findings there are
proposals to him, not beads.

## Abandoned

None yet.
