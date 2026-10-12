package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/api/dashboardspa"
)

// The redirect target is salted per run so a handler that answers a constant
// Location, or a stale build answering some other operator's path, cannot
// satisfy the Location assertions below.
var rootRedirectTarget = fmt.Sprintf("/v0/city/probe%d/svc/console/", os.Getpid()%9973+3)

// rootRedirectMux mounts the handler on a real SupervisorMux and returns its
// full middleware chain. Requests are driven through a recorder rather than
// a listener: a recorder sees the 302 itself, where a client would have to be
// told not to follow it, and the untagged http_test_server census is a
// ratchet this suite has no reason to raise.
func rootRedirectMux(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	h, err := api.NewRootRedirectHandler(rootRedirectTarget, next)
	if err != nil {
		t.Fatalf("NewRootRedirectHandler(%q): %v", rootRedirectTarget, err)
	}
	mux := api.NewSupervisorMux(
		dashTestResolver{cities: []api.CityInfo{{Name: "alpha", Path: "/srv/alpha", Running: true}}},
		nil, false, "vtest", "btest", time.Now(),
	)
	mux.WithStaticHandler(h)
	return mux.Handler()
}

// serve drives one loopback request through h. Host and RemoteAddr are set
// to loopback because the supervisor's host guard answers 421 to the
// recorder's default example.com, which would read as a refused redirect.
func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRootRedirectSendsTheBareRootToItsTarget pins the whole point of the
// handler: a browser typing host:port with no path lands on the configured
// page. The status is pinned to 302 exactly, not "any 3xx" -- a 301 is cached
// by the browser with no expiry, so an operator who later re-enables the SPA
// or changes the target would keep being sent to the old page with nothing
// on the server left to correct it.
func TestRootRedirectSendsTheBareRootToItsTarget(t *testing.T) {
	h := rootRedirectMux(t, nil)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		resp := serve(h, method, "/")
		if resp.Code != http.StatusFound {
			t.Fatalf("%s / status = %d, want %d", method, resp.Code, http.StatusFound)
		}
		if got := resp.Header().Get("Location"); got != rootRedirectTarget {
			t.Fatalf("%s / Location = %q, want %q", method, got, rootRedirectTarget)
		}
	}
}

// TestRootRedirectClaimsOnlyTheBareRoot pins the other half: the handler sits
// in the "/" catch-all slot, so it sees every unmatched path, and anything it
// answered besides the bare root would shadow the SPA's client routes or turn
// a stale /v0 caller's 404 into a redirect. Driven with and without the SPA
// behind it, because those are the two postures the supervisor mounts it in.
func TestRootRedirectClaimsOnlyTheBareRoot(t *testing.T) {
	t.Run("SPA behind it", func(t *testing.T) {
		spa, err := dashboardspa.NewStaticHandler()
		if err != nil {
			t.Fatalf("NewStaticHandler: %v", err)
		}
		h := rootRedirectMux(t, spa)
		resp := serve(h, http.MethodGet, "/city/alpha/agents")
		if resp.Code != http.StatusOK || resp.Header().Get("Location") != "" ||
			!strings.Contains(resp.Body.String(), `id="root"`) {
			t.Fatalf("SPA client route status=%d Location=%q, want the SPA shell and no redirect",
				resp.Code, resp.Header().Get("Location"))
		}
		assertTypedV0Served(t, h)
		// A non-GET to the root is not a browser arriving; it belongs to
		// whatever owns the slot behind the redirect.
		post := serve(h, http.MethodPost, "/")
		if post.Code == http.StatusFound {
			t.Fatalf("POST / was redirected; only GET and HEAD are a browser arriving")
		}
	})
	t.Run("nothing behind it", func(t *testing.T) {
		h := rootRedirectMux(t, nil)
		for _, path := range []string{"/city/alpha/agents", "/index.html", "/x/"} {
			resp := serve(h, http.MethodGet, path)
			if resp.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d (Location %q), want 404",
					path, resp.Code, resp.Header().Get("Location"))
			}
		}
		assertTypedV0Served(t, h)
	})
}

// assertTypedV0Served proves the typed API still wins over the "/" slot.
// The city name is asserted, not only the status, so a catch-all answering
// 200 with its own body cannot pass.
func assertTypedV0Served(t *testing.T, h http.Handler) {
	t.Helper()
	resp := serve(h, http.MethodGet, "/v0/cities")
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "alpha") {
		t.Fatalf("GET /v0/cities status=%d body=%.200q, want 200 listing alpha", resp.Code, resp.Body.String())
	}
}

// TestRootRedirectRefusesTargetsThatLeaveThisListener pins the open-redirect
// guard. Every refused spelling here is one a browser resolves OFF this
// origin -- "//host", "///host" and "/\host" are all scheme-relative to a
// browser, and the second is a plain path to url.Parse -- or one that never
// leaves the root at all, which is a redirect loop the browser reports as a
// broken site rather than as a config typo.
func TestRootRedirectRefusesTargetsThatLeaveThisListener(t *testing.T) {
	refused := []string{
		"",
		"/",
		"//evil.example/x",
		"///evil.example/x",
		"/?back=1",
		`/\evil.example/x`,
		"http://evil.example/",
		"https://dbz1.lan:8372/v0/city/city/svc/console/",
		"v0/city/city/svc/console/",
		" /v0/city/city/svc/console/",
		"/v0/city/city/svc/console/\n",
		// Interior whitespace is the one spelling only the whitespace guard
		// refuses: a leading space fails the prefix test and a control byte
		// fails url.Parse, so neither of the two above can pin it.
		"/v0/city/city/svc/con sole/",
	}
	for _, target := range refused {
		if _, err := api.NewRootRedirectHandler(target, nil); err == nil {
			t.Errorf("NewRootRedirectHandler(%q) accepted a target that leaves this listener or loops", target)
		} else if !strings.Contains(err.Error(), fmt.Sprintf("%q", target)) {
			t.Errorf("NewRootRedirectHandler(%q) error %q does not name the refused value", target, err)
		}
	}
	for _, target := range []string{rootRedirectTarget, rootRedirectTarget + "#now", "/v0/cities?x=1"} {
		if _, err := api.NewRootRedirectHandler(target, nil); err != nil {
			t.Errorf("NewRootRedirectHandler(%q) refused a same-listener path: %v", target, err)
		}
	}
}
