// Guards the one seam ci-azvlhn broke: the city NAME travels from this
// package's env exports into a JavaScript consumer, so no Go-only and no
// JS-only test can see the two halves disagree. Both suites were green while
// the contract was broken -- proxy_process_test asserted GC_CITY was the city
// PATH, entrypoints.test.mjs passed GC_CITY: 'lab' as a NAME, and nothing
// asserted that the value one produces is a value the other can consume.
//
// The consumer's parse is read out of its own source rather than restated
// here. A copy of the pattern in Go would agree with itself forever while the
// bridge drifted, which is the failure mode this file exists to remove.
//
// Run: go test ./internal/workspacesvc/ -run TestServiceURLPrefix
package workspacesvc

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/citylayout"
)

// bridgeClientPath is the shared glue every openclaw bridge entrypoint routes
// its city-name resolution through.
const bridgeClientPath = "../../contrib/openclaw-bridge/lib/gc-client.mjs"

// bridgeMountPattern lifts the SERVICE_MOUNT_CITY regex literal out of the
// bridge source and translates it to Go's syntax. The two differ only in
// JavaScript's escaping of "/" inside a literal, which Go does not require.
func bridgeMountPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	source, err := os.ReadFile(bridgeClientPath)
	if err != nil {
		t.Fatalf("read bridge client: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^const SERVICE_MOUNT_CITY = /(.+)/$`).FindSubmatch(source)
	if decl == nil {
		t.Fatalf("no SERVICE_MOUNT_CITY declaration in %s; the bridge no longer derives the city name from the service mount, so this guard is measuring nothing", bridgeClientPath)
	}
	return regexp.MustCompile(strings.ReplaceAll(string(decl[1]), `\/`, `/`))
}

func TestServiceURLPrefixIsParsableByTheBridge(t *testing.T) {
	pattern := bridgeMountPattern(t)
	// Distinct from the service name, and neither is a substring of the other:
	// a parse that returned the wrong segment would otherwise still match.
	const cityName, serviceName = "test-city", "slack-bridge"
	prefix := citylayout.PublicServiceMountPath(cityName, serviceName)
	match := pattern.FindStringSubmatch(prefix)
	if match == nil {
		t.Fatalf("the bridge's pattern %s does not match the prefix this package exports (%q); a service child would fall through to GC_CITY, which is the city PATH, and 404 on every request", pattern, prefix)
	}
	if match[1] != cityName {
		t.Fatalf("bridge parses city name %q from %q, want %q", match[1], prefix, cityName)
	}
}

func TestServiceURLPrefixPatternRejectsOtherShapes(t *testing.T) {
	// Proves the pattern is not `.*`-shaped. Without this, a pattern that
	// matched anything would satisfy the test above and hand the bridge a
	// garbage name instead of letting it refuse.
	pattern := bridgeMountPattern(t)
	for _, notAMount := range []string{
		"/svc/slack-bridge",
		"/not/a/mount/path",
		"/v0/city//svc/slack-bridge", // empty name: must not parse as ""
		"",
	} {
		if pattern.MatchString(notAMount) {
			t.Errorf("bridge pattern matched %q, which is not a service mount path", notAMount)
		}
	}
}
