// Test scope: scripts/check-extmsg-bridge-isolation.sh, the gate holding
// epic:mayor-slack-bridge to acceptance criteria 5 (ZERO hardcoded roles) and
// 8 (the existing alerts seam is untouched).
//
// The suite exists because a gate is not evidence until it has been seen to
// fail. Every refusal in the script is driven here against a mutated tree that
// contains exactly the violation it is written to catch, so a refusal that
// stops firing -- a regexp narrowed, a file set that quietly excludes the file
// the violation lands in -- fails the build instead of reporting OK forever.
// It also fixes the gate INTO the normal run: `make test` sweeps ./... and
// this is the only thing that executes the script.
//
// Cases come in accept/refuse PAIRS wherever the gate draws a scope line,
// because a line is only pinned when both sides of it are. Asserting a file
// count instead would pass over a frozen list. The four pairs:
//
//   - derived, not hand-kept: a role name in a module nothing imports must
//     NOT fail; the same name in a module the bridge imports must.
//   - reachability: a role name in a shell launcher and in an extensionless
//     stand-in must fail, since both are spawned rather than imported and an
//     import-only closure would never see them.
//   - the wide/narrow scope split: an inline env default in a stand-in must
//     NOT fail (it imports no bridge module, so the env() remedy is not
//     available to it), while the same shape in a .mjs must.
//   - id shape vs prose: a conversation-id literal in code must fail, the
//     same id as a README example must not, and a capitalized word shaped
//     like an id (CONVERSATION) must not.
//
// Import syntax is covered in every form ESM offers -- single- and
// double-quoted `from`, and the bare side-effect `import` -- because nothing
// in the bridge package lints quote style, so a specifier the walk cannot
// parse would drop its whole subtree, and any violation in it, out of scan.
//
// It delegates the byte-identity of the city's own notify.sh to the city
// repository, which is where that file lives; see the header of the script.
//
// Run: go test ./scripts -run TestExtmsgBridgeIsolation -count=1
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bridgeGateScript is the gate under test, resolved from the test's own
// directory so the suite runs from anywhere `go test` is invoked.
func bridgeGateScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("check-extmsg-bridge-isolation.sh")
	if err != nil {
		t.Fatalf("resolving the gate script: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("gate script missing: %v", err)
	}
	return path
}

// runCommand is this suite's ONE process-spawn site. Both the gate (bash) and
// the fixture's git plumbing route through it deliberately: the untagged
// subprocess ratchet in internal/testpolicy/resourcecensus counts CALL SITES
// rather than invocations, so two helpers that each spawn charge the ledger
// twice for no extra coverage. Returns (combined output, error).
func runCommand(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runBridgeGate runs the gate over root and reports whether it passed, with
// the combined output for the failure message.
func runBridgeGate(t *testing.T, root string) (bool, string) {
	t.Helper()
	out, err := runCommand(t, "", "bash", bridgeGateScript(t), root)
	return err == nil, out
}

// bridgeFixtureFiles is a miniature of the real tree: the Makefile recipe the
// gate derives the package path from, a bridge package shaped like
// contrib/openclaw-bridge (entrypoint, imported lib, node --test file, README)
// and three pack-declared agent directories, which are what the role
// vocabulary is derived from.
//
// It is deliberately NOT a copy of the real tree. A copy would make every
// mutation case depend on the real bridge staying clean, so one unrelated
// violation would turn the whole suite green-by-accident in the other
// direction: cases would fail for a reason that is not the mutation. The real
// tree is exercised once, by TestExtmsgBridgeIsolation/repository_passes.
var bridgeFixtureFiles = map[string]string{
	"Makefile": "test-openclaw-bridge:\n" +
		"\tcd contrib/openclaw-bridge && npm ci --no-audit --no-fund && npm test\n",
	"contrib/openclaw-bridge/package.json": "{\n  \"type\": \"module\"\n}\n",
	"contrib/openclaw-bridge/slack-bridge.mjs": "#!/usr/bin/env node\n" +
		"import { env } from './lib/gc-client.mjs'\n" +
		"const required = (name) => process.env[name]\n" +
		"const TARGET = required('SLACK_TARGET_AGENT')\n" +
		"const BASE = env('GC_BASE_URL', 'http://127.0.0.1:8372')\n" +
		"console.log(TARGET, BASE)\n",
	"contrib/openclaw-bridge/lib/gc-client.mjs": "" +
		"export const env = (k, d) => (process.env[k] !== undefined ? process.env[k] : d)\n",
	"contrib/openclaw-bridge/test/slack.test.mjs": "" +
		"import { env } from '../lib/gc-client.mjs'\n" +
		"console.log(env('GC_SCOPE_ID', 'lab'))\n",
	"contrib/openclaw-bridge/README.md": "# bridge\n\nSLACK_TARGET_AGENT names the session.\n",
	// A launcher: shebang-rooted, imported by nothing, and it names a session
	// identity without going through required(). This is the file a
	// convenience default lands in first.
	"contrib/openclaw-bridge/demo.sh": "#!/usr/bin/env bash\n" +
		"SLACK_TARGET_AGENT=\"lab/assistant\" exec node ./slack-bridge.mjs\n",
	// An extensionless channel stand-in, carrying the inline env default the
	// real fake-imsg/imsg has. Its presence in the UNMUTATED fixture is the
	// assertion that the narrow scope excludes stand-ins from refusal 3 --
	// paired with the "inline fallback on a raw env read" case below, which
	// refuses the identical shape inside a .mjs.
	"contrib/openclaw-bridge/fake-imsg/imsg": "#!/usr/bin/env node\n" +
		"const dir = process.env.FAKE_IMSG_DIR || '/tmp/fake-imsg'\n" +
		"console.log(dir)\n",
	"examples/pack/agents/mayor/agent.toml":   "prompt = \"x\"\n",
	"examples/pack/agents/deacon/agent.toml":  "prompt = \"x\"\n",
	"examples/pack/agents/polecat/agent.toml": "prompt = \"x\"\n",
}

// newBridgeFixture writes the miniature tree and puts it under git, because
// the gate reads its file set with `git ls-files` -- an untracked scratch file
// must not be able to fail the gate, and the fixture has to honor that.
func newBridgeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range bridgeFixtureFiles {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(name), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	gitAddAll(t, root)
	return root
}

// gitAddAll (re)initializes the fixture's index so `git ls-files` sees every
// file a mutation added. Nothing is committed: the index alone is what
// `git ls-files` reads, and committing would need an identity this suite
// should not depend on.
func gitAddAll(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		run(t, root, "git", "init", "-q")
	}
	run(t, root, "git", "add", "-A")
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	if out, err := runCommand(t, dir, name, args...); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func writeFixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func appendFixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	existing, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	writeFixtureFile(t, root, name, string(existing)+body)
}

// TestExtmsgBridgeIsolation pins that the bridge gate refuses each violation
// of acceptance criteria 5 and 8, and accepts a tree carrying none of them.
//
// Each mutation is the accepting counterpart of one refusal: it is applied to
// a fixture that has just been shown to pass, so a case that goes green
// because the fixture was already failing is not possible. mustFail says
// which way the case must land, and the two mustFail:false cases are load
// bearing -- they pin the gate's scope claims (a module nothing imports is out
// of the bridge path, prose is out of the role scan) that would otherwise be
// only a comment.
func TestExtmsgBridgeIsolation(t *testing.T) {
	t.Run("repository passes", func(t *testing.T) {
		root, err := filepath.Abs("..")
		if err != nil {
			t.Fatalf("resolving the repository root: %v", err)
		}
		if ok, out := runBridgeGate(t, root); !ok {
			t.Fatalf("the gate refuses this repository:\n%s", out)
		}
	})

	t.Run("fixture passes before mutation", func(t *testing.T) {
		if ok, out := runBridgeGate(t, newBridgeFixture(t)); !ok {
			t.Fatalf("the unmutated fixture must pass, else every mutation case "+
				"below is vacuous:\n%s", out)
		}
	})

	cases := []struct {
		name     string
		mutate   func(t *testing.T, root string)
		mustFail bool
		wants    string
	}{{
		name: "role name in an imported module",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/lib/gc-client.mjs",
				"export const fallbackAgent = 'mayor'\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "role name in a module nothing imports",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "contrib/openclaw-bridge/lib/orphan.mjs",
				"export const fallbackAgent = 'mayor'\n")
		},
		mustFail: false,
	}, {
		name: "role name in a module the bridge starts importing",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "contrib/openclaw-bridge/lib/adopted.mjs",
				"export const fallbackAgent = 'mayor'\n")
			appendFixtureFile(t, root, "contrib/openclaw-bridge/slack-bridge.mjs",
				"import { fallbackAgent } from './lib/adopted.mjs'\nconsole.log(fallbackAgent)\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "role name in prose",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/README.md",
				"\nSet SLACK_TARGET_AGENT=lab/mayor to bind that session.\n")
		},
		mustFail: false,
	}, {
		name: "role vocabulary loses a name AGENTS.md requires",
		mutate: func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "examples/pack/agents/mayor")); err != nil {
				t.Fatalf("removing the pack agent directory: %v", err)
			}
		},
		mustFail: true,
		wants:    "role vocabulary derived from the in-tree packs lost",
	}, {
		name: "required knob acquires a default",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/lib/gc-client.mjs",
				"export const target = env('SLACK_TARGET_AGENT', 'lab/lead')\n")
		},
		mustFail: true,
		wants:    "read with required() in one place",
	}, {
		name: "inline fallback on a raw env read",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/lib/gc-client.mjs",
				"export const target = process.env.SLACK_TARGET_AGENT || 'lab/lead'\n")
		},
		mustFail: true,
		wants:    "inline fallback on a raw process.env read",
	}, {
		name: "alerts webhook read by the bridge",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/lib/gc-client.mjs",
				"export const alerts = process.env.SLACK_WEBHOOK_URL\n")
		},
		mustFail: true,
		wants:    "alerts-seam reference in the bridge",
	}, {
		name: "alerts webhook offered in the bridge README",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/README.md",
				"\nOr reuse SLACK_WEBHOOK_URL from the alert seam.\n")
		},
		mustFail: true,
		wants:    "alerts-seam reference in the bridge",
	}, {
		name: "alert seam copied into this repository",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "assets/scripts/notify.sh", "#!/bin/sh\nexit 0\n")
		},
		mustFail: true,
		wants:    "alert seam appears in this repository",
	}, {
		name: "role default in a shell launcher",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/demo.sh",
				"SLACK_TARGET_AGENT=\"lab/mayor\"\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "role name in an extensionless stand-in",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/fake-imsg/imsg",
				"const fallbackAgent = 'polecat'\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "role name behind a double-quoted import",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "contrib/openclaw-bridge/lib/dq.mjs",
				"export const fallbackAgent = 'mayor'\n")
			appendFixtureFile(t, root, "contrib/openclaw-bridge/slack-bridge.mjs",
				"import { fallbackAgent } from \"./lib/dq.mjs\"\nconsole.log(fallbackAgent)\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "role name behind a side-effect import",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "contrib/openclaw-bridge/lib/side.mjs",
				"globalThis.fallbackAgent = 'deacon'\n")
			appendFixtureFile(t, root, "contrib/openclaw-bridge/slack-bridge.mjs",
				"import './lib/side.mjs'\n")
		},
		mustFail: true,
		wants:    "role name in the bridge source",
	}, {
		name: "conversation id literal in a module",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/lib/gc-client.mjs",
				"export const pinned = 'C09XY8ZK4QT'\n")
		},
		mustFail: true,
		wants:    "conversation id literal",
	}, {
		name: "conversation id as a README example",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/README.md",
				"\nSet SLACK_CHANNEL_ID=C09XY8ZK4QT to name the channel.\n")
		},
		mustFail: false,
	}, {
		name: "capitalized word shaped like a conversation id",
		mutate: func(t *testing.T, root string) {
			appendFixtureFile(t, root, "contrib/openclaw-bridge/demo.sh",
				"echo 'CHILD CONVERSATION: a forum topic'\n")
		},
		mustFail: false,
	}, {
		name: "role vocabulary derives to nothing",
		mutate: func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "examples/pack/agents")); err != nil {
				t.Fatalf("removing the pack agent directories: %v", err)
			}
		},
		mustFail: true,
		wants:    "no role names derived",
	}, {
		name: "build no longer defines the bridge package",
		mutate: func(t *testing.T, root string) {
			writeFixtureFile(t, root, "Makefile", "test:\n\techo hi\n")
		},
		mustFail: true,
		wants:    "cannot derive the bridge package",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newBridgeFixture(t)
			tc.mutate(t, root)
			gitAddAll(t, root)
			ok, out := runBridgeGate(t, root)
			switch {
			case tc.mustFail && ok:
				t.Fatalf("the gate accepted this violation; the refusal is dead:\n%s", out)
			case tc.mustFail && !strings.Contains(out, tc.wants):
				t.Fatalf("the gate refused, but not for this reason -- wanted %q in:\n%s",
					tc.wants, out)
			case !tc.mustFail && !ok:
				t.Fatalf("the gate refused something it must accept:\n%s", out)
			}
		})
	}
}
