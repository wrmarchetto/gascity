package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// beadLabelPolicyBody mirrors the response fields this test inspects.
type beadLabelPolicyBody struct {
	HiddenLabels []string `json:"hidden_labels"`
}

func fetchBeadLabelPolicy(t *testing.T, fs *fakeState) beadLabelPolicyBody {
	t.Helper()
	h := newTestCityHandler(t, fs)
	req := httptest.NewRequest("GET", cityURL(fs, "/beads/label-policy"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	var body beadLabelPolicyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, rec.Body.String())
	}
	return body
}

// TestBeadLabelPolicyServesTheReadyExclusionSet pins the endpoint's whole
// reason to exist: the dashboard must be able to ask this binary which bead
// families it does not count as work, so the board never carries a second copy
// of those literals. Compared element-wise against beads.ReadyExcludedLabels()
// rather than against a literal list written here, because a literal list in
// the test is the same rot the endpoint exists to prevent -- it would go green
// against a handler that had stopped reading the canonical set.
func TestBeadLabelPolicyServesTheReadyExclusionSet(t *testing.T) {
	fs := newFakeState(t)
	body := fetchBeadLabelPolicy(t, fs)

	want := beads.ReadyExcludedLabels()
	if len(body.HiddenLabels) != len(want) {
		t.Fatalf("hidden_labels = %v (%d), want %v (%d)", body.HiddenLabels, len(body.HiddenLabels), want, len(want))
	}
	for i := range want {
		if body.HiddenLabels[i] != want[i] {
			t.Errorf("hidden_labels[%d] = %q, want %q", i, body.HiddenLabels[i], want[i])
		}
	}
}

// TestBeadLabelPolicyIsNotEmpty guards the vacuous-pass case the comparison
// above cannot catch on its own: if ReadyExcludedLabels() ever returned
// nothing, the element-wise loop would run zero times and both sides would
// agree at length 0, leaving the dashboard hiding nothing with a green suite.
func TestBeadLabelPolicyIsNotEmpty(t *testing.T) {
	fs := newFakeState(t)
	body := fetchBeadLabelPolicy(t, fs)
	if len(body.HiddenLabels) == 0 {
		t.Fatal("hidden_labels is empty; the dashboard would hide no bookkeeping row")
	}
	found := false
	for _, l := range body.HiddenLabels {
		if l == "gc:extmsg-transcript" {
			found = true
		}
	}
	// The one literal this test does spell out, and deliberately: it is the
	// family that filled the operator's board (ci-zg9lbn). If a refactor drops
	// it from the exclusion set, the element-wise comparison above still passes
	// -- both sides move together -- and only this line notices.
	if !found {
		t.Error(`hidden_labels lacks "gc:extmsg-transcript"; the Slack transcript rows ci-zg9lbn hid would return`)
	}
}
