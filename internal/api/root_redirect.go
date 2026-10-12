package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// NewRootRedirectHandler returns a handler for the supervisor's "/" slot that
// answers a GET or HEAD of exactly "/" with a 302 to target and passes every
// other request to next (404 when next is nil).
//
// It occupies the slot WithStaticHandler already owns rather than adding a
// mount of its own, so the set of raw surfaces pinned by
// TestSupervisorNonHumaSurfacesAreSanctioned is unchanged: with the SPA
// enabled it wraps the SPA, with the SPA disabled it is the whole slot.
//
// 302 and not 301: a 301 is cached by the browser with no expiry, so an
// operator who later changes the target, or re-enables the SPA, keeps being
// sent to the old page with nothing on the server able to correct it.
//
// target must be an absolute path on this listener. A scheme, a host, a
// leading "//" or "/\" (both scheme-relative to a browser), whitespace, or
// "/" itself (a redirect loop) is refused. An off-origin target would make
// this an open redirect on an unauthenticated port.
func NewRootRedirectHandler(target string, next http.Handler) (http.Handler, error) {
	if err := validateRootRedirect(target); err != nil {
		return nil, err
	}
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// validateRootRedirect is the open-redirect guard. The prefix test is the
// whole of it: a target carrying a scheme or a host cannot begin with a
// single "/", so a separate Scheme/Host check on the parsed URL would be
// unreachable. The "//" test must stay a string test and NOT move onto the
// parsed URL -- url.Parse reads "///evil.example" as an empty host and the
// path "/evil.example", while a browser follows it off-origin.
func validateRootRedirect(target string) error {
	if strings.ContainsAny(target, " \t\r\n\v\f\\") {
		return fmt.Errorf("root redirect %q contains whitespace or a backslash", target)
	}
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return fmt.Errorf("root redirect %q is not an absolute path on this listener", target)
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("root redirect %q: %w", target, err)
	}
	if u.Path == "/" {
		return fmt.Errorf("root redirect %q points at the root itself, a redirect loop", target)
	}
	return nil
}
