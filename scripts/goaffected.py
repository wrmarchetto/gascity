"""Affected-Go-package selection from a set of changed repository paths.

This module exists because two independent gates need the same answer to the
same question -- "which packages can a change possibly have broken?" -- and a
second implementation of it would be a silent coverage hole rather than a
duplicate. scripts/ci-static-select asks it to scope lint and vet on pull
requests; scripts/push-gate-select asks it to scope the pre-push test suite.
The Go/shell divergence recorded in ci-c000 is exactly the failure this
sharing prevents: two copies of one rule drift, and the weaker copy is the
one that decides.

The contract every caller depends on is conservatism. affected_package_args
either returns a set it can prove is a superset of what the change can
affect, or it raises SelectionError. It never returns a best-effort answer.
Callers MUST treat SelectionError as "run everything" -- a caller that
degrades to a subset instead has converted an uncertain selection into a
green build, which is the one outcome the whole design exists to prevent.

Scope limit worth knowing before reusing this: the graph here is the Go
dependency graph plus the embed inventory. It does NOT model files a test
reads at run time -- testdata, shell scripts, the Makefile, generated
fixtures. A caller selecting TESTS (rather than lint, which only ever reads
a package's own sources) must therefore refuse to scope changes that touch
paths outside that graph. scripts/push-gate-select does; see the rationale
on its classify_paths.

Verified by scripts/pr_static_scope_contract_test.go (lint scoping, via
ci-static-select) and scripts/push_gate_select_test.go (test scoping).
"""

from __future__ import annotations

from collections import defaultdict, deque
import json
import os
import posixpath
import subprocess
from typing import Any, Iterable


GO_BUILD_INPUT_SUFFIXES = frozenset(
    {
        ".go",
        ".c",
        ".cc",
        ".cpp",
        ".cxx",
        ".m",
        ".h",
        ".hh",
        ".hpp",
        ".hxx",
        ".f",
        ".F",
        ".for",
        ".f90",
        ".s",
        ".S",
        ".sx",
        ".swig",
        ".swigcxx",
        ".syso",
    }
)

EMBED_FILE_FIELDS = ("EmbedFiles", "TestEmbedFiles", "XTestEmbedFiles")
EMBED_PATTERN_FIELDS = ("EmbedPatterns", "TestEmbedPatterns", "XTestEmbedPatterns")

NATIVE_SOURCE_FIELDS = (
    "CgoFiles",
    "CFiles",
    "CXXFiles",
    "MFiles",
    "HFiles",
    "FFiles",
    "SFiles",
    "SwigFiles",
    "SwigCXXFiles",
    "SysoFiles",
)


class SelectionError(Exception):
    """The changed scope could not be selected without losing coverage."""


def command_output(args: list[str]) -> bytes:
    try:
        completed = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except OSError as error:
        raise SelectionError(f"cannot run {args[0]!r}: {error}") from error
    if completed.returncode != 0:
        detail = os.fsdecode(completed.stderr).strip()
        raise SelectionError(detail or f"command failed with exit {completed.returncode}")
    return completed.stdout


def parse_name_status(output: bytes) -> list[tuple[str, str]]:
    fields = output.split(b"\0")
    if fields and fields[-1] == b"":
        fields.pop()
    if len(fields) % 2 != 0:
        raise SelectionError("git produced a malformed NUL-delimited name-status diff")

    records: list[tuple[str, str]] = []
    for index in range(0, len(fields), 2):
        status = os.fsdecode(fields[index])
        path = os.fsdecode(fields[index + 1])
        if len(status) != 1 or status not in "ACDMRT":
            raise SelectionError(f"git produced unsupported status {status!r}")
        if not path or os.path.isabs(path) or ".." in path.split("/"):
            raise SelectionError(f"git produced unsafe path {path!r}")
        records.append((status, path))
    return records


def decode_json_stream(raw: bytes) -> list[dict[str, Any]]:
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise SelectionError(f"go list emitted non-UTF-8 JSON: {error}") from error

    decoder = json.JSONDecoder()
    packages: list[dict[str, Any]] = []
    offset = 0
    while True:
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            return packages
        try:
            value, offset = decoder.raw_decode(text, offset)
        except json.JSONDecodeError as error:
            raise SelectionError(f"go list emitted malformed JSON: {error}") from error
        if not isinstance(value, dict):
            raise SelectionError("go list emitted a non-object package record")
        packages.append(value)


def string_list(package: dict[str, Any], field: str) -> list[str]:
    value = package.get(field, [])
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise SelectionError(f"go list field {field} is not a string list")
    return value


def is_go_build_input(path: str) -> bool:
    return any(path.endswith(suffix) for suffix in GO_BUILD_INPUT_SUFFIXES)


def embedded_repo_path(relative_dir: str, embedded: str) -> str:
    if not embedded or posixpath.isabs(embedded):
        raise SelectionError(f"go list emitted unsafe embedded path {embedded!r}")
    normalized = posixpath.normpath(embedded)
    if normalized != embedded or normalized == ".." or normalized.startswith("../"):
        raise SelectionError(f"go list emitted unsafe embedded path {embedded!r}")
    if relative_dir == ".":
        return normalized
    return posixpath.join(relative_dir, normalized)


def embedded_repo_pattern(relative_dir: str, pattern: str) -> str:
    if pattern.startswith("all:"):
        pattern = pattern.removeprefix("all:")
    return embedded_repo_path(relative_dir, pattern)


def deleted_path_may_match_embed_pattern(path: str, pattern: str) -> bool:
    meta_index = next(
        (index for index, character in enumerate(pattern) if character in "*?[\\"),
        len(pattern),
    )
    literal_prefix = pattern[:meta_index]
    if meta_index == len(pattern):
        return path == pattern or path.startswith(pattern + "/")
    return path.startswith(literal_prefix)


def path_is_beneath_package(path: str, package_dir: str) -> bool:
    return package_dir == "." or path.startswith(package_dir + "/")


def package_inventory(go_tool: str) -> dict[str, Any]:
    """Build the package graph the selectors reason over.

    Returned keys: by_import (ImportPath -> go list record), import_by_dir
    (repo-relative dir -> ImportPath), arg_by_import (ImportPath -> "./dir"
    command-line argument), embed_owners (repo-relative embedded path ->
    owning ImportPaths), embed_patterns, and native_imports.

    Split out of affected_package_args so a caller can classify changed paths
    against the same inventory the closure is computed from. Two separate
    `go list` invocations would let the classification and the closure
    disagree about what the graph contains.
    """
    raw_graph = command_output([go_tool, "list", "-mod=readonly", "-test", "-json", "./..."])
    package_records: list[dict[str, Any]] = []
    for package in decode_json_stream(raw_graph):
        for_test = package.get("ForTest")
        if for_test is not None and not isinstance(for_test, str):
            raise SelectionError("go list field ForTest is not a string")
        matches = string_list(package, "Match")
        if for_test or not matches:
            continue
        package_records.append(package)
    if not package_records:
        raise SelectionError("go list returned no packages for changed paths")

    repo_root = os.path.abspath(
        os.fsdecode(command_output(["git", "rev-parse", "--show-toplevel"])).strip()
    )
    by_import: dict[str, dict[str, Any]] = {}
    import_by_dir: dict[str, str] = {}
    arg_by_import: dict[str, str] = {}
    embed_owners: dict[str, set[str]] = defaultdict(set)
    embed_patterns: list[str] = []
    native_imports: set[str] = set()

    for package in package_records:
        import_path = package.get("ImportPath")
        directory = package.get("Dir")
        if not isinstance(import_path, str) or not import_path:
            raise SelectionError("go list package is missing ImportPath")
        if not isinstance(directory, str) or not directory:
            raise SelectionError(f"go list package {import_path!r} is missing Dir")
        if package.get("Error") or package.get("DepsErrors") or package.get("Incomplete"):
            raise SelectionError(f"go list reported an incomplete graph at {import_path}")
        if import_path in by_import:
            raise SelectionError(f"go list returned duplicate import path {import_path}")

        absolute_dir = os.path.abspath(directory)
        try:
            if os.path.commonpath([repo_root, absolute_dir]) != repo_root:
                raise SelectionError(f"package {import_path} is outside the repository")
        except ValueError as error:
            raise SelectionError(f"package {import_path} is outside the repository") from error
        relative_dir = os.path.relpath(absolute_dir, repo_root).replace(os.sep, "/")
        if relative_dir in import_by_dir:
            raise SelectionError(f"multiple packages map to directory {relative_dir}")

        by_import[import_path] = package
        import_by_dir[relative_dir] = import_path
        arg_by_import[import_path] = "." if relative_dir == "." else f"./{relative_dir}"

        for field in EMBED_FILE_FIELDS:
            for embedded in string_list(package, field):
                embed_owners[embedded_repo_path(relative_dir, embedded)].add(import_path)
        for field in EMBED_PATTERN_FIELDS:
            for pattern in string_list(package, field):
                embed_patterns.append(embedded_repo_pattern(relative_dir, pattern))

        native_sources: list[str] = []
        for field in NATIVE_SOURCE_FIELDS:
            native_sources.extend(string_list(package, field))
        ignored_native_sources = (
            path
            for path in string_list(package, "IgnoredOtherFiles")
            if is_go_build_input(path)
        )
        if native_sources or any(ignored_native_sources):
            native_imports.add(import_path)

    return {
        "by_import": by_import,
        "import_by_dir": import_by_dir,
        "arg_by_import": arg_by_import,
        "embed_owners": embed_owners,
        "embed_patterns": embed_patterns,
        "native_imports": native_imports,
    }


def reverse_closure(seeds: set[str], by_import: dict[str, dict[str, Any]]) -> set[str]:
    """Every package that transitively imports any seed, plus the seeds.

    Test and external-test imports are walked alongside ordinary imports: a
    package whose _test.go files import a changed package is affected by it
    even though its shipped code is not.
    """
    reverse: dict[str, set[str]] = defaultdict(set)
    for importer, package in by_import.items():
        imports = set(
            string_list(package, "Imports")
            + string_list(package, "TestImports")
            + string_list(package, "XTestImports")
        )
        for imported in imports:
            if imported in by_import:
                reverse[imported].add(importer)

    affected = set(seeds)
    pending = deque(sorted(seeds))
    while pending:
        imported = pending.popleft()
        for importer in sorted(reverse[imported]):
            if importer not in affected:
                affected.add(importer)
                pending.append(importer)
    return affected


def affected_package_args(
    records: Iterable[tuple[str, str]],
    go_tool: str,
    inventory: dict[str, Any] | None = None,
) -> list[str]:
    changed = list(records)
    if not changed:
        return []

    if inventory is None:
        inventory = package_inventory(go_tool)
    by_import = inventory["by_import"]
    import_by_dir = inventory["import_by_dir"]
    arg_by_import = inventory["arg_by_import"]
    embed_owners = inventory["embed_owners"]
    embed_patterns = inventory["embed_patterns"]
    native_imports = inventory["native_imports"]

    seeds: set[str] = set()
    for status, path in changed:
        owners = embed_owners.get(path, set())
        seeds.update(owners)
        if status == "D" and any(
            deleted_path_may_match_embed_pattern(path, pattern)
            for pattern in embed_patterns
        ):
            raise SelectionError(
                f"deleted path {path!r} may be absent from the current embed inventory"
            )
        # Native compilers can consume inputs with arbitrary names and from
        # shared directories. The Go package inventory does not expose that
        # dependency graph, so every native package is the smallest safe scope
        # for every changed path without duplicating compiler discovery.
        seeds.update(native_imports)
        build_input = is_go_build_input(path)
        import_path = None
        directory = ""
        if build_input:
            directory = posixpath.dirname(path) or "."
            import_path = import_by_dir.get(directory)
        if (
            status == "D"
            and not owners
            and (not build_input or import_path is None)
            and any(
                path_is_beneath_package(path, package_dir)
                for package_dir in import_by_dir
            )
        ):
            raise SelectionError(
                f"deleted path {path!r} may be absent from the current embed inventory"
            )
        if build_input:
            if import_path is None:
                if native_imports and not path.endswith(".go"):
                    continue
                raise SelectionError(
                    f"changed Go build-input directory {directory!r} has no unique package"
                )
            seeds.add(import_path)

    if not seeds:
        return []

    affected = reverse_closure(seeds, by_import)
    return sorted(arg_by_import[import_path] for import_path in affected)
