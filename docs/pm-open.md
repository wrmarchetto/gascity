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
