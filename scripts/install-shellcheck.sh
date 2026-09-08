#!/usr/bin/env bash
# scripts/install-shellcheck.sh -- install a pinned shellcheck into a bin dir.
#
# The shell-lint gate (scripts/check-shell-lint.sh) fails closed when the
# linter is absent, so something has to provide it. ShellCheck is a Haskell
# binary with no `go install` path, which is why this exists rather than a
# $(GOLANGCI_LINT)-style go install line in the Makefile.
#
# NOTE: no comment line in a shell file may begin with the word "shellcheck".
# ShellCheck reads any comment whose first word is that literal as a
# directive, and an unparseable one aborts analysis of the whole file -- the
# defect this gate exists to refuse. Line 5 of this file's first draft was
# such a line.
#
# The version is pinned and every archive's SHA-256 is recorded below. An
# unpinned "latest" install would let a new release add a check and
# turn CI red on an unrelated PR, with the diff offering no explanation; and
# an unverified download would make the gate a code-execution path for
# whoever controls the release host. The digest table mirrors
# .github/scripts/install-bd-archive.sh, which pins dolt and bd the same way.
#
# Usage: scripts/install-shellcheck.sh VERSION BIN_DIR
#
# The pinned version lives in the Makefile (SHELLCHECK_VERSION) and is passed
# in, so there is one place to bump. TestShellLintGatePinsOneShellcheckVersion
# fails if the Makefile pin and this table disagree.

set -euo pipefail

version="${1:-}"
bin_dir="${2:-}"
if [ -z "$version" ] || [ -z "$bin_dir" ]; then
  echo "usage: scripts/install-shellcheck.sh VERSION BIN_DIR" >&2
  exit 2
fi

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *)
    echo "install-shellcheck: unsupported OS $(uname -s); install shellcheck $version by hand and put it on PATH" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64 | aarch64) arch=aarch64 ;;
  x86_64 | amd64) arch=x86_64 ;;
  *)
    echo "install-shellcheck: unsupported architecture $(uname -m); install shellcheck $version by hand and put it on PATH" >&2
    exit 1
    ;;
esac

# --- pinned archive digests ---
# sha256 of shellcheck-v<version>.<os>.<arch>.tar.xz, measured 2026-09-07
# against the koalaman/shellcheck GitHub release assets.
expected_sha=""
case "${version}:${os}.${arch}" in
  0.11.0:linux.x86_64) expected_sha="8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198" ;;
  0.11.0:linux.aarch64) expected_sha="12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588" ;;
  0.11.0:darwin.x86_64) expected_sha="3c89db4edcab7cf1c27bff178882e0f6f27f7afdf54e859fa041fca10febe4c6" ;;
  0.11.0:darwin.aarch64) expected_sha="56affdd8de5527894dca6dc3d7e0a99a873b0f004d7aabc30ae407d3f48b0a79" ;;
  *)
    echo "install-shellcheck: no pinned SHA-256 for shellcheck ${version} on ${os}.${arch}" >&2
    echo "Add one to the digest table in scripts/install-shellcheck.sh, or revert SHELLCHECK_VERSION in the Makefile." >&2
    exit 1
    ;;
esac

archive="shellcheck-v${version}.${os}.${arch}.tar.xz"
url="https://github.com/koalaman/shellcheck/releases/download/v${version}/${archive}"

# Staged on disk, never in TMPDIR: /tmp here is a fleet-shared RAM tmpfs that a
# concurrent build wave can ENOSPC (AGENTS.md, incident gm-tkz1r).
work="$(mktemp -d -p /var/tmp gc-shellcheck.XXXXXX)"
trap 'rm -rf "$work"' EXIT

echo "Installing shellcheck v${version} (${os}.${arch})..." >&2
attempt=1
max_attempts=3
delay=2
while :; do
  if curl -fsSL --retry 2 --max-time 120 -o "$work/$archive" "$url"; then
    break
  fi
  if [ "$attempt" -ge "$max_attempts" ]; then
    echo "install-shellcheck: failed to download $url after $max_attempts attempts" >&2
    exit 1
  fi
  echo "install-shellcheck: download failed; retrying in ${delay}s..." >&2
  sleep "$delay"
  attempt=$((attempt + 1))
  delay=$((delay * 2))
done

# GNU coreutils on Linux, BSD shasum on macOS -- same split as
# .github/scripts/install-bd-archive.sh's sha256_file.
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha="$(sha256sum "$work/$archive" | cut -d ' ' -f 1)"
else
  actual_sha="$(shasum -a 256 "$work/$archive" | cut -d ' ' -f 1)"
fi
if [ "$actual_sha" != "$expected_sha" ]; then
  echo "install-shellcheck: SHA-256 mismatch for $archive" >&2
  echo "  expected $expected_sha" >&2
  echo "  actual   $actual_sha" >&2
  exit 1
fi

tar -xJf "$work/$archive" -C "$work"
mkdir -p "$bin_dir"
# install(1) writes through a temp inode and renames, so a concurrent `make`
# in another worktree cannot observe a half-written binary on the shared
# GOPATH/bin this defaults to.
install -m 0755 "$work/shellcheck-v${version}/shellcheck" "$bin_dir/shellcheck"
"$bin_dir/shellcheck" --version >&2
