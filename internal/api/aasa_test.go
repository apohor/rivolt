package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAASAServedFromConfig(t *testing.T) {
	r := New(Deps{IOSAppIDs: []string{"CK383X39S5.dev.rivolt.app"}})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/apple-app-site-association", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("AASA: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var doc struct {
		AppLinks struct {
			Details []struct {
				AppIDs     []string            `json:"appIDs"`
				Components []map[string]string `json:"components"`
			} `json:"details"`
		} `json:"applinks"`
		WebCredentials struct {
			Apps []string `json:"apps"`
		} `json:"webcredentials"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.AppLinks.Details) != 1 || doc.AppLinks.Details[0].AppIDs[0] != "CK383X39S5.dev.rivolt.app" ||
		doc.AppLinks.Details[0].Components[0]["/"] != "/app/callback" {
		t.Fatalf("applinks %+v", doc.AppLinks)
	}
	if len(doc.WebCredentials.Apps) != 1 || doc.WebCredentials.Apps[0] != "CK383X39S5.dev.rivolt.app" {
		t.Fatalf("webcredentials %+v", doc.WebCredentials)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/app/callback?code=secret-code", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "secret-code") {
		t.Fatalf("callback page: %d (or it echoed the code)", w.Code)
	}
	for h, want := range map[string]string{"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Frame-Options": "DENY"} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q", h, got)
		}
	}
}

// Unset, nothing is published: the SPA answers instead (no AASA, so iOS
// associates nothing with the domain).
func TestAASANotServedUnlessConfigured(t *testing.T) {
	for name, d := range map[string]Deps{
		"unset":      {},
		"admin host": {IOSAppIDs: []string{"CK383X39S5.dev.rivolt.app"}, AdminOnly: true},
	} {
		w := httptest.NewRecorder()
		New(d).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/apple-app-site-association", nil))
		if w.Header().Get("Content-Type") == "application/json" && strings.Contains(w.Body.String(), "applinks") {
			t.Errorf("%s: AASA served", name)
		}
	}
}

func TestValidAppID(t *testing.T) {
	for id, want := range map[string]bool{
		"CK383X39S5.dev.rivolt.app": true,
		"ck383x39s5.dev.rivolt.app": false,
		"CK383X39S5":                false,
		"dev.rivolt.app":            false,
		"CK383X39S5.dev rivolt":     false,
	} {
		if ValidAppID(id) != want {
			t.Errorf("ValidAppID(%q) != %v", id, want)
		}
	}
}
