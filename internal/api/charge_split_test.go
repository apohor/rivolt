package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/auth"
)

// chargePlaceLabel is the web's chargePlaceLabel (web/src/lib/chargePlace.ts),
// case by case: a DC fast charge is Fast wherever it was; an AC charge is Home or
// Away by place and only its place may come from the cluster; an unclassified charge
// takes the cluster label (Home, Public as Away, Fast).
func TestChargePlaceLabelMatchesTheWeb(t *testing.T) {
	H, P, F, U := analytics.LabelHome, analytics.LabelPublic, analytics.LabelFast, analytics.LabelUnknown
	for _, c := range []struct {
		kind, place string
		cluster     analytics.ClusterLabel
		want        string
	}{
		{"dcfc", "", U, "Fast"}, {"dcfc", "home", H, "Fast"}, {"dcfc", "away", P, "Fast"},
		{"ac", "home", P, "Home"}, {"ac", "away", H, "Away"},
		{"ac", "", H, "Home"}, {"ac", "", P, "Away"}, {"ac", "", F, ""}, {"ac", "", U, ""},
		{"ac", "unknown", H, "Home"}, {"ac", "unknown", U, ""},
		{"", "", H, "Home"}, {"", "", P, "Away"}, {"", "", F, "Fast"}, {"", "", U, ""},
		{"other", "home", H, "Home"}, {"other", "", F, "Fast"},
	} {
		if got := chargePlaceLabel(c.kind, c.place, c.cluster); got != c.want {
			t.Errorf("chargePlaceLabel(%q, %q, %q) = %q, want %q", c.kind, c.place, c.cluster, got, c.want)
		}
	}
}

// Through the real router with full URLs: signed out is 401, signed in without
// stores answers an all-zero split, and the window and vehicle checks are 400s.
func TestChargeSplitThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	get := func(url string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if signedIn {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get("https://rivolt.dev/api/charge-split", false); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", w.Code)
	}
	w := get("https://rivolt.dev/api/charge-split?vehicle_id=rv-1&since=2026-10-01T00:00:00Z&until=2026-10-20T00:00:00Z", true)
	want := `{"home":{"sessions":0,"energy_kwh":0},"away":{"sessions":0,"energy_kwh":0},"fast":{"sessions":0,"energy_kwh":0},"total":0}`
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
		t.Errorf("signed in: %d %s", w.Code, w.Body.String())
	}
	for name, u := range map[string]string{
		"bad time":    "https://rivolt.dev/api/charge-split?until=nope",
		"too wide":    "https://rivolt.dev/api/charge-split?since=2024-01-01T00:00:00Z&until=2026-01-01T00:00:00Z",
		"bad vehicle": "https://rivolt.dev/api/charge-split?vehicle_id=a%20b",
	} {
		if w := get(u, true); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
}
