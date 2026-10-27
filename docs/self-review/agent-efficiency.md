# Self-review: epic:agent-efficiency

Covers:
- gs-78op 12f14e6ee7e01f50422496b3d4d55fb47db6233b
- gs-arar none
- gs-cm0w none
- gs-ian6 76bd2631a5297eb7a9cb62410cbc5fae445c3659
- gs-jbyc c67299a5cc4818aacaf9f01f61363d1f3fc65004
- gs-n3lh f8ed69273978227de9b00f7c369ee4665da57a05
- gs-saj5 1581749b7f493821dccd9cc9a050aef18352d18f
- gs-yvxt none
- gs-z2on de07763bffd47f975393ba9114db4c2f352013ed

Pass taken by gs-5buj over gascity `1596231a7` and city `41692d9d`, with `gc`
built from that gascity tree. Members span both repos: gs-arar, gs-z2on and
half of gs-yvxt landed in the city repo, the rest here.

## Code against docs

- `docs/reference/cli.md`, `gc usage` and `gc usage pair`: flags, defaults
  and refusals match `cmd/gc/cmd_usage.go` and `cmd_usage_pair.go`. Neither
  the page nor the code said anything about a missing usage log (finding 1).
- `engdocs/contributors/agent-efficiency-baseline.md`: its reproduce command,
  re-run, gives the recorded window exactly -- 52,875 facts, 0 undated, 1,121
  sessions, 8,568,433 in, 48,849,102 out, 9,659,609,857 cache-read,
  105,419,466 cache-creation.
- `engdocs/contributors/agent-efficiency-canary.md` and `.toml`:
  `scripts/canary-eval.py` reads the TOML as the criterion, recomputes each
  line from the baseline, and declines an undersized window with exit 3, as
  the document says. Its head still called criterion 4 open (finding 3).
- `engdocs/contributors/agent-instruction-stack-audit.md`: its stated
  measurement (tiktoken `o200k_base`) reproduces at `1f32fead8` -- rig
  `AGENTS.md` 8,073 then 7,622, rig `CLAUDE.md` 26. Its source set is not
  what a session loads (finding 4).
- `AGENTS.md` fork posture: `TestAgentInstructionsStateTheCurrentForkPosture`
  green. `TESTING.md` and `test/test-resources.toml` census rows from gs-jbyc:
  `TestRepositoryLedgerMatchesCensusAndDocumentation` green.
- City `docs/account-rotation.md`, affinity section: the yield and
  future-spawns-only properties are what `claude-pool.py` does, pinned by
  `claude-pool.test.py` (67 pass) and `account-affinity.test.py` (23 pass).
  Its premise, that the next same-account spawn reuses a byte-stable rendered
  prompt, is contradicted by the paired read (finding 5).
- City `docs/city-gate-budget.md`, the 41st-check section: a dated
  measurement of that day's sweep, not a claim about now. Not re-measured.
- `docs/guides/configuring-an-agent.md` was not changed by the epic but is
  contradicted by it: its codex example stopped loading when gs-yvxt's
  refusal landed (finding 2).

## Docs against spec

Against the roadmap's acceptance list at `1596231a7`, where criterion 4 is
dropped (pm-log #147).

- Observability: met. `gc usage` groups by session, type and bead and prints
  a dash for not-observed. The baseline merge `f06c9928b` precedes every other
  member's merge, the next being `5f52a0121` ninety minutes later.
- Prefix hygiene: met as written, not in intent. `38096d0dd` moved the beacon
  and clock line behind the stable payload. The measured read was never
  recorded -- gs-ian6 closed on test evidence, though pm-log #139 puts the
  acceptance in the record citing the run and names the creation/read split
  as the load-bearing figure. This pass took the read (finding 5).
- Explicit model and effort: met. gc refuses an undeclared, wrong-case or
  empty value and an unknown key at load, naming agent, key and choices.
  `effort = "max"` passes gc by design and is refused by the city's
  `agent-effort-pins` doctor check, green on the live city. At the receiving
  end, 8 live claude processes: 4 `--effort high`, 4 `--effort xhigh`, all
  `claude-opus-5-5`, none at max.
- Account affinity: met, premise weakened by finding 5. Runs, deterministic,
  yields and binds future spawns as specified, and city `3a136c07` landed
  after gascity `73092ae1e`, so it followed prefix hygiene.
- Memory drift: met. The gate is green on the live homes and refuses missing,
  empty and garbled inputs.
- Instruction audit: met. Every finding carries a disposition and the
  before/after change reproduces, absolute figures corrected (finding 4).

## Run twice

- `gc usage --by type` over one closed week: two runs byte-identical.
- `gc usage pair ci-1pdl1g ci-5avg51`, table then `--json`: same figures.
- `account-homes-identical-check.py`, two live runs: same verdict, exit 0.
- `agent-effort-pins-check.py` twice, then its doctor wrapper: same, exit 0.
- `account-affinity.py`, dry run twice on the live city: byte-identical plan.
  Not run with `--apply`, which writes the `affinity.json` the live launcher
  reads, and the order already runs it every five minutes.
- `canary-fetch.sh` twice over one window: all four outputs byte-identical.
  `canary-eval.py` twice over a two-store capture: byte-identical, exit 3.
- `go run ./cmd/genschema` after this pass's help-text change: the second
  regeneration was byte-identical to the first.

## Wrong input

- `gc usage`: an unknown `--by`, an unparseable bound, `--since` after
  `--until` and a positional argument are refused naming the flag. `--top -1`
  was accepted as show-all (finding 1). A negative duration is read as its
  absolute value, which `parseUsageBound` does on purpose.
- `gc usage pair`: mismatched slots, one session twice, an unknown id and a
  single argument are each refused.
- Config load: `effort` of `maxx`, `High` or empty, a misspelled `efort` key,
  and `model = "opus-9"` each fail every gc command with the declared choices.
- `account-homes-identical-check.py --homes-dir` at a file or an unrelated
  directory: exit 2, "cannot be checked". `"false"` as a string: exit 1.
- `agent-effort-pins-check.py --city` at the gascity repo: exit 2.
- `canary-eval.py` given a bead dump as usage, usage as beads, or a JSON file
  as criteria: a Python traceback, exit 1 -- a failure, not a named refusal.
  Baseline and window swapped: exit 3.

## Missing input

- `gc usage` with no `.gc/usage.jsonl`: an observed-empty window, exit 0,
  `"ok": true` (finding 1). Aimed at a city checkout, `account-affinity.py`
  then planned an empty map and exited 0.
- `account-homes-identical-check.py`: a missing or empty homes dir exits 2,
  a home with no `settings.json` exits 1 naming the file.
- `agent-effort-pins-check.py`: no `city.toml` exits 2, and a `city.toml`
  with no workspace provider exits 2 naming the missing floor.
- `canary-eval.py`: without `--window-usage`, or with no bead dumps, exit 3
  with sample-size reasons and no word about the missing input (finding 6).
  A nonexistent file: traceback, exit 1.

## Findings

1. `gc usage` read a missing log as zero usage, and took `--top -1` as
   show-all. Both now refused, `3428aa710`. Watched red first, each guard
   mutated to accept and each killed its own subtest. `account-affinity.py`
   and `canary-fetch.sh` already turn a nonzero `gc usage` into a failed read.
2. The guide's Axis 2 example paired `provider = "codex"` with Claude's
   `sonnet` and `plan`, which gc has refused at load since gs-yvxt. One of
   eighteen documented values tried. Fixed, `f0e798aa9`.
3. The canary record's head said criterion 4 was still open. Fixed,
   `f6b7b70f4`.
4. The audit's Claude stacks omit the city `CLAUDE.md` a city worktree
   session loads (3,651 tokens then), its Codex row counts files Codex does not
   read by name (unverified, no Codex session to check), and 3.2% was the
   Codex share alone. The 451-token change stands. Fixed, `8e966ff52`.
5. Not fixed. Criterion 2's read, taken here: `astoria-sel4/lab.engineer-1`
   on account3, consecutive spawns `ci-1pdl1g` and `ci-5avg51`, 52 minutes
   apart with no spawn of that slot on any account between. First
   invocation, uncached / cache-read / cache-creation: 2 / 28,381 / 38,809,
   then 2 / 28,381 / 38,765. The second spawn re-created 58% of its prompt.
   The raw request blobs in both transcripts are byte-identical through tools
   (78,055 chars), system (6,088) and the first 43,337 chars of the first
   user message -- the CLAUDE.md stack -- and diverge at Claude Code's own
   git-status block, whose current branch differs because each session leaves
   its branch checked out in the shared slot worktree. The SessionStart
   message differs too: Claude Code persists the 70.9KB hook output to a
   per-session path. The rendered prompt sits after both, so it is not
   reused. Neither divergence is gc's beacon or clock line. Not fixed here
   because the fix changes what Claude Code sends every city session and
   needs its own paired read to prove. For the epic review to rule on.
6. Not fixed. `canary-eval.py` reports an omitted usage report or bead dump
   as an undersized sample. With the committed criteria every omission tried
   lands on exit 3, never keep or revert, and criterion 4 is dropped. A
   re-run under gs-78op option (a) should require both usage reports.
7. This record could not have landed: gascity's `TestEveryDocsPageIsPublished`
   fails on any `docs/` page outside the Mintlify nav, so the pre-commit
   docsync gate refuses `docs/self-review/`. Exempted by directory beside the
   PM-state files, `105315400`, red with the exemption emptied.
