package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// features.trip_share follows the routes: true only where they are mounted.
func TestConfigTripShareFeature(t *testing.T) {
	for _, on := range []bool{false, true} {
		h := handleConfig(false, false, false, false, nil, nil, configExtras{TripShare: on})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var body struct {
			Features struct {
				TripShare bool `json:"trip_share"`
			} `json:"features"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Features.TripShare != on {
			t.Fatalf("trip_share = %v, want %v", body.Features.TripShare, on)
		}
	}
}
