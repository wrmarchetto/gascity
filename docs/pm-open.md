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
