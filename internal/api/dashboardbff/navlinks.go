package dashboardbff

import (
	"fmt"
	"strings"
)

// NavLink is one operator-declared entry in the dashboard navbar, pointing at
// a page the SPA does not itself route -- a workspace service, an external
// tool, a wiki. The SPA's own views are registered in its frontend view
// registry and never come through here.
//
// This exists because the navbar is otherwise a compile-time list, and a
// deployment's own pages are deployment data: baking one into Go or TSX would
// put a specific service name in SDK source, which AGENTS.md forbids for the
// same reason it forbids role names. The projection follows AccountLabels --
// env in, neutral default out -- rather than a config.City field, because the
// /api plane deliberately does not import internal/config and a nav entry is
// not worth that dependency.
type NavLink struct {
	Label string
	Href  string
}

// ParseNavLinks reads the DASHBOARD_NAV_LINKS spec: comma-separated
// `Label=href` pairs, in the order they are to appear after the SPA's own
// routes. Returns (links, rejected) where rejected holds one human-readable
// diagnosis per entry that was dropped, for the caller to log.
//
// Returning the rejections rather than dropping them silently is the one
// deliberate departure from ParseAccountLabels. A silently dropped nav link
// is invisible from every side: the operator edits the unit file, restarts
// the supervisor, and finds the navbar unchanged with nothing naming the
// typo. Supervisor start is the only moment the spec is read, so it is the
// only moment the complaint can be made.
//
// Absences worth knowing. There is no order field -- declaration order is the
// order, see TestParseNavLinksPreservesDeclarationOrder. There is no target or
// new-tab control: a nav link navigates in place like every other navbar
// entry, and an operator who wants a new tab has the browser's own
// middle-click. And neither half can contain a comma, since the comma is the
// entry separator -- a href needing one percent-encodes it as %2C.
func ParseNavLinks(spec string) (links []NavLink, rejected []string) {
	for _, entry := range strings.Split(spec, ",") {
		if strings.TrimSpace(entry) == "" {
			// An empty entry is how an unset, whitespace-only or
			// trailing-comma spec arrives. It is not a mistake worth
			// reporting, and reporting it would warn on every start of
			// every supervisor that declares no links at all.
			continue
		}
		// Cut on the FIRST "=" so a query string survives in the href. The
		// cost is that a label cannot contain "=", which no label needs.
		label, href, found := strings.Cut(entry, "=")
		label, href = strings.TrimSpace(label), strings.TrimSpace(href)
		switch {
		case !found:
			rejected = append(rejected, fmt.Sprintf("%q: no %q separating label from href", entry, "="))
		case label == "":
			rejected = append(rejected, fmt.Sprintf("%q: empty label", entry))
		case href == "":
			rejected = append(rejected, fmt.Sprintf("%q: empty href", entry))
		case !safeNavHref(href):
			rejected = append(rejected, fmt.Sprintf("%q: href must be an absolute same-origin path (/...) or an http:// or https:// URL", entry))
		default:
			links = append(links, NavLink{Label: label, Href: href})
		}
	}
	return links, rejected
}

// safeNavHref reports whether href may be placed in an <a href> on the
// dashboard. The dashboard port carries no authentication at any bind, so an
// unchecked href is a stored-XSS primitive for anything that can reach the
// supervisor's environment -- `javascript:` and `data:` being the obvious two.
//
// The rejected simpler check is `strings.HasPrefix(href, "/")`, which admits
// `//evil.test/p`: a protocol-relative URL that leaves the origin entirely
// while looking like a path. Hence the explicit second-character test.
//
// Scheme matching is case-folded to ACCEPT `HTTPS://host/p`, which is a
// legal spelling an operator may well type. It is not a bypass defense and
// must not be read as one: this is an allowlist, so dropping the fold would
// fail closed on uppercase rather than open on `JavaScript:`. Established by
// mutation, not by reading -- removing the fold leaves every rejection test
// green (see TestParseNavLinksAcceptsUppercaseHTTPScheme).
func safeNavHref(href string) bool {
	if strings.HasPrefix(href, "/") {
		return !strings.HasPrefix(href, "//")
	}
	lower := strings.ToLower(href)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
