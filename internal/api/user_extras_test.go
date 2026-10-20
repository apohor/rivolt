package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// A restored saved-trip plan or advice is user-supplied: every URL-like field not
// produced by the server is dropped, at any depth.
func TestSanitizeRestoredJSONKeepsOnlyTheTrackerURL(t *testing.T) {
	in := `{
	  "Routes": [{"Name": "r", "photo_url": "https://evil.example/x.png", "n": 12345678901234567890}],
	  "costs": [{"breakdown": [
	    {"price": {"source_url": "https://dcfctracker.com/reports/evgo", "label": "x"}},
	    {"price": {"source_url": "javascript:alert(1)"}},
	    {"price": {"source_url": "http://dcfctracker.com/reports/evgo"}},
	    {"price": {"source_url": "https://dcfctracker.com.evil.example/x"}},
	    {"price": {"sourceUrl": "https://evil.example", "HREF": "https://evil.example", "more_link": 7}}
	  ]}],
	  "note": "see https://evil.example in the text, which is data, not a field"
	}`
	got := sanitizeRestoredJSON(json.RawMessage(in))
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, got)
	}
	s := string(got)
	if !strings.Contains(s, `"https://dcfctracker.com/reports/evgo"`) {
		t.Errorf("the tracker URL was dropped: %s", s)
	}
	for _, bad := range []string{"evil.example/x.png", "javascript:", "http://dcfctracker.com", "dcfctracker.com.evil", `"sourceUrl"`, `"HREF"`, "more_link", `"photo_url"`} {
		if strings.Contains(s, bad) {
			t.Errorf("%q survived: %s", bad, s)
		}
	}
	if !strings.Contains(s, "12345678901234567890") {
		t.Errorf("a large number was changed: %s", s)
	}
	if !strings.Contains(s, "which is data") {
		t.Errorf("free text was touched: %s", s)
	}
	// Not JSON: returned as it was, for the database to reject.
	if got := string(sanitizeRestoredJSON(json.RawMessage(`{broken`))); got != `{broken` {
		t.Errorf("invalid input changed to %q", got)
	}
}
