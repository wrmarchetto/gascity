# Agent efficiency baseline: 2026-09-04 .. 2026-09-10

The reference measurement for `epic:agent-efficiency` criterion 1, recorded
before any other criterion's change landed. Criterion 4's canary is a
comparison against the figures below, and a comparison is only as good as the
window it names -- so the window, what produced it, and what it cannot show
are all part of the record.

Reproduce any figure here with:

```bash
gc usage --by type --since 2026-09-04 --until 2026-09-10 --top 0
gc usage --by bead --since 2026-09-04 --until 2026-09-10
gc usage --by session --since 2026-09-04 --until 2026-09-10 --top 0 --json
```

No currency column exists anywhere in this document. The city runs on Claude
Code subscription usage rather than metered API tokens, so there is no cost to
compute and a dollar figure would be fabricated (pm-log #133). The two real
currencies are wall-clock and usage-window headroom.

## The window

**2026-09-04T00:00:00−05:00 (inclusive) .. 2026-09-10T00:00:00−05:00
(exclusive)** -- six complete local days.

Observed extent inside it: `2026-09-04T00:00:33−05:00 .. 2026-09-09T23:59:38−05:00`,
so the window is covered end to end rather than clipped at either edge.

**Why it starts on 09-04 and not earlier.** Model-usage attribution was broken
until `[daemon] observe_paths` landed in `city.toml`: transcripts were written
under `~/.claude-homes/account<N>/.claude/projects` while `gc` only looked in
`~/.claude/projects`, and the rows that survived came from a workdir fallback
that billed one API message to as many as 104 distinct workers
(`.plans/usage-token-accounting.md`, measured 2026-09-03). The first model fact
of the repaired regime is `2026-09-03T02:18:36−05:00`; 2026-09-02 has **zero**
model facts. 09-03 is therefore a partial day of a changing regime and is
excluded, and everything before it is unusable rather than merely sparse.

**Why it ends on 09-10.** The epic was decomposed on 2026-09-10 (pm-log #137)
and no criterion's change had landed when this was taken, so the window
contains no epic work at all. That is the property that makes it a baseline
rather than a mid-flight reading.

**What was running.** All six accounts pooled (`CLAUDE_POOL = "0 1 2 3 4 5"`),
no provider narrowed, no `claude-<N>` pin. Ordinary fleet work: 357 commits on
`gascity` `main` and 629 on `city` `main` inside the window. None of the epic's
levers were in place -- no explicit model/effort policy (criterion 3), the
per-session SessionStart beacon still ahead of the stable payload
(criterion 2), account affinity unshuffled (criterion 5), and the per-account
harness memory drift unharvested (criterion 6).

## Fleet totals

52,875 facts in the window, 0 undated, across **1,121 sessions**.

| Measure | Value |
|---|---|
| Invocations | 52,774 |
| Input tokens (uncached) | 8,568,433 |
| Output tokens | 48,849,102 |
| Cache-read tokens | 9,659,609,857 |
| Cache-creation tokens | 105,419,466 |
| Wall-clock seconds (compute facts) | 532,472.8 |
| Observed span, lower bound (seconds) | 1,787,202.8 |

Input-side composition: cache reads **98.83%**, cache creation **1.08%**,
uncached input **0.09%**.

## By agent type

Full table, largest token volume first. `-` is **not recorded**, never zero.

| Type | Sessions | Invocations | In | Out | Cache read | Cache create | Wall s | Span s |
|---|---|---|---|---|---|---|---|---|
| toolsmith | 228 | 19,748 | 39,496 | 15,740,683 | 3,730,218,916 | 32,437,208 | 102,311.8 | 502,559.4 |
| lab.engineer | 142 | 11,320 | 22,640 | 10,986,641 | 2,078,382,782 | 21,399,748 | 88,186.2 | 298,618.6 |
| mayor | 1 | 4,856 | 9,712 | 5,276,562 | 1,443,199,798 | 13,134,627 | 160,568.7 | 516,426.2 |
| bench-engineer | 132 | 7,237 | 14,474 | 7,877,358 | 1,429,555,953 | 15,581,494 | 76,391.1 | 177,058.3 |
| lab.pm | 316 | 4,989 | 9,778 | 6,689,114 | 503,209,677 | 18,902,762 | 82,218.2 | 156,810.0 |
| lab.engineer-codex | 152 | 1,980 | 5,436,585 | 782,454 | 181,780,736 | 0 | 690.9 | 35,086.9 |
| toolsmith-codex | 14 | 1,112 | 2,271,400 | 238,610 | 170,814,464 | 0 | - | 18,041.2 |
| governor | 66 | 697 | 1,392 | 530,918 | 48,313,988 | 1,931,196 | 7,538.0 | 20,947.7 |
| bd.dog | 36 | 365 | 730 | 312,246 | 23,394,751 | 1,069,056 | 360.8 | 3,579.0 |
| bench-sitting-astoria | 1 | 140 | 280 | 139,030 | 21,481,113 | 284,423 | 1,249.4 | 3,729.2 |
| bench-operator-codex | 18 | 171 | 600,108 | 40,847 | 13,392,896 | 0 | - | 1,980.8 |
| adversarial-reader-claude | 5 | 85 | 170 | 146,045 | 10,248,774 | 451,540 | 4,889.0 | 2,596.2 |
| adversarial-reader-codex | 1 | 14 | 62,024 | 4,493 | 1,768,192 | 0 | - | 254.5 |
| codex-bench-operator | 3 | 33 | 99,591 | 18,837 | 1,649,024 | 0 | - | 401.4 |
| s | 1 | 12 | 24 | 33,542 | 1,131,690 | 56,053 | - | 478.0 |
| lab.pm-ci-nwwphz | 1 | 13 | 25 | 31,593 | 982,017 | 139,912 | - | 48,610.4 |
| engineer-adhoc-e029c0763e | 1 | 2 | 4 | 129 | 85,086 | 31,447 | 33.1 | 25.2 |
| core.control-dispatcher | 3 | - | - | - | - | - | 8,035.6 | - |

Three rows are attribution residue rather than agent types, and are left as
they are on purpose (see **Known gaps**): `s` is the wisp session prefix,
`lab.pm-ci-nwwphz` is one session whose name carries another session's id, and
`engineer-adhoc-e029c0763e` carries an adhoc instance tag. `core.control-dispatcher`
is not residue -- it is the honest shape of an actor that recorded wall-clock
and no model facts at all.

**The codex lane reports differently and its columns are not comparable to the
Claude lane's.** Codex sessions put millions of tokens in `IN` and zero in
cache-creation, where Claude sessions put single-digit `IN` and everything in
cache. Compare a type against its own earlier figure, never across providers.

## Observation coverage

Per session, in the window:

| Reading | Sessions | Share |
|---|---|---|
| Tokens recorded | 1,105 | 98.6% |
| Wall-clock recorded (compute facts) | 95 | 8.5% |
| Span recorded (>= 2 invocations) | 1,041 | 92.9% |
| Neither wall-clock nor span | 63 | 5.6% |

**Wall-clock is the weak arm of this baseline and criterion 4 must not lean on
it.** Only 101 compute facts exist in the whole window, covering 95 sessions,
so the 532,472.8 s total is the sum over 8.5% of the fleet and is not a fleet
figure. `gc usage` prints `-` for the rest rather than 0.0 -- which is what
`gc costs` prints, and why a per-session wall-clock read taken from `gc costs`
is not usable here.

The `SPAN_S` column is the substitute and it is explicitly a **lower bound**:
the interval between a session's first and last recorded invocation, excluding
everything before the first and after the last. A single-invocation session
bounds no interval and reports `-`.

## By bead

152 beads in the `gascity` store are joined to a session in this window, through
the work bead's own `gc.session_id`. The largest:

| Bead | Invocations | Cache read | Span s | Type |
|---|---|---|---|---|
| gs-dzj | 224 | 55,762,578 | 5,528.8 | lab.engineer |
| gs-9zu | 185 | 45,969,354 | 3,781.3 | lab.engineer |
| gs-8ra | 188 | 34,227,098 | 7,537.3 | lab.engineer |
| gs-fn6 | 143 | 31,198,019 | 3,670.1 | lab.engineer |
| gs-nun | 137 | 28,277,811 | 2,469.2 | lab.engineer |

**976 of 1,121 sessions show as `(no bead recorded)`, and that is mostly not
unattributed work.** The join reads one store, and in a five-rig city most
sessions are held by a bead in another rig's. Read the bead grouping as "what
this rig's beads cost", not as fleet coverage; `gc usage --by bead --rig <name>`
answers for another rig.

7 sessions in this window are named by more than one bead. Those rows overlap:
each carries the whole session, because splitting it between two beads would
invent a ratio no record supports. `TOTAL` accumulates over distinct sessions,
so it is exact while the rows are not additive.

## Per-account usage headroom

Recorded 2026-09-10T22:31 local, **outside the window** -- `quota-collect.sh`
keeps only a current snapshot plus a 300 s history, so no reading of the window
itself exists. It is here as the starting headroom for criterion 5, not as a
window measurement.

| Account | Observed at | 5h used | 7d used |
|---|---|---|---|
| 0 | 2026-09-09T15:33 | 0% | 92% |
| 1 | 2026-09-10T22:31 | 15% | 79% |
| 2 | 2026-09-10T22:30 | 43% | 20% |
| 3 | 2026-09-10T22:30 | 26% | 30% |
| 4 | 2026-09-10T07:38 | 30% | 80% |
| 5 | 2026-09-10T22:30 | 2% | 13% |

**An observation's age is part of the reading.** Accounts 0 and 4 were last
written 31 and 15 hours before the rest, so their percentages are that stale:
account 0's `0%` five-hour figure is a reading from a window that has since
expired, not an idle account. A missing file would mean never observed rather
than idle; all six exist here. An absent window inside a file means not
observed, not 0.

## Known gaps

Each is a place where a plausible repair would move traffic onto an account
that did not incur it, so the gap is reported instead.

**Agent type is derived from the session name and is lossy.** The type is the
session name with the trailing `-<session id>` removed, derived from the two
fields rather than matched against an id-shaped pattern -- the same rule the
city console's tokens-by-role panel uses, so the two analytics file the same
rows under the same name. When a name carries an adhoc tag instead
(`toolsmith-codex-adhoc-e1546d8930` ran in session `ci-wisp-rkib0rl`) the suffix
does not match, and the row is kept verbatim and counted in the residue rather
than trimmed to the nearest configured agent name. 1 session in this window is
in that state; over the whole log it is 6,117 of 65,831 facts.

**There is no per-step attribution and there will not be one.** `usage.Fact`
has a `StepID` field that no production emitter fills: model usage is attributed
at run level, and per-step attribution was retired with the
`gc.active_work_bead` session pointer, which was an unsafe fuzzy session-bead
write (`internal/worker/invocation_telemetry.go`). The bead link here is the
reverse one, read off the work bead. A session that worked several beads reports
one total.

**Count facts, not lines.** `usage.ReadFacts` de-duplicates by idempotency key,
and 36 compute keys account for 17,914 lines of `.gc/usage.jsonl` -- one of them
repeated 3,383 times. `wc -l` on the log therefore overstates the fact count by
about 21%, entirely on the compute side, and a naive per-day count shows a
compute spike on 2026-09-03 that the accounting never sees. Every figure in
this document is post-de-duplication.

**The `outside window` count is not reproducible and the `in window` count is.**
The log grows, so a later re-run reports more facts outside the bounds while the
52,875 inside them stays fixed. Only the bounded figure is a measurement.

## One observation that is NOT a criterion-2 result

916 of the 917 Claude sessions spawned in this window recorded a **nonzero
`cache_read_tokens` on their first invocation**, median 58,459 tokens. Taken at
face value that reads against the epic's premise that the SessionStart beacon
"defeats prompt-cache reuse at every spawn".

**It does not establish that, and criterion 2 must not inherit it either way.**
The log records no prompt length and no cache-miss token count, so a first
invocation that matched its whole ~31K stable payload and one that matched only
the system and tools block ahead of the beacon are indistinguishable in it. The
median first-invocation read being 40% of the same session's own maximum is also
not evidence: a session's cache grows as its conversation extends, so the ratio
would look like that with the prefix perfectly reused.

Criterion 2's stated measurement -- a second consecutive spawn of the same agent
slot on the same account -- is the one that separates those cases, and no pair
of that shape can be isolated from this window's records. The figure above is
recorded so the comparison has a before, not so the question is treated as
answered.
