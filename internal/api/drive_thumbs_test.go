package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The request rules hold before any store is asked: no ids, a plain id list and the cap.
func TestRouteThumbsRequestRules(t *testing.T) {
	h := handleRouteThumbs(nil)
	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/api/drives/route-thumbs?"+q, nil))
		return w
	}
	if w := get(""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Errorf("no ids: %d %q", w.Code, w.Body.String())
	}
	if w := get("ids=live_a,live_a,,live_b"); w.Code != http.StatusOK {
		t.Errorf("a plain list with a duplicate and a blank: %d", w.Code)
	}
	var sixty []string
	for i := 0; i < maxThumbIDs; i++ {
		sixty = append(sixty, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if w := get("ids=" + strings.Join(sixty, ",")); w.Code != http.StatusOK {
		t.Errorf("60 ids: %d", w.Code)
	}
	if w := get("ids=" + strings.Join(sixty, ",") + ",extra"); w.Code != http.StatusBadRequest {
		t.Errorf("61 ids: %d, want 400", w.Code)
	}
	for _, bad := range []string{"ids=a%27%3Bdrop", "ids=a%20b", "ids=" + strings.Repeat("x", 129), "ids=%E2%82%AC"} {
		if w := get(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d, want 400", bad, w.Code)
		}
	}
}

func TestValidDriveID(t *testing.T) {
	for _, ok := range []string{"live_x_1", "2f1c9a1e-0000-4000-8000-000000000001", "a:b.c-d"} {
		if !validDriveID(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a;b", "a'b", strings.Repeat("x", 129)} {
		if validDriveID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
