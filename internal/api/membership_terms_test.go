package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMembershipTermsEndpoint(t *testing.T) {
	get := func(now time.Time) (int, []membershipTermJSON, string) {
		rec := httptest.NewRecorder()
		handleMembershipTerms(func() time.Time { return now })(rec, httptest.NewRequest(http.MethodGet, "/api/settings/charging/membership-terms", nil))
		var out struct {
			Terms []membershipTermJSON `json:"terms"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Terms, rec.Body.String()
	}
	code, rows, body := get(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
	if code != 200 || len(rows) < 8 {
		t.Fatalf("%d, %d rows", code, len(rows))
	}
	for _, r := range rows {
		if r.NetworkSlug == "" || r.Network == "" || r.Plan == "" || r.Terms == "" || !strings.HasPrefix(r.SourceURL, "https://") || r.Checked != "2026-10-20" ||
			r.Label != "As listed by "+r.Network+"; may be out of date; last checked 2026-10-20." {
			t.Errorf("row %+v", r)
		}
	}
	// Reference data: nothing user-specific, and `terms` is [] not null once expired.
	if code, rows, body = get(time.Date(2027, 10, 21, 0, 0, 0, 0, time.UTC)); code != 200 || len(rows) != 0 || !strings.Contains(body, `"terms":[]`) {
		t.Errorf("after 12 months: %d %d rows %s", code, len(rows), body)
	}
}
