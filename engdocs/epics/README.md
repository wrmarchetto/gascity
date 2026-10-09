# Epic write-ups

One plain-language document per closed epic, written by the rig PM in the same
edit that marks the epic `done` in `docs/roadmap.md`. The audience is a reader
outside the session and outside this repository, months later, who wants to
know what an epic actually did without asking anybody. The format and the gate
that requires one belong to the city repository's epic-summary template.

## Why these are here and reachable at `docs/epics/`

`docs/epics` is a SYMLINK to this directory. Do not replace it with a real
directory and do not move these files into `docs/`.

Two path contracts have to hold at once and they disagree:

- The city's summary gate opens each write-up at `<rig>/docs/epics/<slug>.md`.
  That path is fixed in the city's `assets/scripts/citybd.py`, so it is not
  ours to change.
- `docs/` here publishes to docs.gascityhall.com, and
  `TestEveryDocsPageIsPublished` requires every markdown file under `docs/` to
  appear in `docs/docs.json` navigation. An epic write-up is a rig-internal
  record, not a product page, so publishing it is the wrong answer -- and this
  repository's own rule sends engineering documents to `engdocs/`.

A symlink satisfies both: the file lives where this repository says it belongs,
and resolves at the path the city reads. It works because `filepath.WalkDir`
does not follow symlinks, so the publish test never descends into it. That is a
real dependency on walk semantics, and it is load-bearing -- if that test ever
starts following symlinks it will refuse these files, loudly, which is the
failure mode to want.

REJECTED: adding `docs/epics/` to the publish test's exemption list. It works,
and it was written and tested before being dropped. The cost is that the
exemption lives in `test/docsync/docsync_test.go`, which is upstream-owned, so
it becomes a fork patch carried forever -- and the city's retirement gate
cannot express a probe for it, because that gate keeps test paths and reverts
everything else, which for this change means it removes the write-ups and keeps
the exemption. The experiment runs backwards. A patch whose retirement cannot
be measured is the one not to take.

ALSO REJECTED: putting these in `docs/` beside `pm-log.md`, `pm-open.md` and
`roadmap.md`, which carry a per-file exemption approved at the 2026-09-05 init
sitting. Those three are a fixed set. This directory gains a file every time an
epic closes, so a per-file allowlist would refuse the NEXT epic close rather
than catch a forgotten navigation entry.

## One caveat

A checkout without symlink support materializes `docs/epics` as a text file
holding the target path, and the city gate then reports every epic here as
having no write-up. This repository already tracks one symlink (`.agents/skills`)
and builds on Linux, so the case is recorded rather than defended against.
