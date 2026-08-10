package bdflags

import "sort"

// SourcedBeadsVersion is the beads module version bdTopLevelCommands and
// bdCommandsNotInModuleSource were read from. It must equal the version go.mod
// requires -- TestSourcedBeadsVersionMatchesGoMod fails the build when a bump
// moves one without the other, which is the mechanical prompt that turns a
// dependency bump into a review of the name list.
const SourcedBeadsVersion = "v1.1.1-0.20260805093327-bf97b73749ac"

// bdTopLevelCommands maps each name bd registers on its root command to that
// command's published aliases. It exists so gc can tell, without a bd binary,
// whether a verb `gc bd` handles itself is a name bd has already claimed.
//
// The failure it guards is not a design error but a name collision introduced
// by a dependency bump. gc began intercepting "heartbeat" on 2026-06-01, when
// beads v1.0.5 had no such subcommand and the name was genuinely free. The
// 2026-08-05 bump to v1.1.1-0.20260805093327 added claim leases and with them
// `bd heartbeat`, which gc's intercept then shadowed for two months with the
// whole suite green -- every test asserted the intercept did what its author
// intended, and none asked whether bd had since claimed the same name
// (ci-ctkz, ci-mosn).
//
// TOP-LEVEL names only. A nested name like "pour" under `bd mol` is absent on
// purpose: every gc intercept dispatches on argv[0], so only a root-level name
// can be shadowed, and admitting nested names would fail a gc verb that
// collides with nothing reachable.
//
// HIDDEN commands are included. A hidden command is absent from `bd --help`
// but still resolves when invoked, so it shadows exactly as silently as a
// visible one -- and being invisible in help is precisely what would keep a
// hand-audit from noticing.
//
// Provenance is not a transcription: TestBdTopLevelCommandsMatchModuleSource
// re-derives this map from the beads module source at SourcedBeadsVersion on
// every run, so an entry that drifts fails the build rather than aging out.
var bdTopLevelCommands = map[string][]string{
	"admin":             nil,
	"ado":               nil,
	"assign":            nil,
	"audit":             nil,
	"backup":            nil,
	"batch":             nil,
	"blocked":           nil,
	"bootstrap":         nil,
	"branch":            nil,
	"children":          nil,
	"close":             {"done"},
	"codex-hook":        nil,
	"comment":           nil,
	"comments":          nil,
	"compact":           nil,
	"config":            nil,
	"conflicts":         nil,
	"context":           nil,
	"cook":              nil,
	"count":             nil,
	"create":            {"new"},
	"create-form":       nil,
	"cursor-hook":       nil,
	"db-proxy-child":    nil,
	"defer":             nil,
	"delete":            nil,
	"dep":               nil,
	"diff":              nil,
	"doctor":            nil,
	"dolt":              nil,
	"duplicate":         nil,
	"duplicates":        nil,
	"edit":              nil,
	"epic":              nil,
	"export":            nil,
	"federation":        nil,
	"find-duplicates":   {"find-dups"},
	"flatten":           nil,
	"forget":            nil,
	"formula":           nil,
	"gate":              nil,
	"gc":                nil,
	"github":            nil,
	"gitlab":            nil,
	"graph":             nil,
	"heartbeat":         {"hb"},
	"history":           nil,
	"hooks":             nil,
	"human":             nil,
	"import":            nil,
	"info":              nil,
	"init":              nil,
	"init-safety":       nil,
	"jira":              nil,
	"kv":                nil,
	"label":             nil,
	"linear":            nil,
	"link":              nil,
	"lint":              nil,
	"list":              nil,
	"mail":              nil,
	"memories":          nil,
	"merge-slot":        nil,
	"metrics":           nil,
	"migrate":           nil,
	"migrate-personal":  nil,
	"mol":               {"protomolecule"},
	"note":              nil,
	"notion":            nil,
	"onboard":           nil,
	"orphans":           nil,
	"ping":              nil,
	"preflight":         nil,
	"prime":             nil,
	"priority":          nil,
	"promote":           nil,
	"prune":             nil,
	"purge":             nil,
	"q":                 nil,
	"query":             nil,
	"quickstart":        nil,
	"ready":             nil,
	"recall":            nil,
	"reclaim":           nil,
	"recompute-blocked": nil,
	"remember":          nil,
	"rename":            nil,
	"rename-prefix":     nil,
	"reopen":            nil,
	"repo":              nil,
	"restore":           nil,
	"rules":             nil,
	"schema":            nil,
	"search":            nil,
	"set-state":         nil,
	"setup":             nil,
	"ship":              nil,
	"show":              {"view"},
	"sql":               nil,
	"stale":             nil,
	"state":             nil,
	"status":            {"stats"},
	"statuses":          nil,
	"supersede":         nil,
	"swarm":             nil,
	"sync":              nil,
	"tag":               nil,
	"todo":              nil,
	"types":             nil,
	"unclaim":           nil,
	"undefer":           nil,
	"update":            nil,
	"upgrade":           nil,
	"vc":                nil,
	"version":           nil,
	"where":             nil,
	"worktree":          nil,
}

// handCarriedCommand is one top-level bd name the source derivation cannot
// produce, together with the registration that defeats it.
type handCarriedCommand struct {
	// aliases is nil for every entry today: none of the four commands below
	// has any. The field is kept rather than dropped because CommandNames
	// would otherwise have no way to report the aliases of a hand-carried
	// command, and a name missing from that set is a name the collision gate
	// reads as free.
	aliases []string

	// registration is the identifier bd passes to rootCmd.AddCommand whose
	// name the derivation cannot read, or "" for a name that has no
	// registration in bd's source at all because cobra contributes it.
	//
	// It is what links a hand-written name back to the thing it stands for.
	// Without it the derivation can only report "some registration is
	// unreadable" and cannot tell an already-accounted-for one from a new
	// command it has just gone blind to -- and treating the two alike means
	// either failing forever on the known ones or passing silently on the new.
	registration string
}

// bdCommandsNotInModuleSource holds top-level bd names that resolve at runtime
// but cannot be read out of a `rootCmd.AddCommand(<var>)` registration, so the
// source derivation cannot produce them and they are carried by hand.
//
// Both kinds below are pinned by TestBdCommandsNotInModuleSourceStayNecessary:
// an entry whose registration became readable belongs in bdTopLevelCommands,
// and an entry whose registration is gone belongs nowhere. The derivation
// itself fails on any unresolvable registration NOT named here, so a third
// kind appearing is a build failure rather than a silent gap in the name list.
//
// Resolving these two properly -- following metrics.SendMetricsSubcommand into
// another package, and tracking a `Use` assigned by statement onto a struct
// copy -- was rejected. Each is one more beads-internal registration idiom the
// derivation would have to keep matching, and an idiom it matches WRONGLY
// yields a confident wrong name, which is worse here than a name the
// derivation admits it cannot read. Neither name is one gc would ever claim.
var bdCommandsNotInModuleSource = map[string]handCarriedCommand{
	// Use is the constant metrics.SendMetricsSubcommand, not a string
	// literal (beads cmd/bd/send_metrics.go).
	"send-metrics": {registration: "sendMetricsCmd"},

	// A hidden back-compat alias registered as a mutated copy of
	// migrateIssuesCmd, whose Use is assigned by statement after the copy
	// (beads cmd/bd/migrate_issues.go). The copy carries no aliases because
	// the command it is copied from has none.
	"migrate-issues": {registration: "migrateIssuesAliasCmd"},

	// cobra adds both to every root command, so neither appears in bd's
	// source at all -- no registration to name. Their absence from a
	// source-only derivation is why this map is not just the two beads
	// oddities above: `bd help` and `bd completion` both resolve, and a gc
	// verb by either name would shadow them exactly as silently.
	"help":       {},
	"completion": {},
}

// CommandNames returns every top-level name bd answers to -- canonical names
// and aliases alike, sorted. A name in this set is one gc cannot claim for a
// verb of its own without shadowing bd.
func CommandNames() []string {
	names := make([]string, 0, len(bdTopLevelCommands)+len(bdCommandsNotInModuleSource))
	forEachBdCommand(func(canonical string, aliases []string) {
		names = append(names, canonical)
		names = append(names, aliases...)
	})
	sort.Strings(names)
	return names
}

// AliasGroup returns every spelling of the bd command reachable as name --
// its canonical name first, then its aliases -- or nil if bd has no top-level
// command by that name.
//
// A caller intercepting one spelling of a bd command must intercept the whole
// group. gc once intercepted "heartbeat" and not its alias "hb", so the two
// spellings of one bd command reached different code and only one of them
// carried gc's extra write (ci-ctkz).
func AliasGroup(name string) []string {
	// Canonical names are resolved first, and by map lookup rather than by
	// scanning. Not just for speed on the dispatch path: were one command's
	// alias also another command's canonical name, a single scan could match
	// both and return the two groups spliced together, and gc's dispatch reads
	// this to decide which spellings an intercept claims. bd has no such
	// overlap today; deciding it canonically here means one arriving in a
	// later bump changes nothing.
	if aliases, ok := bdTopLevelCommands[name]; ok {
		return append([]string{name}, aliases...)
	}
	if cmd, ok := bdCommandsNotInModuleSource[name]; ok {
		return append([]string{name}, cmd.aliases...)
	}
	var found []string
	forEachBdCommand(func(canonical string, aliases []string) {
		if found != nil {
			return
		}
		for _, a := range aliases {
			if a == name {
				found = append([]string{canonical}, aliases...)
				return
			}
		}
	})
	return found
}

func forEachBdCommand(visit func(canonical string, aliases []string)) {
	for canonical, aliases := range bdTopLevelCommands {
		visit(canonical, aliases)
	}
	for canonical, cmd := range bdCommandsNotInModuleSource {
		visit(canonical, cmd.aliases)
	}
}
