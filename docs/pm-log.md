# gascity pm log

last_seen: gs-kg2 2026-09-05T15:23Z

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

## 3. Roadmap accepted, governor is a separate agent (2026-09-05)

Willie accepted the roadmap and confirmed the governor is its own agent
definition, separate from any rig PM. Grounds from the sitting discussion:
the governor's beat is the whole city while a PM is rig-scoped, restart
permissions belong on the narrowest identity, and merging would put a
90-minute operational sweep in the same queue and failure domain as
PM question answering. The governor is the judgment layer above Health
Patrol: patrol detects mechanically, the governor weighs priorities and
timing. Roadmap criterion updated in the same turn; DECOMP for epic
governor sent to the mayor per the decomposition procedure.

Source: Willie, pm-init sitting gs-4gs

## 4. DECOMP governor accepted as filed (2026-09-05)

Verdict on the mayor's decomp summons gs-kg2: ACCEPT governor. The four
proposed beads cover all seven acceptance criteria with no orphan scope --
gs-x0k carries criteria 1, 2 and 6 (agent definition, 90-minute wake,
Slack summary per check-in), gs-xeh carries 3 (assessment and specific
direction to the mayor), gs-o9i carries 4 and 5 (unbuilt-change detection,
judged restart with mayor handoff), gs-nun carries the soak. The rig has
no hardware or upstream-rig dependencies for them to miss. The mayor's
three flagged additions (recording the reversed supervisor prohibition in
the artifact, cadence as configuration with a recorded per-wake cost
against the unmetered Fable usage-credits pool, the 2026-09-05 failure
corpus as the assessment test set) are constraints in service of the
criteria, not added scope.

Ruling the mayor asked for on the soak: gs-nun stands as filed, closing
by ASKING Willie whether his manual check-in ritual has stopped being
necessary rather than asserting an objective proxy. Criterion 7 names his
ritual stopping as the measure, so his word is the criterion; a
quiet-day-shaped metric would be trivially satisfiable, as the bead
itself records. At close-report time this entry is the precedent that the
epic may close on Willie's recorded say-so plus the injected-corpus soak
evidence.

Source: roadmap governor
