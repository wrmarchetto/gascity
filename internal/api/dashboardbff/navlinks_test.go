package dashboardbff

import "testing"

// Scope: the DASHBOARD_NAV_LINKS spec parser alone. The wire shape of the
// parsed result is pinned by config_test.go and wire_contract_test.go, and
// the rendering of it by the frontend's Header.navlinks.test.tsx. This suite
// exists because the parser is the only place an operator's spec is judged:
// everything downstream treats a NavLink as already trusted, so a href this
// accepts reaches an href attribute on an open LAN port unexamined.
//
// Run: go test ./internal/api/dashboardbff/ -run NavLinks

func TestParseNavLinksReadsLabelHrefPairs(t *testing.T) {
	links, rejected := ParseNavLinks("Summaries=/svc/roadmap/summaries,Wiki=https://example.test/w")
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	want := []NavLink{
		{Label: "Summaries", Href: "/svc/roadmap/summaries"},
		{Label: "Wiki", Href: "https://example.test/w"},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %v, want %v", links, want)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("links[%d] = %v, want %v", i, links[i], want[i])
		}
	}
}

// Declaration order IS the nav order. There is deliberately no order field:
// the links append after the SPA's own routes, and the only ordering an
// operator can express is the order they type. A numeric order would have to
// interleave with EXPLICIT_ROUTES' orders in Header.tsx, which are a frontend
// implementation detail no env spec should have to know.
func TestParseNavLinksPreservesDeclarationOrder(t *testing.T) {
	links, _ := ParseNavLinks("C=/c,A=/a,B=/b")
	got := ""
	for _, l := range links {
		got += l.Label
	}
	if got != "CAB" {
		t.Errorf("label order = %q, want CAB", got)
	}
}

// An href splits on the FIRST "=" so a query string survives. The absence
// that buys: a label cannot contain "=", and neither half can contain a
// comma -- a comma in a href must be percent-encoded as %2C.
func TestParseNavLinksSplitsOnFirstEqualsSoQueryStringsSurvive(t *testing.T) {
	links, rejected := ParseNavLinks("Report=/svc/r?tab=open&sort=age")
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	if len(links) != 1 || links[0].Href != "/svc/r?tab=open&sort=age" {
		t.Fatalf("links = %v, want one entry keeping the whole query", links)
	}
}

// The parser is the XSS boundary. The dashboard port carries no
// authentication at any bind, so a href reaching an <a href> unchecked is a
// script-execution primitive for anything that can set the env. Only a
// same-origin absolute path or an explicit http(s) URL is a link.
func TestParseNavLinksRefusesHrefsThatAreNotHTTPOrAbsolutePath(t *testing.T) {
	cases := map[string]string{
		"javascript scheme": "X=javascript:alert(1)",
		// Comma-free on purpose: a comma is the entry separator, so the
		// usual `data:text/html,<script>` spelling would arrive here as two
		// entries and this case would stop being about the scheme at all.
		"data scheme":     "X=data:text/html;base64/PHNjcmlwdD4=",
		"vbscript scheme": "X=vbscript:msgbox(1)",
		// A protocol-relative href silently leaves the origin: "//evil.test"
		// resolves against the page scheme and is NOT a same-origin path,
		// which is exactly what a bare leading-slash check would miss.
		"protocol relative": "X=//evil.test/p",
		"bare relative":     "X=svc/roadmap",
		"scheme-only":       "X=ftp://files.test/p",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			links, rejected := ParseNavLinks(spec)
			if len(links) != 0 {
				t.Errorf("links = %v, want none", links)
			}
			if len(rejected) != 1 {
				t.Fatalf("rejected = %v, want exactly one diagnosis", rejected)
			}
		})
	}
}

// What the case-fold in safeNavHref actually buys. Written after a mutation
// sweep showed that removing the fold killed NO test: the fold is on an
// ALLOWLIST, so its absence fails closed on a legal uppercase scheme rather
// than open on `JavaScript:`. This is the only test that dies when the fold
// goes, so it is the only evidence the fold is load-bearing.
func TestParseNavLinksAcceptsUppercaseHTTPScheme(t *testing.T) {
	for _, spec := range []string{"X=HTTPS://example.test/w", "X=Http://example.test/w"} {
		links, rejected := ParseNavLinks(spec)
		if len(links) != 1 {
			t.Errorf("%q: links = %v, want one accepted link", spec, links)
		}
		if len(rejected) != 0 {
			t.Errorf("%q: rejected = %v, want none", spec, rejected)
		}
	}
}

// An uppercase or whitespace-padded dangerous scheme stays refused. These
// pass under a case-sensitive check too -- they are here to pin the refusal
// against a future rewrite of the guard, not to evidence the case-fold.
func TestParseNavLinksRefusesDangerousSchemeSpellings(t *testing.T) {
	for _, spec := range []string{"X=JavaScript:alert(1)", "X=  javascript:alert(1)", "X=jAvAsCrIpT:alert(1)"} {
		links, rejected := ParseNavLinks(spec)
		if len(links) != 0 {
			t.Errorf("%q: links = %v, want none", spec, links)
		}
		if len(rejected) != 1 {
			t.Errorf("%q: rejected = %v, want exactly one diagnosis", spec, rejected)
		}
	}
}

// A malformed entry must be diagnosable. ParseAccountLabels, the parser this
// one is modeled on, drops a bad entry silently -- which here would mean the
// operator sets the var, restarts the supervisor and sees no new nav entry
// with nothing anywhere saying why. The rejection text is the remedy.
func TestParseNavLinksReportsEachRejectedEntry(t *testing.T) {
	links, rejected := ParseNavLinks("Good=/ok,NoHref=,=/no-label,Bad=javascript:alert(1),bare")
	if len(links) != 1 || links[0].Label != "Good" {
		t.Fatalf("links = %v, want only the Good entry", links)
	}
	if len(rejected) != 4 {
		t.Fatalf("rejected = %v, want four diagnoses", rejected)
	}
	for _, r := range rejected {
		if r == "" {
			t.Error("a rejection must carry text naming the offending entry")
		}
	}
}

// Unset and whitespace-only are the overwhelmingly common case (no operator
// has declared a link). Both must produce no links and -- importantly -- no
// rejections, or every supervisor start logs a warning about an empty var.
func TestParseNavLinksTreatsUnsetAsNoLinksAndNoComplaint(t *testing.T) {
	for _, spec := range []string{"", "   ", ",", " , , "} {
		links, rejected := ParseNavLinks(spec)
		if links != nil {
			t.Errorf("%q: links = %v, want nil", spec, links)
		}
		if len(rejected) != 0 {
			t.Errorf("%q: rejected = %v, want none", spec, rejected)
		}
	}
}

func TestParseNavLinksTrimsSurroundingWhitespace(t *testing.T) {
	links, rejected := ParseNavLinks("  Summaries  =  /svc/roadmap/summaries  ")
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	if len(links) != 1 || links[0].Label != "Summaries" || links[0].Href != "/svc/roadmap/summaries" {
		t.Fatalf("links = %v, want trimmed label and href", links)
	}
}
