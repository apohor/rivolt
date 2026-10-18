package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/changelog"
)

const changelogFixture = `[
 {"tag_name":"v0.36.21","name":"v0.36.21","body":"## What's new\n\n- Safer sign-in","html_url":"https://github.com/apohor/rivolt/releases/tag/v0.36.21","published_at":"2026-10-18T20:56:28Z"},
 {"tag_name":"v0.36.22","name":"v0.36.22","body":"<!-- rivolt:auto-notes -->\n- x","html_url":"https://github.com/apohor/rivolt/releases/tag/v0.36.22","published_at":"2026-10-19T09:00:00Z"}
]`

func changelogSource(t *testing.T, down bool) *changelog.Source {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(changelogFixture))
	}))
	t.Cleanup(gh.Close)
	return changelog.New(gh.URL)
}

func TestChangelogRoutes(t *testing.T) {
	r := New(Deps{Changelog: changelogSource(t, false)})
	get := func(path, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	feed := get("/changelog.atom", "")
	if feed.Code != http.StatusOK || !strings.HasPrefix(feed.Header().Get("Content-Type"), "application/atom+xml") {
		t.Fatalf("feed: %d %q", feed.Code, feed.Header().Get("Content-Type"))
	}
	if !strings.Contains(feed.Body.String(), "tag:rivolt.dev,2026:v0.36.21") || strings.Contains(feed.Body.String(), "v0.36.22") {
		t.Errorf("feed must hold only the written notes:\n%s", feed.Body.String())
	}
	if etag := feed.Header().Get("ETag"); etag == "" || get("/changelog.atom", etag).Code != http.StatusNotModified {
		t.Errorf("a matching If-None-Match must be a 304 (etag %q)", etag)
	}
	js := get("/api/changelog", "")
	var out struct {
		Releases []changelog.Release `json:"releases"`
	}
	if js.Code != http.StatusOK || json.Unmarshal(js.Body.Bytes(), &out) != nil || len(out.Releases) != 1 || out.Releases[0].Tag != "v0.36.21" {
		t.Fatalf("json: %d %s", js.Code, js.Body.String())
	}
	if js.Header().Get("Cache-Control") == "" {
		t.Error("the changelog JSON should be cacheable")
	}
}

func TestChangelogUnavailableWhenNeverFetched(t *testing.T) {
	r := New(Deps{Changelog: changelogSource(t, true)})
	for _, p := range []string{"/changelog.atom", "/api/changelog"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with GitHub down and no copy: %d", p, rec.Code)
		}
	}
}

func TestChangelogJSONIsNotOnTheAdminHost(t *testing.T) {
	a := newAccessRigWith(t, func(d *Deps) { d.Changelog = changelogSource(t, false) })
	if code := a.get("/api/changelog", a.jwt(t, nil)); code != http.StatusNotFound {
		t.Errorf("/api/changelog on the admin host: %d, want 404", code)
	}
}

func TestChangelogAnswersHEAD(t *testing.T) {
	r := New(Deps{Changelog: changelogSource(t, false)})
	for path, ct := range map[string]string{"/changelog.atom": "application/atom+xml", "/api/changelog": "application/json"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) {
			t.Errorf("HEAD %s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
