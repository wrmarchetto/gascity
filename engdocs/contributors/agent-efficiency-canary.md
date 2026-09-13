# Agent efficiency canary: 2026-09-13 .. 2026-09-27

The live comparison that decides whether `epic:agent-efficiency` helped or
hurt, against the baseline in
[`agent-efficiency-baseline.md`](agent-efficiency-baseline.md). Criterion 4 of
the epic, commissioned by `gs-jbyc`.

The criteria themselves are **not in this document**. They are in
[`agent-efficiency-canary.toml`](agent-efficiency-canary.toml), which
`scripts/canary-eval.py` reads and enforces. This file is the reasoning: where
each margin came from, what the window is, and -- the part no threshold can
express -- what it is too small to see. If the two ever disagree, the TOML is
the criterion and this file is the bug.

Take the reading with:

```bash
scripts/canary-fetch.sh --out /tmp/canary \
  --baseline 2026-09-04T00:00:00-05:00 2026-09-10T00:00:00-05:00 \
  --window   2026-09-13T00:00:00-05:00 2026-09-27T00:00:00-05:00 \
  --store city=/home/willie/projects/city \
  --store gascity=/home/willie/gascity \
  --store astoria-zephyr=/home/willie/projects/astoria-zephyr \
  --store astoria-sel4=/home/willie/projects/astoria-sel4 \
  --store dart=/home/willie/projects/dart

python3 scripts/canary-eval.py \
  --criteria engdocs/contributors/agent-efficiency-canary.toml \
  --baseline-usage /tmp/canary/baseline-usage.json \
  --window-usage   /tmp/canary/window-usage.json \
  --baseline-beads /tmp/canary/baseline-beads-city.json \
  --baseline-beads /tmp/canary/baseline-beads-gascity.json \
  --baseline-beads /tmp/canary/baseline-beads-astoria-zephyr.json \
  --baseline-beads /tmp/canary/baseline-beads-astoria-sel4.json \
  --baseline-beads /tmp/canary/baseline-beads-dart.json \
  --window-beads   /tmp/canary/window-beads-city.json \
  --window-beads   /tmp/canary/window-beads-gascity.json \
  --window-beads   /tmp/canary/window-beads-astoria-zephyr.json \
  --window-beads   /tmp/canary/window-beads-astoria-sel4.json \
  --window-beads   /tmp/canary/window-beads-dart.json
```

Exit 0 keeps the levels, 2 names the agents to revert, 3 means the run could
not separate the regimes and nothing may be concluded from it.

No dollar figure appears here either, for the same reason the baseline carries
none: this city runs on subscription usage, so a currency column would be
fabricated (pm-log #133).

## The window

**2026-09-13T00:00:00−05:00 (inclusive) .. 2026-09-27T00:00:00−05:00
(exclusive)** -- fourteen complete local days, on the same day boundaries and
in the same zone as the baseline.

**Why it does not start earlier.** Three things had to land, and the last of
them landed on the evening of 2026-09-12:

| Time (local) | Event |
|---|---|
| 2026-09-12 08:02 | `gs-yvxt` merged to city `main` (`0762e3b`) |
| 2026-09-12 19:31 | supervisor restarted; compiled config reaches new spawns |
| 2026-09-12 19:33:58 | the always-awake planner session respawns on it |
| 2026-09-12 21:10 | `gs-z2on` account affinity merged (`3a136c0`) |

A merge is not an activation here. `~/.gc/supervisor.toml` carries
`formula_ref = "main"`, so a formula compiles from the committed tip -- but a
running session keeps the model and effort flags it was launched with, and
those are process arguments. Any window starting before 19:31 mixes two
regimes, and a mixed window reads as a diluted result rather than a wrong one,
which is the harder kind to notice.

The affinity merge at 21:10 is the reason the window opens at the following
midnight rather than at 19:34: an affinity reshuffle arriving mid-window would
change what is being measured while it is being measured.

**What is running.** Five pooled accounts, not six -- `CLAUDE_POOL = "1 2 3 4
5"` in `city.toml` since 2026-09-12 (`ci-jhjmw8`, `2d017c1`); account0 is the
operator's alone. **The baseline was taken over six seats and this window runs
over five.** Nothing in the criteria normalizes by pool size, so no threshold
is affected -- but any later reading that divides by seats, or compares
per-account headroom across the two windows, is comparing different
denominators.

## The planner's level, and a reading that was wrong

`gs-jbyc` carries a precondition: do not open the window while the planner
session is still on `effort=max`. It is satisfied. The check, and why it needs
recording, is this:

The planner session was reported at `age=225641s effort=max` on 2026-09-12 at
21:56 local, which would have meant a 2.6-day-old session predating the 19:31
restart. **That reading was of the tmux server, not of a session.** PID
2111769 is `tmux: server`, started 2026-09-10 07:15:36; the kernel keeps the
argv of the `new-session` invocation that first started the server, and that
argv still carries `--effort max`. It is not a Claude process and it never
was.

The live session is PID 49245, started **2026-09-12 19:33:58**, running
`--effort xhigh --model claude-opus-5`, with `GC_ALIAS=mayor` in its
environment. Confirmed from two subsystems rather than one: `ps` reports its
start time and argv, and tmux independently reports the `mayor` session
created at 19:33:59 with 49245 as its pane process.

**The frozen argv is provably stale on a second axis, which is what settles
it.** It also carries `CLAUDE_POOL=0 1 2 3 4 5` -- six seats -- while
`city.toml` has said five since earlier the same day. A process whose
environment still claims the retired seat is not reporting today's effort
either.

**So check the pane, never a `ps | grep` over the process table:**

```bash
tmux -L city list-panes -a -F '#{session_name} #{pane_pid}'
tr '\0' '\n' < /proc/<pane_pid>/cmdline | grep -A1 -x -- --effort
```

Two further facts about this row, both worth carrying into the reading:

- The session was `--resume`d, so its conversation predates the restart even
  though its process does not. Effort is a process flag and every turn after
  19:33:58 runs at `xhigh`, but its context was built under the old regime and
  its early cache figures inherit that.
- Its `gc.session_id` changed across the restart (`ci-6lxm` to
  `ci-wisp-3epyfuc`). Attribution survives -- usage since the restart files
  under type `mayor`, verified live -- but a per-session comparison across the
  boundary is comparing two session records, not one.

## What changed, per agent

`gs-yvxt` did not change any model. Every model here was already pinned before
the baseline window; what it added was an explicit `effort` on each agent plus
a city-wide default of `high`, replacing an inherited provider default of
`max` that, per `a2ca19b`, "was never a decision".

| Agent type | Model | Effort before | Effort after | Role here |
|---|---|---|---|---|
| `mayor` | opus-5 | max | xhigh | subject |
| `lab.engineer` | opus-5 | max | xhigh | subject |
| `bench-engineer` | opus-5 | max | xhigh | subject |
| `lab.pm` | fable-5 | max | high | subject |
| `governor` | fable-5 | max | high | subject |
| `toolsmith` | opus-5 | high | high | **control** |
| `lab.engineer-codex` | gpt-5.6-terra | high | high | **control** |
| `adversarial-reader-claude` | fable-5 | max | high | report-only |
| `bench-sitting-astoria` | opus-5 | max | xhigh | report-only |
| `bd.dog` | opus-5 | max | high | report-only |
| `toolsmith-codex` | **changed 09-05** | high | medium | report-only |
| `bench-operator-codex` | **changed 09-05** | high | low | report-only |

**The two controls are the reason any of this is attributable.** `toolsmith`
was pinned to `opus-5` / `high` on 2026-08-11, a month before the baseline
opened, so both sides of its comparison run the same level -- and it is the
highest-volume agent in the fleet with the tightest spread of any row, which
makes it the most sensitive drift detector available rather than the least.
`lab.engineer-codex` is the same shape on a different provider:
`packs/lab/agents/engineer-codex/agent.toml` is one of the few agent files
`gs-yvxt` did not touch. A move confined to the Claude lane cannot hide behind
the second control, and a move in both is visible as fleet drift.

**The two codex rows marked "changed 09-05" are excluded from attribution and
it is not a sample-size question.** Both moved from `gpt-5.6-terra` to
`gpt-6-astra`, with effort, in `0629341` on 2026-09-05 -- *inside* the
baseline window. Their baseline is a blend of two regimes and their window is
a third. Any movement in those rows belongs to `0629341`, not to this epic.

`toolsmith` carries one more thing. `a2ca19b` left an explicit revert
criterion for it that was never resolved: "Revert to max if bead quality drops
-- more re-derived work, more wrong turns, more beads closed fail -- judged
over a day rather than one bead." This window settles that August question as
a side effect of using the agent as a control.

## Measured: the baseline spread

Everything in this section is a reading off `.gc/usage.jsonl` and the five rig
bead stores over the baseline window. Nothing in it is a projection.

### Which metric triggers a revert, and why not tokens

**Invocations per closed bead.** Not tokens per bead, and the difference
matters: the change under test lowers reasoning effort, which mechanically
lowers output tokens per turn. A tokens-per-bead rule could therefore fall
while an agent needed strictly more turns to finish the same work -- it would
report the lever moving and call it an outcome. Turn count is the figure the
lever does not move by construction, so a rise in it is evidence about results
rather than about the setting.

Tokens and wall-clock are still reported per row; they are simply not what
fires the rule.

### The spread, two independent ways

For each agent, the per-bead figure was resampled inside the baseline (beads
treated as exchangeable draws) and compared against how much the figure
actually moved across the six baseline days:

| Agent type | beads | inv/bead | resample p99.5 | worst single day |
|---|---|---|---|---|
| `lab.pm` | 303 | 15.05 | +4% | **+22%** |
| `toolsmith` | 291 | 63.40 | +5% | **+14%** |
| `lab.engineer` | 96 | 92.41 | +12% | **+13%** |
| `governor` | 67 | 10.25 | +9% | **+32%** |
| `bench-engineer` | 64 | 99.45 | +8% | **+20%** |
| `lab.engineer-codex` | 60 | 26.88 | +17% | **+33%** |

**The two disagree, and the disagreement is the finding.** Day-to-day movement
is two to five times the resampled figure for every high-volume agent. Beads
are not exchangeable draws: work arrives in correlated batches, one day all
small chores and the next all hard debugging. Had the margins been set from
the resample alone they would sit at +4% to +12%, and every agent in the fleet
would trip its revert rule during one ordinary hard week -- precisely the
failure `gs-jbyc` names.

So the margins come from the day-to-day spread. The formula and every derived
number live in the TOML.

### Does averaging days help?

A fourteen-day mean is an average of fourteen daily means, so its spread
depends on how correlated consecutive days are. Measured on the baseline, by
comparing the spread of 2-day and 3-day block means against the 1-day spread:

| Agent type | sd 2-day / sd 1-day | independence predicts |
|---|---|---|
| `lab.pm` | 0.38 | 0.71 |
| `toolsmith` | 0.79 | 0.71 |
| `lab.engineer` | 0.75 | 0.71 |

Scattered around the independence prediction with no consistent sign of excess
correlation -- but this rests on three blocks per agent and cannot carry
weight. **It is recorded as a direction, not a coefficient.** The margins
assume only three independent blocks in a fourteen-day window rather than
fourteen independent days, which is the conservative reading whichever way the
real correlation goes. The price of that conservatism is stated below.

### The planner's authored-bead failure rate

The planner closes no work beads -- it authors them -- so its own token spend
measures how hard it thought and never whether it thought correctly. It is
judged instead on the beads it created:

| Local day | authored & closed | reaching a verdict | failed | rate |
|---|---|---|---|---|
| 2026-09-04 | 53 | 35 | 2 | 5.7% |
| 2026-09-05 | 54 | 37 | 2 | 5.4% |
| 2026-09-06 | 37 | 35 | 0 | 0.0% |
| 2026-09-07 | 66 | 51 | 0 | 0.0% |
| 2026-09-08 | 40 | 32 | 4 | 12.5% |
| 2026-09-09 | 65 | 53 | 3 | 5.7% |
| **window** | **315** | **243** | **11** | **4.53%** |

The daily column swinging from 0% to 12.5% is not variation in planning
quality: at n around 40 and p around 4.5% that swing is entirely counting
noise, which is why the rate metric's margin comes from a binomial standard
error rather than from a day-to-day spread.

**A bead that closed with no `gc.outcome` is counted on neither side.** 581 of
the 1,153 work beads in the baseline window carry no outcome at all, 72 of the
planner's 315 among them. Folding those in as passes would put the rate at
3.49% instead of 4.53% and, worse, would make it fall whenever outcome
recording slipped -- degraded bookkeeping reading as improved planning.

## Predicted, not measured

Kept separate on purpose. A prediction written as a fact gets copied forward
as one, and reads as wrong rather than as tested when the measurement lands.

- **That lowering effort lowers output tokens per invocation.** This is the
  direction each subject's witness checks. It is a prediction about how the
  provider spends thinking tokens, not something this city has measured. The
  witness is deliberately weak because of that: a row is voided only if its
  output per invocation *rises* past its own margin, never on a failure to
  fall by any predicted amount.
- **That the window will carry enough volume.** The floors assume the fleet
  runs at roughly the baseline's rate, which projects about 2.3 times the
  baseline's bead count over fourteen days. If it does not, rows report
  `insufficient` and the run exits 3.
- **One reading that points the predicted way and settles nothing.** In the
  first three hours after the restart the planner's output per invocation was
  943 against a baseline mean of 1,082, a 13% fall. That is 122 invocations,
  well inside the baseline's own day-to-day range of 963 to 1,218, and it
  comes from the same process whose command line was already read -- so it is
  the same observation counted twice, not corroboration.

## What this window cannot see

**Regressions smaller than 25% to 57%, depending on the agent.** This is the
direct price of placing each line outside ordinary jitter with only six
baseline days. The smallest sustained rise in invocations per bead that this
canary would catch four times in five:

| Agent type | margin | line | 80%-power regression |
|---|---|---|---|
| `lab.engineer` | +20% | 110.89 | **+25%** |
| `lab.pm` | +30% | 19.56 | **+37%** |
| `bench-engineer` | +30% | 129.29 | **+37%** |
| `governor` | +45% | 14.87 | **+57%** |
| `toolsmith` (control) | +20% | 76.07 | +25% |
| `lab.engineer-codex` (control) | +75% | 47.05 | +95% |
| `mayor` | +105% | 9.28% | rate reaching **10%** |

A real 10% regression in any row passes this window silently. That is a known,
accepted limit, not an oversight: the alternative is a rule that fires on
ordinary work-mix variation, which would be worse because it would be acted
on.

**Reopened beads, which `gs-jbyc` names as a rework signal, are not
measurable.** The bead store keeps current state, not a change log: across
13,687 beads in the city store and 204 in gascity, **zero** open beads carry a
`closed_at`, so a reopen leaves no trace in the record a query can reach. The
only route is walking Dolt version history per bead, and that history is a
whole-database commit log -- 170 entries for a single bead -- not a per-bead
change log. The rework signals this window *can* read are the planner's
authored-failure rate and the fleet failure counts; reopens are absent, and
their absence is not evidence that none occurred.

**Skeptic rejections are not measurable either.** `hunter-the-skeptic` and the
other investigators are harness-native subagents dispatched inside a session.
They produce no bead, no session record and no usage row of their own, so
their rejections cannot be counted from any store.

**Rare events will not appear.** A rework signal that fires monthly has an
expected count near 0.5 over fourteen days. Per-agent failure counts in the
baseline are already down at 0 to 4 per six days for every agent except the
planner, which is exactly why no per-agent failure *rate* carries a threshold
here. If the window reports zero failures for an agent, that is a sample size,
not a result.

**The control cannot separate a provider-side change from a work-mix change.**
It only says "the fleet moved", which voids the run. Deciding which of the two
it was needs evidence this comparison does not collect.

**Wall-clock is reported and cannot carry a threshold.** Only 101 compute
facts exist in the whole baseline window, covering 8.5% of sessions, and the
`SPAN_S` substitute is an explicit lower bound whose per-bead figure swung
+72%, +122% and +150% across baseline days for `toolsmith`, `lab.engineer` and
`governor` respectively. The baseline document's warning that criterion 4 must
not lean on wall-clock is upheld here by measurement, not by deference.

## What to do with each verdict

- **Exit 0, keep.** Record the figures in this file under a Results heading
  and close the epic's criterion 4. `a2ca19b`'s open August question about
  `toolsmith` is answered at the same time, by the control row.
- **Exit 2, revert.** The report names each crossed agent. Revert *only*
  those, by restoring that agent's pre-epic effort in its `agent.toml`, and
  leave the rest. The bead is explicit that these are separate bets: dropping
  a technician from max to high and dropping the planner from max to xhigh are
  not the same wager and do not fail together.
- **Exit 3, no verdict.** Nothing may be concluded. Read which rows declined
  and why. An `insufficient` row wants a longer window; a moved control wants
  the fleet-drift question answered first; a `no-witness` row means that
  agent's new level probably never reached its running sessions, which is a
  configuration bug to fix before re-running rather than a result.

Do not, in any of the three cases, adjust a margin in the TOML to change the
answer. A threshold with an erased history cannot be told from one that was
tuned, which is why the planner's own pre-window correction is recorded in
that file rather than applied silently.

## Provenance

Margins fixed 2026-09-12, from the `gs-cm0w` baseline alone, with no window
figure in existence. Verified before the window opened by running the
evaluator with the baseline as *both* sides: every row reads `inside`, the
verdict is `keep`, and each recomputed baseline figure reproduces the
hand-derived table above exactly (`92.41`, `15.05`, `99.45`, `10.25`, `63.40`,
`26.88`, and `4.53%`). That is an implementation cross-check between two
separate joins over the same data -- it does not independently confirm the
data.

`scripts/canary_eval_test.go` pins the evaluator's arithmetic and all three
exit codes; `scripts/canary-eval.mutation-sweep.toml` proves each refusal is
enforced, 14 of 14 mutations died.
