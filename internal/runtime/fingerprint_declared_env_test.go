package runtime

import "testing"

// The defect these pin (ci-yulan1, root-caused in ci-vfwkdm): envFingerprintAllow
// is a closed allow-list of GC_ keys, so a key a user DECLARES in a config env
// block -- [workspace.env], [providers.<name>.env], an agent [env] -- contributed
// to no fingerprint at all. An env-only edit therefore moved neither Core nor
// Provision, and an edit that also touched Command was classified launch-only and
// took the warm-box relaunch, which applies no env values. The live symptom was a
// mayor session running on a provider env block the process had never seen.
//
// Run: go test ./internal/runtime/ -run DeclaredEnv

func TestDeclaredEnvKeyValueIsFingerprinted(t *testing.T) {
	base := Config{
		Env:             map[string]string{"CLAUDE_ACCOUNTS": "0 4"},
		DeclaredEnvKeys: []string{"CLAUDE_ACCOUNTS"},
	}
	changed := base
	changed.Env = envWith(base.Env, "CLAUDE_ACCOUNTS", "0 1")

	if CoreFingerprint(base) == CoreFingerprint(changed) {
		t.Error("changing a declared env VALUE must move CoreFingerprint")
	}
	if ProvisionFingerprint(base) == ProvisionFingerprint(changed) {
		t.Error("declared env is provision-half: the value change must move ProvisionFingerprint")
	}
	if LaunchFingerprint(base) != LaunchFingerprint(changed) {
		t.Error("declared env must NOT move LaunchFingerprint -- a launch-only verdict takes the warm relaunch, which applies no env")
	}
}

// Withdrawing a declaration is a behavior change even when the key survives in
// Env: the value then comes from the ambient controller environment instead of
// from config, and the two agree only by coincidence. Hashing the selected values
// alone cannot see this -- Env is byte-identical across the edit -- so the
// declared key SET is a fingerprint input in its own right.
func TestWithdrawnDeclarationMovesFingerprintWithIdenticalEnv(t *testing.T) {
	env := map[string]string{"CLAUDE_POOL": "0 1 2 3 4 5"}
	declared := Config{Env: env, DeclaredEnvKeys: []string{"CLAUDE_POOL"}}
	ambient := Config{Env: env}

	if CoreFingerprint(declared) == CoreFingerprint(ambient) {
		t.Error("dropping the declaration must move CoreFingerprint even though Env is unchanged")
	}
	if ProvisionFingerprint(declared) == ProvisionFingerprint(ambient) {
		t.Error("dropping the declaration must move ProvisionFingerprint")
	}
}

// The allow-list's whole purpose survives: an ambient key nobody declared stays
// invisible, so the ~50 GC_ vars from service discovery and the non-GC_
// passthrough (PATH, HOME, CLAUDE_CODE_OAUTH_TOKEN, LANG) still cannot drive a
// fleet-wide drift restart. This is the test that would have failed had the fix
// keyed on the GC_ prefix rather than on provenance.
func TestUndeclaredEnvStaysOutOfTheFingerprint(t *testing.T) {
	// Both arms matter and only the second is load-bearing. With no declaration
	// hashEnvFingerprint takes its len==0 early return, so the first arm never
	// reaches the include predicate the exclusion actually lives in -- an
	// implementation admitting every key inside the declared branch passes it.
	for _, declared := range [][]string{nil, {"SOMETHING_ELSE"}} {
		for _, key := range []string{"PATH", "HOME", "CLAUDE_CODE_OAUTH_TOKEN", "LANG", "GC_SESSION_ID"} {
			base := Config{
				Env:             map[string]string{key: "before", "SOMETHING_ELSE": "s"},
				DeclaredEnvKeys: declared,
			}
			changed := Config{
				Env:             map[string]string{key: "after", "SOMETHING_ELSE": "s"},
				DeclaredEnvKeys: declared,
			}
			if CoreFingerprint(base) != CoreFingerprint(changed) {
				t.Errorf("undeclared %s moved CoreFingerprint (declared=%v)", key, declared)
			}
		}
	}
}

// A declared key ABSENT from Env still contributes, through the set and not
// through any value. env_remove dropping a key, or a later layer deleting it,
// is a change of declared identity even though there is no value to hash --
// and the obvious implementation, building the key list only from declared
// keys that are present in Env, loses exactly this and nothing else.
//
// Not covered by TestWithdrawnDeclaration... above, whose key IS present in
// Env, nor by the determinism test below, which drops the same absent key from
// both sides and so cannot see it.
func TestDeclaredKeyAbsentFromEnvStillMovesTheHash(t *testing.T) {
	env := map[string]string{"X": "1"}

	// Two configs differing ONLY in WHICH absent key each declares. Comparing a
	// declaring config against a bare one instead is the trap: the declared
	// branch writes its "declared-env" sentinel either way, so those two differ
	// on the sentinel and the assertion holds even when the key name is dropped.
	// Both sides here take the declared branch, so the key name is the only
	// remaining difference.
	a := Config{Env: env, DeclaredEnvKeys: []string{"MISSING_A"}}
	b := Config{Env: env, DeclaredEnvKeys: []string{"MISSING_B"}}

	if CoreFingerprint(a) == CoreFingerprint(b) {
		t.Error("two configs declaring different absent keys must hash differently")
	}
	if ProvisionFingerprint(a) == ProvisionFingerprint(b) {
		t.Error("and must differ in the provision half, like any other declared key")
	}
	// The value contribution is genuinely nil: neither key is in Env, so the
	// only thing separating them is the set.
	if got := len(env); got != 1 {
		t.Fatalf("fixture Env grew to %d keys; the absent-key premise no longer holds", got)
	}
}

// A declared key absent from Env contributes no VALUE, and the hash must not
// depend on map or slice ordering. The set contribution is the previous test.
func TestDeclaredEnvHashIsDeterministic(t *testing.T) {
	cfg := Config{
		Env:             map[string]string{"B": "2", "A": "1"},
		DeclaredEnvKeys: []string{"B", "A", "MISSING"},
	}
	reordered := Config{
		Env:             map[string]string{"A": "1", "B": "2"},
		DeclaredEnvKeys: []string{"MISSING", "A", "B"},
	}
	if CoreFingerprint(cfg) != CoreFingerprint(reordered) {
		t.Error("declared-env hashing must not depend on key order")
	}
}

// Nil and empty must agree, matching the Env boundary the golden net already
// pins: a session whose config declares nothing is one identity, however the
// builder happened to represent "nothing".
func TestNilAndEmptyDeclaredEnvHashIdentically(t *testing.T) {
	nilCfg := Config{Env: map[string]string{"X": "1"}}
	emptyCfg := Config{Env: map[string]string{"X": "1"}, DeclaredEnvKeys: []string{}}
	if CoreFingerprint(nilCfg) != CoreFingerprint(emptyCfg) {
		t.Error("nil and empty DeclaredEnvKeys must hash identically")
	}
}

// The incident shape, end to end at the hash level. The city.toml edit replaced
// the mayor provider's `command = "claude-5"` pin with the pool script AND added
// [providers.claude-mayor.env] CLAUDE_ACCOUNTS. Command is launch-half, so with
// env contributing nothing the reconciler's launchOnlyDrift predicate held
// (stored provision == current provision, stored launch != current launch) and it
// took the warm-box relaunch -- which applies no env values, leaving the mayor
// binding to account5, outside its declared "0 4".
//
// The predicate is an inline expression in cmd/gc/session_reconciler.go rather
// than a function, so what is pinned here is the fact that makes it false: the
// provision halves must differ. If they ever agree again for this pair, that
// relaunch comes back.
func TestProviderEnvChangeIsNotLaunchOnlyDrift(t *testing.T) {
	before := Config{
		Command: "claude-5",
		Env:     map[string]string{"CLAUDE_POOL": "0 1 2 3 4 5"},
	}
	after := Config{
		Command:         "claude-pool.py",
		Env:             map[string]string{"CLAUDE_POOL": "0 1 2 3 4 5", "CLAUDE_ACCOUNTS": "0 4"},
		DeclaredEnvKeys: []string{"CLAUDE_ACCOUNTS"},
	}

	if LaunchFingerprint(before) == LaunchFingerprint(after) {
		t.Fatal("the command change must still move the launch half -- otherwise this fixture does not reproduce the drift at all")
	}
	if ProvisionFingerprint(before) == ProvisionFingerprint(after) {
		t.Error("provision halves agree, so the reconciler classifies this launch-only and warm-relaunches: the added env value never reaches the agent (ci-yulan1)")
	}
}
