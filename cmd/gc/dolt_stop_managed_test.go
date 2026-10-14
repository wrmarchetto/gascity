package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

func TestWaitForManagedDoltProcessExit(t *testing.T) {
	const pid = 4242
	tests := []struct {
		name         string
		timeout      time.Duration
		aliveThrough int
		wantCalls    int
		wantElapsed  time.Duration
	}{
		{name: "already dead", timeout: 45 * time.Millisecond, aliveThrough: 0, wantCalls: 1},
		{name: "exits after two polls", timeout: time.Second, aliveThrough: 2, wantCalls: 3, wantElapsed: 40 * time.Millisecond},
		{name: "never exits uses exact remainder", timeout: 45 * time.Millisecond, aliveThrough: -1, wantCalls: 4, wantElapsed: 45 * time.Millisecond},
		{name: "nonpositive bound probes once", timeout: 0, aliveThrough: -1, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				started := time.Now()
				waitForManagedDoltProcessExit(pid, tt.timeout, func(gotPID int) bool {
					if gotPID != pid {
						t.Fatalf("alive pid = %d, want %d", gotPID, pid)
					}
					calls++
					return tt.aliveThrough < 0 || calls <= tt.aliveThrough
				})

				if calls != tt.wantCalls {
					t.Errorf("alive calls = %d, want %d", calls, tt.wantCalls)
				}
				if elapsed := time.Since(started); elapsed != tt.wantElapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tt.wantElapsed)
				}
			})
		})
	}
}

func TestManagedDoltStopPollInterval(t *testing.T) {
	cases := []struct {
		name  string
		grace time.Duration
		want  time.Duration
	}{
		{"default grace keeps 500ms", 30 * time.Second, 500 * time.Millisecond},
		{"exactly 500ms keeps 500ms", 500 * time.Millisecond, 500 * time.Millisecond},
		{"sub-poll grace shrinks to grace", 200 * time.Millisecond, 200 * time.Millisecond},
		{"tiny grace shrinks to grace", 100 * time.Millisecond, 100 * time.Millisecond},
		{"zero grace keeps 500ms", 0, 500 * time.Millisecond},
		{"negative grace keeps 500ms", -1 * time.Second, 500 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedDoltStopPollInterval(tc.grace); got != tc.want {
				t.Errorf("managedDoltStopPollInterval(%v) = %v, want %v", tc.grace, got, tc.want)
			}
		})
	}
}

func TestResolveManagedDoltStopTimeoutDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`
[workspace]
name = "test"

[[agent]]
name = "mayor"
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	got := resolveManagedDoltStopTimeout(dir)
	if got != config.DefaultDoltStopTimeout {
		t.Errorf("resolveManagedDoltStopTimeout() = %v, want %v (default)", got, config.DefaultDoltStopTimeout)
	}
}

func TestResolveManagedDoltStopTimeoutCustom(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`
[workspace]
name = "test"

[daemon]
dolt_stop_timeout = "1m"

[[agent]]
name = "mayor"
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	got := resolveManagedDoltStopTimeout(dir)
	if got != time.Minute {
		t.Errorf("resolveManagedDoltStopTimeout() = %v, want 1m", got)
	}
}

func TestResolveManagedDoltStopTimeoutMissingCityFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	// No city.toml — loadCityConfig should fail and we should fall back.
	got := resolveManagedDoltStopTimeout(dir)
	if got != config.DefaultDoltStopTimeout {
		t.Errorf("resolveManagedDoltStopTimeout() with no city.toml = %v, want %v (default)", got, config.DefaultDoltStopTimeout)
	}
}

func TestResolveManagedDoltStopTimeoutEmptyCityPathReturnsDefault(t *testing.T) {
	// An empty cityPath must NOT trigger loadCityConfig("", …), which would
	// resolve "city.toml" relative to cwd and materialize builtin packs
	// there. Plant a stray ./city.toml with a non-default dolt_stop_timeout;
	// resolveManagedDoltStopTimeout("") must ignore it and return the default.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`
[workspace]
name = "stray"

[daemon]
dolt_stop_timeout = "1m"

[[agent]]
name = "mayor"
`), 0o644); err != nil {
		t.Fatalf("write stray city.toml: %v", err)
	}
	t.Chdir(dir)

	got := resolveManagedDoltStopTimeout("")
	if got != config.DefaultDoltStopTimeout {
		t.Errorf("resolveManagedDoltStopTimeout(\"\") = %v, want %v (default — must not read stray ./city.toml)", got, config.DefaultDoltStopTimeout)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gc")); err == nil {
		t.Error("resolveManagedDoltStopTimeout(\"\") materialized .gc/ under cwd; empty cityPath must not load config")
	}
}

func TestResolveManagedDoltStopTimeoutInvalidValueFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`
[workspace]
name = "test"

[daemon]
dolt_stop_timeout = "not-a-duration"

[[agent]]
name = "mayor"
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	got := resolveManagedDoltStopTimeout(dir)
	if got != config.DefaultDoltStopTimeout {
		t.Errorf("resolveManagedDoltStopTimeout() with invalid duration = %v, want %v (default)", got, config.DefaultDoltStopTimeout)
	}
}

// deletedCityScopeForTest builds a bd-provider city scope, then deletes it,
// returning the path that no longer exists. It is the state the dolt scope
// watchdog acts in: the scope-gone check has just proved the directory is
// gone, and everything it calls afterwards still names it.
//
// clearGCEnv supplies the GC_HOME that a production watchdog inherits.
// Without one EnsureBuiltinRuntimeAssets returns before the shim writer, so
// a test lacking it would pass against the defect it exists to catch.
func deletedCityScopeForTest(t *testing.T) string {
	t.Helper()
	clearGCEnv(t)
	city := filepath.Join(t.TempDir(), "scope")
	if err := os.MkdirAll(city, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(city, "city.toml"), []byte("[workspace]\nname = \"scope\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(city); err != nil {
		t.Fatal(err)
	}
	return city
}

// A config load names a city that does not exist, so it must leave none
// behind. The builtin-asset step ran before the read that reports the
// missing city.toml, and its shim writer MkdirAll'd <city>/.gc/scripts back
// into existence (ci-0z2h08).
func TestLoadCityConfigOfDeletedScopeRecreatesNothing(t *testing.T) {
	city := deletedCityScopeForTest(t)
	if _, err := loadCityConfig(city, io.Discard); err == nil {
		t.Fatalf("loadCityConfig(%s) of a deleted scope succeeded", city)
	}
	if _, err := os.Stat(city); !os.IsNotExist(err) {
		t.Fatalf("stat %s after loading a deleted scope: err = %v, want not-exist", city, err)
	}
}

// The scope watchdog's reap path resolves the stop grace from the scope it
// has just proved deleted. It must fall back to the default and recreate
// nothing -- this is the call that left /var/tmp gate worktrees holding a
// fresh .gc/scripts/gc-beads-bd.sh 42s after removal (ci-0z2h08).
//
// Pinned at this call rather than through a watchdog process, because the
// suite's watchdog is a re-exec of the TEST binary and cannot represent the
// defect. Measured: with the guard removed, a test-binary watchdog given a
// real scope and GC_HOME reaped and recreated nothing, and strace showed it
// never touched GC_HOME. That end-to-end test passed against the defect and
// was deleted. The production path was instead checked by hand with real
// binaries, a fake dolt on PATH and the scope rm -rf'd: the installed gc
// recreated <scope>/.gc/scripts/gc-beads-bd.sh and this branch's build did
// not.
func TestResolveManagedDoltStopTimeoutForDeletedScopeRecreatesNothing(t *testing.T) {
	city := deletedCityScopeForTest(t)
	if got := resolveManagedDoltStopTimeout(city); got != config.DefaultDoltStopTimeout {
		t.Errorf("resolveManagedDoltStopTimeout(deleted) = %v, want default %v", got, config.DefaultDoltStopTimeout)
	}
	if _, err := os.Stat(city); !os.IsNotExist(err) {
		t.Fatalf("stat %s after resolving its stop timeout: err = %v, want not-exist", city, err)
	}
}
