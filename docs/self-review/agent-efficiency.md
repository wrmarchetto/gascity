# Self-review: epic:agent-efficiency

Covers:
- gs-78op 12f14e6ee7e01f50422496b3d4d55fb47db6233b
- gs-arar none
- gs-cm0w none
- gs-ian6 76bd2631a5297eb7a9cb62410cbc5fae445c3659
- gs-jbyc c67299a5cc4818aacaf9f01f61363d1f3fc65004
- gs-n3lh f8ed69273978227de9b00f7c369ee4665da57a05
- gs-qrgd f72c6c0b081b3c907dd72d294c66c1389ea6623c
- gs-saj5 1581749b7f493821dccd9cc9a050aef18352d18f
- gs-yvxt none
- gs-z2on de07763bffd47f975393ba9114db4c2f352013ed

Pass taken by gs-5buj over gascity `1596231a7` and city `41692d9d`, with `gc`
built from that gascity tree, then by gs-tra9 over gascity `4eba658e1` and
city `a07e8619d` for gs-qrgd, the member review g1 filed. Members span both
repos: gs-arar, gs-z2on, gs-qrgd and half of gs-yvxt landed in the city repo,
the rest here. Between the two passes no other member's file changed in
gascity except by gs-5buj's own fixes. In the city only gs-qrgd's commits,
finding 8's fix and two edits to the shared `merge-closed-features.test.py`
touched one, so gs-5buj's bullets stand except where they say otherwise.
Bullets opening "gs-tra9" are the second pass's, run with the installed `gc`,
which refuses a missing usage log.

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
  prompt, was contradicted by the paired read until gs-qrgd (finding 5).
- City `docs/city-gate-budget.md`, the 41st-check section: a dated
  measurement of that day's sweep, not a claim about now. Not re-measured.
- `docs/guides/configuring-an-agent.md` was not changed by the epic but is
  contradicted by it: its codex example stopped loading when gs-yvxt's
  refusal landed (finding 2).
- gs-tra9: city `.claude/settings.json` sets `includeGitInstructions` to
  false, and gc's staged `.gc/settings.json` carries it. The transcripts
  agree: both of gs-5buj's pre-change sessions hold a `gitStatus` block, and
  none of the six post-change sessions read below holds one.
- gs-tra9: gs-qrgd's close lists what the key also removes. On Claude Code
  2.1.312 with only the key changed, the Bash tool's description loses
  exactly the "# Git" block -- no `-i` flags, `gh`, bare `@name`, commit or
  push only when asked, and the attribution lines -- and nothing else.
- gs-tra9: gs-qrgd's paragraph in city `docs/account-rotation.md` said the
  rendered prompt is byte-stable only while the key is false, and that every
  respawn re-creates it (finding 8).

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
  as the load-bearing figure. gs-5buj took the read (finding 5).
- gs-tra9, prefix hygiene after gs-qrgd, ruled unmet at review g1 (pm-log
  #170): met in intent on three reads. gs-qrgd's pair `ci-yua939` then
  `ci-qpq53g`, uncached / cache-read / cache-creation, re-read as 2 / 27,148 /
  43,619 then 2 / 55,724 / 15,514. Two later consecutive same-account pairs
  of that slot, on accounts 2 and 3, agree: `ci-t5lv2j` then `ci-ob2t24`
  28,212 / 42,558 then 55,725 / 15,047, and `ci-jr61a0` then `ci-azpvcg`
  28,212 / 42,566 then 55,726 / 15,045. The second spawn re-creates 21-22%,
  against 58% before, and within 471 tokens of the first spawn's tail past
  the shared prefix. Bound: all three pairs are `astoria-sel4/lab.pm`, and no
  other slot had two consecutive post-change spawns on one account by 22:10Z.
  Order and consecutiveness come from the usage log's first-seen times,
  because `gc usage pair` checks neither (review g1 F17).
- Explicit model and effort: met. gc refuses an undeclared, wrong-case or
  empty value and an unknown key at load, naming agent, key and choices.
  `effort = "max"` passes gc by design and is refused by the city's
  `agent-effort-pins` doctor check, green on the live city. At the receiving
  end, 8 live claude processes: 4 `--effort high`, 4 `--effort xhigh`, all
  `claude-opus-5-5`, none at max.
- Account affinity: met, premise weakened by finding 5. Runs, deterministic,
  yields and binds future spawns as specified, and city `3a136c07` landed
  after gascity `73092ae1e`, so it followed prefix hygiene.
- gs-tra9, account affinity after gs-qrgd: the premise holds. Its payoff
  reaches only a respawn landing on its predecessor's account, and of the
  eight consecutive `astoria-sel4/lab.pm` respawns from 21:33Z to 22:10Z,
  three did. One slot over 37 minutes, so a bound, not a rate.
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
- `go run ./cmd/genschema` after gs-5buj's help-text change: the second
  regeneration was byte-identical to the first.
- gs-tra9: `gc usage pair ci-yua939 ci-qpq53g`, the table twice and `--json`
  twice: byte-identical within each form, the figures in gs-qrgd's close
  reason. gs-qrgd added no script or tool, so its key was run instead, below.

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
- gs-tra9: the key, on Claude Code 2.1.312 in a scratch repo with a fresh
  config dir and `ANTHROPIC_BASE_URL` at a closed local port, each transcript
  read for `gitStatus` and the scratch commit's subject. Boolean false: absent.
  Misspelled `includeGitInstruction`: present, with no warning. The string
  `"false"`: present, and Claude Code says the whole settings file did not
  load, hooks and permission rules included (finding 9).
- gs-tra9: `gc usage` over the live log, which now holds two torn lines,
  304149 and 304322, both from mayor session `ci-wisp-3epyfuc`. Each is
  skipped and named on stderr, exit 0. The first is a prefix of the record on
  the next line, same idempotency key, so nothing is lost there. The second
  is cut before any identifying field. `--json` prints `"ok": true` with no
  field for a skip, and `account-affinity.py` shows a successful call's
  stderr nowhere. The sink is upstream `internal/usage` and nothing shown was
  undercounted, so not a finding.

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
- gs-tra9: `gc usage` at a scratch city with no `.gc/usage.jsonl`: refused,
  exit 1, naming the path and `--city`, so finding 1's fix is in this binary.
- gs-tra9: the key absent, same rig as above: the `gitStatus` block returns.
  That is the control showing the rig can see the block, and nothing in the
  city reports the key's absence (finding 9).

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
5. Fixed by gs-qrgd, city `d224675ac` and `f72c6c0b0`. Criterion 2's read as
   gs-5buj took it: `astoria-sel4/lab.engineer-1` on account3, consecutive
   spawns `ci-1pdl1g` and `ci-5avg51`, 52 minutes apart with no spawn of that
   slot on any account between. First invocation, uncached / cache-read /
   cache-creation: 2 / 28,381 / 38,809, then 2 / 28,381 / 38,765. The second
   spawn re-created 58% of its prompt. The raw request blobs are
   byte-identical through tools, system and the CLAUDE.md stack, and diverge
   at Claude Code's git-status block, whose branch differs because each
   session leaves its own branch in the shared slot worktree. gs-5buj also
   put the SessionStart output ahead of the rendered prompt. It is not:
   gs-qrgd's request records place it after the prompt, and the three
   post-change reads above take the whole prompt from cache with that output
   still per-session. Neither divergence is gc's beacon or clock line.
6. Not fixed. `canary-eval.py` reports an omitted usage report or bead dump
   as an undersized sample. With the committed criteria every omission tried
   lands on exit 3, never keep or revert, and criterion 4 is dropped. A
   re-run under gs-78op option (a) should require both usage reports.
7. This record could not have landed: gascity's `TestEveryDocsPageIsPublished`
   fails on any `docs/` page outside the Mintlify nav, so the pre-commit
   docsync gate refuses `docs/self-review/`. Exempted by directory beside the
   PM-state files, `105315400`, red with the exemption emptied.
8. gs-qrgd's account-rotation paragraph said the rendered prompt is
   byte-stable only while the key is false. Its bytes are identical either
   way, so a reader checking them with the key on would conclude the key is
   unneeded. It also said every respawn re-creates the prompt, where only one
   whose snapshot moved does. Rewritten with the wrong-input results above,
   city `a07e8619d`.
9. Not fixed. Nothing pins `includeGitInstructions`. A deleted or misspelled
   key silently restores the 58% re-creation, and a string value unloads the
   whole `.gc/settings.json`, which carries the city's PreToolUse,
   SessionStart and Stop hooks. JSON takes no comment, and no test or doctor
   check reads the key. A guard could read the staged file for the boolean,
   or, as gs-qrgd proposed, read first-request blobs for a `gitStatus` block,
   the second being the witness of what a session consumed. A new city check
   is a gate-budget call (city `docs/city-gate-budget.md`), so it is for the
   epic review to rule on.
