# gascity pm log

last_seen: gs-4gs 2026-09-05T14:44Z

Numbered entries below, newest last. Each carries a `Source:` line.

## 1. Init sitting: rig scope, fork stance, governor epic (2026-09-05)

Decisions from the init sitting interview, first round:

- This rig touches no hardware. It is the `gc` SDK/binary substrate; the
  city's other rigs are the orchestrators for the real work.
- Fork of github.com/gastownhall/gascity, significantly diverged. Upstream
  pulls are unplanned; if ever, only from an official release, and expected
  to be a difficult merge. Molding the city to Willie's use case is the
  preferred route over tracking upstream. This contradicts the upstream-
  alignment mission in AGENTS.md, which predates this decision; flagged to
  Willie in the sitting.
- New epic `governor`: a Fable-model agent waking every 90 minutes to check
  in with the mayor (the check-in itself makes the mayor notice stalled and
  quarantined workers it otherwise misses), assess city state, direct the
  mayor when needed, and own the judged rebuild/restart cycle for unbuilt
  gascity changes -- restart in a lull or have the mayor wrap up first, with
  a mayor hand-off before every restart. Permissions to administrate and
  restart the city.

Source: Willie, pm-init sitting gs-4gs

## 2. Init sitting round two: exemption edit approved, epic finalized (2026-09-05)

Decisions from the sitting's second round:

- The docsync gate collision (PM state files under docs/ vs
  TestEveryDocsPageIsPublished) is resolved by option 1: the three PM state
  files are added to docsPublishExemptions in
  test/docsync/docsync_test.go. Approved by Willie as a one-time PM
  bootstrap edit, same shape as astoria-sel4's spec doc-index bootstrap
  (their pm-log #1). The PM edit boundary is otherwise unchanged.
- T3 Code and the DoltLite beads backend are context only. No live
  dependencies gate this rig's work.
- No done epics are to be back-recorded, and no planned epics exist beyond
  governor for now.
- governor is set in-progress. The DECOMP request to the mayor waits for
  roadmap acceptance, per the pm-init procedure.
- New governor acceptance criterion: every check-in posts a Slack summary of
  the whole city's state and any action taken.
- Under discussion, not yet decided: whether the governor is a separately
  defined agent or a duty carried by the gascity PM itself.

Source: Willie, pm-init sitting gs-4gs
