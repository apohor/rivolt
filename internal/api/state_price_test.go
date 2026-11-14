package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatePrice(t *testing.T) {
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handleStatePrice(rec, httptest.NewRequest(http.MethodGet, "/api/settings/charging/state-price?"+q, nil))
		return rec
	}
	rec := get("country=US&state=Texas")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		State       string  `json:"state"`
		PricePerKWh float64 `json:"price_per_kwh"`
		Currency    string  `json:"currency"`
		Source      struct {
			Name, URL, Period, Released, Checked string
		} `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "TX" || out.PricePerKWh <= 0.05 || out.PricePerKWh >= 0.6 || out.Currency != "USD" {
		t.Errorf("%+v", out)
	}
	if out.Source.Name == "" || !strings.HasPrefix(out.Source.URL, "https://www.eia.gov/") || out.Source.Period == "" || out.Source.Released == "" || out.Source.Checked == "" {
		t.Errorf("the answer is not dated and cited: %+v", out.Source)
	}
	if rec := get("country=us&state=tx"); rec.Code != 200 {
		t.Errorf("lower case: %d", rec.Code)
	}
	for _, q := range []string{"", "state=TX", "country=US", "country=CA&state=ON", "country=CA&state=CA", "country=US&state=Atlantis", "country=US&state=" + strings.Repeat("x", 41)} {
		if rec := get(q); rec.Code != http.StatusNotFound {
			t.Errorf("%q: %d, want 404", q, rec.Code)
		}
	}
}
