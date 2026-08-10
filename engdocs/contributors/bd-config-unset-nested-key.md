---
title: bd config unset does not remove nested keys
description: An upstream beads defect that silently leaves a Dolt push target configured, the fix, and the same assumption in our own contract layer.
---

`bd config unset <dotted.key>` reports success and removes nothing when the
key is stored in nested form. The value stays live, and for `sync.remote` --
the Dolt PUSH TARGET -- `bd dolt remote remove origin` then commits the
unchanged, git-tracked `.beads/config.yaml` with the message
`bd: clear sync.remote`.

This page exists because the fix lives upstream in beads, not here, so
nothing in this repository would otherwise record it. Gas City calls
`bd config set` and `bd config get` but never `bd config unset`, so no
gascity code path is broken by this. What is affected is every operator and
agent who types the command, and -- separately -- our own
`internal/beads/contract` key deletion, which makes the same flat-only
assumption against a key list that includes `dolt.password`.

Delete this page once a bd release containing the fix is pinned in
`deps.env`.

## Reproduction

Measured 2026-08-09 against `bd 1.1.1-0.20260805093327-bf97b73749ac`, the
build `deps.env` pins as `BD_CURRENT_VERSION`.

```console
$ cat .beads/config.yaml
sync:
  remote: "git+ssh://git@github.com/wrmarchetto/gascity.git"

$ bd config unset sync.remote
Unset sync.remote (in config.yaml)
$ echo $?
0

$ bd config get sync.remote
git+ssh://git@github.com/wrmarchetto/gascity.git
```

The key survives, `bd config get` still reads it, and the command exits 0.
The same file with the key written flat (`sync.remote: "..."` on one line)
unsets correctly, which is what makes the failure look intermittent.

## Mechanism

Two independent defects compose into a silent no-op. Both are in
`internal/config/yaml_config.go` upstream.

1. `commentOutYamlKey` matches only `^(\s*)<literal-key>\s*:`. Against
   `sync.remote` that regex can match a flat `sync.remote:` line and
   nothing else, so a `sync:` block with an indented `remote:` never
   matches and the content is returned verbatim.
2. `commentOutYamlKey` returns only a string. "Matched nothing" is not
   representable in the signature, so `UnsetYamlConfig` cannot detect it and
   `cmd/bd/config.go` prints `Unset %s` unconditionally.

Fixing either half alone is not enough. Without (1) nested keys still
survive; without (2) a key that was never set still reports a removal.

### Which spelling a key gets, and why it looks random

The deciding line is an early return in `updateNestedYamlKey`:
`if len(root.Content) == 0`. That yields a rule worth stating outright,
because it is the reason two adjacent `dolt.*` keys in one file behave
differently:

- The first dotted key written into an **empty** `config.yaml` lands flat,
  and every later write to that key stays flat.
- Every dotted key written while the file is **non-empty** lands nested.

So whether a given key is removable by `bd config unset` depends on the
order it was first written in, months earlier. In the gascity store that
left `dolt.auto-start` and `dolt.mode` flat (removable) and
`dolt.local-only` nested (not removable).

### Affected versions

The flat-only regex is byte-identical at `v1.0.4` (`BD_PREV_VERSION`),
`v1.1.0` (`BD_VERSION`), the pinned `BD_CURRENT_REF`, and upstream `main` at
`2632d57`. The whole cross-version contract matrix sits on affected builds,
so there is no bd we pin today that behaves correctly. Releases older than
`v1.0.4` were not checked.

## Operator workaround until a fixed bd ships

Edit `.beads/config.yaml` by hand, then confirm with
`bd config get <key>` -- the read path resolves both spellings, so it is a
trustworthy check even though the write path is not.

`bd config set <key> ""` also clears the value in place and makes
`bd config get` report not-set. It leaves an empty string bound rather than
removing the key, so it is only equivalent for consumers that treat `""` as
absent; that was verified for `bd config get` and not for bd's internal
readers.

## The fix

`patches/bd-config-unset-nested-key.patch` applies to
gastownhall/beads at `2632d57e09399862279555f06a9da4cd6e83089e`. It has not
been filed upstream -- no fork and no `gh` credential exist on this machine,
so filing it is a human step.

```bash
git clone https://github.com/gastownhall/beads.git
cd beads && git am /path/to/bd-config-unset-nested-key.patch
```

What it changes:

- `commentOutYamlKey` resolves the nested path through the YAML node tree
  and comments out the key line plus its whole value block. It deliberately
  does NOT widen the regex to the leaf name: `^(\s*)remote\s*:` would also
  match `remote:` under any other parent, so unsetting `sync.remote` could
  clear an unrelated key.
- Both spellings are commented when both are present. viper resolves a
  duplicated key to the flat one, so removing only the nested copy would
  leave the value live.
- `commentOutYamlKey` returns a `found` flag; `UnsetYamlConfig` and
  `UnsetUserYamlConfig` return it. A key that matched nothing leaves the
  file untouched instead of rewriting it byte-identically.
- `bd config unset` prints `<key> was not set` instead of `Unset <key>` when
  nothing matched, and adds `removed` to its `--json` output. Exit status
  stays 0 either way, so unset remains idempotent for provisioning scripts.
- `bd dolt remote remove origin` commits `config.yaml` only when the clear
  actually changed it, and warns naming the file when it did not.

Two adjacent defects fall out of the same rewrite: the terminal newline is
no longer stripped from a git-tracked file on every unset, and
`commentOutYamlKey` no longer uses a `bufio.Scanner`, whose default 64 KiB
token cap would have silently truncated the file past an over-long line with
no error path to notice. `updateYamlKey` still uses a `Scanner` and was left
alone.

Verified with the package suite (`go test ./internal/config/`) and by
building `bd` from the patched tree and re-running the reproduction above:
the nested key is removed, `bd config get` reports not-set, the trailing
newline survives, unsetting a never-set key reports `was not set` at exit 0,
and the flat path is unchanged.

## The same assumption in our own tree

`internal/beads/contract/files.go` deletes keys from `.beads/config.yaml`
through two paths, and both are top-level-only:

- `deleteKeys` compares `root.Content[i].Value` against the key, which only
  ever sees top-level mapping entries.
- `ensureCanonicalConfigFallback` routes through `topLevelConfigLine`, which
  explicitly rejects indented lines.

Their input, `deprecatedConfigKeys`, includes `dolt.password`. Measured
2026-08-09: `bd config set dolt.password <value>` against a non-empty
`config.yaml` writes

```yaml
dolt:
    password: <value>
```

which neither path can see, so a deprecated stored password survives a
canonicalization that reports success. This is a separate defect from the
upstream one and is tracked on its own bead; it is recorded here because the
two share a root assumption and a reader of one should know about the other.

That duality has already been hit once in this repository and patched per
key rather than generally: `setNestedBool`, `nestedConfigBoolValue`, and
`ensureFallbackNestedDoltDisableEventFlush` exist solely because
`dolt.disable-event-flush` sometimes lands nested.
