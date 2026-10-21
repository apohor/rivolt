package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func putOffpeak(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handleOffpeakPut(nil)(rec, httptest.NewRequest(http.MethodPut, "/api/settings/charging/offpeak", strings.NewReader(body)))
	return rec
}

// A schedule that breaks a rule is a 400 {"error","field"} naming the field, and
// is refused before the store is touched (the nil store would otherwise be a 503).
func TestOffpeakPutRefusals(t *testing.T) {
	for _, c := range []struct {
		name, body, field string
	}{
		{"overlap", `{"tz":"America/Chicago","windows":[{"days":"weekdays","start":"06:00","end":"12:00","rate":0.2},{"days":"all","start":"11:00","end":"13:00","rate":0.1}]}`, "windows[1].start"},
		{"rate over 5", `{"tz":"America/Chicago","windows":[{"days":"all","start":"21:00","end":"06:00","rate":9}]}`, "windows[0].rate"},
		{"empty days", `{"tz":"America/Chicago","windows":[{"days":"","start":"21:00","end":"06:00","rate":0.1}]}`, "windows[0].days"},
		{"no zone", `{"windows":[{"days":"all","start":"21:00","end":"06:00","rate":0.1}]}`, "tz"},
	} {
		rec := putOffpeak(c.body)
		var out struct {
			Error string `json:"error"`
			Field string `json:"field"`
		}
		if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Field != c.field || out.Error == "" {
			t.Errorf("%s: %d %s, want 400 naming %s", c.name, rec.Code, rec.Body.String(), c.field)
		}
	}
	for name, body := range map[string]string{
		"unknown field": `{"tz":"America/Chicago","windows":[],"x":1}`,
		"not json":      `{`,
		"trailing":      `{"windows":[]} {"windows":[]}`,
		"too big":       `{"tz":"` + strings.Repeat("a", 9000) + `"}`,
	} {
		if rec := putOffpeak(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	// A valid schedule passes validation and only then meets the store (nil: 503).
	if rec := putOffpeak(`{"tz":"America/Chicago","windows":[{"days":"weekdays","start":"21:00","end":"06:00","rate":0.09}]}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("valid with no store = %d, want 503", rec.Code)
	}
}
