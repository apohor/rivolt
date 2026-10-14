package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigLandingAndDemoFlags(t *testing.T) {
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var out struct {
			Features map[string]any `json:"features"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Features
	}
	t.Setenv("RIVOLT_LANDING", "")
	f := get()
	if f["landing_enabled"] != false || f["demo_enabled"] != false {
		t.Fatalf("defaults: %v", f)
	}
	t.Setenv("RIVOLT_LANDING", "true")
	f = get()
	if f["landing_enabled"] != true || f["demo_enabled"] != false {
		t.Fatalf("landing on: %v", f)
	}
}

func TestSecurityHeadersNoCloudflareInsights(t *testing.T) {
	rec := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "cloudflareinsights") {
		t.Fatalf("csp still allows cloudflareinsights: %s", csp)
	}
	if !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("script-src: %s", csp)
	}
}
