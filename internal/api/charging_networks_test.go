package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type chargingNetworksResp struct {
	Networks         []chargingNetworkJSON `json:"networks"`
	ObservedTermsURL string                `json:"observed_terms_url"`
	ObservedSource   string                `json:"observed_source"`
}

func getChargingNetworks(t *testing.T, now time.Time) (chargingNetworksResp, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	handleChargingNetworks(func() time.Time { return now })(rec, httptest.NewRequest(http.MethodGet, "/api/charging-networks", nil))
	var out chargingNetworksResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return out, rec
}

func TestChargingNetworksShowsPlansAndLabelledObservedPrices(t *testing.T) {
	out, rec := getChargingNetworks(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if out.ObservedTermsURL != "https://dcfctracker.com/terms" || out.ObservedSource != "DCFC Tracker" {
		t.Errorf("source: %+v", out)
	}
	by := map[string]chargingNetworkJSON{}
	for _, n := range out.Networks {
		by[n.Slug] = n
		if n.Slug == "unmatched" {
			t.Error("the fallthrough row is not a network")
		}
		if n.Plans == nil {
			t.Errorf("%s: plans must be [] not null", n.Slug)
		}
		for _, p := range n.Plans {
			if p.SourceURL == "" || !strings.HasPrefix(p.Label, "As listed by ") || p.Checked == "" {
				t.Errorf("%s plan %q: missing source, label or date: %+v", n.Slug, p.Plan, p)
			}
		}
		if n.Observed != nil {
			if n.Price != n.Observed.Label || !strings.HasPrefix(n.Price, "Typical price seen by DCFC Tracker, ") ||
				!strings.HasSuffix(n.Price, " The price at the charger may differ.") {
				t.Errorf("%s: price label %q", n.Slug, n.Price)
			}
			low := strings.ToLower(n.Price)
			if strings.Contains(low, "listed") || strings.Contains(low, "official") || strings.Contains(low, "current") {
				t.Errorf("%s: forbidden word in %q", n.Slug, n.Price)
			}
			if n.Observed.URL == "" || n.Observed.Month == "" {
				t.Errorf("%s: observed figure without link or month", n.Slug)
			}
		} else if n.Price != "Price shown at the charger." {
			t.Errorf("%s: no figure, yet price = %q", n.Slug, n.Price)
		}
	}
	if by["ea"].Observed == nil || by["tesla_sc"].Observed == nil || by["tesla_sc"].Observed.LowNote == "" {
		t.Errorf("expected observed figures for ea and tesla_sc (with its two ends): %+v", by["tesla_sc"].Observed)
	}
	if n, ok := by["shell_recharge"]; !ok || n.Observed != nil || len(n.Plans) != 3 {
		t.Errorf("shell_recharge should list its plans and say the price is shown at the charger: %+v", n)
	}
}

// A figure over 12 months old is not returned (the server rule), a plan last checked over 12 months
// ago is not either; what remains still says where the price is.
func TestChargingNetworksHidesStaleRows(t *testing.T) {
	// 2027-10-01: the October 2026 observations are 12 months old; the plans checked 2026-10-20 are not.
	out, _ := getChargingNetworks(t, time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC))
	if len(out.Networks) == 0 {
		t.Fatal("plans checked 2026-10-20 should still show")
	}
	for _, n := range out.Networks {
		if n.Observed != nil {
			t.Errorf("%s: a 12-month-old figure is still shown", n.Slug)
		}
		if n.Price != "Price shown at the charger." {
			t.Errorf("%s: price = %q", n.Slug, n.Price)
		}
	}
	// A month later nothing is left to show.
	out, _ = getChargingNetworks(t, time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC))
	if len(out.Networks) != 0 {
		t.Errorf("%d networks after everything is over 12 months old", len(out.Networks))
	}
}
