# A Slack channel that works like the mayor's terminal window

epic: mayor-slack-bridge
closed: 2026-09-11

## what this was for

Talking to the mayor meant being at the machine. The city already had a Slack
channel, but it was one-way alerts. What was wanted was a second channel that
behaves like having the mayor's terminal window open in Slack: anything posted
there lands in the mayor's prompt, and the mayor's own answers appear in the
channel without it having to do anything.

## what was done

Two small services, kept outside the main process and supervised so they come
back after a restart.

The first carries messages from the channel into the mayor's prompt. It
connects outward to Slack rather than being connected to, so no public
endpoint is opened on this machine.

The second watches the mayor's session event stream and forwards only the
final text of each of its turns -- not tool output, not other agents' traffic
-- chunking anything too long for Slack with part labels rather than dropping
it. Nothing is required of the mayor: no reply command, no nudge.

Credentials live in the same secrets file the alerts channel uses, never in
the city configuration or the repository.

Two things this work must NOT do were turned into a gate that exits nonzero
rather than into prose: no role name anywhere the bridge can execute, and the
existing alerts channel left untouched. Six refusals, each scanning a set
DERIVED from the source rather than listed by hand -- the role vocabulary
comes from the configured agents, the bridge's own file set from the recipe
that tests it.

## what was found

The gate found a live defect on its first real use, and it is the kind
nothing else would have caught: the bridge required its credentials under the
SAME names the alerts sender reads from the same file, and the bridge's own
setup instructions told the operator to put them there. Configuration alone
would have pointed the mirror at the alerts channel. The names were changed
and a refusal added for the collision.

Proving the gate was more instructive than writing it. Ten real violations
were planted in the bridge and all went red with the right message. Then the
gate itself was weakened nine different ways and every weakening was killed.
Three holes surfaced during that, each of which had made the gate green over
exactly the violation it existed to catch -- an import scan that matched only
one quoting style, so a single differently quoted import dropped its whole
subtree from the walk, the launcher scripts being unscanned at all, which is
the first place a default role name lands, and a pattern rule whose wildcard
crossed directory separators and so treated every module as a package root.

The mirror's first live outing produced a flood: ninety-two messages in
forty-five seconds on one cold wake, because it was publishing the mayor's
running narration between tool calls rather than its answering turn. Fixed by
treating a replayed snapshot of the stream as a watermark and publishing only
genuine growth. The re-run held at two messages in the same opening window.

A separate piece of the inbound path was telling every agent in the city to
run a command that no part of the program implements. It was removed.

The first acceptance demonstration was closed as a FAILURE on purpose,
although the round trip itself worked. Two reasons, and both are worth
reading. The operator's first message went nowhere at all: the channel had
been created private, the bot held only the public-channel permission, and
Slack was dropping the events silently with no error anywhere on this side.
He made the channel public and it worked on the next post with no code change
-- which incidentally proved the adapter does not care about channel type.
But that run also had the mirror stopped and restarted by hand partway
through, which measures the operator rather than the bridge.

## where it ended

The round trip was demonstrated for real on 2026-09-11, unplanned, which is
the strongest form it could have taken: Willie posted from Slack, the message
reached the mayor, the mayor answered in the channel, and nothing was started,
stopped or restarted by hand at any point.

The evidence for that last clause is NEGATIVE -- that nothing was touched --
so a transcript cannot carry it. What carries it is that both services'
process start times were thirty-five minutes before the demonstration began,
and both predate the mayor session that answered. Start time is the stronger
record than matching process numbers, because a process number can in
principle be reused while a start time proves the process ran continuously
across the round trip.

One acceptance criterion was WITHDRAWN rather than met, and nobody should
read this as evidence it works. Waking a sleeping mayor from a channel message
has never been demonstrated. Willie's ruling was that there is no value in
proving it, because the mayor is always awake in normal operation. That
withdrawal expires with its premise: if the mayor ever stops being an
always-awake session, the criterion returns, and it belongs to whoever makes
that change.

## references

- gs-wnn -- the inbound adapter and its outward-only connection
- gs-eh2 -- the mirror daemon, assistant turns only, chunked
- gs-lq9 -- the credential-name collision with the alerts channel
- gs-8ra -- the two negatives made mechanical, and the three holes in it
- gs-a9bm, gs-msc2 -- supervising the pair so it survives a restart
- gs-t0d8 -- the ninety-two message flood and the watermark fix
- gs-77tn -- the inbound reminder naming a command that does not exist
- gs-z09 -- the first demonstration, closed failed, and why
- gs-z1m9 -- the live round trip and the withdrawn cold-wake criterion
