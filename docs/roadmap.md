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

status: in-progress

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

## Abandoned

None yet.
