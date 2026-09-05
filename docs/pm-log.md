# gascity pm log

last_seen: gs-wli 2026-09-05T18:50Z

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

## 5. pm-idle gs-8iv: governor stays in-progress pending Willie's ruling (2026-09-05)

pm-idle:gascity summons gs-8iv answered. All four governor beads closed
pass with verification in their close reasons: gs-x0k carries criteria
1/2/6 (config-only skeleton, one real unattended wake driven end to end,
Slack leg confirmed at the transport, 22/22 suite with 12/12 mutants
killed); gs-xeh carries 3 (assessor verified against the live city, 17/17
mutants on the second sweep, one real finding corroborated three ways);
gs-o9i carries 4/5 (handoff-gated rebuild authority, fail-closed
preflight); gs-nun the soak (caught wake 1's 74-minute silent park, a
stale supervisor, and an unstaged starved pool; 29 mutants die).

Criterion 7 is deliberately ASKED, not asserted: ci-waw3o7 (city store,
P1, assignee human) behind gate ci-gbhkpa, both open. Per pm-log #4 the
epic closes on Willie's recorded say-so, which has not landed, so the
epic stays in-progress. The bead that puts motion back is ci-waw3o7 --
outside this rig's store, which is exactly why the sweep read the rig as
idle. Recorded with the after-governor gap as pm-open #2.

No epic promoted: none is open (governor is the roadmap's only epic). No
DECOMP sent. The idle condition lapsed before this sitting anyway: gs-hph
(P2 formula version-check bug, filed by gs-nun) went in_progress with
lab.engineer-2 at 18:31Z. gs-ewi (pm-epic-close:governor) is open for the
next PM session -- same decision from opposite evidence; it should
re-check ci-waw3o7 and may find Willie's ruling there.

Residuals the next session should keep in view, from gs-nun: no scheduled
wake has yet run WITH the assessment layer (both recorded wakes predate
it), and a governor that assesses then reports something else is
mechanically unobservable today.

Source: roadmap governor

## 6. pm-epic-close gs-ewi: governor stays in-progress, ruling still pending (2026-09-05)

Epic-close summons gs-ewi answered -- the same decision as pm-idle gs-8iv
(pm-log #5) from opposite evidence, re-verified rather than recalled. The
closed set under epic:governor is unchanged (gs-x0k, gs-xeh, gs-o9i,
gs-nun; criteria 1-6 verified in their close reasons, pm-log #5), and
nothing in this rig's store moved since last_seen except the summons
itself. Re-checked the criterion-7 chain live in the city store:
ci-waw3o7 (P1, assignee human) still open, gate ci-gbhkpa still open and
operator-paged. Per pm-log #4 the epic closes on Willie's recorded
say-so, which has not landed, so governor stays in-progress and the bead
that puts motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2, so no
new pm-open entry. While gate ci-gbhkpa stays open the trace sweep keeps
filing pm-epic-close:governor summonses by design; each is answered by
re-checking ci-waw3o7, and the first session that finds it resolved rules
on the epic from Willie's close reason. Residuals in pm-log #5 stand.

Source: roadmap governor

## 7. pm-idle gs-wli: governor stays in-progress, ruling still pending (2026-09-05)

Third ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi), re-verified live
rather than recalled. The closed set under epic:governor is unchanged
(gs-x0k, gs-xeh, gs-o9i, gs-nun -- criteria 1-6 verified in their close
reasons, pm-log #5). Criterion-7 chain re-checked in the city store this
turn: ci-waw3o7 (P1, assignee human) still open, gate ci-gbhkpa still
open. The untracked bench-alerts.log that bead names as operator action 2
is still present in the rig root, corroborating that no operator action
has landed. Per pm-log #4 the epic closes on Willie's recorded say-so; it
has not landed, so governor stays in-progress and the bead that puts
motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. The waiting-on-external condition is already recorded as pm-open #2;
no new entry. The summons's idle premise was stale at answer time: gs-hph
(P2 formula version-check bug, pm-log #5) shows in_progress with
lab.engineer-2 but its lease is expired, heartbeat ~20 minutes back -- a
worker-health matter for patrol and the mayor, not a PM edit, noted here
because a stalled P2 is exactly the class of fault the governor epic
exists to catch. gs-ps7 (pm-epic-close:governor) is open for the next
session -- same decision from opposite evidence, answered by re-checking
ci-waw3o7. Residuals in pm-log #5 stand.

Source: roadmap governor
