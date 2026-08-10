package materialize

// internal/materialize/skills_sinkignore.go
//
// The comment block sits below the package clause on purpose: skills.go
// holds this package's doc comment, and a second block above a `package`
// line would become a competing one.
//
// Keeps a materialization pass from showing up as working-tree changes in
// the rig repository it writes into.
//
// The sink is a vendor-mandated path inside the repo (.claude/skills and
// friends -- see vendorSinks), so every symlink and bookkeeping file this
// package writes lands somewhere git reports. That is not cosmetic: agents
// arriving in a rig tree are instructed to stop and escalate when the tree
// is dirty, on the reasoning that uncommitted work belongs to someone
// mid-thought. With the sink always dirty that condition holds on every
// arrival, so the check either fires constantly on artifacts nobody wrote
// or gets learned as noise and stops protecting the case it exists for
// (ci-x4mv, where a real abandoned change had to be told apart from this
// noise by hand).
//
// The fix is a .gitignore inside the sink, listing exactly the entries gc
// wrote. Two properties make it safe, and both are load-bearing:
//
//   - It ENUMERATES names instead of globbing '*'. A blanket ignore would
//     also silence project skills committed to the sink on purpose, which
//     is how a repo quietly stops tracking real content later.
//   - It is REWRITTEN each pass, never appended. A stale entry for a name
//     gc no longer materializes would keep ignoring that path, so a
//     project skill added at the freed name would never appear in git
//     status.
//
// Rejected alternative: appending to the repo-root .gitignore via
// cmd/gc's ensureGitignoreEntries. That file is tracked, so writing to it
// dirties the very tree this code exists to keep clean, and the helper is
// append-only, so it accumulates exactly the stale entries described
// above.
//
// Rejected alternative: .git/info/exclude. Right semantics -- machine-local
// rules that are never committed -- but git shares info/exclude across all
// linked worktrees while each per-session worktree has its own sink at its
// own repo-relative path, so one file would have to accumulate and prune
// patterns for every worktree that ever existed. The sink-local file is
// per-sink by construction.
//
// Invariant: every path this package creates under a sink is covered by
// sinkIgnoreLines. Verified by
// TestSinkIgnoreCoversItsOwnBookkeepingFiles in
// skills_sinkignore_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/fsys"
)

// sinkIgnoreFile is the name of the ignore file written inside each sink.
const sinkIgnoreFile = ".gitignore"

// sinkIgnoreHeader is both the first line of the generated file and the
// ownership sentinel. A .gitignore in a sink that does not start with this
// line was authored by someone else and is left untouched -- the same
// leave-user-content-alone rule the cleanup walk applies to non-symlink
// sink entries.
const sinkIgnoreHeader = "# Gas City skill materialization -- generated, do not edit."

// sinkIgnorePreamble explains the file to whoever finds it in a diff or a
// git status they did not expect. Kept in the file rather than only here
// because the reader who needs it is standing in a rig repo, not in this
// package.
const sinkIgnorePreamble = `#
# Every path below is something gc wrote into this directory: a symlink to
# a machine-local pack-cache path, or gc's own bookkeeping. None of it is
# committable -- the targets resolve on one machine, and move whenever a
# pack's pinned commit changes.
#
# Names are listed individually rather than ignored with '*' so a project
# skill added here by hand still shows up in git status. The list is
# rewritten on every pass, so a name gc stops materializing stops being
# ignored. To take this file over, delete the header line above: gc does
# not touch a .gitignore it did not write.
`

// sinkIgnoreBookkeeping are the non-skill-name paths gc writes into a
// sink. The tmp entry is a glob because atomicSymlink's temp names carry a
// random suffix. Those are renamed within the same call, so one only
// survives an interrupted pass -- and a stranded one would otherwise read
// as a hand-placed file forever.
var sinkIgnoreBookkeeping = []string{
	"/" + sinkIgnoreFile,
	"/" + ownershipManifestFile,
	"/.*.tmp.*",
}

// escapeGitignorePattern renders name as a literal path segment.
//
// git reads \*?[ in a .gitignore line as glob syntax, so an unescaped
// name containing one would ignore paths gc never wrote while failing to
// cover the entry it was emitted for -- silencing content gc does not own,
// which is the failure this whole file exists to avoid. Trailing spaces
// are escaped for the same reason: git strips unescaped ones.
//
// ']' needs no escape (it is literal outside a bracket expression), and a
// leading '!' or '#' cannot be misread because every emitted line is
// prefixed with '/'.
func escapeGitignorePattern(name string) string {
	trailing := len(name) - len(strings.TrimRight(name, " "))
	var b strings.Builder
	b.Grow(len(name) + 8)
	for _, r := range name[:len(name)-trailing] {
		switch r {
		case '\\', '*', '?', '[':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat("\\ ", trailing))
	return b.String()
}

// sinkIgnoreLines renders the full file body for the given materialized
// names. Names arrive sorted from Run, and the bookkeeping entries lead,
// so the output is byte-stable across passes and produces no diff churn.
func sinkIgnoreLines(names []string) string {
	var b strings.Builder
	b.WriteString(sinkIgnoreHeader)
	b.WriteByte('\n')
	b.WriteString(sinkIgnorePreamble)
	for _, entry := range sinkIgnoreBookkeeping {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	for _, name := range names {
		b.WriteString("/")
		b.WriteString(escapeGitignorePattern(name))
		b.WriteByte('\n')
	}
	return b.String()
}

// writeSinkIgnore brings the sink's ignore file in line with names, the
// set of entries this pass materialized.
//
// Skipped names are deliberately absent from names: user content occupying
// a desired sink path is not gc's to hide. Returns a warning string (not
// an error) when an unrecognized .gitignore blocks the write, matching how
// Run reports every other per-entry condition -- a rig whose sink a
// developer has claimed still materializes correctly, it just keeps
// reporting the sink as dirty.
func writeSinkIgnore(absSink string, names []string) string {
	path := filepath.Join(absSink, sinkIgnoreFile)
	// A read error other than not-exist counts as present-but-unreadable,
	// which classifies as not-ours: the safe direction, since the
	// alternative is overwriting a file we could not inspect.
	current, err := os.ReadFile(path)
	exists := err == nil || !os.IsNotExist(err)
	if exists && !strings.HasPrefix(string(current), sinkIgnoreHeader) {
		return fmt.Sprintf("leaving unmanaged %s in sink %q alone: materialized skill links will keep showing as untracked changes; delete it to let gc manage the ignore list", sinkIgnoreFile, absSink)
	}
	// Nothing written and nothing to correct -- do not create the file
	// just to say gc owns an empty sink.
	if len(names) == 0 && !exists {
		return ""
	}
	body := []byte(sinkIgnoreLines(names))
	if exists && string(current) == string(body) {
		return ""
	}
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, body, 0o644); err != nil {
		return fmt.Sprintf("writing %s in sink %q: %v", sinkIgnoreFile, absSink, err)
	}
	return ""
}
