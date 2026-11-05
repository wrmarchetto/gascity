# Self-review: epic:bead-cycle-time

Covers:
- ci-3imbeu 1fe0ea1eadf6e1facb89848ecbb0041c0f888b65
- ci-5c95j4 4f0d887e5de9193420692dbd356b48b4db5afe27
- ci-7npmly none
- ci-bq7qvz 19bb445c50af6f5ab55ef445313e50d98686e41c
- ci-ce84ou 682d890b5e63205e6d3ea3191e187015f35649bd
- ci-cicsvc 8354d8b5e3f8e2ff729ee249e3aaae422681034d
- ci-cj9utx 19399c5a0a592907d048a969401ef9486bdb6dd0
- ci-cu1tu3 none
- ci-f9cj1b 61b49df5a2f8788ee92a822debcd3434f4f40595
- ci-fb3uff 3fe364d77072c6b72bea8bdaf55868a74133fdc0
- ci-m6ifdq 1c4026399dc9a119a46cd2ac79e95b2c2b9cfc32
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
and ci-tr56b0's `6d2e61066` are not on city main at this pass: their sweep
merges `c9e50d8ab` and `7487c4c89` were made at 21:44Z on 11-05 and sit on
no branch, so both were read at their branch tips.

Four more members carry the label in the astoria stores, which the Covers
block cannot list (finding 7): az-r41s, az-8ujz, as-hq04p and as-8384y. All
four are on their rigs' main. They were read for landing and their tools
run, in the bullets that name them.

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
- Astoria: zephyr `docs/gate-suite-cost.md` (az-r41s) cites the city
  baseline at its committed shas and the zephyr cap by its constant.
  as-hq04p keeps its ranking and before/after sizes in its close reason
  and its bead comment, with no page.

## Docs against spec

Against the roadmap's acceptance list at gascity `e6cc8beaa`, read with
pm-log #150 to #193.

- Baseline: met. `659b7fd0f` reached city main in `c6cccfbff` at 10-23
  15:33Z. The first reduction to land, gs-zle6's `1b93a1575`, followed at
  20:15Z.
- Target: unmet at the last measurement. ci-cj9utx's cohort misses in
  city, astoria-sel4 and astoria-zephyr, and gascity and dart have no row.
  Its stage beads have shipped: ci-5c95j4 landed at 19:56Z and ci-ce84ou
  at 20:06Z on 11-05, and zephyr's ci-fb3uff and ci-tr56b0 are not on city
  main at this pass. No measurement follows any of them. This pass's own
  cohort run is a determinism check, not a re-measure: its last commits
  end at 16:50:19Z, so it tests none of the four.
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
  gs-0y4d.

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
- The city suites of every script the epic touched, at `a0798b51c`:
  bead-cycle-time and merge-queue-wait OK, test-growth-gate 29/29,
  notify-on-close OK, epic-review-drive 57/57, merge-closed-features 1819
  checks in 464 tests.

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

## Findings

1. gascity `AGENTS.md`: the done-criterion said the push gate runs the
   full fast suite for any Go change. Since gs-0y4d it runs the scoped
   selection, and the full run is the sweep's. Fixed in gascity
   `0622d0789`.
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
7. `citybd.epic_members` reads only the rig and city stores, so the four
   astoria members can be named neither in this Covers block nor in the
   review it opens. The closure machinery's, not this epic's: filed as
   city ci-5li8wk (toolsmith). This pass read them by hand.

The city fixes are on city branch `fix/gs-q1ix-bead-cycle-time-self-review`,
for the sweep to land after gs-q1ix closes. Noted and left: PUSH_SELECT
reads the hook's catch-all line as unknown, and zephyr's `plan` blames the
base for a missing `--root`. Both err toward counting or testing more.
