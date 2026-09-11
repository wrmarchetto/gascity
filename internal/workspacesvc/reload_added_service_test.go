// internal/workspacesvc/reload_added_service_test.go
//
// Fork-owned coverage for growing the [[service]] set across a reload.
//
// A separate file rather than an addition to proxy_process_test.go, which
// upstream owns: this fork proposes nothing upstream, so every edit to an
// upstream-owned path is carried forever and owes a retirement probe, while a
// branch of new files owes none.  The probe would also be meaningless here --
// it reverts a patch's production changes and re-runs its tests, and this
// patch has no production change to revert.
//
// Same package on purpose.  It reuses testRuntime, setHelperPassthrough and
// TestProxyProcessHelper rather than restating them; a second copy of the
// helper is how the child-environment assertions would drift apart.

package workspacesvc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestManagerReloadStartsAServiceBlockAddedAfterTheFirstReload pins the
// answer to "does a newly added [[service]] block need a supervisor restart,
// or does gc reload bring it up?".
//
// It is reload, and the question was live rather than rhetorical: the
// dashboard's own bind is a known exception to "reload is enough", so the
// city could not assume either direction for services. The existing coverage
// did not settle it. TestManagerReloadProxyProcessStartsAndProxies starts a
// service from a FRESH manager, TestManagerReloadReusesUnchangedInstances
// reloads the same set twice, and TestManagerReloadClosesChangedInstances
// mutates a block already present -- none of them grows the set, which is the
// shape an operator actually edits city.toml into.
//
// The witness is GC_SERVICE_SOCKET, read back from each child's own environ,
// not the manager's status. A status assertion would go green on an entry the
// manager constructed and never spawned, which is the failure this exists to
// exclude; the socket path is unique per instance and can only be reported by
// a process that is running. The first service's socket is required to be
// UNCHANGED in the same breath, because "reload starts the new one" would be
// a poor trade if it also bounced the adapter that was already serving --
// that is the reuse arm of Reload, asserted here against the live child
// rather than against a call count.
func TestManagerReloadStartsAServiceBlockAddedAfterTheFirstReload(t *testing.T) {
	t.Setenv("GC_SERVICE_HELPER", "1")
	setHelperPassthrough(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}

	helper := func(name string) config.Service {
		return config.Service{
			Name: name,
			Kind: "proxy_process",
			Process: config.ServiceProcessConfig{
				Command:    []string{exe, "-test.run=^TestProxyProcessHelper$", "--"},
				HealthPath: "/healthz",
			},
		}
	}

	rt := &testRuntime{
		cityPath: t.TempDir(),
		cityName: "test-city",
		cfg:      &config.City{Services: []config.Service{helper("adapter")}},
		sp:       runtime.NewFake(),
		store:    beads.NewMemStore(),
	}

	mgr := NewManager(rt)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	defer mgr.Close() //nolint:errcheck // best-effort cleanup

	// The child is spawned by Reload but reaches its own listener
	// asynchronously, so every read here retries. It returns "" rather than
	// failing on a miss: the second service's absence before the config
	// change is the expected state, and the caller distinguishes the two.
	socketOf := func(name string) string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			req := httptest.NewRequest(http.MethodGet, "/svc/"+name+"/env", nil)
			rec := httptest.NewRecorder()
			if ok := mgr.ServeHTTP(rec, req); ok && rec.Code == http.StatusOK {
				var env map[string]string
				if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
					t.Fatalf("decode %s env: %v", name, err)
				}
				return env["GC_SERVICE_SOCKET"]
			}
			if time.Now().After(deadline) {
				return ""
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	adapterBefore := socketOf("adapter")
	if adapterBefore == "" {
		t.Fatal("adapter did not report GC_SERVICE_SOCKET after the first reload")
	}

	rt.cfg = &config.City{Services: []config.Service{helper("adapter"), helper("mirror")}}
	if err := mgr.Reload(); err != nil {
		t.Fatalf("second Reload: %v", err)
	}

	mirror := socketOf("mirror")
	if mirror == "" {
		t.Fatal("service added to the config was not running after reload; " +
			"a newly added [[service]] block would need a supervisor restart")
	}
	if got := socketOf("adapter"); got != adapterBefore {
		t.Fatalf("adapter GC_SERVICE_SOCKET = %q after reload, want %q unchanged; "+
			"adding a service block restarted an unrelated one", got, adapterBefore)
	}
	if mirror == adapterBefore {
		t.Fatalf("both services report GC_SERVICE_SOCKET %q, so the witness "+
			"cannot tell them apart", mirror)
	}
}
