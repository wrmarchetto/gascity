#!/usr/bin/env bash
#
# canary-fetch.test.sh -- scripts/canary-fetch.sh leaves a whole reading or
# none: a failed gc or bd call leaves no output file, and a non-empty --out is
# refused before anything is fetched.
#
# Run: bash scripts/canary-fetch.test.sh (go test ./scripts/ runs it).
# hands off to: scripts/canary_eval_test.go, for what the evaluator reads.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FETCH="$TEST_DIR/canary-fetch.sh"

pass=0
fail=0
record_pass() {
  echo "  ok   $1"
  pass=$((pass + 1))
}
record_fail() {
  echo "  FAIL $1 -- $2"
  fail=$((fail + 1))
}
assert_eq() {
  if [[ "$2" == "$3" ]]; then record_pass "$1"; else record_fail "$1" "got '$2', want '$3'"; fi
}
assert_contains() {
  if [[ "$2" == *"$3"* ]]; then record_pass "$1"; else record_fail "$1" "missing '$3' in: $2"; fi
}

work="$(mktemp -d -p "${TMPDIR:-/var/tmp}")"
trap 'rm -rf -- "$work"' EXIT

# Stub gc and bd share one call counter. Each refuses a call it was not written
# for, and on call $STUB_FAIL_AT writes half a document and exits 1, so a
# failed call is one whose stdout is not empty.
stubs="$work/bin"
mkdir -p "$stubs" "$work/rig-a/.beads" "$work/rig-b/.beads"
for tool in gc bd; do
  verb=usage
  [ "$tool" = bd ] && verb=list
  cat >"$stubs/$tool" <<EOF
#!/usr/bin/env bash
[ "\$1" = $verb ] || { echo "stub $tool: unscripted call: \$*" >&2; exit 64; }
n=\$(( \$(cat "\$STUB_CALLS" 2>/dev/null || echo 0) + 1 ))
echo "\$n" >"\$STUB_CALLS"
if [ "\$n" = "\${STUB_FAIL_AT:-0}" ]; then printf '{"half": ['; exit 1; fi
printf '[]\n'
EOF
  chmod +x "$stubs/$tool"
done

# run_fetch OUT FAIL_AT -- prints the script's stderr, returns its exit.
run_fetch() {
  local out="$1" fail_at="$2"
  rm -f -- "$work/calls"
  {
    PATH="$stubs:$PATH" STUB_CALLS="$work/calls" STUB_FAIL_AT="$fail_at" \
      "$FETCH" --out "$out" \
      --baseline 2026-09-04T00:00:00-05:00 2026-09-10T00:00:00-05:00 \
      --window 2026-09-13T00:00:00-05:00 2026-09-27T00:00:00-05:00 \
      --store a="$work/rig-a" --store b="$work/rig-b" >/dev/null
  } 2>&1
}

entries() { find "$1" -mindepth 1 2>/dev/null | sed "s|^$1/||" | sort | tr '\n' ' '; }

echo "canary-fetch: a complete reading"
run_fetch "$work/ok" 0 >/dev/null
assert_eq "exits 0" "$?" 0
assert_eq "writes the six files and nothing else" "$(entries "$work/ok")" \
  "baseline-beads-a.json baseline-beads-b.json baseline-usage.json window-beads-a.json window-beads-b.json window-usage.json "
assert_eq "every stub call was made" "$(cat "$work/calls")" 6

# Call 2 is the window's gc usage, after the baseline's has succeeded. Call 5
# is store b's baseline bd list, after four files are complete.
for fail_at in 2 5; do
  echo "canary-fetch: call $fail_at fails"
  err="$(run_fetch "$work/fail-$fail_at" "$fail_at")"
  rc=$?
  assert_eq "call $fail_at: exits nonzero" "$([ "$rc" -ne 0 ] && echo yes)" yes
  assert_eq "call $fail_at: leaves no output file" "$(entries "$work/fail-$fail_at")" ""
  assert_contains "call $fail_at: names the failed call" "$err" "failed"
done

echo "canary-fetch: a failed run's empty --out is reusable"
run_fetch "$work/fail-2" 0 >/dev/null
assert_eq "exits 0" "$?" 0

echo "canary-fetch: a non-empty --out"
mkdir -p "$work/stale"
printf 'stale\n' >"$work/stale/window-beads-old.json"
err="$(run_fetch "$work/stale" 0)"
rc=$?
assert_eq "exits nonzero" "$([ "$rc" -ne 0 ] && echo yes)" yes
assert_contains "names the cause" "$err" "is not empty"
assert_eq "fetches nothing" "$([ -e "$work/calls" ] && echo called || echo none)" none
assert_eq "leaves the directory as found" "$(entries "$work/stale")" "window-beads-old.json "

echo "canary-fetch: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
