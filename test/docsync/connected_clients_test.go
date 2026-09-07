package docsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api"
)

const connectedClientsGuidePage = "guides/connected-clients"

func TestConnectedClientsGuideIsInGuidesNavigation(t *testing.T) {
	root := repoRoot()
	configPath := filepath.Join(root, "docs", "docs.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading docs.json: %v", err)
	}

	var decoded struct {
		Navigation struct {
			Groups []struct {
				Group string `json:"group"`
				Pages []any  `json:"pages"`
			} `json:"groups"`
		} `json:"navigation"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parsing docs.json: %v", err)
	}

	for _, group := range decoded.Navigation.Groups {
		if group.Group != "Guides" {
			continue
		}
		if !containsMintPage(group.Pages, connectedClientsGuidePage) {
			t.Fatalf("docs.json Guides navigation must include %q", connectedClientsGuidePage)
		}
		return
	}

	t.Fatalf("docs.json navigation is missing the Guides group")
}

func TestConnectedClientsAPIReferenceDocumentsEndpointFamily(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", "reference", "api.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading docs/reference/api.md: %v", err)
	}
	text := string(data)

	for _, endpoint := range []string{
		"POST /v0/city/{city}/extmsg/adapters",
		"POST /v0/city/{city}/extmsg/inbound",
		"POST /v0/city/{city}/extmsg/outbound",
	} {
		if !strings.Contains(text, endpoint) {
			t.Errorf("docs/reference/api.md must document %s", endpoint)
		}
	}

	if !strings.Contains(strings.ToLower(text), "out-of-process") {
		t.Errorf("docs/reference/api.md must state that external messaging adapters are out-of-process")
	}
}

func TestConnectedClientsGuideRoutesAreRegistered(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", connectedClientsGuidePage+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.ToSlash(filepath.Join("docs", connectedClientsGuidePage+".md")), err)
	}

	routes := documentedCityRoutes(string(data))
	wantRoutes := map[string]bool{
		"POST /v0/city/{city}/extmsg/adapters": true,
		"POST /v0/city/{city}/extmsg/inbound":  true,
		"POST /v0/city/{city}/extmsg/outbound": true,
	}
	if len(routes) != len(wantRoutes) {
		t.Errorf("%s documents city routes %v, want %v", connectedClientsGuidePage+".md", routes, mapKeys(wantRoutes))
	}
	for route := range wantRoutes {
		if !routes[route] {
			t.Errorf("%s must document %s", connectedClientsGuidePage+".md", route)
		}
	}

	sm := api.NewSupervisorMux(connectedClientsEmptyResolver{}, nil, false, "", "", time.Time{})
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	sm.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json returned %d: %s", rec.Code, rec.Body.String())
	}

	var openAPI struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &openAPI); err != nil {
		t.Fatalf("parsing live OpenAPI: %v", err)
	}
	for documented := range routes {
		method, path, _ := strings.Cut(documented, " ")
		path = strings.Replace(path, "{city}", "{cityName}", 1)
		if _, ok := openAPI.Paths[path][strings.ToLower(method)]; !ok {
			t.Errorf("%s documents %s, but the live router does not register it", connectedClientsGuidePage+".md", documented)
		}
	}
}

func TestConnectedClientsGuideDocumentsRequiredSections(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", connectedClientsGuidePage+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.ToSlash(filepath.Join("docs", connectedClientsGuidePage+".md")), err)
	}

	headings := markdownHeadings(string(data))
	for _, section := range []struct {
		name    string
		pattern string
	}{
		{name: "register adapter", pattern: "register"},
		{name: "receive outbound messages", pattern: "outbound"},
		{name: "submit inbound messages", pattern: "inbound"},
		{name: "restart", pattern: "restart"},
	} {
		if !hasHeadingContaining(headings, section.pattern) {
			t.Errorf("%s must include a %q section heading", connectedClientsGuidePage+".md", section.name)
		}
	}
}

var (
	headingRE             = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	documentedCityRouteRE = regexp.MustCompile("(?m)`(GET|POST|PUT|PATCH|DELETE)\\s+(/v0/city/\\{city\\}/[^`\\s]+)`")
)

type connectedClientsEmptyResolver struct{}

func (connectedClientsEmptyResolver) ListCities() []api.CityInfo { return nil }

func (connectedClientsEmptyResolver) CityState(_ string) api.State { return nil }

func documentedCityRoutes(content string) map[string]bool {
	routes := make(map[string]bool)
	for _, match := range documentedCityRouteRE.FindAllStringSubmatch(content, -1) {
		routes[match[1]+" "+match[2]] = true
	}
	return routes
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}

func containsMintPage(pages []any, want string) bool {
	for _, page := range pages {
		switch x := page.(type) {
		case string:
			if x == want {
				return true
			}
		case map[string]any:
			if nested, ok := x["pages"].([]any); ok && containsMintPage(nested, want) {
				return true
			}
		}
	}
	return false
}

func markdownHeadings(content string) []string {
	matches := headingRE.FindAllStringSubmatch(content, -1)
	headings := make([]string, 0, len(matches))
	for _, match := range matches {
		heading := strings.TrimSpace(match[1])
		heading = strings.Trim(heading, "`*_ ")
		headings = append(headings, strings.ToLower(heading))
	}
	return headings
}

func hasHeadingContaining(headings []string, pattern string) bool {
	for _, heading := range headings {
		if strings.Contains(heading, pattern) {
			return true
		}
	}
	return false
}
