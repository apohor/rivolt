package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// With no stored choice the receipts opt-in is off, and with the operator
// flag absent the feature is reported unavailable.
func TestRivianReceiptsOptInDefaultsOff(t *testing.T) {
	rec := httptest.NewRecorder()
	handleRivianReceiptsGet(nil, nil)(uuid.New(), rec, httptest.NewRequest("GET", "/api/settings/rivian-receipts", nil))
	var got map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || got["opt_in"] || got["available"] {
		t.Errorf("status %d body %v", rec.Code, got)
	}
}
