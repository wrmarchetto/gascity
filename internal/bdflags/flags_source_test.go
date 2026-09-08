// Freshness gate for the per-subcommand flag manifests in bdflags.go.
//
// The manifests decide, for a bd argv gc is about to forward, which tokens are
// flag values and which are bead ids. Their consumers fail closed: a flag in
// neither the value set nor the no-value set makes cmd/gc/cmd_bd.go refuse the
// write before bd runs. So an omission does not degrade a lint check, it
// refuses a legitimate command -- gs-9zu, where every `gc bd update
// --if-assignee` was rejected in cities configuring a [beads]
// pre_write_command, taking bd's compare-and-swap guard out of reach exactly
// where guarded writes were the point.
//
// This suite re-derives each manifest from the bd source at the version go.mod
// pins and fails on any flag the manifest is MISSING. Nothing shells out and
// nothing skips.
//
// Why the source and not `bd <verb> --help`, which freshness_test.go already
// reads: the help transcript is the wrong oracle twice over. It cannot see a
// MarkHidden'd flag, and bd hides four value-consuming aliases on close alone
// (--resolution, --comment, -m/--message), each of which was missing from the
// manifest and each of which bd's parser accepts like any other. And a help
// read needs an installed binary, which is what put the existing check behind
// the integration tag and on a skip -- so it went green on every ordinary run
// while the manifest was missing flags on all 17 known subcommands. The bd
// source at the pinned version is present unconditionally (see the blank
// import in commands_source_test.go) and is what gc actually ships against.
//
// freshness_test.go is retained rather than replaced: it checks the manifest
// against the bd binary on PATH, which can be NEWER than go.mod's pin, and
// that skew is a real thing to know about. This gate is the one that must be
// green.
//
// What this gate CANNOT see, so the --help check above is not redundant: a
// registration shape it does not read at all. It follows a receiver that is a
// command identifier or a function's *cobra.Command parameter, and a flag
// registered through anything else -- a method, a struct field, a slice of
// commands -- is attributed to no command and silently contributes nothing,
// which a one-directional gate calls clean.
// TestBdFlagDerivationSeesEachRegistrationShape pins the shapes bd uses today;
// a NEW one is caught by the installed-binary check in freshness_test.go,
// which reads bd's parser rather than its source and so is blind to none of
// them.
//
// Delegated elsewhere: the top-level command-NAME list
// (commands_source_test.go), and the consumer behavior a complete manifest
// produces (cmd/gc/bd_mutation_flag_manifest_test.go).
//
//	go test ./internal/bdflags/
package bdflags

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pflagTypeTakesNoValue maps a pflag registration's TYPE name -- what is left
// of the method name after the Var/P suffixes come off -- to whether a bare
// occurrence of the flag consumes the following argv token.
//
// Only Bool does not. BoolSlice does, which is why this is a table on the
// whole type name and not a prefix match on "Bool": `HasPrefix(m, "Bool")`
// would file --some-bool-slice as no-value and the scanner would then read its
// value as a bead id.
//
// The empty key is Var/VarP, a custom pflag.Value. pflag consumes a value for
// one unless the registrar also sets NoOptDefVal, which is picked up
// separately below, so value-consuming is the correct default rather than a
// guess. bd registers exactly one on a known subcommand: close's --reason,
// whose Type() is "string".
//
// Absences are deliberate. Count is listed because pflag gives it
// NoOptDefVal="+1" and it therefore takes no value -- bd has none today, and
// the entry exists so adding one lands on a decided answer. Every OTHER pflag
// type is absent, and an absent type FAILS this gate rather than defaulting:
// a new registration shape must be classified by a human, because both wrong
// answers corrupt the id scan rather than widening it.
var pflagTypeTakesNoValue = map[string]bool{
	"":               false, // Var / VarP -- custom pflag.Value
	"Bool":           true,
	"BoolSlice":      false,
	"Count":          true, // pflag sets NoOptDefVal="+1"
	"Duration":       false,
	"Float32":        false,
	"Float64":        false,
	"Int":            false,
	"Int32":          false,
	"Int64":          false,
	"IntSlice":       false,
	"String":         false,
	"StringArray":    false,
	"StringSlice":    false,
	"StringToString": false,
	"Uint":           false,
	"Uint32":         false,
	"Uint64":         false,
}

// derivedFlag is one flag registration recovered from bd's source.
type derivedFlag struct {
	long      string // "--if-assignee"
	short     string // "-a", empty when the registration declares no shorthand
	takesNone bool   // consumes no following argv token
	where     string // "update.go:822", for a failure a reader can go look at

	// persistent records registration on PersistentFlags() rather than
	// Flags(). Only a persistent flag on the root command is inherited by
	// subcommands: bd registers -V/--version on rootCmd.Flags(), and bd
	// rejects it on `bd update`, so treating every root registration as
	// global would put a flag in the global set that no subcommand accepts.
	persistent bool

	// offset is the registration call's position, used only to confine a
	// registrar helper's registrations to that helper's own body.
	offset token.Pos
}

// flagRegistration is a registration call before its receiver is resolved to a
// cobra command. Splitting resolution out is what lets one pass attribute both
// the direct `updateCmd.Flags().String(...)` form and a registrar helper's
// `cmd.Flags().VarP(...)`, where the receiver is a function parameter.
type flagRegistration struct {
	recv string // receiver identifier as written
	flag derivedFlag
}

// TestBdSubcommandFlagManifestsMatchModuleSource fails when bd's source
// registers a flag the manifest does not carry for that subcommand.
//
// One-directional on purpose, matching the manifest's stated contract: it is
// the newest-known SUPERSET of bd's flags, and both consumers stay correct
// when it is ahead of bd. Only a MISSING flag misbehaves, and it misbehaves by
// refusing a valid write.
func TestBdSubcommandFlagManifestsMatchModuleSource(t *testing.T) {
	derived := deriveBdSubcommandFlags(t)

	for _, sub := range Subcommands() {
		t.Run(sub, func(t *testing.T) {
			flags, ok := derived[sub]
			if !ok {
				t.Fatalf("no bd command resolves to %q, so this manifest entry is checked against nothing.\n"+
					"Either bd renamed or dropped the subcommand -- remove the entry -- or the derivation has gone blind to its registration shape.", sub)
			}
			valueSet := ValueFlags(sub)
			boolSet := BoolFlags(sub)

			var missing, misfiled []string
			for _, f := range flags {
				for _, name := range []string{f.long, f.short} {
					if name == "" {
						continue
					}
					known := valueSet[name] || boolSet[name]
					if !known {
						missing = append(missing, fmt.Sprintf("%s (%s, registered %s)", name, takesValueLabel(f.takesNone), f.where))
						continue
					}
					// Membership of BOTH sets is a defect whatever the flag's
					// real arity, because the two consumers break the tie in
					// OPPOSITE directions: bdMutationWriteIDs tests the value
					// set first and consumes the following token,
					// ScanUnknownFlags tests the boolean set first and does
					// not. One argv then scans two ways. Checked before the
					// arity comparisons below rather than folded into them,
					// because each of those requires absence from the other
					// set -- so a flag in both satisfied neither and read as
					// clean until a mutation sweep flipped it (gs-9zu).
					if valueSet[name] && boolSet[name] {
						misfiled = append(misfiled, fmt.Sprintf("%s is in BOTH valueFlagsBySub[%q] and boolFlagsBySub[%q]; cmd_bd would consume its value and gc lint would not (registered %s)", name, sub, sub, f.where))
						continue
					}
					// A flag in the WRONG set is not a lesser problem than an
					// absent one. Filed as no-value when it consumes a token,
					// the scanner reads its value as a bead id; filed as
					// value-consuming when it does not, the scanner swallows
					// the id that follows it.
					if f.takesNone && valueSet[name] {
						misfiled = append(misfiled, fmt.Sprintf("%s is in valueFlagsBySub[%q] but takes no value (registered %s)", name, sub, f.where))
					}
					if !f.takesNone && boolSet[name] {
						misfiled = append(misfiled, fmt.Sprintf("%s is in boolFlagsBySub[%q] but consumes its value (registered %s)", name, sub, f.where))
					}
				}
			}
			sort.Strings(missing)
			sort.Strings(misfiled)

			if len(missing) > 0 {
				t.Errorf("bd %s registers %d flag(s) absent from the manifest in bdflags.go:\n  %s\n"+
					"Each one makes cmd/gc refuse a legitimate `gc bd %s` before bd runs. Add them to valueFlagsBySub/boolFlagsBySub for %q.",
					sub, len(missing), strings.Join(missing, "\n  "), sub, sub)
			}
			if len(misfiled) > 0 {
				t.Errorf("bd %s has %d flag(s) in the wrong manifest set:\n  %s\n"+
					"A misfiled flag corrupts the bead-id scan rather than widening it -- move them, do not add them to both.",
					sub, len(misfiled), strings.Join(misfiled, "\n  "))
			}
		})
	}
}

// TestBdGlobalFlagManifestMatchesModuleSource pins the global sets against
// rootCmd's persistent flags.
//
// Separate from the per-subcommand gate because a missing global is missing on
// EVERY subcommand at once: --database, --mem-profile, --cpu-profile and
// --no-color were absent, so all 17 refused. It also covers the case a help
// transcript structurally cannot -- bd MarkHidden's --format, a
// value-consuming global, and hiding a flag does not stop bd's parser from
// consuming its value.
func TestBdGlobalFlagManifestMatchesModuleSource(t *testing.T) {
	regs := collectBdFlagRegistrations(t, map[string]bool{bdRootCommandIdent: true})
	valueSet := GlobalValueFlags()
	boolSet := mergeFlagSets(globalBoolFlags)

	var missing, misfiled []string
	seen := 0
	for _, r := range regs {
		// Root-LOCAL flags are deliberately excluded: bd's -V/--version lives
		// on rootCmd.Flags() and `bd update -V` is rejected as an unknown
		// shorthand, so adding it to the global set would describe a flag no
		// subcommand argv can carry.
		if r.recv != bdRootCommandIdent || !r.flag.persistent {
			continue
		}
		seen++
		for _, name := range []string{r.flag.long, r.flag.short} {
			if name == "" {
				continue
			}
			if !valueSet[name] && !boolSet[name] {
				missing = append(missing, fmt.Sprintf("%s (%s, registered %s)", name, takesValueLabel(r.flag.takesNone), r.flag.where))
				continue
			}
			// Same opposite-tie-break hazard as the per-subcommand gate; see
			// the comment there.
			if valueSet[name] && boolSet[name] {
				misfiled = append(misfiled, fmt.Sprintf("%s is in BOTH globalValueFlags and globalBoolFlags; cmd_bd would consume its value and gc lint would not (registered %s)", name, r.flag.where))
				continue
			}
			if r.flag.takesNone && valueSet[name] {
				misfiled = append(misfiled, fmt.Sprintf("%s is in globalValueFlags but takes no value (registered %s)", name, r.flag.where))
			}
			if !r.flag.takesNone && boolSet[name] {
				misfiled = append(misfiled, fmt.Sprintf("%s is in globalBoolFlags but consumes its value (registered %s)", name, r.flag.where))
			}
		}
	}
	if seen == 0 {
		t.Fatalf("found no persistent flag registrations on %s in bd's cmd/bd; the global-flag derivation is blind and would report an empty bd as a clean one", bdRootCommandIdent)
	}
	sort.Strings(missing)
	sort.Strings(misfiled)

	if len(missing) > 0 {
		t.Errorf("bd's root command registers %d global flag(s) absent from the manifest in bdflags.go:\n  %s\n"+
			"A missing global refuses a legitimate `gc bd` write on EVERY subcommand. Add them to globalValueFlags/globalBoolFlags.",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(misfiled) > 0 {
		t.Errorf("bd has %d global flag(s) in the wrong manifest set:\n  %s", len(misfiled), strings.Join(misfiled, "\n  "))
	}
}

// TestBdFlagDerivationSeesEachRegistrationShape is the blindness canary for
// the two gates above.
//
// Both are one-directional by design: they fail on a flag the manifest is
// MISSING and stay silent when the manifest is ahead of bd. That posture has a
// hole neither gate can see on its own -- a derivation that stops recognizing
// a registration shape reports fewer flags, and fewer flags is exactly what a
// one-directional gate calls clean. It counts what is PRESENT in the manifest,
// never what was actually CONSUMED from bd's source, so every mechanism below
// could break at once and both gates would go green.
//
// Each row is a flag that reaches the derivation through exactly ONE mechanism
// and would vanish if that mechanism broke. Asserting a total flag count
// instead would go green on any two offsetting changes and would have to be
// re-baselined on every bd bump; these do not move unless bd's registration
// style does, which is the thing worth being told about.
func TestBdFlagDerivationSeesEachRegistrationShape(t *testing.T) {
	derived := deriveBdSubcommandFlags(t)

	cases := []struct {
		sub       string
		flag      string
		takesNone bool
		mechanism string
	}{
		{
			sub: "update", flag: "--if-assignee", takesNone: false,
			mechanism: "a direct `updateCmd.Flags().String(...)` registration",
		},
		{
			sub: "close", flag: "-r", takesNone: false,
			mechanism: "one level of registrar-helper indirection -- bd registers close's --reason/-r inside registerCloseReasonFlag(cmd *cobra.Command)",
		},
		{
			sub: "update", flag: "--body", takesNone: false,
			mechanism: "a registrar helper shared by two commands -- registerCommonIssueFlags(cmd) serves both create and update",
		},
		{
			sub: "list", flag: "--max-rows", takesNone: false,
			mechanism: "a flag name given as a package-level string constant (maxRowsFlagName), reached through addMaxRowsFlag(cmd)",
		},
		{
			sub: "list", flag: "--deps", takesNone: true,
			mechanism: "the NoOptDefVal override, which is what makes a String-registered flag consume nothing",
		},
		{
			sub: "mol burn", flag: "--yes", takesNone: true,
			mechanism: "the nested command walk -- `mol burn` resolves only by following molCmd.AddCommand(molBurnCmd)",
		},
		{
			sub: "close", flag: "-m", takesNone: false,
			mechanism: "a MarkHidden'd alias, which is present in bd's source and in no --help transcript",
		},
	}

	for _, tc := range cases {
		t.Run(tc.sub+" "+tc.flag, func(t *testing.T) {
			flags, ok := derived[tc.sub]
			if !ok {
				t.Fatalf("no bd command resolves to %q", tc.sub)
			}
			for _, f := range flags {
				if f.long != tc.flag && f.short != tc.flag {
					continue
				}
				if f.takesNone != tc.takesNone {
					t.Fatalf("bd %s %s derived as %q, want %q.\nThe derivation still sees the flag but has misread %s, so the manifest gate is now checking the wrong set.",
						tc.sub, tc.flag, takesValueLabel(f.takesNone), takesValueLabel(tc.takesNone), tc.mechanism)
				}
				return
			}
			t.Fatalf("the derivation no longer sees bd %s %s, which reaches it through %s.\n"+
				"The manifest gates are one-directional: with this flag missing from the derivation they report the manifest as clean, whatever it actually contains. Repair the derivation -- do not delete this row.",
				tc.sub, tc.flag, tc.mechanism)
		})
	}
}

// bdRootCommandIdent is the identifier bd binds its root cobra.Command to.
// Its PERSISTENT registrations are the global flags every subcommand
// inherits; its local ones (-V/--version) reach no subcommand.
const bdRootCommandIdent = "rootCmd"

func takesValueLabel(takesNone bool) string {
	if takesNone {
		return "takes no value"
	}
	return "consumes its value"
}

// deriveBdSubcommandFlags returns, per manifest subcommand key, every flag
// bd's source makes acceptable on that subcommand's argv: the ones registered
// on the command itself, plus each ancestor's PERSISTENT registrations, which
// cobra passes down.
//
// The ancestor half is not speculative bookkeeping. `mol pour` accepts
// anything on molCmd.PersistentFlags(), and such a flag is attributed to
// molCmd -- not a manifest key -- so without this it would be derived and then
// dropped, and the gate would report the manifest clean while missing it. bd
// registers no persistent flags on molCmd, gateCmd or depCmd today; the point
// is that adding one lands on a gate instead of on nobody. Root's persistent
// flags are excluded here because they are the global set, checked separately
// and merged into every subcommand's manifest by ValueFlags/BoolFlags.
//
// Keys that resolve to no bd command are absent from the result rather than
// empty, so the caller can tell "bd no longer has this subcommand" from "bd
// has it and registers nothing".
func deriveBdSubcommandFlags(t *testing.T) map[string][]derivedFlag {
	t.Helper()

	identForKey, ancestorsForKey := resolveBdSubcommandIdents(t)
	relevant := map[string]bool{bdRootCommandIdent: true}
	for _, ident := range identForKey {
		relevant[ident] = true
	}
	for _, chain := range ancestorsForKey {
		for _, ident := range chain {
			relevant[ident] = true
		}
	}
	regs := collectBdFlagRegistrations(t, relevant)

	byIdent := make(map[string][]derivedFlag)
	persistentByIdent := make(map[string][]derivedFlag)
	for _, r := range regs {
		byIdent[r.recv] = append(byIdent[r.recv], r.flag)
		if r.flag.persistent {
			persistentByIdent[r.recv] = append(persistentByIdent[r.recv], r.flag)
		}
	}

	out := make(map[string][]derivedFlag, len(identForKey))
	for key, ident := range identForKey {
		flags := byIdent[ident]
		for _, ancestor := range ancestorsForKey[key] {
			flags = append(flags, persistentByIdent[ancestor]...)
		}
		out[key] = flags
	}
	return out
}

// resolveBdSubcommandIdents maps each manifest subcommand key ("update",
// "mol pour") to the identifier bd binds that command to ("updateCmd",
// "molPourCmd"), by walking the AddCommand graph from rootCmd and reading each
// literal's Use field. The second result gives each key's intermediate
// ancestors, excluding rootCmd, whose persistent flags the subcommand
// inherits.
//
// A key that resolves to nothing is left out and fails in the caller. Silently
// returning an empty flag list instead would make a renamed subcommand look
// like one bd registers no flags for, which is the same green run this gate
// exists to stop.
func resolveBdSubcommandIdents(t *testing.T) (map[string]string, map[string][]string) {
	t.Helper()

	files := parseBdCommandFiles(t)
	literals := make(map[string]*ast.CompositeLit)
	children := make(map[string][]string)
	for _, f := range files {
		collectCobraLiterals(f.file, literals)
		collectAddCommandEdges(t, f, children)
	}
	if len(children[bdRootCommandIdent]) == 0 {
		t.Fatalf("no %s.AddCommand registrations found in bd's cmd/bd; the command-tree walk is blind", bdRootCommandIdent)
	}

	// nameOf reads the invoked name (first word of Use) for a command ident.
	nameOf := func(ident string) string {
		lit, ok := literals[ident]
		if !ok {
			return ""
		}
		cmd, err := readCobraCommand(lit)
		if err != nil {
			return ""
		}
		return cmd.name
	}

	out := make(map[string]string)
	ancestors := make(map[string][]string)
	for _, key := range Subcommands() {
		ident := bdRootCommandIdent
		var chain []string
		ok := true
		for _, word := range strings.Fields(key) {
			next := ""
			for _, child := range children[ident] {
				if nameOf(child) == word {
					next = child
					break
				}
			}
			if next == "" {
				ok = false
				break
			}
			if ident != bdRootCommandIdent {
				chain = append(chain, ident)
			}
			ident = next
		}
		if ok {
			out[key] = ident
			ancestors[key] = chain
		}
	}
	return out, ancestors
}

// collectAddCommandEdges records parent-to-child edges from every
// `<parent>.AddCommand(<child>, ...)` call, accepting `&child` as well -- bd
// registers one command by address.
//
// A call whose receiver or argument is not an identifier fails on the spot
// rather than being skipped: an unreadable edge hides a whole command from the
// walk, and a subcommand the walk cannot reach is reported by the caller as
// one bd does not have.
func collectAddCommandEdges(t *testing.T, f parsedBdFile, into map[string][]string) {
	t.Helper()
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AddCommand" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			t.Errorf("AddCommand at %s takes a receiver that is not an identifier, so its children are unreachable from the command-tree walk.\n"+
				"Teach collectAddCommandEdges that shape -- until then every subcommand under it looks absent from bd.", f.pos(sel.Pos()))
			return true
		}
		for _, arg := range call.Args {
			if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				arg = unary.X
			}
			ident, ok := arg.(*ast.Ident)
			if !ok {
				t.Errorf("%s.AddCommand at %s takes an argument that is neither an identifier nor the address of one, so the command it registers cannot be reached.",
					recv.Name, f.pos(arg.Pos()))
				continue
			}
			into[recv.Name] = append(into[recv.Name], ident.Name)
		}
		return true
	})
}

// collectBdFlagRegistrations returns every flag registration in bd's cmd/bd,
// with its receiver resolved through one level of registrar-helper
// indirection.
//
// The indirection is not hypothetical: bd registers close's --reason/-r inside
// `registerCloseReasonFlag(cmd *cobra.Command)` and calls it with closeCmd, so
// a derivation reading only `<ident>.Flags()` receivers reports close as having
// no --reason at all -- and would have passed against a manifest missing it.
func collectBdFlagRegistrations(t *testing.T, relevant map[string]bool) []flagRegistration {
	t.Helper()
	files := parseBdCommandFiles(t)
	consts := collectPackageStringConsts(files)

	// Pass 1: registrations keyed by the receiver identifier as written, plus
	// each function's *cobra.Command parameter names.
	var direct []flagRegistration
	helperParams := make(map[string][]string) // func name -> param name per position, "" for non-command params
	helperRegs := make(map[string][]flagRegistration)

	for _, f := range files {
		// A function parameter is provisionally relevant: it cannot be known
		// whose flags it registers until a call site is read, and one of them
		// may pass a command the manifest covers.
		fileRelevant := make(map[string]bool, len(relevant))
		for k := range relevant {
			fileRelevant[k] = true
		}
		for _, decl := range f.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				for _, p := range cobraCommandParamNames(fn) {
					if p != "" {
						fileRelevant[p] = true
					}
				}
			}
		}

		fileRegs := collectFlagRegistrationCalls(t, f, consts, fileRelevant)
		noValue := collectNoOptDefValOverrides(t, f, consts)
		for i := range fileRegs {
			if noValue[fileRegs[i].recv+" "+fileRegs[i].flag.long] {
				fileRegs[i].flag.takesNone = true
			}
		}

		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			params := cobraCommandParamNames(fn)
			if len(params) == 0 {
				continue
			}
			helperParams[fn.Name.Name] = params
			paramSet := make(map[string]bool)
			for _, p := range params {
				if p != "" {
					paramSet[p] = true
				}
			}
			for _, r := range fileRegs {
				if paramSet[r.recv] && withinNode(fn, r.flag.offset) {
					helperRegs[fn.Name.Name] = append(helperRegs[fn.Name.Name], r)
				}
			}
		}

		direct = append(direct, fileRegs...)
	}

	// Pass 2: attribute a registrar helper's registrations to the command each
	// call site passes it.
	out := direct
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fnIdent, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			regs, ok := helperRegs[fnIdent.Name]
			if !ok || len(regs) == 0 {
				return true
			}
			params := helperParams[fnIdent.Name]
			for pos, paramName := range params {
				if paramName == "" || pos >= len(call.Args) {
					continue
				}
				arg := call.Args[pos]
				if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
					arg = unary.X
				}
				target, ok := arg.(*ast.Ident)
				if !ok {
					t.Errorf("%s at %s registers flags on its *cobra.Command parameter but is called with an argument that is not an identifier, so those flags cannot be attributed to a command.\n"+
						"Until this shape is read, whichever command it is has flags the manifest is not checked against.", fnIdent.Name, f.pos(arg.Pos()))
					continue
				}
				for _, r := range regs {
					if r.recv != paramName {
						continue
					}
					r.recv = target.Name
					out = append(out, r)
				}
			}
			return true
		})
	}
	return out
}

// cobraCommandParamNames returns fn's parameter names positionally, with a
// non-empty entry only where the parameter is a *cobra.Command.
func cobraCommandParamNames(fn *ast.FuncDecl) []string {
	if fn.Type.Params == nil {
		return nil
	}
	var names []string
	found := false
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		isCmd := false
		if ok {
			if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Command" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "cobra" {
					isCmd = true
				}
			}
		}
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			if isCmd && name.Name != "_" {
				names = append(names, name.Name)
				found = true
			} else {
				names = append(names, "")
			}
		}
	}
	if !found {
		return nil
	}
	return names
}

// collectFlagRegistrationCalls finds every `<recv>.Flags().<Method>(...)` and
// `<recv>.PersistentFlags().<Method>(...)` registration in one file.
//
// A registration whose flag name is not a string literal, or whose pflag type
// this gate has no classification for, fails rather than being dropped. Both
// are cases where the derivation has stopped being able to answer, and a
// derivation that answers "no flag here" when it means "I cannot tell" reports
// a manifest gap as a clean run.
func collectFlagRegistrationCalls(t *testing.T, f parsedBdFile, consts map[string]string, relevant map[string]bool) []flagRegistration {
	t.Helper()
	var out []flagRegistration
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, persistent, ok := flagSetReceiver(method.X)
		if !ok {
			return true
		}
		// bd registers flags on ~40 commands; the manifest covers 22. A
		// registration on a command no manifest entry names cannot affect any
		// consumer, so an unreadable one there is not a gate failure -- bd
		// registers init's removed-backend tombstones from a loop over a
		// struct slice, and failing on that would make the gate permanently
		// red over a command gc never scans.
		if !relevant[recv] {
			return true
		}
		nameIdx, shortIdx, typeName, ok := pflagRegistrationShape(method.Sel.Name)
		if !ok {
			return true
		}
		takesNone, classified := pflagTypeTakesNoValue[typeName]
		if !classified {
			t.Errorf("bd registers a flag at %s with pflag method %q, whose type %q is not classified in pflagTypeTakesNoValue.\n"+
				"Decide whether a bare occurrence consumes the following argv token and add the entry. Guessing either way corrupts the bead-id scan.",
				f.pos(call.Pos()), method.Sel.Name, typeName)
			return true
		}
		if nameIdx >= len(call.Args) {
			t.Errorf("bd's %s call at %s has too few arguments to hold a flag name; the registration shape has changed.", method.Sel.Name, f.pos(call.Pos()))
			return true
		}
		long, ok := stringExprValue(call.Args[nameIdx], consts)
		if !ok {
			t.Errorf("bd registers a flag at %s, on a command the manifest covers, whose name is neither a string literal nor a package-level string constant.\n"+
				"The manifest is unchecked for that flag until this derivation can read the name. Teach stringExprValue the shape bd uses here.", f.pos(call.Args[nameIdx].Pos()))
			return true
		}
		reg := flagRegistration{
			recv: recv,
			flag: derivedFlag{
				long:       "--" + long,
				takesNone:  takesNone,
				persistent: persistent,
				where:      f.pos(call.Pos()),
				offset:     call.Pos(),
			},
		}
		if shortIdx >= 0 {
			if shortIdx >= len(call.Args) {
				t.Errorf("bd's %s call at %s has no shorthand argument; the registration shape has changed.", method.Sel.Name, f.pos(call.Pos()))
				return true
			}
			short, ok := stringExprValue(call.Args[shortIdx], consts)
			if !ok {
				t.Errorf("bd registers a shorthand at %s that is not a string literal.", f.pos(call.Args[shortIdx].Pos()))
				return true
			}
			if short != "" {
				reg.flag.short = "-" + short
			}
		}
		out = append(out, reg)
		return true
	})
	return out
}

// flagSetReceiver returns the identifier a `.Flags()` / `.PersistentFlags()`
// call hangs off, e.g. "updateCmd" for `updateCmd.Flags()`, and whether the
// flag set was the persistent one.
func flagSetReceiver(e ast.Expr) (recv string, persistent bool, ok bool) {
	call, isCall := e.(*ast.CallExpr)
	if !isCall || len(call.Args) != 0 {
		return "", false, false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", false, false
	}
	switch sel.Sel.Name {
	case "Flags":
	case "PersistentFlags":
		persistent = true
	default:
		return "", false, false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", false, false
	}
	return ident.Name, persistent, true
}

// pflagRegistrationShape decodes a pflag registration method name into the
// argument index of the flag name, the index of its shorthand (-1 when the
// method declares none), and the pflag type name.
//
// The shape is positional and uniform across pflag: a Var suffix means the
// first argument is the destination pointer and pushes the name to index 1, a
// P suffix appends a shorthand right after the name. Returns false for a
// method that is not a registration at all -- Lookup, Changed, GetString and
// the rest of the FlagSet surface.
func pflagRegistrationShape(method string) (nameIdx, shortIdx int, typeName string, ok bool) {
	switch {
	case strings.HasSuffix(method, "VarP"):
		nameIdx, shortIdx, typeName = 1, 2, strings.TrimSuffix(method, "VarP")
	case strings.HasSuffix(method, "Var"):
		nameIdx, shortIdx, typeName = 1, -1, strings.TrimSuffix(method, "Var")
	case strings.HasSuffix(method, "P"):
		nameIdx, shortIdx, typeName = 0, 1, strings.TrimSuffix(method, "P")
	default:
		nameIdx, shortIdx, typeName = 0, -1, method
	}
	// Every other FlagSet method reachable on a `.Flags()` receiver -- Lookup,
	// Changed, Set, MarkHidden, GetString, FlagUsages, HasFlags -- would
	// otherwise be decoded as a registration of some unclassified type and
	// fail the gate. Reject them by name so the unclassified-type failure
	// stays reserved for a genuinely new pflag type.
	switch typeName {
	case "Lookup", "Changed", "Set", "MarkHidden", "MarkDeprecated",
		"MarkShorthandDeprecated", "MarkRequired", "FlagUsages", "HasFlags",
		"HasAvailableFlags", "Visit", "VisitAll", "Parse", "Args", "Arg",
		"NFlag", "NArg", "AddFlagSet", "AddFlag", "SetNormalizeFunc",
		"SortFlags", "PrintDefaults", "SetOutput", "ShorthandLookup":
		return 0, 0, "", false
	}
	if strings.HasPrefix(typeName, "Get") {
		return 0, 0, "", false
	}
	return nameIdx, shortIdx, typeName, true
}

// collectNoOptDefValOverrides returns the "<recv> --<flag>" keys whose
// NoOptDefVal bd sets after registering them, making a bare occurrence consume
// nothing.
//
// Without this, `bd list --deps` is derived as value-consuming from its String
// registration, and a scanner told so swallows the token after a bare --deps.
// The override is invisible in `bd list --help`, which prints "--deps string"
// like any other -- a second thing the help transcript cannot see.
//
// Resolved through the variable a Lookup is bound to rather than by pairing
// within one statement: bd writes the assignment inside an if-body whose
// Lookup sits in the if-init, so a per-statement pairing has to decide which
// enclosing statement owns it and reports the inner block as an unpaired
// assignment. Binding the variable handles that shape, the direct
// `Lookup(...).NoOptDefVal =` chain, and the two-statement form alike.
func collectNoOptDefValOverrides(t *testing.T, f parsedBdFile, consts map[string]string) map[string]bool {
	t.Helper()

	// lookupVars maps a local variable to the flag its Lookup named.
	lookupVars := make(map[string]string)
	ast.Inspect(f.file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || i >= len(assign.Rhs) {
				continue
			}
			if recv, name, found := lookupCallTarget(assign.Rhs[i], consts); found {
				lookupVars[ident.Name] = recv + " --" + name
			}
		}
		return true
	})

	out := make(map[string]bool)
	ast.Inspect(f.file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NoOptDefVal" {
				continue
			}
			// Direct chain: X.Flags().Lookup("n").NoOptDefVal = v
			if recv, name, found := lookupCallTarget(sel.X, consts); found {
				out[recv+" --"+name] = true
				continue
			}
			// Through a bound variable.
			if base, ok := sel.X.(*ast.Ident); ok {
				if key, ok := lookupVars[base.Name]; ok {
					out[key] = true
					continue
				}
			}
			t.Errorf("bd assigns NoOptDefVal at %s and this gate cannot tell which flag it names.\n"+
				"That flag consumes no value: left filed as value-consuming, the scanner swallows the argv token after it. Teach lookupCallTarget the shape bd uses here.",
				f.pos(assign.Pos()))
		}
		return true
	})
	return out
}

// lookupCallTarget returns the receiver and flag name of a
// `<recv>.Flags().Lookup("<name>")` call expression.
func lookupCallTarget(e ast.Expr, consts map[string]string) (recv, name string, found bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Lookup" {
		return "", "", false
	}
	r, _, ok := flagSetReceiver(sel.X)
	if !ok {
		return "", "", false
	}
	lit, ok := stringExprValue(call.Args[0], consts)
	if !ok {
		return "", "", false
	}
	return r, lit, true
}

// stringExprValue resolves e to a string, accepting a literal or a
// package-level string constant.
//
// The constant form is load-bearing, not defensive: bd registers --max-rows
// through `addMaxRowsFlag(cmd)` as `cmd.Flags().Int(maxRowsFlagName, ...)`,
// and --max-rows is a real flag on list and ready. A derivation reading only
// literals reports it as unreadable, and the honest thing a gate can do with
// unreadable is fail -- so the constant has to be resolved for the gate to be
// green while correct.
func stringExprValue(e ast.Expr, consts map[string]string) (string, bool) {
	if s, ok := stringLitValue(e); ok {
		return s, true
	}
	if ident, ok := e.(*ast.Ident); ok {
		if v, ok := consts[ident.Name]; ok {
			return v, true
		}
	}
	return "", false
}

// collectPackageStringConsts indexes every package-level `const name = "..."`
// (and var of the same shape) in bd's cmd/bd, so a flag registered under a
// named constant can still be read.
func collectPackageStringConsts(files []parsedBdFile) map[string]string {
	out := make(map[string]string)
	for _, f := range files {
		for _, decl := range f.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := stringLitValue(vs.Values[i]); ok {
						out[name.Name] = s
					}
				}
			}
		}
	}
	return out
}

// parsedBdFile is one parsed file of bd's cmd/bd package, carrying enough
// position information to name a source line in a failure.
type parsedBdFile struct {
	name string
	file *ast.File
	fset *token.FileSet
}

func (f parsedBdFile) pos(p token.Pos) string {
	return fmt.Sprintf("%s:%d", f.name, f.fset.Position(p).Line)
}

// parseBdCommandFiles parses every non-test file of bd's cmd/bd package at the
// version go.mod pins.
//
// Build tags are deliberately NOT honored, matching the command-name
// derivation: a flag registered only under some tag still contributes, because
// the question is whether bd's parser could ever accept it. Over-reporting
// bd's flags costs the manifest a redundant entry; under-reporting costs a
// refused write.
func parseBdCommandFiles(t *testing.T) []parsedBdFile {
	t.Helper()
	dir := filepath.Join(beadsModuleDir(t), "cmd", "bd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading beads cmd/bd at %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []parsedBdFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}
		out = append(out, parsedBdFile{name: name, file: file, fset: fset})
	}
	if len(out) == 0 {
		t.Fatalf("no non-test Go files in %s; the flag derivation has nothing to read and must not pass", dir)
	}
	return out
}

// withinNode reports whether pos falls inside n's extent, used to keep a
// registrar helper's registrations from picking up an identically named
// receiver elsewhere in the same file.
func withinNode(n ast.Node, pos token.Pos) bool {
	return pos >= n.Pos() && pos <= n.End()
}
