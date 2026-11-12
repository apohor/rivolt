package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// features.history_import follows the routes: true only where they are mounted.
func TestConfigHistoryImportFeature(t *testing.T) {
	for _, on := range []bool{false, true} {
		h := handleConfig(false, false, false, false, nil, nil, configExtras{HistoryImport: on})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var body struct {
			Features struct {
				HistoryImport bool `json:"history_import"`
			} `json:"features"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Features.HistoryImport != on {
			t.Fatalf("history_import = %v, want %v", body.Features.HistoryImport, on)
		}
	}
}
