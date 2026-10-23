package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

const signedNotes = "https://dc-service-prod-techpubs-r1-docs.s3.us-east-1.amazonaws.com/Vehicle/r1x/RNS/UpdateDetails/1/en-US/update-details-1.pdf?X-Amz-Expires=3600&X-Amz-Security-Token=SECRETTOKEN&X-Amz-Signature=SECRETSIG"

type fakeOTA struct {
	calls int
	info  *rivian.OTAInfo
	err   error
}

func (f *fakeOTA) OTAUpdateDetails(context.Context, string) (*rivian.OTAInfo, error) {
	f.calls++
	return f.info, f.err
}

func notesRouter(src rivian.OTASource, cache *otaNotesCache, uid uuid.UUID, logs *bytes.Buffer) http.Handler {
	r := chi.NewRouter()
	logger := slog.New(slog.NewTextHandler(logs, nil))
	h := handleOTANotes(func(uuid.UUID) rivian.OTASource { return src }, cache, logger)
	r.Get("/vehicles/{vehicleID}/ota/notes", func(w http.ResponseWriter, req *http.Request) { h(uid, w, req) })
	return r
}

func getNotes(h http.Handler, query, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/vehicles/veh-1/ota/notes"+query, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func twoLinks() *rivian.OTAInfo {
	return &rivian.OTAInfo{
		Current:   &rivian.OTAVersion{Version: "2026.30", ReleaseNotesURL: signedNotes, HasNotes: true},
		Available: &rivian.OTAVersion{Version: "2026.38"},
	}
}

func TestOTANotesRedirectsForTheBrowserAndReturnsJSONForTheApp(t *testing.T) {
	src := &fakeOTA{info: twoLinks()}
	var logs bytes.Buffer
	h := notesRouter(src, newOTANotesCache(), uuid.New(), &logs)

	rec := getNotes(h, "", "text/html")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != signedNotes {
		t.Fatalf("browser: %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", rec.Header())
	}
	rec = getNotes(h, "?which=current", "application/json")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"url"`) || !strings.Contains(rec.Body.String(), "update-details-1.pdf") {
		t.Fatalf("app: %d %s", rec.Code, rec.Body.String())
	}
	// Cached: the second answer did not ask Rivian again.
	if src.calls != 1 {
		t.Errorf("Rivian was read %d times, want 1 (the link is cached for 50 minutes)", src.calls)
	}
	// An available version with no link is a 404, answered from the same cache.
	if rec := getNotes(h, "?which=available", "application/json"); rec.Code != http.StatusNotFound || src.calls != 1 {
		t.Errorf("available: %d after %d reads", rec.Code, src.calls)
	}
	if rec := getNotes(h, "?which=latest", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad which: %d", rec.Code)
	}
	if strings.Contains(logs.String(), "SECRET") || strings.Contains(logs.String(), "amazonaws") {
		t.Errorf("the link reached the log: %s", logs.String())
	}
}

func TestOTANotesNeverRedirectsOffRivian(t *testing.T) {
	for _, bad := range []string{
		"http://dc-service.s3.amazonaws.com/x.pdf",    // not https
		"https://evil.example/x.pdf",                  // another host
		"https://amazonaws.com.evil.example/x.pdf",    // suffix trick
		"https://notrivian.com/x",                     // not a rivian.com host
		"https://user:pw@docs.s3.amazonaws.com/x.pdf", // credentials
		"https://docs.s3.amazonaws.com:8443/x.pdf",    // a port
		"javascript:alert(1)",
	} {
		src := &fakeOTA{info: &rivian.OTAInfo{Current: &rivian.OTAVersion{Version: "1", ReleaseNotesURL: bad}}}
		rec := getNotes(notesRouter(src, newOTANotesCache(), uuid.New(), &bytes.Buffer{}), "", "text/html")
		if rec.Code == http.StatusFound || rec.Header().Get("Location") != "" {
			t.Errorf("%q was followed: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, good := range []string{"https://docs.s3.us-east-1.amazonaws.com/x.pdf?a=b", "https://rivian.com/support/n", "https://www.rivian.com/n"} {
		if !otaNotesHostOK(good) {
			t.Errorf("%q refused", good)
		}
	}
}

func TestOTANotesSpacesTheReadsOfRivian(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	cache := newOTANotesCache()
	cache.now = func() time.Time { return now }
	src := &fakeOTA{err: errors.New("dial " + signedNotes)}
	var logs bytes.Buffer
	h := notesRouter(src, cache, uuid.New(), &logs)

	rec := getNotes(h, "", "application/json")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream failure: %d", rec.Code)
	}
	// The upstream's own text never reaches the answer or the log.
	if strings.Contains(rec.Body.String(), "SECRET") || strings.Contains(rec.Body.String(), "amazonaws") ||
		strings.Contains(logs.String(), "SECRET") || strings.Contains(logs.String(), "amazonaws") || strings.Contains(logs.String(), "dial") {
		t.Errorf("error text leaked: body %q log %q", rec.Body.String(), logs.String())
	}
	// A second tap right away is not a second read of Rivian.
	rec = getNotes(h, "", "application/json")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || src.calls != 1 {
		t.Fatalf("second tap: %d Retry-After %q after %d reads", rec.Code, rec.Header().Get("Retry-After"), src.calls)
	}
	now = now.Add(otaNotesGap + time.Second)
	src.err, src.info = nil, twoLinks()
	if rec := getNotes(h, "", "application/json"); rec.Code != http.StatusOK || src.calls != 2 {
		t.Fatalf("after the gap: %d after %d reads", rec.Code, src.calls)
	}
	// The cached link is dropped after 50 minutes and read again.
	now = now.Add(otaNotesTTL + time.Second)
	if rec := getNotes(h, "", "application/json"); rec.Code != http.StatusOK || src.calls != 3 {
		t.Fatalf("after the TTL: %d after %d reads", rec.Code, src.calls)
	}
}

func TestOTANotesUpstreamClassesAreFixed(t *testing.T) {
	for err, want := range map[error]int{rivian.ErrUpstreamPaused: 503, rivian.ErrNeedsReauth: 401, rivian.ErrNotAuthenticated: 401, errors.New("boom"): 502} {
		src := &fakeOTA{err: err}
		rec := getNotes(notesRouter(src, newOTANotesCache(), uuid.New(), &bytes.Buffer{}), "", "application/json")
		if rec.Code != want || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%v: %d %s, want %d and a fixed message", err, rec.Code, rec.Body.String(), want)
		}
	}
	// A user with no Rivian client has no release notes, not an error.
	h := notesRouter(nil, newOTANotesCache(), uuid.New(), &bytes.Buffer{})
	if rec := getNotes(h, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("no client: %d", rec.Code)
	}
}

func TestOTANotesAreKeptPerUser(t *testing.T) {
	cache := newOTANotesCache()
	src := &fakeOTA{info: twoLinks()}
	a, b := uuid.New(), uuid.New()
	getNotes(notesRouter(src, cache, a, &bytes.Buffer{}), "", "application/json")
	getNotes(notesRouter(src, cache, b, &bytes.Buffer{}), "", "application/json")
	if src.calls != 2 {
		t.Errorf("two users, %d reads: one user's cached link was served to the other", src.calls)
	}
}
