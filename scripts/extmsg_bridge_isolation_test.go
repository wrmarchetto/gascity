// Scope: scripts/check-extmsg-bridge-isolation.sh, the gate holding the two
// cross-cutting negatives of epic:mayor-slack-bridge -- acceptance criterion 5
// (no role name on the bridge path) and criterion 8 (the alerts seam stays out
// of the bridge's reach). Bead gs-8ra.
//
// Why the suite exists at all: both criteria are satisfied by every other bead
// in the epic today and broken by any later edit, and neither is visible in the
// epic's round-trip demo -- a demo that passes proves nothing about whether a
// role name is hardcoded or whether the alerts channel is reachable. So the
// rule has to be a command that exits nonzero, and this suite is what
// establishes the command can exit nonzero. A gate nobody has watched fail is
// worth less than no gate: it makes an unproven tree look verified.
//
// Every case below is a mutation. Each refusal the script implements gets a
// fixture that trips exactly that refusal and must die; the accepting cases
// (comments, test files) pin the false positives that would otherwise get the
// gate suppressed the first week it fires. Two cases are the fail-closed arm --
// an empty scan set and a missing role taxonomy both have to be loud, because
// each turns the gate into a no-op that reports OK.
//
// Delegated elsewhere: whether the epic's measurement answers its question is
// not here. That the gate RUNS on every push comes from scripts/ being listed
// in scripts/push-gate-always-run.manifest (this package reads the source tree
// through `git ls-files`, which is that manifest's membership rule), and from
// the Makefile and CI wiring pinned by
// TestExtmsgBridgeIsolationIsWiredIntoTheGateRun.
//
// Run: go test ./scripts -run TestExtmsgBridgeIsolation

package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bridgeGateFixture is a miniature of the real tree in a real git repo: the
// script derives its scan set with `git ls-files`, so an unstaged fixture is
// invisible to it and every case would pass for the wrong reason.
//
// The fixture carries stub copies of both role taxonomies rather than letting
// the script fall back to the real repo's. Injecting the tree is the whole
// point -- a fixture that reached past its own root into the checkout would
// pin the checkout, not the derivation.
type bridgeGateFixture struct {
	root string
}

// fixtureBaseline is the clean miniature: one bridge component whose entrypoint
// names no route (the shape that a route-matching-only derivation misses), the
// lib file that does name one, a test file that must be excluded, and both
// taxonomy sources.
func fixtureBaseline() map[string]string {
	return map[string]string{
		// The runtime role taxonomy. Trimmed to the shape the extractor
		// reads: `var roleEmoji = map[string]string{` then quoted keys.
		"internal/runtime/tmux/tmux.go": `package tmux

var roleEmoji = map[string]string{
	"mayor":        "\U0001F3A9",
	"deacon":       "\U0001F43A",
	"polecat":      "\U0001F63A",
	"health-check": "\U0001F43A",
}
`,
		// The pack-portability guard's taxonomy, the second source. It
		// legitimately differs from roleEmoji (boot and dog have no emoji),
		// which is why the script unions the two instead of requiring
		// them to match.
		"examples/gastown/tmux_theme_script_test.go": `package gastown_test

func TestTmuxThemeScriptHasNoHardcodedRoleNames(t *testing.T) {
	forbidden := []string{
		"polecat", "mayor", "deacon", "boot", "dog",
	}
	_ = forbidden
}
`,
		// package.json plus a route-naming file is what makes this
		// directory a bridge component.
		"contrib/demo-bridge/package.json": `{
  "name": "gc-demo-bridge",
  "type": "module"
}
`,
		"contrib/demo-bridge/lib/gc-client.mjs": `export const post = (m) =>
  fetch('/extmsg/inbound', { method: 'POST', body: JSON.stringify(m) })
`,
		// The entrypoint names no route. It is in scope only because it
		// sits inside the component, and it is the file a convenience
		// default would land in.
		"contrib/demo-bridge/demo-bridge.mjs": `import { post } from './lib/gc-client.mjs'
const target = process.env.GC_TARGET_SESSION
if (!target) {
  console.error('[demo-bridge] GC_TARGET_SESSION is required')
  process.exit(2)
}
post({ target })
`,
		"contrib/demo-bridge/test/demo.test.mjs": `import { test } from 'node:test'
test('fixture', () => {})
`,
	}
}

func newBridgeGateFixture(t *testing.T, overlay map[string]string) *bridgeGateFixture {
	t.Helper()

	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create fixture root: %v", err)
	}
	files := fixtureBaseline()
	for name, content := range overlay {
		if content == "" {
			delete(files, name)
			continue
		}
		files[name] = content
	}
	for name, content := range files {
		writeTestFile(t, filepath.Join(root, name), content)
	}

	f := &bridgeGateFixture{root: root}
	f.git(t, "init", "-q", "-b", "main")
	f.git(t, "config", "user.email", "bridge-gate@example.invalid")
	f.git(t, "config", "user.name", "bridge-gate-test")
	f.git(t, "config", "commit.gpgsign", "false")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-qm", "fixture baseline")
	return f
}

func (f *bridgeGateFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := testCommand("git", args...)
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=bridge-gate-test", "GIT_AUTHOR_EMAIL=bridge-gate@example.invalid",
		"GIT_COMMITTER_NAME=bridge-gate-test", "GIT_COMMITTER_EMAIL=bridge-gate@example.invalid",
		"HOME="+f.root,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runBridgeGate runs the real script against a root and returns its combined
// output plus whether it exited zero.
func runBridgeGate(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "check-extmsg-bridge-isolation.sh")
	cmd := testCommand("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func (f *bridgeGateFixture) run(t *testing.T) (string, bool) {
	t.Helper()
	return runBridgeGate(t, f.root)
}

// TestExtmsgBridgeIsolationAcceptsTheCleanTree pins the accepting side on both
// the fixture and the checkout. The real-tree arm is not redundant with the
// fixture: it is the only thing that would catch the script tripping over the
// bridge path as it actually stands, which is what makes it safe to wire into
// the gate run.
func TestExtmsgBridgeIsolationAcceptsTheCleanTree(t *testing.T) {
	t.Run("fixture", func(t *testing.T) {
		f := newBridgeGateFixture(t, nil)
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("clean fixture must pass, got failure:\n%s", out)
		}
	})

	t.Run("real tree", func(t *testing.T) {
		out, ok := runBridgeGate(t)
		if !ok {
			t.Fatalf("the checkout's bridge path must be clean, got failure:\n%s", out)
		}
	})
}

// TestExtmsgBridgeIsolationRefusesRoleNames is criterion 5's mutation set. The
// second case is the one that decides the derivation: an entrypoint naming no
// route is invisible to a route-matching scan, and it is exactly where a
// convenience default lands, so a gate that only reads route-naming files
// would report OK over it.
func TestExtmsgBridgeIsolationRefusesRoleNames(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
		wantMsg string
	}{
		{
			name: "role literal in a route-naming file",
			overlay: map[string]string{
				"contrib/demo-bridge/lib/gc-client.mjs": `export const target = 'mayor'
export const post = (m) => fetch('/extmsg/inbound', { body: m })
`,
			},
			wantMsg: "mayor",
		},
		{
			name: "role literal in a component file that names no route",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `import { post } from './lib/gc-client.mjs'
const target = process.env.GC_TARGET_SESSION || 'mayor'
post({ target })
`,
			},
			wantMsg: "mayor",
		},
		{
			// Same hole as the case above, in the other language. Criterion 4
			// puts the mirror daemon in the adapter's supervised shape and
			// nothing requires it to be JavaScript, so a Go main package is a
			// bridge component too -- and its entrypoint is as free to name no
			// route as the adapter's is.
			name: "role literal in a Go component entrypoint that names no route",
			overlay: map[string]string{
				"cmd/gc-mirror/main.go": `package main

func main() {
	target := "mayor"
	_ = target
}
`,
				"cmd/gc-mirror/client.go": `package main

const outboundRoute = "/extmsg/outbound"
`,
			},
			wantMsg: "mayor",
		},
		{
			name: "role name taken from the second taxonomy only",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const target = 'dog'
export default target
`,
			},
			wantMsg: "dog",
		},
		{
			name: "role literal in bridge config rather than code",
			overlay: map[string]string{
				"contrib/demo-bridge/package.json": `{
  "name": "gc-demo-bridge",
  "type": "module",
  "gcTargetSession": "mayor"
}
`,
			},
			wantMsg: "mayor",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, tc.overlay)
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected refusal, got pass:\n%s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("refusal must name the offending role %q so the remedy is obvious:\n%s", tc.wantMsg, out)
			}
		})
	}
}

// TestExtmsgBridgeIsolationAcceptsProseAndTests pins the two false positives
// that would get this gate suppressed rather than obeyed. A comment naming the
// mayor is documentation -- the sibling guard
// TestTmuxThemeScriptHasNoHardcodedRoleNames strips comments for the same
// reason -- and a test fixture naming a role is how role-as-configuration gets
// tested at all.
func TestExtmsgBridgeIsolationAcceptsProseAndTests(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
	}{
		{
			name: "role name in a line comment",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `// Binds whatever session config names -- "mayor" in this city, but the
// binding is configuration and this file must not know that.
const target = process.env.GC_TARGET_SESSION
export default target
`,
			},
		},
		{
			name: "role name in a shell comment",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
# Example: GC_TARGET_SESSION=mayor ./run.sh
exec node demo-bridge.mjs
`,
			},
		},
		{
			name: "role name in a test file on the bridge path",
			overlay: map[string]string{
				"contrib/demo-bridge/test/demo.test.mjs": `import { test } from 'node:test'
test('binds the configured session', () => {
  process.env.GC_TARGET_SESSION = 'mayor'
})
`,
			},
		},
		{
			// The comment a careful engineer writes on being told the two
			// channels stay separate. Refusing it would get the criterion 8
			// check deleted rather than narrowed, so it has to pass.
			name: "comment documenting the alerts-seam prohibition",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `// Deliberately NOT SLACK_CHANNEL_ID or SLACK_WEBHOOK_URL: those name the
// one-way alerts destination and the two channels are not unified. The
// bridge does not call notify.sh either.
const channel = process.env.GC_BRIDGE_CHANNEL
export default channel
`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, tc.overlay)
			out, ok := f.run(t)
			if !ok {
				t.Fatalf("expected pass, got refusal:\n%s", out)
			}
		})
	}
}

// TestExtmsgBridgeIsolationRefusesADefaultedBinding is the arm that catches the
// role nobody listed. The name check can only refuse taxonomies the tree
// already knows; a pack-defined role is invisible to it. What is not invisible
// is the SHAPE -- a binding key with a literal fallback -- so the gate refuses
// that whatever the fallback spells.
func TestExtmsgBridgeIsolationRefusesADefaultedBinding(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
	}{
		{
			name: "js or-default on an unknown role",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const target = process.env.GC_TARGET_SESSION || 'archivist'
export default target
`,
			},
		},
		{
			name: "js nullish default on an unknown role",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const target = process.env.GC_SESSION_NAME ?? "archivist"
export default target
`,
			},
		},
		{
			name: "env helper default on an unknown role",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `import { env } from './lib/gc-client.mjs'
const target = env('GC_MIRROR_TARGET', 'archivist')
export default target
`,
			},
		},
		{
			name: "shell parameter default on an unknown role",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
exec node demo-bridge.mjs "${GC_TARGET_AGENT:-archivist}"
`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, tc.overlay)
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected refusal of a defaulted binding, got pass:\n%s", out)
			}
		})
	}
}

// TestExtmsgBridgeIsolationAcceptsANonBindingDefault pins the boundary of the
// shape check. Defaults are ordinary and only a BINDING may not carry one, so a
// key that does not name what it binds keeps its default. Without this case the
// first false failure gets the whole check deleted rather than narrowed.
func TestExtmsgBridgeIsolationAcceptsANonBindingDefault(t *testing.T) {
	f := newBridgeGateFixture(t, map[string]string{
		"contrib/demo-bridge/demo-bridge.mjs": `import { env } from './lib/gc-client.mjs'
const base = env('GC_BASE_URL', 'http://127.0.0.1:8372')
const logDir = env('GC_SESSION_LOG_DIR', '/var/tmp/bridge')
export default { base, logDir }
`,
	})
	out, ok := f.run(t)
	if !ok {
		t.Fatalf("a default on a key that binds nothing must pass:\n%s", out)
	}
}

// TestExtmsgBridgeIsolationRefusesTheAlertsSeam is criterion 8. The threat is
// narrow and specific: a later edit quietly points the mirror at the alerts
// channel. It does that by reading the alert seam's own destination keys, by
// calling the alert seam's scripts, or by carrying a channel id as a literal --
// so all three are refused.
//
// The channel-id case refuses ANY Slack channel id rather than the alerts one.
// Matching the real alerts id would need that id in the repo, which criterion 6
// forbids, and a bridge with its own id hardcoded is broken anyway.
func TestExtmsgBridgeIsolationRefusesTheAlertsSeam(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
		wantMsg string
	}{
		{
			name: "reads the alert seam's channel key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = process.env.SLACK_CHANNEL_ID
export default channel
`,
			},
			wantMsg: "SLACK_CHANNEL_ID",
		},
		{
			name: "reads the alert seam's webhook key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const hook = process.env.SLACK_WEBHOOK_URL
export default hook
`,
			},
			wantMsg: "SLACK_WEBHOOK_URL",
		},
		{
			name: "calls the alert seam",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
exec assets/scripts/notify.sh "bridge up"
`,
			},
			wantMsg: "notify.sh",
		},
		{
			name: "delegates to the alert seam's deliverer",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
exec assets/scripts/slack-deliver.py "bridge up"
`,
			},
			wantMsg: "slack-deliver",
		},
		{
			name: "carries a channel id literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = process.env.GC_BRIDGE_CHANNEL || 'C09ABCDEFGH'
export default channel
`,
			},
			wantMsg: "C09ABCDEFGH",
		},
		{
			name: "carries a bot token literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const token = 'xoxb-2026-fixture-not-a-real-token'
export default token
`,
			},
			wantMsg: "xoxb-",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, tc.overlay)
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected refusal, got pass:\n%s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("refusal must name %q:\n%s", tc.wantMsg, out)
			}
		})
	}
}

// TestExtmsgBridgeIsolationFailsClosed is the arm that matters most and the one
// a read of the script does not predict. Both mutations here leave the gate
// exiting zero over a tree it never inspected: an empty scan set means the
// derivation stopped finding the bridge, and an empty taxonomy means it is
// checking against no role names at all. Neither is distinguishable from a
// clean tree by anything except this test.
func TestExtmsgBridgeIsolationFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
		wantMsg string
	}{
		{
			name: "no bridge component in the tree",
			overlay: map[string]string{
				"contrib/demo-bridge/package.json":      "",
				"contrib/demo-bridge/lib/gc-client.mjs": "",
				"contrib/demo-bridge/demo-bridge.mjs":   "",
				"contrib/demo-bridge/test/demo.test.mjs": `import { test } from 'node:test'
test('fixture', () => {})
`,
			},
			wantMsg: "scan set is empty",
		},
		{
			name: "component directory with no route-naming file",
			overlay: map[string]string{
				"contrib/demo-bridge/lib/gc-client.mjs": `export const post = (m) => fetch('/v0/other', { body: m })
`,
				"contrib/demo-bridge/demo-bridge.mjs": `export default 1
`,
			},
			wantMsg: "scan set is empty",
		},
		{
			name: "runtime role taxonomy renamed away",
			overlay: map[string]string{
				"internal/runtime/tmux/tmux.go": `package tmux

var agentGlyphs = map[string]string{
	"mayor": "\U0001F3A9",
}
`,
			},
			wantMsg: "roleEmoji",
		},
		{
			name: "pack-guard taxonomy removed",
			overlay: map[string]string{
				"examples/gastown/tmux_theme_script_test.go": `package gastown_test

func TestTmuxThemeScriptHasNoHardcodedRoleNames(t *testing.T) {}
`,
			},
			wantMsg: "forbidden",
		},
		{
			name: "runtime taxonomy source deleted outright",
			overlay: map[string]string{
				"internal/runtime/tmux/tmux.go": "",
			},
			wantMsg: "roleEmoji",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, tc.overlay)
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected a fail-closed refusal, got pass:\n%s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("refusal must name what went missing (%q):\n%s", tc.wantMsg, out)
			}
		})
	}

	// A root that is not a checkout would already exit nonzero through the
	// empty-scan-set refusal, so this case is about the MESSAGE. Three lines of
	// `fatal: not a git repository` read as a broken gate rather than a
	// misinvocation, and a gate that looks broken gets removed from the run.
	t.Run("root is not a git work tree", func(t *testing.T) {
		out, ok := runBridgeGate(t, t.TempDir())
		if ok {
			t.Fatalf("expected a refusal for a non-checkout root, got pass:\n%s", out)
		}
		if !strings.Contains(out, "not a git work tree") {
			t.Errorf("refusal must say the root is the problem, not the tree:\n%s", out)
		}
	})

	// The third fail-closed hole, and the one a read of the script misses: a
	// path that is still tracked but gone from the worktree. grep answers
	// nonzero for a missing file exactly as it does for a clean one, so
	// without the readability guard this file leaves the scan silently while
	// the reported file count still includes it.
	t.Run("scan-set file tracked but gone from the worktree", func(t *testing.T) {
		f := newBridgeGateFixture(t, nil)
		if err := os.Remove(filepath.Join(f.root, "contrib", "demo-bridge", "demo-bridge.mjs")); err != nil {
			t.Fatalf("remove tracked bridge file: %v", err)
		}
		out, ok := f.run(t)
		if ok {
			t.Fatalf("expected a fail-closed refusal, got pass:\n%s", out)
		}
		if !strings.Contains(out, "not readable") {
			t.Errorf("refusal must say the file was never inspected:\n%s", out)
		}
	})
}

// TestExtmsgBridgeIsolationDerivationAnchors pins the derivation against the
// real tree. The two anchors are the mechanism, not an inventory: the first
// says the scan set still reaches the canonical bridge entrypoint the epic
// builds on, the second says it still reaches the gc-side extmsg route
// implementation. A derivation that silently narrowed to nothing, or widened to
// prose, breaks one of them.
//
// Deliberately NOT an exact file list. The epic is adding files to the bridge
// path in parallel with this gate, and a list would fail on every one of them
// -- which is the rot this gate exists to avoid reproducing.
func TestExtmsgBridgeIsolationDerivationAnchors(t *testing.T) {
	out, ok := runBridgeGate(t, "--list")
	if !ok {
		t.Fatalf("--list must succeed:\n%s", out)
	}
	scanned := strings.Fields(out)

	for _, anchor := range []string{
		"contrib/openclaw-bridge/bridge.mjs",
		"internal/extmsg/types.go",
	} {
		found := false
		for _, path := range scanned {
			if path == anchor {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("derivation no longer reaches %s; the gate may be scanning nothing.\n"+
				"scan set (%d files):\n%s", anchor, len(scanned), out)
		}
	}

	for _, path := range scanned {
		if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".txt") {
			t.Errorf("prose file %s is in the scan set; criterion 5 is about source, "+
				"and docs/pm-log.md legitimately discusses roles", path)
		}
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, ".test.") {
			t.Errorf("test file %s is in the scan set; role names in fixtures are how "+
				"role-as-configuration gets tested", path)
		}
	}
}

// TestExtmsgBridgeIsolationIsWiredIntoTheGateRun is criterion 5's "part of the
// normal gate run, not a one-time audit" clause. A gate script with no caller
// is prose. Three callers are asserted because each covers a different way the
// others are skipped: the Makefile target is the hand-run and CI entry, the CI
// step is what a pushed branch cannot avoid, and this package's membership in
// the always-run manifest is what makes the pre-push hook run it even when the
// change touched no Go build input.
func TestExtmsgBridgeIsolationIsWiredIntoTheGateRun(t *testing.T) {
	root := repoRoot(t)
	const target = "check-extmsg-bridge-isolation"

	makefile := readTestFile(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(makefile, "\n"+target+":") {
		t.Errorf("Makefile has no %q target; the gate has no hand-run entry point", target)
	}
	if !strings.Contains(makefile, "scripts/check-extmsg-bridge-isolation.sh") {
		t.Errorf("Makefile target does not invoke the script")
	}

	ci := readTestFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	if !strings.Contains(ci, "make "+target) {
		t.Errorf("ci.yml never runs `make %s`; a pushed branch could skip the gate entirely", target)
	}

	manifest := readTestFile(t, filepath.Join(root, "scripts", "push-gate-always-run.manifest"))
	if !strings.Contains(manifest, "\n./scripts\n") {
		t.Errorf("./scripts is not in the always-run manifest, so this suite would be " +
			"dropped from a scoped pre-push run that touched no Go build input")
	}
}

// TestExtmsgBridgeIsolationExcludesItself pins the one exclusion that is about
// the gate rather than the bridge. This script has to spell out what it
// refuses, so SLACK_CHANNEL_ID sits in its own code and 'mayor' in its own
// prose. The moment a comment in it names an extmsg route literally, the
// route-matching derivation pulls the script into its own scan set and it
// refuses itself, citing the alerts seam -- a message indistinguishable from a
// real criterion-8 violation, which is how a gate gets suppressed rather than
// obeyed. Measured 2026-09-07: one added comment line turned a clean tree red.
//
// The fixture reproduces the shape rather than editing the real script: a
// component file that both names a route and carries the same seam literals
// this gate does. It must be refused there -- the exclusion is by filename and
// must NOT generalize to any file that happens to look like a gate.
func TestExtmsgBridgeIsolationExcludesItself(t *testing.T) {
	t.Run("the gate's own files are never scanned", func(t *testing.T) {
		out, ok := runBridgeGate(t, "--list")
		if !ok {
			t.Fatalf("--list must succeed:\n%s", out)
		}
		for _, path := range strings.Fields(out) {
			if strings.HasSuffix(path, "check-extmsg-bridge-isolation.sh") ||
				strings.HasSuffix(path, "extmsg_bridge_isolation_test.go") {
				t.Errorf("%s is in its own scan set; a route named literally in "+
					"this gate's prose would make it refuse itself", path)
			}
		}
	})

	// The exclusion is applied twice, on two different sets, and each one has
	// a job the other cannot do. Filtering the route-match set is what stops
	// this gate's own directory from being classified as a bridge component --
	// measured 2026-09-07: without it the scan set goes from 16 files to 75 and
	// the gate false-fails on scripts/push-gate-lock-lib.sh, which has nothing
	// to do with the bridge. Filtering the final scan set is what covers the
	// gate's filename surfacing through the component arm instead, which is the
	// case below. Neither is redundant, and without this case the second one
	// looks like it is and gets deleted.
	t.Run("the gate's filename is excluded wherever it sits", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/check-extmsg-bridge-isolation.sh": `#!/usr/bin/env bash
for seam in SLACK_CHANNEL_ID SLACK_WEBHOOK_URL notify.sh; do echo "$seam"; done
`,
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("the gate's own filename must not be scanned even when the "+
				"component arm surfaces it:\n%s", out)
		}
	})

	t.Run("the exclusion does not generalize", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/lib/gc-client.mjs": `// POST /extmsg/inbound
export const alertsKey = 'SLACK_CHANNEL_ID'
`,
		})
		out, ok := f.run(t)
		if ok {
			t.Fatalf("a bridge file carrying the seam literal must still be refused:\n%s", out)
		}
		if !strings.Contains(out, "SLACK_CHANNEL_ID") {
			t.Errorf("refusal must name the seam key:\n%s", out)
		}
	})
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
