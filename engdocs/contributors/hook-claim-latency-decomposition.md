# Where `gc hook --claim` spends its wall clock

One claim run, decomposed by subprocess and by syscall, so the next
person arguing about claim latency argues against numbers instead of
against intuition.

This exists because an aggregate answer was already available and was
not enough. 215 recorded claim runs established that the empty-output
failure a codex session saw is exactly `duration > the caller's yield
budget` -- every crossing empty, every non-crossing fine, all exiting 0
with correct stdout. That made latency the entire exposure and left
one question open: WHY is the command sometimes slow. Aggregates cannot
answer that, because no single run had ever been broken down. This file
is that breakdown.

Measured 2026-08-14 on `dbz1`, city `/home/willie/projects/city`, from
the gascity worktree `.gc/worktrees/gascity/toolsmith-2`. Numbers age;
the method below is the durable part.

## The answer

Three candidate explanations were on the table. Naming them as they
were posed:

- (a) the four-store discovery sweep over mostly-empty stores
- (b) `bd update --claim` plus the bookkeeping round trips
- (c) per-process `bd`/Dolt connect overhead multiplied by ~60
  invocations

It is **(c)**, and (a) is the multiplier that makes (c) large: the
four stores are queried STRICTLY SERIALLY, twelve `bd` processes each.
(b) is not a factor -- 13 of the 14 slow runs in the 2026-08-12
episode were `action=drain`, which mutates nothing, and the drain path
still pays the full ladder.

A fourth cost none of the three anticipated turned up and matters more
than its size suggests: **every `gc` invocation that loads city config
re-reads the same cached pack files dozens of times**, ~21k `openat`
calls into `~/.gc/cache/repos/` over only ~1.5k distinct paths. It is
scope-invariant, it is most of what `gc prime` does, and it is
therefore the shared component that explains why `gc prime` -- which
never runs a work query at all -- degraded 6x in the same window as the
claim. Any account of that episode that reaches only for the work-query
ladder cannot explain the `gc prime` number; this one can.

## Method

No code change is needed to reproduce any of this.

```bash
strace -f -tt -e signal=none -e trace=execve -o /tmp/claim.trace \
  gc hook <agent>
```

`-e signal=none` is not cosmetic. Without it the trace is buried in
Go-runtime `SIGURG` preemption lines at a ratio of roughly 20:1.

Do NOT add `-qq`. It suppresses the `+++ exited with N +++` lines, and
those are what pair an `execve` with the end of that pid's life. A
first pass here ran with `-qq`, silently fell back to "assume the child
ran until the trace ended", and produced a decomposition in which four
different subprocesses each appeared to consume 4.5s of a 6.8s run.
Every conclusion drawn from it was wrong.

Pairing `execve` to `+++ exited` per pid is safe even though a Go child
is multi-threaded: strace reports each thread's exit under its own tid,
and only a pid that recorded an `execve` is ever matched.

The pid counter is the cheapest load instrument on this host and needs
no tracing at all:

```bash
mark() { sh -c 'echo $$'; }
a=$(mark); gc hook <agent> >/dev/null 2>&1; b=$(mark)
echo "consumed $((b-a-1)) pid slots"
```

It counts threads as well as processes, since `clone(CLONE_THREAD)`
allocates a pid. That is a feature for this purpose -- thread creation
is real scheduler work -- but it means the number is not a process
count and must never be reported as one.

## The ladder is serial

From an `execve` trace of `gc hook toolsmith-codex` (`scope = "city"`,
federating four bead stores):

```
store work_query passes (sh -c ... -- toolsmith-codex):
  start + 2.108s  end + 3.911s  dur 1.803s   12 bd invocations
  start + 3.914s  end + 4.587s  dur 0.673s   12 bd invocations
  start + 4.589s  end + 5.302s  dur 0.713s   12 bd invocations
  start + 5.305s  end + 6.006s  dur 0.701s   12 bd invocations
```

Each pass starts 2-3ms after the previous one ends. There is no
overlap to exploit and none is being wasted; the four stores are simply
walked one after another. 49 `bd` processes in the run, plus 20 `jq`,
49 `git rev-parse` and 28 `git config`.

Untraced wall clock for the same command, five runs: 4.11 / 4.20 /
3.48 / 3.89 / 3.07s. For a narrow-scope agent (`toolsmith-2`, one
store): 0.55 / 0.69 / 0.58 / 0.61 / 0.78s. The delta is four serial
store passes, not a difference in what any one pass does.

A single bare `bd query --json 'status=open' --limit=1` against the
city store costs 0.18-0.20s, five runs. That is the floor a per-process
design pays 49 times. Nested `bd` invocations are cheaper than that
floor because the parent has already resolved repo context, which is
the same observation from the other side: the cost is context
rediscovery per process, not query work.

## The fixed pack-cache re-walk

`gc prime` renders a prompt template. It runs no bead query, touches no
store, and completes in 0.19-0.22s. What it does instead:

| syscall      | calls  |
| ------------ | ------ |
| fcntl        | 70,736 |
| read         | 35,383 |
| newfstatat   | 26,998 |
| openat       | 26,851 |
| close        | 26,465 |
| fstat        | 18,237 |
| getdents64   | 17,371 |

About 220,000 syscalls to expand a template. 25,474 of the `openat`
calls are under `~/.gc/cache/repos/`, across **1,487 distinct paths**.
The average cached pack file is opened ~17 times per run. The worst
are not near the average:

- `.../examples/bd/dolt/pack.toml` -- opened **75** times
- every sibling under that pack (`orders/*.toml`,
  `commands/*/command.toml`, `agents/*/agent.toml`,
  `doctor/*/doctor.toml`) -- opened **exactly 56** times each

The count does not move with agent scope. `gc hook toolsmith-2`
(narrow) and `gc hook toolsmith-codex` (four stores) both perform
exactly 21,288 cache `openat` calls; only the subprocess count differs,
24 versus 167. Even `gc --version` -- not a real flag, rejected with a
usage dump and exit 1 -- performs 8,764 of them before the command is
dispatched.

Warm, the whole walk costs well under 0.2s and hides inside every
other number here. That is precisely why it went unsuspected for so
long, and why it is worth naming: it is ~90k filesystem operations
whose cost is entirely a function of dentry and page-cache residency,
which is the first thing a burst of concurrent process creation
degrades. It is also the only substantial thing `gc prime` does.

## What a claim costs the host

Pid slots consumed per invocation, measured by the `mark()` method
above:

| invocation                        | pid slots |
| --------------------------------- | --------- |
| `gc hook <city-scoped agent>`     | 1,068     |
| `gc hook <narrow agent>`          | 179       |
| `gc prime <agent>`                | 37        |
| `gc --version` (rejected flag)    | 28        |

One city-scoped claim allocates over a thousand pids. Against this
host's idle baseline of ~40 pid/s (next section), that single command
is 27 seconds' worth of the machine's entire baseline pid-allocation
rate.

Cost therefore scales with agent scope, which means city-scoped agents
pay for the whole city's store list on every claim, every session, at
startup.

## The 2026-08-12 window, and what survived of it

The episode: between 13:33:05 and 13:43:49 on 2026-08-12, every claim
took 11-15s where 3-4s was normal, and 13 of the 14 recorded runs over
10s fall inside it.

**Almost nothing was retained.** Stating what is gone matters as much
as what is left, because the absences are why this had to be
reconstructed rather than read:

- `sar` is installed and its 5-minute `debian-sa1` cron fires, but
  `/etc/default/sysstat` has `ENABLED="false"` and
  `/var/log/sysstat/` is empty. There is no stored host-load history
  for this window or any other.
- The session-reconciler trace rotates into ~14MB two-hour segments
  and retains only the current day. The 08-12 segments were already
  gone by 08-14.
- The Dolt server writes no log file, so no per-query latency record
  exists.

Two things did survive.

**The Dolt server did not restart.** `dolt sql-server` for this city
started 2026-08-12 08:45:54 and was still the same pid on 08-14. It
spanned the episode without flapping, which removes a restart or a
crash-recovery stall from the candidate list.

**The journal's pid numbers are an intact fork-rate record.** The
sysstat cron logs a pid every 5 minutes whether or not sysstat itself
collects anything, and `pid_max` here is 4194304, so nothing wrapped.
Differencing consecutive samples gives pid allocations per second:

```
     interval        pids   pids/s  x-baseline
11:35-11:45         24271     40.5   1.0x
12:35-12:45         24278     40.5   1.0x
12:45-12:55         72579    121.0   3.0x
12:55-13:05        268300    447.2  11.1x   <-- storm begins
13:05-13:15        195455    325.8   8.1x
13:15-13:25        242216    403.7  10.0x
13:25-13:35        263828    439.7  10.9x   <-- contains 13:33:05
13:35-13:45        130260    217.1   5.4x   <-- contains 13:43:49
13:45-13:55        107538    179.2   4.4x
13:55-14:05        104721    174.5   4.3x
14:05-14:15         24326     40.5   1.0x   <-- back to baseline
14:15-14:25         24248     40.4   1.0x
```

The slow episode sits inside a sustained 4-11x elevation in process
and thread creation that begins around 12:55, peaks in the bucket
containing 13:33, decays through the bucket containing 13:43:49, and
returns to exactly baseline at 14:05. Elsewhere in the afternoon the
host alternates between 40/s idle and ~120/s bumps; nothing else that
day approaches 440/s.

This refines the earlier account rather than confirming it. The
suspected trigger was a session spawn storm from 13:20:39 to 13:33:05.
Elevated process creation in fact starts about 25 minutes EARLIER, at
12:55, so the spawn storm is a segment of a longer load period and not
the whole of it.

The journal is otherwise clean across 13:25-13:55: no OOM kill, no
I/O error, no service restart, no unit failure. Whatever consumed the
host was ordinary userspace work, which is consistent with the
measurements above -- gc's own claim path is one of the most
fork-intensive and syscall-intensive things running here.

## What this does NOT establish

- **Causation for the 2026-08-12 episode.** The pid-rate record is a
  correlation between a 4-11x process-creation storm and a 3x claim
  slowdown, on a command measured to be extremely fork- and
  syscall-heavy. That is a strong correlation with a plausible
  mechanism, and it is still not a controlled experiment. Reproducing
  it means driving synthetic concurrent load and watching claim
  latency, which was NOT done here -- deliberately, because generating
  that load on this host disrupts every other session on it.
- **The cost of the pack-cache walk under contention.** It was
  measured warm only, for the same reason: the cold-cache measurement
  needs a cache drop, which requires root and penalizes the whole
  fleet.
- **Why 56 and 75.** The multiplier on the re-walk is measured, not
  explained. What produces exactly that many repeats is a question for
  the config loader, tracked separately.
- **strace timings as absolute values.** strace adds roughly 100us per
  syscall, which on a 220k-syscall command is not a perturbation, it
  is most of the traced run. Every traced number here is used for
  ATTRIBUTION -- which component, in what order, how many times -- and
  every duration and count claim is taken from an untraced run.

## Sources

- `cmd/gc/cmd_hook_claim.go` -- claim protocol, including
  `hookClaimExistingAssignment` (the re-run idempotence relied on by
  the harness-side mitigation)
- `internal/config/implicit.go`, `internal/config/revision.go` --
  pack cache root and convention discovery
- journalctl on `dbz1`, 2026-08-12 11:00-16:00, `debian-sa1` cron pids
