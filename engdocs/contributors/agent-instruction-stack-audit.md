# Agent instruction-stack audit

This is the criterion-7 evidence for `epic:agent-efficiency` (`gs-saj5`). It
audits the instruction sources loaded for the lab roles against the checklist
in PM log #133: verification rituals, thoroughness boosters, mandatory
scaffolds, stale examples, and contradictory rules.

## Scope and measurement

The audit read the three lab role templates, their `agent.toml` files and the
lab pack. `append_fragments` is absent from the effective lab configuration,
so no appended fragment contributes text. It also read the rig `AGENTS.md`,
the local `CLAUDE.md`, and Willie's global `~/.claude/CLAUDE.md`.

Token counts use `tiktoken`'s `o200k_base` encoding over the UTF-8 contents of
each source, once, in load order. The Codex template was rendered with this
session's four values before counting; the other templates have no material
effect on the Codex stack. The local `CLAUDE.md`'s `@AGENTS.md` import is not
counted twice because the file is already a separately loaded source. This is
a reproducible source-size measure, not a claim about provider framing tokens.

| Component | Before | After |
| --- | ---: | ---: |
| Global `~/.claude/CLAUDE.md` | 5,139 | 5,139 |
| Rig `AGENTS.md` | 8,073 | 7,622 |
| Rig `CLAUDE.md` | 26 | 26 |
| Rendered `lab.engineer-codex` template | 879 | 879 |
| **Codex engineer stack** | **14,117** | **13,666** |

For coverage, the same source set changed from 16,024 to 15,573 tokens for
`lab.engineer` and from 19,429 to 18,978 for `lab.pm`. The repository-owned
change removed 451 tokens (3.2%) from every lab role's stack.

## Findings and dispositions

| Checklist class | Finding | Disposition |
| --- | --- | --- |
| Contradictory rules | `AGENTS.md` called this an upstream-alignment, T3 Code, and DoltLite integration, but PM log #1 says the fork is intentionally diverged and those integrations are context only. | **FIXED.** The 65-line mission, upstream-alignment, and archaeology block is now a 12-line current fork posture. It retains the only operational direction: use history for a suspected regression and preserve provider boundaries. |
| Mandatory scaffolding | The Codex template's exact hook claim, no-discovery rule, branch prefix, and final drain acknowledgment look repetitive. | **KEEP.** Each constrains a separate machine boundary. The template cites the close-digest refusal (`ci-ac97yz`) and closed-branch sweep miss (`ci-1ft6wm`); the hook and drain form the pool protocol. The nudge repeats the startup command because it is an independent mid-session wake path. |
| Verification rituals | TDD, focused evidence, quality gates, and the documented close/push sequence recur between the template and rig instructions. | **KEEP.** The template assigns the worker's fail-first obligation; `AGENTS.md` supplies project-specific gates and the recoverability constraints behind them. The quality-gate wording is conditional on code changes, so this audit's documentation-only change does not pretend to require a code-suite run. |
| Thoroughness boosters | The six-primitives and layer descriptions in `AGENTS.md` are long relative to a one-bead task. | **KEEP.** They state the repository's import and side-effect invariants, not an instruction to perform extra work. The API and worker-boundary sections further narrow their applicability to named paths. No duplicate mandatory action was found. |
| Stale examples | The PM template mentions `git pull --rebase` while explaining what a PM must never run. | **KEEP.** It is a negative example paired with the explicit prohibition and `pm-git-gate.py`; deleting the named unsafe form would make the guard's purpose less clear. |
| Mandatory scaffolding | The PM template repeats the same interview quotation in the `pm-init` and `pm-plan` paths. | **KEEP.** The paths are independently entered by distinct beads; each must remain self-contained because an agent acts from the matching section at wake time. The duplicate is small compared with the cost of a wrong, unasked PM decision. |
| Appended fragments | A fragment layer might silently add stale or contradictory directions. | **KEEP AS EMPTY.** Neither the lab pack nor the three lab agent definitions configures `append_fragments`; there is no hidden text to remove or account for. |
| Personal global file | The global file includes hardware-, C-, and Python-specific material that does not apply to this Go SDK session. | **PROPOSAL TO WILLIE ONLY.** Split universal worker safety/quality rules from hardware and language-specific guidance, then load the latter only for matching work. This is the stack's largest removable candidate (5,139 tokens), but it is outside every rig and was not edited or tracked as a bead. |

## Result

The only repository-owned contradiction found was corrected. The remaining
apparently ritualized text either protects a distinct execution boundary with
an incident-backed reason or is a proposal against Willie's personal file,
which this bead must not modify. Re-run the table's stated tokenizer and source
set after any template or instruction change so before/after figures stay
comparable.
