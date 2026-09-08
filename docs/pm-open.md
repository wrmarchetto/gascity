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

Question: should the roadmap carry a standing epic (or an explicit
standalone policy line) for gc substrate maintenance, so off-roadmap fixes
have a declared home?

Would settle it: Willie's call in a `gc city pm plan` or `gc city pm chat`
sitting -- either a maintenance epic with acceptance criteria, or a recorded
policy that substrate fixes stay `standalone` by default.
