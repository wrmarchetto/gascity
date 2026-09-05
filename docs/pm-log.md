# gascity pm log

last_seen: gs-8zd 2026-09-05T22:18Z

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

## 8. pm-epic-close gs-ps7: governor stays in-progress, ruling still pending (2026-09-05)

Fourth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli),
re-verified live rather than recalled. The closed set under epic:governor is
unchanged (gs-x0k, gs-xeh, gs-o9i, gs-nun -- criteria 1-6 verified in their
close reasons, pm-log #5). Criterion-7 chain re-checked in the city store
this turn: ci-waw3o7 (P1, assignee human) still open, gate ci-gbhkpa still
open and operator-paged. bench-alerts.log is still untracked in the rig
root, corroborating that neither operator-only action named in that bead
has landed. Per pm-log #4 the epic closes on Willie's recorded say-so; it
has not landed, so governor stays in-progress and the bead that puts motion
back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. gs-hph (P2, pm-log #7) is still stalled: in_progress with
lab.engineer-2, lease expired, heartbeat 24 minutes back -- four minutes
worse than #7's observation, still a patrol/mayor matter rather than a PM
edit, and still the class of fault the governor epic exists to catch.
Residuals in pm-log #5 stand: no scheduled wake has yet run WITH the
assessment layer, and a governor that assesses then reports something else
is mechanically unobservable today.

Source: roadmap governor

## 9. pm-idle gs-5k6: governor stays in-progress, ruling still pending (2026-09-05)

Fifth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7), re-verified live rather than recalled. The closed set under
epic:governor is unchanged (gs-x0k, gs-xeh, gs-o9i, gs-nun -- criteria 1-6
verified in their close reasons, pm-log #5). Criterion-7 chain re-checked in
the city store this turn: ci-waw3o7 (P1, assignee human) still open, gate
ci-gbhkpa still open and operator-paged. bench-alerts.log is still untracked
in the rig root, corroborating that neither operator-only action named in
that bead has landed. Per pm-log #4 the epic closes on Willie's recorded
say-so; it has not landed, so governor stays in-progress and the bead that
puts motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. The summons's idle premise reads gs-hph as not-running: it is still
in_progress with lab.engineer-2 but its lease is expired, heartbeat 34
minutes back -- ten minutes worse than #8's observation, a worsening trend
across three sittings (20/24/34 min). Still a patrol/mayor matter rather
than a PM edit, and still the class of fault the governor epic exists to
catch. gs-b53 (pm-epic-close:governor, filed 19:05Z) is open for the next
session -- same decision from opposite evidence, answered by re-checking
ci-waw3o7. Residuals in pm-log #5 stand.

Source: roadmap governor

## 10. pm-epic-close gs-b53: governor stays in-progress, ruling still pending (2026-09-05)

Sixth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7, #9 gs-5k6), re-verified live rather than recalled. The closed set
under epic:governor is unchanged (gs-x0k, gs-xeh, gs-o9i, gs-nun --
criteria 1-6 verified in their close reasons, pm-log #5). Criterion-7 chain
re-checked in the city store this turn: ci-waw3o7 (P1, assignee human)
still open, gate ci-gbhkpa still open and operator-paged. bench-alerts.log
is still untracked in the rig root, corroborating that neither
operator-only action named in that bead has landed. Per pm-log #4 the epic
closes on Willie's recorded say-so; it has not landed, so governor stays
in-progress and the bead that puts motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. Nothing in this rig's store moved since last_seen except this
summons. gs-hph (P2, pm-log #7-#9) is still stalled: in_progress with
lab.engineer-2, lease expired, heartbeat 37 minutes back -- the trend
across four sittings is now 20/24/34/37 min. Still a patrol/mayor matter
rather than a PM edit, and still the class of fault the governor epic
exists to catch. Residuals in pm-log #5 stand: no scheduled wake has yet
run WITH the assessment layer, and a governor that assesses then reports
something else is mechanically unobservable today.

Source: roadmap governor

## 11. pm-idle gs-cp7: governor stays in-progress, ruling still pending (2026-09-05)

Seventh ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7, #9 gs-5k6, #10 gs-b53), re-verified live rather than recalled. The
closed set under epic:governor is unchanged (gs-x0k, gs-xeh, gs-o9i, gs-nun
-- criteria 1-6 verified in their close reasons, pm-log #5). Criterion-7
chain re-checked in the city store this turn: ci-waw3o7 (P1, assignee
human) still open, gate ci-gbhkpa still open and operator-paged.
bench-alerts.log is still untracked in the rig root, corroborating that
neither operator-only action named in that bead has landed. Per pm-log #4
the epic closes on Willie's recorded say-so; it has not landed, so governor
stays in-progress and the bead that puts motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. One change since #10: the gs-hph stall broke. The bead is now
assigned to lab.engineer-1 (was lab.engineer-2, pm-log #5) with a heartbeat
5 minutes back, against the 20/24/34/37-minute worsening trend #7-#10
tracked -- apparently reclaimed and active again, though the lease still
prints expired. Still a patrol/mayor matter, recorded here only to close
out that trend. gs-ugx (pm-epic-close:governor, open since last_seen) waits
for the next session -- same decision from opposite evidence, answered by
re-checking ci-waw3o7. Residuals in pm-log #5 stand: no scheduled wake has
yet run WITH the assessment layer, and a governor that assesses then
reports something else is mechanically unobservable today.

Source: roadmap governor

## 12. pm-epic-close gs-ugx: governor stays in-progress, ruling still pending (2026-09-05)

Eighth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7, #9 gs-5k6, #10 gs-b53, #11 gs-cp7), re-verified live rather than
recalled. The closed set under epic:governor is unchanged (gs-x0k, gs-xeh,
gs-o9i, gs-nun -- criteria 1-6 verified in their close reasons, pm-log #5).
Criterion-7 chain re-checked in the city store this turn: ci-waw3o7 (P1,
assignee human) still open, gate ci-gbhkpa still open and operator-paged.
bench-alerts.log is still untracked in the rig root, corroborating that
neither operator-only action named in that bead has landed. Per pm-log #4
the epic closes on Willie's recorded say-so; it has not landed, so governor
stays in-progress and the bead that puts motion back remains ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. Nothing in this rig's store moved since last_seen except this
summons. gs-hph holds the recovery pm-log #11 recorded: still in_progress
with lab.engineer-1, heartbeat 9 minutes back (lease prints expired, as it
did through #7-#11) -- not the resumed 20-plus-minute worsening trend, but
worth the next session's glance. Still a patrol/mayor matter rather than a
PM edit. Residuals in pm-log #5 stand: no scheduled wake has yet run WITH
the assessment layer, and a governor that assesses then reports something
else is mechanically unobservable today.

Source: roadmap governor

## 13. pm-idle gs-3sy: governor stays in-progress, ruling deferred to 2026-09-07 (2026-09-05)

Ninth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7, #9 gs-5k6, #10 gs-b53, #11 gs-cp7, #12 gs-ugx), re-verified live
rather than recalled. The closed set under epic:governor is unchanged
(gs-x0k, gs-xeh, gs-o9i, gs-nun -- criteria 1-6 verified in their close
reasons, pm-log #5). This sitting was paused mid-turn by a usage limit, so
the criterion-7 chain was checked twice, and the two checks bracket a
change: at 19:36Z ci-waw3o7 (P1, assignee human) was open as in #5-#12; at
22:11Z it is deferred (status DEFERRED, date 2026-09-07), with gate
ci-gbhkpa still open and operator-paged. bench-alerts.log is still
untracked in the rig root. Willie's say-so has still not landed -- the
deferral postpones the question rather than answering it -- so per pm-log
#4 governor stays in-progress, and the bead that puts motion back remains
ci-waw3o7, now carrying an earliest-revisit date of 2026-09-07.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. The gs-hph stall trend #7-#12 tracked ended during the pause: the
bead is closed by lab.engineer-codex-1 (version-check verdicts restored
via gc.formula_name metadata resolution, visible diagnostics, a JSON
result schema; regression tests, full cmd/gc suite, vet, pre-commit and
make test green; pushed 35839700e to origin/main). The summons's idle
premise lapsed the same way: gs-c6f is open and gs-z39 in_progress, both
unlabeled, and the sweep filed gs-22c and gs-8zd (both assigned to this
PM) to place them -- one unit each for the next sessions, alongside the
still-open gs-qzm (pm-epic-close:governor). Residuals in pm-log #5 stand:
no scheduled wake has yet run WITH the assessment layer, and a governor
that assesses then reports something else is mechanically unobservable
today.

Source: roadmap governor

## 14. pm-epic-close gs-qzm: governor stays in-progress, ruling deferred to 2026-09-07 (2026-09-05)

Tenth ruling of this shape (pm-log #5 gs-8iv, #6 gs-ewi, #7 gs-wli, #8
gs-ps7, #9 gs-5k6, #10 gs-b53, #11 gs-cp7, #12 gs-ugx, #13 gs-3sy),
re-verified live rather than recalled. The closed set under epic:governor is
unchanged (gs-x0k, gs-xeh, gs-o9i, gs-nun -- criteria 1-6 verified in their
close reasons, pm-log #5). Criterion-7 chain re-checked in the city store
this turn: ci-waw3o7 (P1, assignee human) holds the DEFERRED status pm-log
#13 found, earliest revisit 2026-09-07, and gate ci-gbhkpa is still open and
operator-paged. bench-alerts.log is still untracked in the rig root,
corroborating that neither operator-only action named in that bead has
landed. Per pm-log #4 the epic closes on Willie's recorded say-so; it has
not landed -- the deferral postpones the question rather than answering it
-- so governor stays in-progress and the bead that puts motion back remains
ci-waw3o7.

No epic promoted -- governor is the roadmap's only epic -- and no DECOMP
sent. What gascity builds next is already recorded as pm-open #2; no new
entry. Since last_seen the rig store moved only by this summons and the two
placement beads pm-log #13 already recorded (gs-22c for gs-c6f, gs-8zd for
gs-z39, both assigned to this PM) -- one unit each for the next sessions,
not this one. Residuals in pm-log #5 stand: no scheduled wake has yet run
WITH the assessment layer, and a governor that assesses then reports
something else is mechanically unobservable today.

Source: roadmap governor

## 15. Placement summons gs-8zd: gs-z39 ruled standalone (2026-09-05)

gs-z39 (superseded fix/gs-hph-formula-version-check branch -- salvage or
delete) carries no epic label and the sweep summoned this PM to place it.
Ruling: `standalone`, applied this turn with
`bd update gs-z39 --add-label standalone`. Grounds: it descends from gs-hph,
a gc bug the governor soak FOUND, but finding a bug is not owning its
cleanup -- no epic:governor acceptance criterion closes over branch hygiene,
gs-hph itself was never labeled epic:governor, and the roadmap holds no
other epic. Forcing it into governor would be orphan scope by the same
standard pm-log #4 applied at DECOMP review, and would have re-armed the
epic-close sweep against a bead the epic does not need.

The summons premise had half-lapsed at ruling time: gs-z39 was already
CLOSED by lab.engineer-codex-1 -- the branch's duplicate version-check fix
ruled do-not-merge, and the one salvageable artifact (the JSON flag/result-
schema coverage test) ported as cmd/gc/json_flag_schema_coverage_test.go,
verified fail-first, committed 992fe4f33 and pushed. The label still
matters: it is the durable record that no epic's acceptance was ever meant
to close over this bead.

The recurring shape -- substrate fixes with no roadmap home, gs-hph then
gs-c6f then gs-z39 -- is recorded as pm-open #3 (standing maintenance epic
vs a declared standalone-by-default policy, Willie's call), courtesy ping
delivered (exit 0). gs-22c (placing gs-c6f) stays one unit for the next
session; nothing in this entry pre-decides it.

Source: roadmap governor
