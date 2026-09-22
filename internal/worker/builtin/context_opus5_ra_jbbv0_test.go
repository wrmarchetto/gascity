package builtin

import "testing"

// TestBuiltinClaudeModelChoicesIncludeOpus5 is the falsifiable floor for
// ra-jbbv0 / ra-4cq5w: the builtin claude provider's "model" select is a
// closed enum, and a value outside it yields no FlagArgs — so gc silently
// emits no --model flag at all rather than erroring, and 'gc config show'
// keeps reporting the pin while the launched process runs the provider
// default model. claude-sonnet-5 (#3867) and claude-fable-5 (#3284) were
// added to this enum; claude-opus-5 was not.
func TestBuiltinClaudeModelChoicesIncludeOpus5(t *testing.T) {
	claude, ok := BuiltinProviders()["claude"]
	if !ok {
		t.Fatal("BuiltinProviders() missing claude")
	}

	var modelOption BuiltinProviderOption
	for _, option := range claude.OptionsSchema {
		if option.Key == "model" {
			modelOption = option
			break
		}
	}
	if modelOption.Key == "" {
		t.Fatal("claude provider missing model option")
	}

	byValue := make(map[string]BuiltinOptionChoice, len(modelOption.Choices))
	for _, choice := range modelOption.Choices {
		byValue[choice.Value] = choice
	}

	choice, ok := byValue["opus-5"]
	if !ok {
		t.Fatal("claude model choices missing \"opus-5\" (claude-opus-5 has no enum entry, " +
			"so resolving it yields no --model FlagArgs and gc silently launches the provider default)")
	}
	wantFlagArgs := []string{"--model", "claude-opus-5"}
	if len(choice.FlagArgs) != 2 || choice.FlagArgs[0] != wantFlagArgs[0] || choice.FlagArgs[1] != wantFlagArgs[1] {
		t.Errorf("opus-5 FlagArgs = %v, want %v", choice.FlagArgs, wantFlagArgs)
	}
	if len(choice.FlagAliases) != 1 || len(choice.FlagAliases[0]) != 2 ||
		choice.FlagAliases[0][0] != "-m" || choice.FlagAliases[0][1] != "claude-opus-5" {
		t.Errorf("opus-5 FlagAliases = %v, want [[-m claude-opus-5]]", choice.FlagAliases)
	}

	// Unlike the sonnet/fable-5 precedent (#3867, #3284), bare "opus" is NOT
	// repointed at the new latest here: internal/config/provider_test.go
	// (TestBuiltinProvidersClaudeModelChoices) pins "opus" to claude-opus-4-8
	// as a deliberate stability guarantee, and opus-5 is added as a new
	// explicit alias alongside it rather than replacing the default.
	bare, ok := byValue["opus"]
	if !ok {
		t.Fatal("claude model choices missing \"opus\"")
	}
	if len(bare.FlagArgs) != 2 || bare.FlagArgs[1] != "claude-opus-4-8" {
		t.Errorf("opus (bare) FlagArgs = %v, want [--model claude-opus-4-8] (unchanged)", bare.FlagArgs)
	}
}

// TestBuiltinClaudeModelChoicesAcceptCanonicalIDsVerbatim is the second half
// of ra-jbbv0's root cause: operators pin the full provider model ID
// ("claude-opus-5", not the short alias "opus-5") in agent.toml. The incident
// showed loial/egwene/siuan/perrin pinned to exactly "claude-opus-5" and
// moiraine to "claude-opus-5[1m]" — none of which were enum values, so the
// named-session resolution path hard-errored ("invalid value for model:
// claude-opus-5") while the launch path silently dropped --model instead.
// Neither #3867 (Sonnet 5) nor #3284 (Fable 5) added the canonical-id form as
// an accepted value — only the short alias — so this gap predates and is
// broader than Opus 5 alone.
func TestBuiltinClaudeModelChoicesAcceptCanonicalIDsVerbatim(t *testing.T) {
	claude, ok := BuiltinProviders()["claude"]
	if !ok {
		t.Fatal("BuiltinProviders() missing claude")
	}
	var modelOption BuiltinProviderOption
	for _, option := range claude.OptionsSchema {
		if option.Key == "model" {
			modelOption = option
			break
		}
	}
	byValue := make(map[string]BuiltinOptionChoice, len(modelOption.Choices))
	for _, choice := range modelOption.Choices {
		byValue[choice.Value] = choice
	}

	for _, canonical := range []string{"claude-opus-5", "claude-opus-5[1m]", "claude-sonnet-5", "claude-fable-5"} {
		choice, ok := byValue[canonical]
		if !ok {
			t.Errorf("claude model choices missing canonical id %q as a directly-accepted value", canonical)
			continue
		}
		if len(choice.FlagArgs) != 2 || choice.FlagArgs[0] != "--model" || choice.FlagArgs[1] != canonical {
			t.Errorf("%s FlagArgs = %v, want [--model %s]", canonical, choice.FlagArgs, canonical)
		}
	}
}

// TestBuiltinClaudeModelChoicesIncludeOpus55 extends the ra-jbbv0 floor to
// Opus 5.5 (ci-q6qu8z). The operator's ruling is that seats run Opus 5.5
// explicitly, and until this entry existed no agent.toml could ask for it in
// any spelling -- the enum is closed, so the launch path emitted no --model
// and the named-session resolution path hard-errored, the exact split
// ra-jbbv0 cost a city.
//
// The expected id is written here as a literal on purpose rather than read
// back from the choice under test: the whole failure mode is a wrong or
// missing id, and an expectation derived from the implementation agrees with
// whatever the implementation says. "claude-opus-5-5" was read out of the
// shipped Claude CLI's baked model catalog (2.1.280,
// ~/.local/share/claude/versions/), where it appears as
// id:"claude-opus-5-5", display_name:"Opus 5.5", with first_party and
// gateway provider_ids of the same string and a short alias mapping
// "opus-5-5" -> "claude-opus-5-5".
func TestBuiltinClaudeModelChoicesIncludeOpus55(t *testing.T) {
	claude, ok := BuiltinProviders()["claude"]
	if !ok {
		t.Fatal("BuiltinProviders() missing claude")
	}

	var modelOption BuiltinProviderOption
	for _, option := range claude.OptionsSchema {
		if option.Key == "model" {
			modelOption = option
			break
		}
	}
	if modelOption.Key == "" {
		t.Fatal("claude provider missing model option")
	}

	byValue := make(map[string]BuiltinOptionChoice, len(modelOption.Choices))
	for _, choice := range modelOption.Choices {
		byValue[choice.Value] = choice
	}

	// Both halves are asserted in one test because a half-added model is the
	// ra-jbbv0 shape itself: the silent launch-path half is the one nobody
	// notices, so neither half may be able to land without the other.
	for _, value := range []string{"opus-5-5", "claude-opus-5-5", "claude-opus-5-5[1m]"} {
		choice, ok := byValue[value]
		if !ok {
			t.Errorf("claude model choices missing %q (a value outside this enum yields no "+
				"--model FlagArgs on launch and hard-errors on named-session resolution)", value)
			continue
		}
		wantID := "claude-opus-5-5"
		if value == "claude-opus-5-5[1m]" {
			wantID = "claude-opus-5-5[1m]"
		}
		if len(choice.FlagArgs) != 2 || choice.FlagArgs[0] != "--model" || choice.FlagArgs[1] != wantID {
			t.Errorf("%s FlagArgs = %v, want [--model %s]", value, choice.FlagArgs, wantID)
		}
		if len(choice.FlagAliases) != 1 || len(choice.FlagAliases[0]) != 2 ||
			choice.FlagAliases[0][0] != "-m" || choice.FlagAliases[0][1] != wantID {
			t.Errorf("%s FlagAliases = %v, want [[-m %s]]", value, choice.FlagAliases, wantID)
		}
	}

	// Absence recorded where a reader would look for it: bare "opus" is NOT
	// repointed at 5.5. TestBuiltinProvidersClaudeModelChoices in
	// internal/config/provider_test.go pins it to claude-opus-4-8 as a
	// stability guarantee, and moving a seat is a separate operator step --
	// config load refuses an option default the provider does not declare, so
	// an agent.toml may only name 5.5 after the rebuilt gc is installed.
	bare, ok := byValue["opus"]
	if !ok {
		t.Fatal("claude model choices missing \"opus\"")
	}
	if len(bare.FlagArgs) != 2 || bare.FlagArgs[1] != "claude-opus-4-8" {
		t.Errorf("opus (bare) FlagArgs = %v, want [--model claude-opus-4-8] (unchanged)", bare.FlagArgs)
	}
}
