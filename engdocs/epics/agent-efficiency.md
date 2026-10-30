# Making agent sessions cheaper in time and usage headroom

epic: agent-efficiency
closed: 2026-10-29

## what this was for

The city runs on Claude subscription usage, so what it runs out of is
wall-clock time and room in each account's five-hour and seven-day usage
windows, not money. Every agent session started cold, nearly every agent ran
at the highest effort level, and something that changed per session sat in
front of the large unchanging part of each prompt, which was believed to stop
the prompt cache from ever being reused. This epic was to measure where usage
goes, then cut it in the ways that measurement supports.

## what was done

- A `gc usage` command that totals input, cache-read, cache-creation and
  output tokens and elapsed time per session, per agent type and per bead,
  plus a recorded baseline week taken before any other change landed.
- The per-session marker line and clock line were moved behind the stable
  prompt, and `gc usage pair` was added to compare the first request of two
  back-to-back sessions of the same agent slot on the same account.
- Claude Code's own git-status block was switched off in the city's agent
  settings, because it was the one thing still differing between sessions
  ahead of the role prompt.
- Every agent now names its model and effort, a fleet-wide default stops
  anything falling back to maximum effort, and `gc` refuses to load a config
  with an unrecognized model or effort value instead of quietly using the
  provider's default.
- A city script that assigns each agent type a preferred account from
  measured usage headroom, re-run every five minutes. The preference gives
  way when an account nears its limit and only affects sessions started
  afterwards.
- The per-account Claude memory that had built up in the account homes was
  harvested into the bead store and deleted, and a city check now fails if
  the homes drift apart again.
- The instruction files every agent loads were audited for padding and
  contradictions, each finding fixed or kept with a reason, and their size
  measured before and after.

## what was found

- The idea the epic started from was only partly right. Almost every session
  in the baseline week already read some of its prompt from cache on its
  first request (the baseline document in engdocs holds the figure). What the
  epic actually needed to show was that the large rendered prompt is reused.
- Moving the marker and clock lines was not enough. The implementers' paired
  measurement still found the rendered prompt rebuilt every session. A later
  look at the raw requests showed the only real divergence ahead of it was
  Claude Code's git-status block, which differs because each session leaves
  its own branch checked out. The startup hook's output sits after the
  prompt and does not matter.
- With that block switched off, a second paired measurement showed the
  rendered prompt served from cache on a same-slot, same-account respawn.
  The figures are in the follow-up's close reason. The self-review record
  adds the same slot on two more accounts, then every same-slot,
  same-account respawn in the sixteen hours after the change, engineer and
  toolsmith slots included. Reuse held only when the respawn's first
  request came within about an hour of its predecessor's first request, so
  a respawn after a session that ran longer than an hour re-creates the
  whole prompt.
  Nothing mechanically pins the setting either: a later settings edit that
  turns git status back on silently undoes the reuse.
- Switching git status off also drops Claude Code's built-in git guidance and
  its commit-attribution reminder. The operator's global rules already forbid
  that attribution. No instruction file the city loads restates the git
  guidance, among it the warning against interactive `-i` git commands.
- The canary comparison of effort levels per agent could not reach a
  verdict: three of the five agents being measured changed model or role
  during the two-week window. It was dropped once the whole fleet moved to
  the same model, which removed the question it was asking.
- The account-preference script lands only some preferences in a week when
  half the accounts are near their limits, by design. Its payoff depends on
  the cache reuse above, so it reaches only a respawn that starts within
  about an hour of its predecessor.

## where it ended

All work under the epic is finished, the cache-reuse follow-up included.
The epic's first review ruled the prefix-hygiene requirement unmet, before
that follow-up landed, and its findings were deferred as standalone work.
Every one of them has since closed with a shipped fix. Whether the epic is
approved and closed is the operator's call, recorded in the review
generation, not here.

Left undone on purpose: the canary comparison, dropped by Willie when the
fleet converged on one model. The audit's proposals for Willie's own global
instruction file are his to make, not work items. No guard checks that git
status stays off in agent settings. Nothing keeps a slot's cached prompt
alive past its hour, which was found after the epic's work closed and has
no work item yet: the epic review rules on it.

## references

- gs-cm0w -- `gc usage` and the baseline
  (engdocs/contributors/agent-efficiency-baseline.md)
- gs-ian6 -- marker and clock moved, `gc usage pair` added
- gs-yvxt -- explicit model and effort, refusal at config load
- gs-z2on -- account preference script (city repo)
- gs-arar -- account-home memory removed and gated (city repo)
- gs-saj5 -- instruction-stack audit with before and after size
- gs-jbyc, gs-n3lh, gs-78op -- canary window, its void reading, and the drop
- gs-5buj, gs-tra9, gs-t6uu -- implementers' self-review passes,
  docs/self-review/agent-efficiency.md
- gs-qrgd -- the cache-reuse follow-up and its paired read (city d224675ac)
- gs-rv15a377628c08-f01-deferred to f17-deferred -- review findings, deferred
  and then fixed
- city ci-rv15a377628c08 -- the epic review generation, verdict recorded there
- pm-log #139, #147, #170, #188
