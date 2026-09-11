// Scope: that the two hand-written AgentDefaults merge bodies each carry
// EVERY field on the struct. Per-field merge semantics -- scalar
// last-layer-wins, list append-unique, alias prefer-canonical -- are pinned by
// the tests next to those behaviors; this file only asserts nothing is
// missing, which is the failure neither of them can see.
//
// Why this suite exists: AgentDefaults had no field-sync guard at all while
// config.Agent, AgentPatch, AgentOverride and ProviderSpec all had one
// (field_sync_test.go). mergeAgentDefaults and
// mergeAgentDefaultsAliasPreferCanonical are hand-kept enumerations, and a
// field absent from both parses, composes and round-trips perfectly while
// being dropped the moment it arrives from a pack or a fragment rather than
// from city.toml directly. ci-07ebae added idle_timeout and hit exactly that:
// the field worked in every direct-construction test and would have shipped
// inert for the layered path nobody tests by hand.
//
// The guard is reflective on purpose. An allowlist of field names is the same
// hand-kept enumeration one level up and rots on the same schedule -- it would
// have been written in the same edit that forgot the field.
//
// Run: go test ./internal/config/ -run TestAgentDefaultsMerge

package config

import (
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

// populatedAgentDefaults returns an AgentDefaults with every field set to a
// distinct non-zero value, built reflectively so a newly added field is
// populated without anyone remembering to come here. A field whose type this
// cannot fill fails loudly rather than being skipped: a silent skip would
// leave the new field looking merged when it is not, which is the entire
// failure this file exists to catch.
func populatedAgentDefaults(t *testing.T) AgentDefaults {
	t.Helper()
	var d AgentDefaults
	v := reflect.ValueOf(&d).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("sentinel-" + v.Type().Field(i).Name)
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				t.Fatalf("AgentDefaults.%s is a slice of %s; teach populatedAgentDefaults to fill it",
					v.Type().Field(i).Name, f.Type().Elem().Kind())
			}
			f.Set(reflect.ValueOf([]string{"sentinel-" + v.Type().Field(i).Name}))
		default:
			t.Fatalf("AgentDefaults.%s has unsupported kind %s; teach populatedAgentDefaults to fill it",
				v.Type().Field(i).Name, f.Kind())
		}
	}
	return d
}

func assertEveryAgentDefaultsFieldSet(t *testing.T, got AgentDefaults, mergeName string) {
	t.Helper()
	v := reflect.ValueOf(got)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("%s dropped AgentDefaults.%s -- add it to that merge body",
				mergeName, v.Type().Field(i).Name)
		}
	}
}

// The fragment/pack merge path: every field set on a later layer must reach
// an empty base.
func TestAgentDefaultsMergeCoversAllFields(t *testing.T) {
	var dst AgentDefaults
	mergeAgentDefaults(&dst, populatedAgentDefaults(t), "test-layer", nil)
	assertEveryAgentDefaultsFieldSet(t, dst, "mergeAgentDefaults")
}

// The legacy [agents] alias fold: with nothing defined under the canonical
// [agent_defaults] table, every alias field must be adopted. Decoding an
// empty document is what makes IsDefined false for all of them, which is the
// "canonical table absent" case this function is written for.
func TestAgentDefaultsMergeAliasCoversAllFields(t *testing.T) {
	meta, err := toml.Decode("", &struct{}{})
	if err != nil {
		t.Fatalf("decoding an empty document: %v", err)
	}
	var dst AgentDefaults
	mergeAgentDefaultsAliasPreferCanonical(&dst, populatedAgentDefaults(t), meta)
	assertEveryAgentDefaultsFieldSet(t, dst, "mergeAgentDefaultsAliasPreferCanonical")
}
