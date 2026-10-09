# An agent that takes over the every-couple-of-hours check-in

epic: governor
closed: 2026-09-10

## what this was for

Willie was checking in on the city by hand every couple of hours. The check-in
was not itself doing anything -- the mayor frequently failed to notice stalled
or quarantined workers on its own, but the act of being asked was enough to
make it look and fix them. So a human ritual was carrying real load, and it
could not be skipped.

A second duty was riding along with it. Somebody has to decide when unbuilt
changes to the framework are worth rebuilding and restarting the city for, and
to time that restart around work in flight rather than doing it blindly.

## what was done

An agent that wakes every ninety minutes, assesses the city itself, tells the
mayor specifically what needs doing, and posts a summary to the alerts
channel.

It is configuration only. An agent definition, a schedule, prompt templates,
one read-only assessment script and a preflight check for the rebuild path.
Nothing in the framework knows the role exists, which is the house rule for
every role here.

The split between the script and the prompt is the design decision worth
knowing, because weighing cannot be unit tested. The script settles what is
TRUE and what each finding COSTS, and orders its output by measured cost --
how blocked, how much work is behind it, how old -- never by importance. The
prompt decides which of several true findings to lead with and phrases it as
an instruction.

That is also why it is not simply a relay of the health checks, which already
nudge the mayor within minutes of turning red. The value it adds is the join
to consequence. A marked-out working directory is a health finding. A
marked-out working directory with urgent work queued behind it is a
four-hour outage. Equally important is the other direction: findings with
nothing stuck behind them are suppressed rather than reported.

One absence is deliberate and documented in the script so a later editor does
not undo it. The work lease is never read, in any shape. Heartbeats ride the
same event as a prompt submission, so a long working turn always shows an
expired lease, and a rule keyed on that would fire on every busy session. What
the script reads instead is whether the session row exists and whether the
terminal pane is there.

## what was found

Over a 65-hour unattended interval: forty wakes fired, forty were claimed,
closed and posted, and all forty ran with the assessment layer. Fourteen
distinct real conditions were relayed to the mayor, and zero false conditions
reached it. Seven of the fourteen closed with a timestamp -- the fastest three
minutes after it was reported.

The cadence held at 90.1 to 92.2 minutes across thirty-six intervals, with
three real gaps at 136, 183 and 212 minutes. Those two populations separate by
forty-four minutes, which is what makes the distinction usable: a rule keyed
on the nominal ninety would fire on ordinary jitter everywhere. All of the
silent wakes and two of the three gaps trace to an account quota fault that
the governor itself reported. They are not governor faults.

The assessor DID produce a false finding -- it reported the wake bead it had
just claimed as abandoned, on three consecutive wakes -- and the judgment
layer in the prompt dropped it every time, warned the mayor off the action it
suggested, and escalated it. That was then root-caused and fixed, verified
against a genuinely abandoned claim, and has not recurred in the twenty-nine
wakes since.

What one wake costs, measured on the first unattended one rather than a forced
one: thirty-seven seconds to start and about eighty seconds of work, nine
commands. At ninety minutes that is sixteen wakes a day plus sixteen mayor
turns. Treat it as a LOWER bound -- the city was quiet, there was nothing to
report, and a failed check runs its step twice. The two figures were also not
taken back to back, so the sum holds only if parked time costs nothing.

The verification lesson is the one worth repeating. The first sweep of
deliberately weakened checks flipped seventeen guards and ELEVEN survived --
far more than a read of the suite predicted. Every threshold was invisible
because only its old side was ever driven, two separate states were carried by
one over-specified fixture, and one case was reading file timestamps out of a
LIVE checkout instead of its own fixture, so it was passing on another
session's unrelated uncommitted work. All eleven were closed and the second
sweep killed all seventeen.

## where it ended

Willie ruled the manual ritual retired on 2026-09-10, in his words: the
governor has indeed made regular check-ins unnecessary.

The largest residual is open and nothing mechanical closes it. A governor that
runs the assessment and then reports something ELSE passes every check that
exists. There is proof that an assessment happened, and no proof that the
check-in carried it. Confirming the governor is truthful still costs a human
pass -- all twenty-five findings-wakes in the interval record were audited
against the store by hand.

The same blindness turned up in the measuring instrument rather than only in
the governor: the soak instrument's failed-wake path was unreachable, and two
real retries went unreported. That was filed as its own piece of work, with
the failing test that has to come first.

Deliberately not done: the fault corpus was never injected into the live city.
Four of the five fault shapes can only be manufactured by damaging the
operator's running work -- marking a live working directory stale, wedging a
session, breaking a project's commit gate -- which this work did not
authorize. They stay covered by a test suite that drives the real script
against refusing stand-ins. The live city supplied the fifth shape on its own.

One thing that was not predicted and is worth knowing: a wake that hits an
account quota is DEFERRED, not lost. The first wake parked for seventy-four
minutes and then resumed and completed itself with its work intact.

## references

- gs-x0k -- the agent definition, the ninety-minute wake and the check-in
- gs-o9i -- unbuilt-change detection and the rebuild preflight
- gs-xeh -- the assessment script and the script-versus-prompt split
- gs-nun -- the soak instrument, the faults it caught, and the bound
- gs-r76 -- the 65-hour interval record and the re-ask
- docs/governor-soak.md -- the soak record, last section first
