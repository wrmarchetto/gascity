package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Salted per run, so a wiring that answers a constant Location -- or one that
// drops the configured value and falls back to some default -- cannot pass.
var rootRedirectProbe = fmt.Sprintf("/v0/city/probe%d/svc/console/", os.Getpid()%9973+3)

// rootStatus drives one loopback request through the mux attachDashboard
// filled and returns (status, Location). A recorder rather than a listener,
// so the 302 is observed directly and the untagged http_test_server census
// ratchet is not raised. Host and RemoteAddr are loopback because the
// supervisor's host guard answers 421 to the recorder's default example.com.
func rootStatus(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

// TestAttachDashboardRootRedirectReachesTheRootInBothPostures pins the
// wiring, which is where a configured value is most easily dropped: the
// redirect has to fill the "/" slot whether or not the SPA does. The
// disabled posture is the one this field was added for -- with the SPA off
// and no redirect the bare root is a plain 404 -- and the enabled posture
// must keep every SPA client route while giving up only the root.
func TestAttachDashboardRootRedirectReachesTheRootInBothPostures(t *testing.T) {
	for _, posture := range []struct {
		name, env  string
		clientPath int // status the SPA client route must answer
	}{
		{"dashboard disabled", "0", http.StatusNotFound},
		{"dashboard enabled", "", http.StatusOK},
	} {
		t.Run(posture.name, func(t *testing.T) {
			t.Setenv("GC_SUPERVISOR_DASHBOARD", posture.env)
			mux := newTestSupervisorMuxForDashboard()
			var warn bytes.Buffer
			if _, err := attachDashboard(mux, fakeDashResolver{}, false, "127.0.0.1", 8372, rootRedirectProbe, &warn); err != nil {
				t.Fatalf("attachDashboard: %v", err)
			}
			if warn.Len() != 0 {
				t.Fatalf("a valid root_redirect produced a warning: %q", warn.String())
			}
			if code, loc := rootStatus(t, mux.Handler(), "/"); code != http.StatusFound || loc != rootRedirectProbe {
				t.Fatalf("GET / = %d Location %q, want 302 to %q", code, loc, rootRedirectProbe)
			}
			if code, loc := rootStatus(t, mux.Handler(), "/city/alpha/agents"); code != posture.clientPath || loc != "" {
				t.Fatalf("GET /city/alpha/agents = %d Location %q, want %d and no redirect", code, loc, posture.clientPath)
			}
		})
	}
}

// TestAttachDashboardRefusedRootRedirectWarnsAndKeepsTheSlot pins the
// failure arm. A bad value is a WARNING, not a boot error: the supervisor
// is one process for every city, and refusing to start over a cosmetic
// redirect would take every city down for a typo. The warning must name the
// value and the remedy, and the slot must fall back to exactly what it holds
// with no redirect configured -- the SPA when enabled, 404 when not.
func TestAttachDashboardRefusedRootRedirectWarnsAndKeepsTheSlot(t *testing.T) {
	const bad = "//evil.example/"
	for _, posture := range []struct {
		name, env string
		root      int
	}{
		{"dashboard disabled", "0", http.StatusNotFound},
		{"dashboard enabled", "", http.StatusOK},
	} {
		t.Run(posture.name, func(t *testing.T) {
			t.Setenv("GC_SUPERVISOR_DASHBOARD", posture.env)
			mux := newTestSupervisorMuxForDashboard()
			var warn bytes.Buffer
			if _, err := attachDashboard(mux, fakeDashResolver{}, false, "127.0.0.1", 8372, bad, &warn); err != nil {
				t.Fatalf("attachDashboard must not fail the boot over root_redirect: %v", err)
			}
			msg := warn.String()
			if !strings.Contains(msg, "root_redirect") || !strings.Contains(msg, fmt.Sprintf("%q", bad)) || !strings.Contains(msg, "gc supervisor stop && gc supervisor start") {
				t.Fatalf("warning %q must name root_redirect, the refused value and the restart remedy", msg)
			}
			if code, loc := rootStatus(t, mux.Handler(), "/"); code != posture.root || loc != "" {
				t.Fatalf("GET / = %d Location %q, want %d and no redirect", code, loc, posture.root)
			}
		})
	}
}
