# Self-review: epic:bead-cycle-time

Covers:
- as-8384y 9a55b09d56ce2935cd4b241a56efcbbd35b49b31
- as-hq04p e54d0de3709a259481727db7bd0fff4bdce92bae
- az-8ujz bf3e658f8992d110a3cd66e9d68ed3ea6c44a2ac
- az-r41s 36e259ea94a453761c4428bb074eea6d2ac7b999
- ci-3imbeu 1fe0ea1eadf6e1facb89848ecbb0041c0f888b65
- ci-5c95j4 4f0d887e5de9193420692dbd356b48b4db5afe27
- ci-71d5yl 0f658224c993efa246d056b75e72bb86ed2dfaf8
- ci-7npmly none
- ci-bq7qvz 19bb445c50af6f5ab55ef445313e50d98686e41c
- ci-ce84ou 682d890b5e63205e6d3ea3191e187015f35649bd
- ci-cicsvc 8354d8b5e3f8e2ff729ee249e3aaae422681034d
- ci-cj9utx 19399c5a0a592907d048a969401ef9486bdb6dd0
- ci-cu1tu3 none
- ci-f9cj1b 61b49df5a2f8788ee92a822debcd3434f4f40595
- ci-fb3uff 3fe364d77072c6b72bea8bdaf55868a74133fdc0
- ci-m5pd91 8ab57c3655330678dc7cdce048e7cd0d6b783440
- ci-m6ifdq 1c4026399dc9a119a46cd2ac79e95b2c2b9cfc32
- ci-mozmau 712dea4269ab9ee07c0b009b7d224934d412cff4
- ci-ms65m7 6094f34cdb83f9a5aa8e7d3af4ad377b5c20e2c0
- ci-ms847d 2df7e0b6da96d6f0f47dae4302c74ff649a799ae
- ci-npfgao 22b77062daf687db1a5e74f672a66b1f7c2671c9
- ci-p8kdpw 460b52d5ef075f953899b00b3ea0c4e205f1a6b1
- ci-r81svr 5fe037c369176ba1d2acdbccb25dcf20335cf3f6
- ci-sx056l none
- ci-tr56b0 6d2e61066b5a9876cc0e09fa28333aecc7ff775c
- ci-ufxl74 00dabd0a3ad543d607acaa01661c964ecda5d292
- gs-0y4d 57285ce8ee725aede76717b282c4a625d275d034
- gs-46s4 2a8db9c3d0d19e05dffbe70ad91a015dd8429c91
- gs-gln1 none
- gs-okl8 39e2e75adbd8cd0ce9d6f8563fb58ec5334e2108
- gs-r9ph 154386504d349d196fabf3365de598d7daf0c86a
- gs-rvw5 153970e45d70ff489ff4a2e9b25f16071c71b5f0
- gs-sch3 7a01e0ea6cb08f2f3863a2fd980f66e7b279c41e
- gs-sodh 47115639d382747f53378a6055624d2d53c3fd7b
- gs-wk1w 818f96907d8af3d8e3710217efeaa0d49058337e
- gs-zle6 623370e2ecd57d31494144f354dd9ec1e7d3607b

Pass taken by gs-q1ix over gascity `e6cc8beaa` and city `a0798b51c`, with
the city fixes rebased onto city `9282c3991`. The `ci-` members and gs-sodh
landed in the city repo, the other `gs-` members here, each read over its
whole merged branch, not only its listed commit. ci-fb3uff's `3fe364d77`
and ci-tr56b0's `6d2e61066` were read at their branch tips, their sweep
merges `c9e50d8ab` and `7487c4c89` (21:44Z on 11-05) being on no branch
then. Both are on city main at the refresh.

Refreshed by gs-hyos over gascity `d1b1ccfbd`, city `091587a88`,
astoria-sel4 `a3783b03` and astoria-zephyr `d0d98eb8`, after city
ci-5li8wk (finding 7) let the Covers block name the four astoria members.
Each was read over its whole merged branch: as-8384y's `4a817c4c` and
as-hq04p's `08b314a9` in sel4, az-r41s's `e687d003` and az-8ujz's
`173729bb` in zephyr, the last carrying az-10ry's `c0d1eb8d` after the
listed commit. Their tools were run at the astoria shas above, in scratch
worktrees, and the gs-q1ix bullets stand as written unless amended here.

Refreshed again by gs-g0m7 over gascity `17c9657d7`, city `df79a5f81` and
ci-mozmau's branch tip `712dea426`, after review g1's `unmet:target`
(pm-log #195) placed ci-71d5yl, whose re-measure filed ci-m5pd91 and
ci-mozmau. ci-71d5yl's `0f658224c` and ci-m5pd91's `8ab57c365` are on city
main in `5461080e2` and `df79a5f81`. ci-mozmau's `712dea426` is on gascity
branch `fix/ci-mozmau-pool-tiers-one-fifo`, queued in the sweep and on no
main at this pass, so it was read and run there. The earlier bullets stand
as written unless amended here.

## Code against docs

- City `docs/merge-queue-wait.md`: its re-run command, as written, over
  `docs/merge-queue-wait-passes.json` writes `docs/merge-queue-wait.csv`
  byte for byte.
- City `docs/bead-cycle-time-cohort.md`: the second re-run command, joined
  to the cohort CSV on (repo, branch) as its Method says, reproduces the
  queue table: city 7 rows 36m / 59m, sel4 32 rows 32m / 78m, zephyr 18
  rows 4.2h / 7.6h, and every share cell. Unjoined, the script prints
  zephyr n=20, so the 18 rests on the join the page states. The first
  command cannot be re-run: the cohort opens 11-04T12:29Z, the oldest
  retained run log is now 11-05T00:11Z, and the page says its copy was not
  kept.
- City `docs/bead-cycle-time-post-change.md`: its queue command over the
  committed passes file prints the city gate figures the page quotes, 5
  gates, 5m / 7m / 7m, 0.54 and 0.70 of the 600 s cap.
- City `docs/bead-cycle-time-baseline.md`: method and classifier rows read
  against `bead-cycle-time.py`. `q()` is linear interpolation. PUSH_SELECT
  matches every line `.githooks/pre-push` prints at gascity main except its
  catch-all "unrecognized scope decision", which reads as unknown. The
  re-run cannot be repeated, its transcripts from 09-22 having rolled off,
  as the page warns.
- City `docs/merge-sweep-batching.md`: admission, landing and bisection
  match `try_batch` and `bisect_batch`, and every live repo, gascity
  included, sets `batch_max=None`. Its section "gascity's gate runs no Go
  tests" was false from ci-ms847d on (finding 4).
- City `docs/test-growth-gate.md`: repo order, merge-base and direct-commit
  counting, the fail-closed list and the relative `--reason-file` rule all
  match `test-growth-gate.py`, which is the last entry in
  `bd-prewrite-gates.py`. Its claim that the unit patterns are the
  baseline's holds for Go only (finding 5).
- City `docs/fleet-review-gate.md` and the mayor prompt (ci-ce84ou):
  `notify-on-close.py` mails an unkeyed review's filer within its 6 h
  `REVIEW_WINDOW`, skipping an order, nobody or the reader, and the prompt
  carries the fix-round key the code cannot set, as the page says.
- City `docs/city-gate-suite-cost.md`: `epic-review-drive.py` reads the
  generations with one `bd list --id` per tick, and the console,
  city-status and mayor-chat suites serve at `poll_interval` 0.01.
- City prompts: the extend-before-add directive is in
  `packs/lab/agents/engineer/prompt.template.md` and
  `agents/toolsmith/prompt.template.md`.
- gascity `TESTING.md`, "Scoping the push gate": the hook, selector and
  `scripts/test-affected` do what it says. Its job-layout and priority
  paragraphs match `scripts/test-local-parallel` (`UNIT_CORE_TOTAL` 8,
  `PRODUCTMETRICS_TOTAL` 1, `GO_TEST_TIMEOUT` 20m), and the city sweep's
  gascity gate sets `LOCAL_TEST_YIELD=0` as it requires of a merge-path
  caller.
- gascity `AGENTS.md` said the push gate runs the full fast suite
  (finding 1). `scripts/push-gate-select` cited a docstring note gs-rvw5
  deleted, and `scripts/push-gate-replay.py` a `TESTING.md` passage by its
  old title (findings 2 and 3).
- as-8384y, sel4 `docs/affected-host-tests.md`, its `CLAUDE.md` paragraph
  and spec D16: selection rules 1 to 5, the seam (the three roots, what
  they import or source, `dts/`, `gen_layout.py`, the `(session)` row), a
  rename as a removal and an addition, exit 5 on an empty selection,
  `--record-reads` refused beside any flag, and the xdist refusal all match
  `affected_tests.py`, `host_suite_reads.py` and `run_host_tests.sh` at
  `4a817c4c`. sel4's later `641f6b8f` (F27) stamps the table with its
  recording commit and runs whole every module edited since, and the page
  was amended in the same commit.
- as-hq04p has no page: its ranking and sizes are its close reason and
  bead comment. Read in code: `b25d690c`'s candidate pattern is a superset
  trigger of HEX, DEC and SHIFT on one line, each hit line read whole.
  `e54d0de3`'s lexer memo keys on text and path and hands each caller a
  fresh list. `67b45c1f` removed the never-released case and moved its one
  distinct assertion, `openocd.refused == 0`, into the slow-poll case.
- az-8ujz, zephyr `docs/hardware-testing.md` "Affected-only runs" and
  `CLAUDE.md`: the four full-run triggers, the added-line rule, the build
  roots and the whole-tree reader rule match `select` and `change_set` in
  `affected_tests.py`. `run_tests.sh` refuses `--hardware`, the `--memc-*`
  arms, `--board` and `--no-retry` beside `--affected`, and `--host` by
  its own refusal, as the page says. The page does not say the selected
  gates run one at a time (finding 10).
- az-r41s, zephyr `docs/hardware-testing.md` "The repo gates": nproc
  slots, the one foreground line, GATE FAIL by line, GATE STOP, INCOMPLETE,
  the re-raised signal and the bash 5.1 refusal match `run_repo_gates.sh`.
  Its "about 180 invocations" is 227 at `d0d98eb8`, and zephyr `b0510283`
  already replaced it with the `REPO GATES:` line's own count.
  `docs/gate-suite-cost.md` quotes the baseline's zephyr rows, its 82 of
  118 budget-exhausted rows and its suite sizes as city `659b7fd0` states
  them, and the 890 s cap as `ZEPHYR_GATE_TIMEOUT` still reads on city
  main. The merge's `docs/invariant-provenance.md` paragraphs are a comment
  block `5408f992` moved there, as its body says.
- ci-71d5yl, city `docs/bead-cycle-time-remeasure.md` (ci-ozsp1r's
  report). Its second re-run command over the committed passes file
  reproduces the queue-decomposition table, the gate table and the load
  paragraph cell for cell. Its first, over a copy of the 220 run-log files
  retained at this pass, reproduces the Finding and Stages tables, every
  call and every p90. That CSV differs from the committed one only in
  `passes_passed_over`, by 1 on three city rows (ci-pl7szb, ci-qr8hlr,
  ci-pqa4fn), because the oldest retained log is now 11-07T03:45:13Z
  against the report's 01:31:38Z. The anchors the page reads from outside
  the tools hold: city main's reflog puts `7487c4c89` at 11-05T21:57:31Z
  and `0becc0ea9` at 11-06T11:38:02Z, the 2.1.319 binary was born at
  11-06T01:30:31Z, and zephyr's `77cef984`, `7230e5c6` and `3a617a98`
  carry 19:30:36Z, 19:31:49Z and 17:41:04Z.
- ci-m5pd91, city `docs/fleet-review-gate.md` "After the relay fix". The
  Claude reader's cap of 1 is `agents/adversarial-reader-claude/agent.toml`,
  `c11223729` cites ci-t1m2c5, the attestation clause is
  `agents/adversarial-reader/prompt.template.md`'s, and the cross-model
  rule is the 08-31 paragraph of `docs/codex-rig-engineer-brake.md`.
  Recounted from the city store: 33 reader reviews keyed
  `gc.fleet_review_of` were filed in the window, 32 on the Claude reader
  and 1 on Codex, and 27 flagged (7 attested, 20 withheld) and 6 passed,
  as the page says. Its claim-wait minutes and slot-busy shares were not
  re-derived. It cited the re-measure on a branch that had already landed
  (finding 11).
- ci-mozmau, gascity `internal/config/workquery.go` and
  `engdocs/architecture/dispatch.md`. The generated query runs both pool
  tiers and sorts the merged rows by `created_at`, then `id`. bd prints
  `created_at` as whole-second UTC (`2026-11-07T23:51:42Z` on gs-g0m7), so
  the string sort is the time sort. The city store's times for ci-x1o46h,
  ci-qr8hlr, ci-p27qhl, ci-6uq71o, ci-uon713 and the three beads that
  filled the pool match the commit body. The merge's comment misdescribed
  a failing tier (finding 12). The page reported the replay's 2.6 h as
  measured, and said the work-query form prints the first match
  (finding 13).

## Docs against spec

Against the roadmap's acceptance list at gascity `e6cc8beaa`, read with
pm-log #150 to #193.

- Baseline: met. `659b7fd0f` reached city main in `c6cccfbff` at 10-23
  15:33Z. The first reduction to land, gs-zle6's `1b93a1575`, followed at
  20:15Z.
- Target: unmet at the last measurement. ci-cj9utx's cohort misses in
  city, astoria-sel4 and astoria-zephyr, and gascity and dart have no row.
  Its stage beads have shipped: ci-5c95j4 landed at 19:56Z, ci-ce84ou at
  20:06Z, and zephyr's ci-fb3uff and ci-tr56b0 at 21:44Z on 11-05. No
  measurement follows any of them at the refresh. gs-q1ix's own cohort run
  is a determinism check, not a re-measure: its last commits end at
  16:50:19Z, so it tests none of the four.
- Merge queue first: met. Lanes (`6094f34c`, ci-ms65m7) and one merge
  result per repo with bisection (`8354d8b5`, ci-cicsvc, ci-sx056l closed
  as its duplicate) landed, and queue wait is decomposed before
  (`docs/merge-queue-wait.md`) and after (post-change and cohort pages).
  The cohort still finds the queue dominant behind a red main (zephyr) and
  a held branch (city), the stages ci-tr56b0 and ci-ce84ou address.
- Slow tests first: met. Each rig with a material suite is ranked: city
  (`docs/city-gate-suite-cost.md`), gascity (gs-zle6, gs-okl8, gs-sch3,
  then gs-gln1, closed on 9 consecutive gates at or under 714 s), zephyr
  (az-r41s) and sel4 (as-hq04p). dart is ruled met by the baseline
  (pm-log #157). The one test merged, in as-hq04p, cites a 5-row
  redundancy sweep.
- Extend before add: met. The directive is in both prompts, and the gate
  is seen to refuse: ci-ufxl74's own close without a `new-tests:` line
  exits 1. Bound: its runtime half reads unmeasured on every close at this
  pass, because no `/var/tmp/gascity-test.jsonl.*` exists, and only
  `make test` writes one. pm-log #152 gives that half to #151's report.
- Run once per landing: met, with the done-criterion's text wrong until
  finding 1. The sweep runs each repo's full suite once over the combined
  result (ci-cicsvc), gascity's included (ci-ms847d). In-session and push
  runs are affected-only in gascity (gs-rvw5, gs-0y4d), sel4 (as-8384y)
  and zephyr (az-8ujz). gascity's push-gate fallback rate is measured in
  `TESTING.md`, and its common trigger, a new remote branch, is fixed by
  gs-0y4d. Bounds on the astoria half at the refresh: sel4's table,
  recorded at `641f6b8f`, runs whole the 177 of 332 modules edited since
  and the 29 it never measured, so any affected run there selects at
  least 206 of 332 until `--record-reads` runs again, a cost its page
  states. zephyr's affected run executes its selected gates serially
  (finding 10). Neither has a push gate to narrow.
  `docs/bead-cycle-time-post-change.md` counts each rig's in-session full
  and scoped runs per landing since, but times none of them, so az-8ujz's
  and as-8384y's savings in time are still their own predictions.
- Target, at this refresh, read against `docs/roadmap.md` at `17c9657d7`
  and pm-log #194 to #198: unmet at the third measurement. ci-71d5yl's
  re-measure (cohort 11-05T21:57:31Z to 11-06T23:58:52Z, measured at
  11-07T22:21Z) misses in city, astoria-sel4 and astoria-zephyr. gascity
  misses as found at n=7, and its half is accepted on that sample by the
  operator's ruling (pm-log #196). dart is unmeasured at n=1. ci-71d5yl's
  done-criterion, a committed report and every rig's verdict in its close,
  is met. City's stage bead ci-m5pd91 closed as a finding. The mayor's
  routing of Claude-authored reviews to the Codex reader followed it, held
  in `gc bd remember` and in no mechanism, and the attestation question
  is with the operator. sel4's ci-mozmau is on its branch. zephyr has no
  stage bead of its own, its red main waiting on the operator through
  ci-t7q8kp. Every stage line is predicted, untested, and the next
  re-measure that ci-71d5yl's close calls for is placed by no bead yet.
- The other four criteria are unchanged by the three new members.
  ci-mozmau changes claim order, not the sweep, and its test change and
  this pass's are each net 0 test functions.
- Left for the pm-epic-close this review leads to: the roadmap's
  Conclusion and `engdocs/epics/bead-cycle-time.md` still call the stage
  fixes unmeasured and ci-71d5yl open. Both were true at #195, and both
  are the PM's to rewrite with the verdict.

## Run twice

- `bead-cycle-time.py --cohort 2026-11-05T01:00:00Z 2026-11-05T16:50:19Z
  --until 2026-11-05T21:40:00Z` over one frozen copy of the 192 live run
  logs, twice: CSV and stdout byte-identical, 110 s and 149 s. Its sel4
  and zephyr rows equal the committed cohort's, since the earlier T0 adds
  rows only in city.
- `merge-queue-wait.py` over the cohort passes file twice: identical. It
  reads the CSV `bead-cycle-time.py` wrote as `--baseline-csv`, which is
  the one tool run over the other's output.
- `test-growth-gate.py` on ci-ufxl74, gs-zle6 and gs-okl8, twice each:
  identical lines and exits, refusing +1 and +3 and admitting 0.
- gascity `scripts/push-gate-select` over three member commits pushed
  against their parents, twice: full for `623370e2e` on `TESTING.md`, and
  the same 63-package scope for `2a8db9c3d` and `39e2e75ad`. `--worktree`
  twice: none.
- gascity `scripts/push-gate-replay.py replay` over 2026-09-22 to
  2026-10-23, then `summarize` over its own rows: 38 landings, 33 changing
  a Go build input, 27 scoped, 6 full (18.2%), 5 none, as `TESTING.md`
  records. That needed finding 3's fix: before it, summarize would not run
  without a `make test` log.
- sel4 `scripts/affected_tests.py` at `9a55b09d5` against `~8`, and zephyr
  `scripts/affected_tests.py plan` at `bf3e658f8` against `~3`, twice
  each: identical, both full because the selector or `run_tests.sh`
  itself changed.
- sel4 `affected_tests.py` at `a3783b03` against `HEAD~5` and `HEAD~1`,
  twice each: identical. `HEAD~5` is full, `dts/astoria-soc.dtsi` having
  changed. `HEAD~1`, three sweep specs, selects 214 of 332 modules, 206 of
  them run whole for the table's age (Docs against spec).
- sel4 `host_suite_reads.py`: one module recorded with `--record-reads`,
  then `fold` over its log directory twice: byte-identical tables. The
  selector reads that table as full, "the table names no recorded tree",
  because only `record` writes the `(recorded at)` stamp and `fold` does
  not. That errs toward running more and was left.
- zephyr `affected_tests.py plan` at `d0d98eb8` against `HEAD~1` and
  `HEAD~3`, twice each: identical, 75 and 90 of 227 invocations, build
  skipped. `run_tests.sh --affected --base HEAD~1` then ran the plan's own
  output: green, 362 s at load 12 to 34, build skipped, and its verdicts
  block names the affected run.
- zephyr `run_repo_gates.sh` over the same tree: 227 invocations, 0
  failed, 425 s at load 34 to 73. The plan's 75 lines back to back, each
  sourcing `gate_quiet.sh` as the affected arm does: 121 s through `xargs
  -P 24` at load 41 to 48, then 578 s one at a time through a spike to
  about 120. The affected run's own serial gates took about 345 s at load
  12 to 34, so even at the lower load serial cost 2.9 times concurrent
  (finding 10). No gate failed in either.
- as-hq04p added no tool. Its equivalence and memo cases, `-k 'literal or
  lex or memo'` over `test_check_layout.py` and `test_layout_derivation.py`,
  pass at `a3783b03`, 56 of them.
- The city suites of every script the epic touched, at `a0798b51c`:
  bead-cycle-time and merge-queue-wait OK, test-growth-gate 29/29,
  notify-on-close OK, epic-review-drive 57/57, merge-closed-features 1819
  checks in 464 tests.
- ci-71d5yl changed no tool. Its report's second command,
  `merge-queue-wait.py` over `docs/bead-cycle-time-remeasure-passes.json`,
  twice: stdout and CSV byte-identical, and equal to the page (Code
  against docs). Its first, `bead-cycle-time.py` over one frozen copy of
  the 220 retained run-log files, twice: CSV and stdout byte-identical,
  109 s and 178 s at load 45 on 24 cores.
- ci-mozmau's subset, `go test ./cmd/gc/ -run
  'HookClaimOrdering|PoolAlias|WorkQuery|PoolDemand'` at `712dea426`,
  twice: 90 tests pass each time,
  `TestHookClaimOrderingTakesTheOlderBeadAcrossBothPoolTiers` and both its
  cases among them. `./internal/config/` and `./test/docsync/` pass.
- ci-m5pd91 added no tool. Its counts were re-derived from the store
  instead (Code against docs).

## Wrong input

- `bead-cycle-time.py`: a reversed `--cohort`, and an `--until` before T1,
  are refused by name. `--until tomorrow` raises ValueError naming it.
  `--cohort` with `--window-start` exits 0 and ignores the window, which a
  cohort replaces. A `--csv` in a missing directory raises at the write,
  after 107 s of reading.
- `merge-queue-wait.py`: a CSV as `--passes` raises JSONDecodeError. Its
  own CSV as `--baseline-csv` printed the report's title line, then raised
  KeyError 'mode', and an `--as-of` before every pass raised
  `min() iterable argument is empty` (finding 6).
- `test-growth-gate.py`: an unknown bead, non-JSON args, a relative or an
  unreadable `--reason-file`, a `new-tests:` line under 20 characters, and
  `update --status closed` in both spellings are each refused with a
  remedy.
- `push-gate-select`: a three-field record, an unknown remote sha and an
  unknown flag each widen to full with the reason on stderr. A
  deletion-only push decides none.
- `push-gate-replay.py`: `replay` over a backwards window is refused, "no
  sweep merges in that window". `summarize` raised ZeroDivisionError on an
  empty log and IndexError with a `--since` past every row (finding 3).
- sel4 `affected_tests.py --base no-such-ref`: exit 2 with the
  `--affected=<ref>` remedy. zephyr `plan --base no-such-ref`: exit 0, a
  full run with its reason, the side it is built to fail toward.
- sel4 `run_host_tests.sh`: `--record-reads --sitting`, `--affected
  --affected=main` and an empty `--affected=` each exit 64 with the usage
  line. `--affected=no-such-ref` exits 2, NOT RUN, with the remedy.
- sel4 `host_suite_reads.py fold` over sel4 logs, given the zephyr
  checkout as its root: exit 0, and it wrote a two-row table of empty reads
  into that checkout's `scripts/`. This pass did that to the zephyr rig
  root by mistake and removed the file at once. The table carries no
  stamp, so a selector reading it runs full. Left.
- zephyr `run_tests.sh`: `--affected` beside `--hardware`,
  `--memc-shipped`, `--board`, `--no-retry` or `--host` in either order,
  `--base` without `--affected`, and `--base` with no value each exit 2
  with the reason.
- zephyr `affected_tests.py audit no-such-gate` printed "0 target(s), 0
  uncovered read(s)" and exited 0, which is what a clean audit of a real
  gate prints (finding 9).
- ci-mozmau's generated query (`pool_Work_bd105.golden`) under `sh`,
  against a stub bd that serves the two pool tiers from the environment
  and refuses every other call. Both tiers served: the older alias row
  first, where main's query returns the newer routed row alone. A tier
  printing text, an error object or `null`: `[]`, losing the other tier's
  valid row with it. main's query passed such output through for the
  claim to fail on. bd printed nothing on stdout for every failure tried
  (a missing store, a malformed `--metadata-field`, an unknown `--sort`),
  so this shape needs a bd that writes non-JSON to stdout. Left, and the
  comment now states it (finding 12).

## Missing input

- `bead-cycle-time.py --run-logs` at a missing directory, and at an empty
  one: exit 0 with a full report whose headline table looked right. Over
  the same cohort, 60 of 71 CSV rows differed from the real run, and
  `runlog_covered` fell from 59 to 0 (finding 6).
- `merge-queue-wait.py --run-logs` at a missing or empty directory is
  refused, "no pass records under". A missing `--passes` or
  `--baseline-csv` raises FileNotFoundError naming the path.
- `test-growth-gate.py` with `GC_CITY` unset, `GC_BD_ARGS_JSON` unset
  (read as invalid JSON), or a city with no `.gc/site.toml`: refused. A
  bead with no `gc.work_commit` is admitted silently, as documented.
- `push-gate-select` with empty stdin, or `--worktree` outside a
  repository: full, reason printed.
- `push-gate-replay.py summarize` with no `make test` log, and none
  survives in `/var/tmp` at this pass: argparse exit 2, so `TESTING.md`'s
  re-run reproduced nothing (finding 3).
- sel4 `affected_tests.py` outside a checkout: refused. zephyr `plan` with
  `--root` at a missing directory: a full run, though its reason names the
  base, not the root.
- sel4 `host_suite_reads.py fold` over a missing log directory, and over
  an empty one: exit 0, and the committed table, 26306 lines, became `{}`
  (finding 8). `record` reaches the same fold.
- sel4 `affected_tests.py` with the table deleted, or holding only a
  foreign `(recorded at)` stamp: full, with the refresh remedy.
- zephyr `affected_tests.py audit --root` at a missing directory: a
  CalledProcessError traceback, exit 1. Nonzero, so left, unnamed as it is.
- ci-mozmau's query with one tier printing nothing and exiting 1, as bd
  does: the other tier's row is served alone, as main's ladder served it.
  Nothing pinned that. ` && ` for `; ` between the tiers left all 90
  tests green and failed only the string goldens (finding 12).
- The same query with both tiers printing nothing: `[]`, and it falls to
  the legacy tiers. With `jq` absent from PATH: `[]` with a row in each
  tier, where main's query served the routed row. jq is doctor-checked,
  and the other pool tiers already pipe through it. Left.
- ci-71d5yl's first re-run command over this pass's log copy, the
  report's own copy having aged out: the one-column drift under Code
  against docs.

## Findings

1. gascity `AGENTS.md`: the done-criterion said the push gate runs the
   full fast suite for any Go change. Since gs-0y4d it runs the scoped
   selection, and the full run is the sweep's. Fixed in gascity
   `0622d0789`, and `22dd6257f` makes the docsync suite read the sentence
   against `.githooks/pre-push`, seen red both ways before it passed.
2. gascity `scripts/push-gate-select`: its embed-only branch pointed at a
   "DELIBERATE ABSENCE note above" that gs-rvw5 (`6cc97eff8`) moved into
   `TESTING.md`. Fixed in `0622d0789`.
3. gascity `scripts/push-gate-replay.py summarize` needed a `make test`
   log, none survives, and an empty log or an empty row set raised. Its
   docstring named `TESTING.md`'s passage by the title gs-0y4d replaced.
   Fixed in `4a8197f4b`, verified by the replay above.
4. City `docs/merge-sweep-batching.md` said gascity's sweep gate runs no Go
   tests. Fixed in city `bfc63b5bc`.
5. City `docs/test-growth-gate.md` said its unit patterns are the
   baseline's. They are for Go and narrower for JS, Python and ZTEST.
   Fixed in city `06b5906b1`.
6. City `bead-cycle-time.py` exited 0 on a run-log directory holding no
   pass, and `merge-queue-wait.py` crashed unnamed on a foreign
   `--baseline-csv` and an early `--as-of`. Fixed in city `65d753a0e`,
   test first, with three mutation rows, 29/29 died.
7. `citybd.epic_members` read only the rig and city stores, so the four
   astoria members could be named neither in this Covers block nor in the
   review it opens. The closure machinery's, not this epic's: filed as
   city ci-5li8wk (toolsmith), fixed in city `54b6be7d1`, on city main
   since `b1e93e4e4`. The refresh lists the four and reads them.
8. sel4 `host_suite_reads.py fold` over a log directory holding no log
   wrote `{}` over the recorded table and exited 0, the class of finding
   6. Fixed in sel4 `6eebd80c`: refused by name, exit 2, nothing written.
   Test first, both shapes red at `a3783b03`. Its sweep spec
   `scripts/gs_hyos_fold_no_log.sweep.toml`, 3/3 died.
9. zephyr `affected_tests.py audit <target>` with a target matching no
   gate line traced nothing and exited 0 with a clean audit's summary.
   Fixed in zephyr `4254e039`: refused by name, exit 2. Test first, red at
   `d0d98eb8`. Three rows added to `affected_tests.mutation-sweep.toml`,
   3/3 died.
10. zephyr `run_tests.sh --affected` runs its selected gates one at a
    time, an `eval` loop written against the serial runner, while az-r41s
    made plain `run_repo_gates.sh` concurrent. az-8ujz landed after
    az-r41s and kept the loop. The timing pair under Run twice puts the
    serial subset at about 2.9 times the concurrent one. A runner change
    with its own sweeps and timing, so filed as zephyr az-20v1f
    (astoria-zephyr/lab.engineer), not as a member of this epic.
11. City `docs/fleet-review-gate.md` (ci-m5pd91) cited the re-measure "on
    city branch `fix/ci-ozsp1r-cohort-remeasure`". That branch reached
    city main in `5461080e2` nine minutes before ci-m5pd91's own merge.
    Fixed in city `6389dab50`.
12. gascity `internal/config/workquery.go` (ci-mozmau): the merge's
    comment said a tier printing no JSON fails the merge. A tier failing
    as bd does prints nothing, and the other tier's rows survive, which no
    test pinned. The control test's comment still credited the ladder.
    Fixed in gascity `8b780781b`: the fake bd scripts a tier failure, the
    control runs with the routed tier empty and failing, and
    `cmd/gc/pool_queue_tier_merge_sweep.toml` records the ` && `
    mutation, 1/1 died.
13. gascity `engdocs/architecture/dispatch.md` (ci-mozmau) gave the
    replay's 2.6 h as measured, beside the 2.8 h the slot actually held
    the slung bead, and said the work-query form prints the first match.
    Fixed in gascity `52f515a83`.

gs-q1ix's city fixes landed on city main in `fa46fcac4`. gs-hyos's sel4
and zephyr fixes are on each rig's main since, `6eebd80c` and
`4254e039`. gs-g0m7's city fix is on city branch
`docs/gs-g0m7-bead-cycle-time-self-review`, and its gascity commits are on
the gascity branch of that name, cut from ci-mozmau's tip, so it carries
`712dea426` if the sweep has not landed it first. Noted and left:
PUSH_SELECT reads the hook's catch-all line as unknown, zephyr's `plan`
blames the base for a missing `--root`, sel4's `fold` writes no recording
stamp, and fold accepts logs from another checkout. Each errs toward
counting or testing more.
