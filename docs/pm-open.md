# gascity pm open questions

Questions the PM could not answer from spec, roadmap, or log. Each entry
names the question and what evidence would settle it.

## 1. Stop-gate/drain-ack handshake repeats every idle sitting turn (2026-09-05)

During the init sitting (gs-4gs), every turn that ended while the sitting
idled was blocked by the stop gate demanding `gc runtime drain-ack`; the
controller then kept the session on `drain-ack-assigned-work`, and the next
turn's re-claim returned `existing_assignment` for the same bead. The loop
costs two round trips per idle turn and reads as a controller/stop-gate
disagreement about sessions holding an open sitting bead.

Question: is this intended behavior for sitting-holding sessions, or a gc
bug worth a bead?

Would settle it: Willie's read on the intended drain semantics for sessions
holding an open sitting bead, or a look at the controller's drain-eligibility
logic in this repo.

## 2. Rig idle behind governor acceptance; nothing queued after governor (2026-09-05)

epic:governor's last criterion -- Willie's say-so that his manual check-in
ritual is unnecessary -- is out of this rig's hands: ci-waw3o7 (city store,
P1, assignee human) puts the question, gate ci-gbhkpa pages it, and both are
open. Two operator-only actions block a full answer, per that bead: the
stale supervisor (PID 3926095, running a build older than gascity 3411d0f2a)
and the untracked bench-alerts.log in /home/willie/gascity that makes
governor-rebuild-preflight.sh refuse every rebuild.

Separately: the roadmap holds no open epic behind governor, so when governor
closes this rig has nothing to promote. pm-log #2 recorded "no planned epics
beyond governor for now", so this is a known gap, not a lost decision.

Question: (a) Willie's ruling on ci-waw3o7, and (b) what this rig builds
after governor.

Would settle it: Willie resolving gate ci-gbhkpa -- a "not yet" should name
what he still checks by hand, which per that bead becomes the next thing the
governor is taught -- and a `gc city pm plan gascity '<epic>'` sitting
for (b).

Update 2026-09-06 (pm-log #29): both operator-only actions have lapsed.
bench-alerts.log is git-ignored by 6deeededa, so the rebuild preflight no
longer refuses on it, and stale supervisor PID 3926095 is gone from the
process table (a supervisor now runs as PID 978187). What remains open is
(a) the ruling itself -- ci-waw3o7, DEFERRED, earliest revisit 2026-09-07 --
and (b), unchanged.

Update 2026-09-07 (pm-log #65): ci-waw3o7's 48h deferral has expired; the
bead now reads OPEN (updated 2026-09-07), live on its human assignee's
queue. Substance unchanged -- Willie's answer is still the 2026-09-05 "NOT
YET" and gate ci-gbhkpa is still open, so (a) remains the open half. The
re-ask is owed by the bead's owner (gascity/lab.engineer-1) carrying the
interval record its notes require (pm-log #62). (b) was settled by pm-log
#59: mayor-slack-bridge is the epic after governor and is in-progress.

Update 2026-09-08 (pm-log #101): the (a) wait had a demand gap. The re-ask
is owed by gascity/lab.engineer-1, but that pool identity sat at zero
sessions with no bead assigning it the work, so 35 identical rulings
(pm-log #65-#100) waited on an act nothing would trigger. gs-r76 (this
rig, epic:governor, assignee gascity/lab.engineer-1) now carries it:
produce the post-assessment interval record, put it on ci-waw3o7, re-ask.
(a) itself remains open until Willie rules on ci-waw3o7.

Update 2026-09-08 (pm-log #102): gs-r76 is closed -- the record exists and
the re-ask is made. The 65.1-hour interval record (40 wakes, all with the
assessment layer, 14 real conditions relayed, 0 false relays reached the
mayor) is on ci-waw3o7's notes as of 2026-09-08T13:31Z, and gate
ci-gbhkpa's description now points to it as superseding the old 1.5-hour
soak summary. Both operator-only blockers are recorded cleared on the gate
itself. Nothing is owed by any agent anymore: (a) is now solely Willie's
ruling on ci-waw3o7, standing paged via ci-gbhkpa. Until he rules, the
15-minute pm-epic-close sweep is the watch -- each summons re-reads
ci-waw3o7 and re-closes if unruled.

Update 2026-09-10 (pm-log #132): RESOLVED, both halves. Willie ruled at
2026-09-10T12:55Z: ci-waw3o7 closed "Governor has indeed made regular
check-ins unnecessary" and gate ci-gbhkpa closed "Governor has retired the
manual check-in". (a) is answered and epic:governor is done on the roadmap;
(b) was settled earlier by pm-log #59 (mayor-slack-bridge). The successor
queue-after question is pm-open #5.

## 3. Substrate maintenance beads have no epic home; gs-z39 ruled standalone (2026-09-05)

gs-z39 (superseded fix/gs-hph-formula-version-check branch -- salvage or
delete) was labeled `standalone` by summons gs-8zd: it descends from gs-hph,
a gc bug the governor soak FOUND, but no epic:governor acceptance criterion
closes over branch cleanup, and the roadmap holds no other epic. Forcing it
into governor would be orphan scope by the same standard pm-log #4 applied
to the DECOMP review. It was already closed at ruling time (engineer-codex-1
salvaged the JSON coverage test, 992fe4f33). Summons gs-22c then ruled
gs-c6f (the slot-unwedge/salvage sibling) standalone on the same grounds
(pm-log #16); it too was already closed at ruling time.

The pattern behind it is the real question: this rig's substrate work --
bugs found in gc, branch hygiene, tooling fixes like gs-hph/gs-c6f/gs-z39 --
has no roadmap home, so every such bead re-raises this per-bead placement
question. This is pm-open #2(b) seen from below.

Update 2026-09-06 (pm-log #38): summons gs-jv1 ruled gs-eep standalone on
the same grounds. gs-eep is the mayor's durable ruling bead for the wedged
rig root (repair deferred to the operator, resolved by abort and closed
2026-09-06) -- an operational incident carrier, not feature work, so no
epic's acceptance closes over it. Incident-management beads join the class
alongside substrate fixes; the standing question below is unchanged.

Update 2026-09-07 (pm-log #61): gs-olu (connected-clients.md repair plus a
route-checking docsync test) joins the class, fourth after gs-z39, gs-c6f,
and gs-eep. The pm-log #60 DECOMP verdict ruled it standalone: substrate doc
rot the pm-log #57 survey found, covered by no epic:mayor-slack-bridge
acceptance criterion. The mayor dropped the epic label without adding
standalone, sweep summons gs-wm2 caught the unlabeled bead, and the label
was added there. The standing question below is unchanged.

Update 2026-09-07 (pm-log #73): gs-dzj (gate scripts/ on shellcheck) joins
the class, fifth after gs-z39, gs-c6f, gs-eep, and gs-olu. Sweep summons
gs-nv0 ruled it standalone: repo-wide lint gating whose seed defect gs-fn6's
bridge work FOUND -- a malformed directive had left a gate script unparsed
-- but that no epic:mayor-slack-bridge criterion closes over. Same
descent-is-not-coverage standard that kept gs-z39 out of epic:governor. The
standing question below is unchanged, and the class growing to five is
itself evidence for settling it.

Update 2026-09-07 (pm-log #77): gs-a6j (widen the shell-lint sweep to the
pack scripts the SDK ships) joins the class, sixth after gs-z39, gs-c6f,
gs-eep, gs-olu, and gs-dzj. Summons gs-153 ruled it standalone: gs-a6j is
gs-dzj's own widening, scoped from scripts/ and .githooks/ out to
internal/bootstrap/packs, the SDK-shipped pack scripts (one carries a live
rm -rf hazard on two possibly-empty variables) -- and neither epic names
shell-lint sweep scope in its acceptance criteria. One link further down a
chain pm-log #73 already placed outside the roadmap, not a fresh descent
question. The class growing to six is itself further evidence for settling
the standing question below.

Update 2026-09-10 (pm-log #131): gs-hzrg (machine supervisor never
installed as a service; GC_SUPERVISOR_ENV opt-ins baked at install time,
unrecorded) joins the class, seventh after gs-z39, gs-c6f, gs-eep, gs-olu,
gs-dzj, and gs-a6j. Summons gs-n6ja ruled it standalone: a machine-state
record proposing no gascity code change, assignee human -- the gs-eep
operational-record shape -- with its bridge-relevant half already carried
in-epic by gs-msc2. Per the mayor's mail of the same hour
(ci-wisp-2jg4mwz), `gc supervisor install` has now run for the first time
on this machine, remedying finding 1 while the standing question below
stays open.

Update 2026-09-13 (pm-log #143): gs-cbhj (supervisor unit env baked at
install time -- make the opt-ins configured and gated) joins the class,
eighth after gs-z39, gs-c6f, gs-eep, gs-olu, gs-dzj, gs-a6j, and gs-hzrg.
Summons gs-quyd ruled it standalone: it is finding 2 of gs-hzrg split out
as engineering work, and no epic:agent-efficiency criterion closes over
supervisor unit generation. Same descent-is-not-coverage standard as
pm-log #131. The class at eight members is itself further evidence for
settling the standing question below.

Update 2026-09-13 (pm-log #144): gs-1soi (triage 9 closed beads whose
work never reached main -- the rig-side half of the ci-qof1bz backlog
triage) joins the class, ninth after gs-z39, gs-c6f, gs-eep, gs-olu,
gs-dzj, gs-a6j, gs-hzrg, and gs-cbhj. Summons gs-e5bi ruled it
standalone: landing hygiene for closed beads' branches, covered by no
epic:agent-efficiency criterion, and three of its nine rows are prior
class members' own commits. The class at nine members is itself further
evidence for settling the standing question below.

Update 2026-10-25 (pm-log #165): gs-338t (record the operator's ruling on
city ci-3hywso, "nothing for now" after bead-cycle-time) joins the class,
tenth after the nine above. Summons gs-gciv ruled it standalone: a PM
bookkeeping bead routed to gascity/lab.pm, the gs-eep ruling-record
shape, and neither epic's acceptance closes over what follows
bead-cycle-time. Its content answers pm-open #11(b), which the session
working gs-338t records. The standing question below is unchanged.

Question: should the roadmap carry a standing epic (or an explicit
standalone policy line) for gc substrate maintenance, so off-roadmap fixes
have a declared home?

Would settle it: Willie's call in a `gc city pm plan` or `gc city pm chat`
sitting -- either a maintenance epic with acceptance criteria, or a recorded
policy that substrate fixes stay `standalone` by default.

## 4. Should gascity ever build a provider-neutral reply mechanism for non-mirrored providers? (2026-09-08)

Ruled "not now" on pm-log #129 (bead gs-3shj): epic:mayor-slack-bridge's own criterion 2
resolved the mayor's reply-path problem with a per-provider mirror, not a CLI, and no
epic asks for a shared `gc extmsg reply` wrapper over the existing
POST /v0/city/{city}/extmsg/outbound endpoint. That default holds only against today's
roadmap, which has exactly one provider integration. No prior entry, before or after this
one, has ruled on whether such a capability is wanted in principle for a FUTURE provider
that does not want a full mirror daemon -- pm-log #129 explicitly found this is new
ground, not a reversal.

Would settle it: a future provider integration that needs to reply without adopting the
contrib/openclaw-bridge mirror shape, or a `gc city pm plan`/`gc city pm chat` sitting
where Willie decides the policy ahead of that need rather than at the moment it bites.

## 5. Nothing queued after mayor-slack-bridge (2026-09-10)

epic:governor was ruled done at pm-log #132, leaving mayor-slack-bridge as
the roadmap's only epic not done -- in-progress since pm-log #59, still
carrying open work (gs-msc2 hold:external; gs-77tn and gs-t0d8 in progress
as of pm-log #129/#130). No epic has status: open, so when
mayor-slack-bridge closes this rig has nothing to promote -- the same gap
pm-open #2(b) recorded after governor, one epic later.

Question: what does gascity build after mayor-slack-bridge?

Would settle it: a `gc city pm plan gascity '<epic>'` sitting. pm-open #3's
standing question (a maintenance epic, or a declared standalone-by-default
policy for substrate fixes) is one candidate answer that sitting could fold
in.

Update 2026-09-10 (pm-log #133): decided in principle -- Willie chose an
efficiency epic (prompt caching at spin-up, effort calibration, prompt
instruction audit; metric is time and usage-limit headroom, not dollars) as
the post-bridge epic in chat sitting gs-6v5n. This entry closes when the
epic's text lands in docs/roadmap.md with acceptance criteria, later in
that same sitting or a follow-up.

Update 2026-09-10 (pm-log #136): RESOLVED. epic:agent-efficiency is in
docs/roadmap.md at status: open with seven acceptance criteria, queued for
normal promotion when mayor-slack-bridge closes.

## 6. agent-efficiency promoted on an instruction known only via the mayor's relay (2026-09-10)

DECOMP summons gs-781f requested decomposition of epic:agent-efficiency
while its stated dependency (mayor-slack-bridge closing) was unmet,
quoting Willie's overnight instruction verbatim: "hopefully you can close
out the slack bridge tonight and get the following epic started as well.
if the bridge hits a blocker, go ahead and start the next epic." pm-log
#137 accepted and promoted on that instruction plus a store-verified
blocker: gs-msc2 (human, hold:external) and the live-round-trip
criterion both need Willie, so no agent action could close the bridge
tonight. The instruction itself appears in no record the PM can read --
not spec, pm-log, or roadmap -- only in the mayor's relay.

Question: does Willie confirm the overnight instruction and the early
start of agent-efficiency?

Would settle it: Willie's word when he wakes -- in the mayor's channel, a
pm-chat sitting, or a note on any agent-efficiency bead. If he did not
give the instruction, promotion reverts to the #134 path (bridge close)
and the slung work parks.

## 7. Accounts-identical gate placed in the city repo on PM inference (2026-09-11)

gs-ntgf (asked by gs-arar, epic:agent-efficiency criterion 6) was answered
at pm-log #138: the invariant gate lands in /home/willie/projects/city
assets/scripts beside claude-pool.py and account-cap-sweep.py, built on a
city-repo feature worktree/branch; gs-arar stays in this rig and its close
cites that branch; no Gas City integration point. The components of the
ruling are recorded but no record names the landing repo, so the placement
is unconfirmed.

Question: does Willie confirm the city repo as the gate's home and the
close-cites-branch disposition -- and what merge path lands a city-repo
feature branch on that repo's mainline, since gs-arar's criterion is only
met once the gate runs live?

Would settle it: Willie's word in any sitting or a note on gs-arar or
gs-ntgf; or the branch landing on city main by whatever path that repo
uses, which confirms the disposition by exercise.

## 8. Nothing queued after agent-efficiency (2026-09-11)

epic:mayor-slack-bridge is parked on its acceptance demo (pm-log #140:
criterion 1's cold-wake arm and criterion 9's clean no-manual-step round
trip, re-run bead requested from the mayor) and epic:agent-efficiency is
in-progress with five of its seven criterion carriers already closed, only
gs-jbyc (canary window) and gs-z2on (affinity) open. No epic has status:
open, so when agent-efficiency closes this rig has nothing to promote --
the same gap pm-open #2(b) and #5 recorded, one epic later.

Question: what does gascity build after agent-efficiency?

Would settle it: a `gc city pm plan gascity '<epic>'` sitting. pm-open #3's
standing question (a maintenance epic, or a declared standalone-by-default
policy for substrate fixes) is one candidate answer that sitting could fold
in, as #5 noted before it.

Update 2026-10-23 (pm-log #154): RESOLVED. Willie brought
epic:bead-cycle-time to pm-chat sitting gs-otx1 and accepted it
in-progress.

## 9. What epic:agent-efficiency criterion 4 becomes after the voided canary (2026-09-27)

The gs-jbyc canary window read exit 3 (pm-log #145): mayor, lab.pm and
governor left the level under test mid-window on operator rulings and the
liaison re-charter, and the codex control is parked. Only lab.engineer,
bench-engineer and the toolsmith control held their regime, and all three
are inside their margins. gs-78op asks which of (a) re-baseline and re-run
under a level freeze, (b) narrow the criterion to the held rows, (c) drop
it. Escalated as human gate gs-3vdb, which blocks gs-78op. PM recommends
(b). The same gate asks Willie to confirm the fleet's live model+effort
levels so criterion 3's assignments of record can be superseded.

Would settle it: gs-3vdb's close reason. The next PM wake on gs-78op
answers from it under a new numbered entry and edits the roadmap
accordingly.

Update 2026-09-27 (pm-log #146): the level-confirmation half is answered
from the record by the liaison's gs-3vdb comment (adversarial-reader-claude
xhigh intended, city f8d28e40). Only the a/b/c choice is still open, queued
to Willie as city ci-efnkgw.

Update 2026-10-23 (pm-log #147): RESOLVED. Willie chose (c), criterion 4
dropped, via gs-3vdb's close reason, and confirmed it in sitting gs-otx1.
Criterion 3's assignments of record are superseded by the Opus 5.5
convergence.

## 10. Which percentile "everything under an hour" means (2026-10-23)

Willie's baseline ruling (city ci-uuh5rs) was "everything under an hour,
shorter better", with no exact targets. pm-log #158 wrote bead-cycle-time's
Target criterion as p90 under one hour in every rig. The alternatives were
median (half of beads may exceed the hour) and max (one outlier fails the
epic). Also unconfirmed there: criteria 3 to 5 kept under "perhaps
revector", rather than dropped.

Would settle it: Willie confirming or correcting the percentile, and
whether the remaining test-speed criteria stay, at the epic review or a
pm-chat sitting.

Update 2026-10-23 (pm-log #161): RESOLVED. The liaison read Willie's words
as delegating the measure, so p90 stands as the PM's ruling, and criteria
3-5 stay because every one cuts time inside the Target interval.


## 11. Rig idle behind bead-cycle-time's measurement, nothing queued after it (2026-10-24)

Every gascity bead labeled epic:bead-cycle-time is closed, the last being
gs-0y4d (scoped push gate, 08:54Z). The epic stays in-progress because its
Target criterion closes on a measurement, not on landed changes: city
ci-r81svr (D, analyst-fable) measures a post-change window against the
1 h p90 target in every rig. It is date-parked in the city store until
2026-10-25T18:24Z, waiting for the queue changes to be on city main and a
representative span after the last landing. City-store deferrals are
returned by orders/deferred-undefer-sweep.toml, so that park has a wake.
Not verified from here: whether those commits are on city main yet (the
PM git gate refuses reads of another repository's history), and the state
of the zephyr and sel4 beads under the epic.

No epic has status: open, so when bead-cycle-time closes this rig has
nothing to promote -- the gap #2(b), #5 and #8 recorded, one epic later.

Question: (a) does D's window meet the Target in every rig, and (b) what
does gascity build after bead-cycle-time?

Would settle it: (a) ci-r81svr's report, which the epic-close or idle
summons that follows reads, and a miss names the stage that carries it
and gets a bead. (b) a `gc city pm plan gascity '<epic>'` sitting.

Update 2026-10-25 (pm-log #166): (b) RESOLVED. The operator answered city
ci-3hywso "nothing for now": no new gascity epic after bead-cycle-time.
(a) stays open on ci-r81svr's report. A follow-up raised by that
measurement comes back as a new pm-open entry, not under (b).

Update 2026-10-29 (pm-log #169): (a) answered MISS. ci-r81svr read city,
astoria-zephyr and gascity over the Target, astoria-sel4 under, and dart
unmeasured. The follow-up is city ci-7npmly. What stays open: gascity's
half of the Target and gs-gln1's last 3 gates both need gascity landings.
No gascity work is queued by ruling (#166), so both wait on maintenance
work arriving from outside the roadmap. Would settle it: enough gascity
landings for the re-measurement and 3 more merge-gate gates, or Willie
accepting the gascity Target on a smaller sample.


## 12. Operator-deferred review findings carry no epic home (2026-10-29)

Review generation ci-rv15a377628c08/001 of agent-efficiency (g1) deferred
all seventeen findings to the gascity engineer, the operator's rationale
being "defer all findings to the gascity engineer and close the epic".
The liaison filed one remediation bead per finding,
gs-rv15a377628c08-f01-deferred through -f17-deferred, each labeled
`epic-deferred:agent-efficiency` and not `epic:agent-efficiency`. The
roadmap sweep does not count the deferred label as a placement, so it
files one epic-unmapped summons per bead.

Ruling (pm-log #172): standalone. Labeling them `epic:agent-efficiency`
would make them members of the epic the operator asked to close, and
hold it at implementing. bead-cycle-time's criteria do not close over
them. Opening an epic for them would be planning the operator declined
(#166). Applied to -f17 by summons gs-5da8. The other sixteen take the
same ruling under their own summonses.

Question: (a) is `epic-deferred:<slug>` meant to count as placed, so the
sweep stops summoning about each deferral, and (b) is anything owed back
to the epic when a deferred finding lands?

Would settle it: the operator or the mayor saying what the deferred label
promises, and a sweep that reads it if it is meant as a placement.
