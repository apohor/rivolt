package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const appStoreLink = "https://apps.apple.com/us/app/rivolt/id1234567890"

func iosConfig(t *testing.T) map[string]any {
	t.Helper()
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

// While RIVOLT_IOS_APP_URL is unset (or not an Apple link) nothing promotes the app: no config
// field, no /app, no banner tag.
func TestIOSAppIsHiddenUntilConfigured(t *testing.T) {
	shell, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := spaHandler(fstest.MapFS{"index.html": {Data: shell}})
	for _, v := range []string{"", "https://evil.example/app/id1234567890", "https://apps.apple.com/app/id1234567890?utm_source=x"} {
		t.Setenv("RIVOLT_IOS_APP_URL", v)
		if _, ok := iosConfig(t)["ios_app_url"]; ok {
			t.Errorf("%q: ios_app_url should be absent", v)
		}
		if rec := seoGet(t, h, "rivolt.dev", "/app"); rec.Code != http.StatusNotFound {
			t.Errorf("%q: /app = %d, want 404", v, rec.Code)
		}
		if b := seoGet(t, h, "rivolt.dev", "/").Body.String(); strings.Contains(b, "apple-itunes-app") {
			t.Errorf("%q: banner tag present", v)
		}
	}
}

func TestIOSAppAppStoreLink(t *testing.T) {
	shell, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := spaHandler(fstest.MapFS{"index.html": {Data: shell}})
	t.Setenv("RIVOLT_IOS_APP_URL", appStoreLink)
	if got := iosConfig(t)["ios_app_url"]; got != appStoreLink {
		t.Errorf("ios_app_url = %v", got)
	}
	rec := seoGet(t, h, "rivolt.dev", "/app")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != appStoreLink || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("/app = %d %q %q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"))
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "http://rivolt.dev/app", nil))
	if post.Code != http.StatusNotFound {
		t.Errorf("POST /app = %d, want 404", post.Code)
	}
	if b := seoGet(t, h, "rivolt.dev", "/").Body.String(); !strings.Contains(b, `<meta name="apple-itunes-app" content="app-id=1234567890" />`) {
		t.Error("public page has no Smart App Banner tag")
	}
	// A page that is not public gets none.
	if b := seoGet(t, h, "rivolt.dev", "/drives").Body.String(); strings.Contains(b, "apple-itunes-app") {
		t.Error("a signed-in route carries the banner tag")
	}
}

// TestFlight links work for /app and the config but have no app id, so no banner tag.
func TestIOSAppTestFlightLink(t *testing.T) {
	shell, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := spaHandler(fstest.MapFS{"index.html": {Data: shell}})
	const tf = "https://testflight.apple.com/join/AbCdEf12"
	t.Setenv("RIVOLT_IOS_APP_URL", tf)
	if got := iosConfig(t)["ios_app_url"]; got != tf {
		t.Errorf("ios_app_url = %v", got)
	}
	if rec := seoGet(t, h, "rivolt.dev", "/app"); rec.Code != http.StatusFound || rec.Header().Get("Location") != tf {
		t.Errorf("/app = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if b := seoGet(t, h, "rivolt.dev", "/").Body.String(); strings.Contains(b, "apple-itunes-app") {
		t.Error("a TestFlight link produced a banner tag")
	}
}
