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
// (comments, test files, component prose) pin the false positives that would
// otherwise get the gate suppressed the first week it fires. Two cases are the
// fail-closed arm -- an empty scan set and a missing role taxonomy both have to
// be loud, because each turns the gate into a no-op that reports OK.
//
// WHY SO MANY CASES ASSERT THE MESSAGE AND NOT ONLY THE EXIT STATUS. The gate
// is fail-fast over eight arms and the fixtures are small, so one arm readily
// covers for another: every conversation-id case was first written as
// `process.env.X || <id>`, which the inline-fallback arm also refuses while
// echoing a line containing the id, and the whole literal arm could then be
// deleted with this suite green. A mutation sweep over the script found it; a
// reading of the suite did not. Each refusing case therefore names the arm that
// must be the one to fire.
//
// The bead gs-fn6 additions -- the copied-seam refusal, component prose, knob
// drift, the inline fallback, SLACK_BOT_TOKEN, hooks.slack.com and the widened
// conversation-id shape -- came from the origin/main gate that lost the gs-8ra
// add/add merge. Every one of them was watched to fail before it was watched to
// pass, and the 17-mutant sweep that establishes it is recorded in the commit
// body rather than kept as a script: it edits the gate in place, so a copy left
// in the tree is a copy that can be run against the wrong checkout.
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
		// The component's prose, carrying all three things the gate must
		// NOT refuse in a README: a role name (criterion 5 is about
		// source, and a README naming the session an operator binds is
		// the criterion being SATISFIED), a credential placeholder, and
		// a channel-id example. It sits in the baseline rather than in
		// one overlay so that every case in this suite goes red if any
		// of the three exclusions is lost -- the dedicated accepting
		// case below is still not redundant, because a later edit to
		// this baseline would silently retire the invariant with no
		// test naming it.
		"contrib/demo-bridge/README.md": `# demo-bridge

Bind the session the deployment names -- in this city that is the mayor,
but the bridge must not know it:

    GC_TARGET_SESSION=mayor
    BRIDGE_SLACK_BOT_TOKEN=xoxb-...
    BRIDGE_SLACK_CHANNEL_ID=C09ABCDEFGH
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
	// Asserted alongside the role name itself. Two of these fixtures spell the
	// role as a default on a binding key, which the shape arm also refuses
	// while echoing a line that contains the name -- so without this the name
	// arm could be deleted with the table green.
	const wantArm = "uses the role name"

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
			if !strings.Contains(out, wantArm) {
				t.Errorf("the name arm must be what refused this, not the shape arm:\n%s", out)
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
//
// Every case asserts the MESSAGE, not just the exit status, and that is not
// decoration. The inline-fallback refusal below trips on the same two .mjs
// fixtures for a different reason, so a bare `if ok` here would stay green with
// the whole shape arm deleted. The message is the only thing that says which
// refusal fired.
func TestExtmsgBridgeIsolationRefusesADefaultedBinding(t *testing.T) {
	const wantMsg = "literal default"

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
			if !strings.Contains(out, wantMsg) {
				t.Errorf("the shape arm must be what refused this, not another check:\n%s", out)
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
	// The two arms this table drives, asserted per case. Exit status alone
	// does not distinguish them, and the fixtures are close enough that one
	// arm covers for the other: every conversation-id case was originally
	// written as `process.env.GC_BRIDGE_CHANNEL || <id>`, which the
	// inline-fallback refusal also trips, echoing a line that contains the id
	// -- so the whole literal arm could be deleted with this table green.
	// Caught by the mutation sweep, not by reading the suite.
	const (
		armSeam    = "reaches the alerts seam"
		armLiteral = "conversation id or token literal"
	)

	cases := []struct {
		name    string
		overlay map[string]string
		wantMsg string
		wantArm string
	}{
		{
			name: "reads the alert seam's channel key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = process.env.SLACK_CHANNEL_ID
export default channel
`,
			},
			wantMsg: "SLACK_CHANNEL_ID",
			wantArm: armSeam,
		},
		{
			name: "reads the alert seam's webhook key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const hook = process.env.SLACK_WEBHOOK_URL
export default hook
`,
			},
			wantMsg: "SLACK_WEBHOOK_URL",
			wantArm: armSeam,
		},
		{
			// The hole the merge left open. main's gate caught only the
			// xoxb-/xapp- LITERALS, so a bridge reading the alert seam's
			// own bot-token key by name passed it clean -- recorded on
			// gs-fn6 as the one dropped refusal that was not a weaker
			// restatement of something main already had.
			name: "reads the alert seam's bot-token key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const token = process.env.SLACK_BOT_TOKEN
export default token
`,
			},
			wantMsg: "SLACK_BOT_TOKEN",
			wantArm: armSeam,
		},
		{
			// The hole in the exact spelling gs-fn6 names. Measured against
			// the gate as it stood: this fixture exits 0 there and 1 here.
			// Kept alongside the process.env spelling above because the two
			// are what distinguishes closing the hole by NAME, over the whole
			// scan set, from closing it by intersecting the seam's keys with
			// the required() call sites -- the narrower form origin used,
			// which the process.env case walks straight past.
			name: "requires the alert seam's bot-token key",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `import { required } from './lib/gc-client.mjs'
const token = required('SLACK_BOT_TOKEN')
export default token
`,
			},
			wantMsg: "SLACK_BOT_TOKEN",
			wantArm: armSeam,
		},
		{
			// A hardcoded incoming-webhook URL names none of the seam's
			// env keys and carries no xoxb-/xapp- token, so every other
			// arm passes it. The host is the alerts destination in its
			// most direct form.
			name: "hardcodes the alerts webhook host",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const hook = 'https://hooks.slack.com/services/T01/B02/zzz'
export default hook
`,
			},
			wantMsg: "hooks.slack.com",
			wantArm: armSeam,
		},
		{
			name: "calls the alert seam",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
exec assets/scripts/notify.sh "bridge up"
`,
			},
			wantMsg: "notify.sh",
			wantArm: armSeam,
		},
		{
			name: "delegates to the alert seam's deliverer",
			overlay: map[string]string{
				"contrib/demo-bridge/run.sh": `#!/bin/sh
exec assets/scripts/slack-deliver.py "bridge up"
`,
			},
			wantMsg: "slack-deliver",
			wantArm: armSeam,
		},
		{
			name: "carries a channel id literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = 'C09ABCDEFGH'
export default channel
`,
			},
			wantMsg: "C09ABCDEFGH",
			wantArm: armLiteral,
		},
		{
			// The digit is not in position 2, which is the only position
			// main's old C[0-9][A-Z0-9]{7,} would accept it in. Slack
			// does not promise that position.
			name: "carries a channel id whose digit is not second",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = 'CAB9DEFGHIJ'
export default channel
`,
			},
			wantMsg: "CAB9DEFGHIJ",
			wantArm: armLiteral,
		},
		{
			name: "carries a private-group id literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = 'G012ABCDEF'
export default channel
`,
			},
			wantMsg: "G012ABCDEF",
			wantArm: armLiteral,
		},
		{
			name: "carries a dm conversation id literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const channel = 'D01ABCDEFG'
export default channel
`,
			},
			wantMsg: "D01ABCDEFG",
			wantArm: armLiteral,
		},
		{
			name: "carries a bot token literal",
			overlay: map[string]string{
				"contrib/demo-bridge/demo-bridge.mjs": `const token = 'xoxb-2026-fixture-not-a-real-token'
export default token
`,
			},
			wantMsg: "xoxb-",
			wantArm: armLiteral,
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
			if !strings.Contains(out, tc.wantArm) {
				t.Errorf("the %q arm must be what refused this, not another check:\n%s", tc.wantArm, out)
			}
		})
	}
}

// TestExtmsgBridgeIsolationAcceptsANamespacedBridgeKey pins the boundary
// between the alerts seam's keys and the bridge's own. Criterion 8 requires the
// bridge to carry a separate channel key, so the refusal above must match
// SLACK_CHANNEL_ID as a whole identifier and not as a substring.
// BRIDGE_SLACK_CHANNEL_ID names a different destination and is the shape the
// isolation actually takes in contrib/openclaw-bridge/slack-bridge.mjs.
//
// Rejected: dropping the seam keys from the denylist once the bridge grew keys
// of its own. That would let a later edit read the alert destination directly,
// which is the whole of criterion 8. The boundary is what distinguishes the two,
// not the presence of the name.
func TestExtmsgBridgeIsolationAcceptsANamespacedBridgeKey(t *testing.T) {
	f := newBridgeGateFixture(t, map[string]string{
		"contrib/demo-bridge/demo-bridge.mjs": `import { required } from './lib/gc-client.mjs'
const channel = required('BRIDGE_SLACK_CHANNEL_ID')
const hook = required('BRIDGE_SLACK_WEBHOOK_URL')
const token = required('BRIDGE_SLACK_BOT_TOKEN')
export default { channel, hook, token }
`,
	})
	out, ok := f.run(t)
	if !ok {
		t.Fatalf("a bridge-owned key that merely contains a seam key must pass:\n%s", out)
	}
}

// TestExtmsgBridgeIsolationAcceptsACapitalisedWord pins the digit requirement
// on the conversation-id shape. [CGD] followed by eight or more uppercase
// alphanumerics also matches ordinary shouted English -- CONVERSATION,
// CREDENTIALS, DESTINATION -- and it does in this tree: a banner string reading
// CONVERSATION in contrib/openclaw-bridge/demo-telegram.sh:203 is the only
// shape-only hit across the whole scan set, measured 2026-09-07. An id carries
// digits; a word in caps does not.
//
// ABSENCE, so the next reader does not think it was missed: an all-letter
// conversation id walks past this refusal. Nothing distinguishes such a token
// from prose, and buying it would cost every capitalized word on the bridge
// path. The backstops are the seam arm above and the required() read that makes
// a literal redundant in the first place.
func TestExtmsgBridgeIsolationAcceptsACapitalisedWord(t *testing.T) {
	f := newBridgeGateFixture(t, map[string]string{
		"contrib/demo-bridge/run.sh": `#!/bin/sh
echo "CHILD CONVERSATION -- DESTINATION unset, CREDENTIALS from secrets.env"
exec node demo-bridge.mjs
`,
	})
	out, ok := f.run(t)
	if !ok {
		t.Fatalf("a shouted English word is not a conversation id:\n%s", out)
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

// TestExtmsgBridgeIsolationRefusesTheSeamCopiedIntoTheRepo is criterion 8's
// other half, and the only refusal here that does not read the scan set. The
// city's notify.sh cannot be checked for modification from a CI checkout that
// has no city tree, so what is enforceable from here is the move that would
// make it modifiable from here: landing a copy of the seam in this repository.
// That arrives as a NEW FILE rather than as an edit, which every scan-set arm
// misses -- a copy dropped outside any bridge component is in no scan set at
// all, and the fixture puts it there deliberately.
func TestExtmsgBridgeIsolationRefusesTheSeamCopiedIntoTheRepo(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "notify.sh outside every component", path: "assets/scripts/notify.sh"},
		{name: "slack-deliver.py outside every component", path: "tools/slack-deliver.py"},
		{name: "notify.sh at the repository root", path: "notify.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, map[string]string{
				tc.path: "#!/bin/sh\necho alert\n",
			})
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected refusal of a copied alert seam, got pass:\n%s", out)
			}
			if !strings.Contains(out, tc.path) {
				t.Errorf("refusal must name the copy (%q):\n%s", tc.path, out)
			}
		})
	}

	// The match is anchored on a whole path component. Without the anchor a
	// bridge's own bridge-notify.sh is refused as the city's seam, and the
	// remedy an author reaches for then is to delete the check.
	t.Run("a differently named script is not the seam", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/bridge-notify.sh": "#!/bin/sh\necho up\n",
			"contrib/demo-bridge/slack-deliver.mjs": `export const deliver = () => 1
`,
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("only notify.sh and slack-deliver.py are the seam:\n%s", out)
		}
	})
}

// TestExtmsgBridgeIsolationRefusesTheAlertsSeamInProse is the prose half of
// criterion 8. A README telling an operator to set SLACK_WEBHOOK_URL unifies
// the two channels exactly as effectively as code that reads it, and the
// operator acts on the README.
//
// The scope is deliberately narrower than the code scan's: the COMPONENT arm
// alone, never the repo-wide route match. docs/pm-log.md discusses both the
// mayor and the alert seam's one-way decision at length and must never enter
// it, which the accepting case below pins.
//
// Known over-refusal, in the same direction as the code arm's trailing-comment
// refusal: prose documenting the prohibition ("never point this at
// SLACK_WEBHOOK_URL") is refused too, because nothing mechanical separates a
// warning from an instruction. The remedy is to name the city's alert seam
// rather than its keys, and refusing too much is the survivable direction.
func TestExtmsgBridgeIsolationRefusesTheAlertsSeamInProse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantMsg string
	}{
		{
			name:    "points the operator at the alerts webhook",
			body:    "# demo-bridge\n\nReuse the alert webhook: SLACK_WEBHOOK_URL=https://...\n",
			wantMsg: "SLACK_WEBHOOK_URL",
		},
		{
			name:    "points the operator at the alerts channel key",
			body:    "# demo-bridge\n\nSet SLACK_CHANNEL_ID to the same channel the alerts use.\n",
			wantMsg: "SLACK_CHANNEL_ID",
		},
		{
			name:    "tells the operator to call the seam",
			body:    "# demo-bridge\n\nOn failure the bridge shells out to notify.sh.\n",
			wantMsg: "notify.sh",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeGateFixture(t, map[string]string{
				"contrib/demo-bridge/README.md": tc.body,
			})
			out, ok := f.run(t)
			if ok {
				t.Fatalf("expected refusal of an alerts-seam instruction, got pass:\n%s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("refusal must name the seam (%q):\n%s", tc.wantMsg, out)
			}
		})
	}

	// The whole reason the code scan excludes prose. Re-admitting .md
	// repo-wide would refuse the project's own log for discussing the
	// decision it records, and that finding is unactionable.
	t.Run("prose outside every component is untouched", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"docs/pm-log.md": "#57 the mayor's alerts stay one-way: SLACK_WEBHOOK_URL and\n" +
				"SLACK_CHANNEL_ID belong to notify.sh, and the bridge gets its own.\n",
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("the repo's own log must not be scanned as bridge prose:\n%s", out)
		}
	})

	// The role arm must NOT follow the seam arm into prose. A README naming
	// the session an operator binds is criterion 5 being satisfied -- it
	// shows the identity arriving as configuration -- and the baseline
	// fixture's README already carries one, so this case names what every
	// other case in the suite is silently relying on.
	t.Run("a role name in component prose is accepted", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/README.md": "# demo-bridge\n\nRun GC_TARGET_SESSION=mayor to bind the mayor's session.\n",
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("prose naming the bound session must pass:\n%s", out)
		}
	})

	// Nor the credential and conversation-id literals. Documenting the shape
	// of the value an operator must supply is how the README says the value
	// is configuration; refusing the example teaches the next author to stop
	// writing examples. contrib/openclaw-bridge/README.md:298-299 carries the
	// two token placeholders verbatim. Its channel example is C012345, which
	// is too short for the conversation-id shape to reach, so the full-length
	// id here is the case the real tree does NOT yet exercise -- which is
	// exactly why it is pinned rather than left to the real-tree arm.
	t.Run("credential and channel placeholders in prose are accepted", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/README.md": "# demo-bridge\n\n" +
				"    BRIDGE_SLACK_BOT_TOKEN=xoxb-...\n" +
				"    BRIDGE_SLACK_APP_TOKEN=xapp-...\n" +
				"    BRIDGE_SLACK_CHANNEL_ID=C09ABCDEFGH\n",
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("placeholders showing the operator what to supply must pass:\n%s", out)
		}
	})
}

// TestExtmsgBridgeIsolationRefusesKnobDrift pins the cross-file refusal, the
// one no per-file arm can see. Requiredness is a property of the KNOB, not of
// one call site: a convenience default added next to an existing required()
// read -- in another file, which is why it survives review -- turns a missing
// configuration into a silent bind to whatever the default names.
func TestExtmsgBridgeIsolationRefusesKnobDrift(t *testing.T) {
	t.Run("required in one file, defaulted in another", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/demo-bridge.mjs": `import { required } from './lib/gc-client.mjs'
export default required('BRIDGE_DEMO_CHANNEL')
`,
			"contrib/demo-bridge/lib/slack.mjs": `import { env } from './gc-client.mjs'
export const channel = env('BRIDGE_DEMO_CHANNEL', 'fallback')
`,
		})
		out, ok := f.run(t)
		if ok {
			t.Fatalf("expected refusal of a knob that is required in one place and defaulted in another:\n%s", out)
		}
		if !strings.Contains(out, "BRIDGE_DEMO_CHANNEL") {
			t.Errorf("refusal must name the drifting knob:\n%s", out)
		}
		if !strings.Contains(out, "read with required() in one place") {
			t.Errorf("the knob-drift arm must be what refused this, not another check:\n%s", out)
		}
	})

	// The boundary. Defaults are ordinary and required reads are ordinary;
	// only the DISAGREEMENT is the violation. Without this case the refusal
	// could be narrowed to "any env() default" and stay green.
	t.Run("disjoint required and defaulted knobs are accepted", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/demo-bridge.mjs": `import { required } from './lib/gc-client.mjs'
export default required('BRIDGE_DEMO_CHANNEL')
`,
			"contrib/demo-bridge/lib/slack.mjs": `import { env } from './gc-client.mjs'
export const base = env('BRIDGE_DEMO_BASE_URL', 'http://127.0.0.1:8372')
`,
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("a knob that is only ever defaulted, alongside one that is only ever required, must pass:\n%s", out)
		}
	})
}

// TestExtmsgBridgeIsolationRefusesAnInlineEnvFallback is the evasion knob drift
// cannot see: a fallback written straight onto a raw process.env read, so the
// knob never appears in an env() call at all. Forbidding the SHAPE means the
// refusal holds whatever the default spells and whatever the key is named --
// the existing shape arm covers only keys that name what they bind, and this
// one covers the rest.
func TestExtmsgBridgeIsolationRefusesAnInlineEnvFallback(t *testing.T) {
	// The key deliberately does not end in SESSION/AGENT/TARGET/HANDLE/ROLE,
	// so the binding-shape arm cannot be what refuses it.
	t.Run("or-default on a raw read", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/lib/slack.mjs": `export const room = process.env.BRIDGE_DEMO_ROOM || 'general'
`,
		})
		out, ok := f.run(t)
		if ok {
			t.Fatalf("expected refusal of an inline fallback on a raw read:\n%s", out)
		}
		if !strings.Contains(out, "BRIDGE_DEMO_ROOM") {
			t.Errorf("refusal must show the offending line:\n%s", out)
		}
		if !strings.Contains(out, "fallback on a raw process.env read") {
			t.Errorf("the inline arm must be what refused this, not the shape arm:\n%s", out)
		}
	})

	t.Run("nullish default on a raw read", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/lib/slack.mjs": `export const room = process.env.BRIDGE_DEMO_ROOM ?? 'general'
`,
		})
		out, ok := f.run(t)
		if ok {
			t.Fatalf("expected refusal of an inline nullish fallback:\n%s", out)
		}
	})

	// The one sanctioned exception, exempted by its DEFINITION TEXT rather
	// than by filename, so a second default hidden elsewhere in that same
	// file is still refused. It cannot itself hide a default: its fallback is
	// its own second argument, supplied by the caller the gate is reading.
	//
	// The exemption is inert against the helper as written today --
	// contrib/openclaw-bridge/lib/gc-client.mjs:12 spells the fallback with
	// !== tests rather than ?? and matches nothing. It is carried because the
	// obvious simplification of that line, `process.env[k] ?? d`, does match,
	// and a gate that refuses the one place defaults are allowed to live gets
	// deleted rather than obeyed.
	t.Run("the env helper's own definition is accepted", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/lib/gc-client.mjs": `export const post = (m) =>
  fetch('/extmsg/inbound', { method: 'POST', body: JSON.stringify(m) })
export const env = (k, d) => process.env[k] ?? d
`,
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("the one sanctioned default helper must pass:\n%s", out)
		}
	})

	// Scope boundary, derived from the EXTENSION and not from a list of
	// exempt filenames. The extensionless stand-ins under the component --
	// fake-imsg/imsg and fake-telegram/bot-api in the real tree -- are
	// separate programs spawned by a demo launcher. They load node builtins
	// alone and read only their own FAKE_* knobs, so they can bind neither a
	// session nor a channel, and the fix for refusing them would be to import
	// a bridge module into a stand-in, which is worse than the thing refused.
	// A stand-in rewritten as a .mjs and imported is in scope the moment it
	// is. Measured 2026-09-07: those two files are the only inline-fallback
	// hits in the real scan set.
	t.Run("an extensionless stand-in keeps its own default", func(t *testing.T) {
		f := newBridgeGateFixture(t, map[string]string{
			"contrib/demo-bridge/fake-chat/chat-api": `#!/usr/bin/env node
const PORT = Number(process.env.FAKE_CHAT_PORT || 8932)
console.log(PORT)
`,
		})
		out, ok := f.run(t)
		if !ok {
			t.Fatalf("a stand-in outside the module graph keeps its own default:\n%s", out)
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
