package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

func otaForwardRouter(src rivian.OTASource, cache *otaNotesCache, uid uuid.UUID, sf *stateForward, logs *bytes.Buffer) http.Handler {
	r := chi.NewRouter()
	h := handleOTANotesForwarded(func(uuid.UUID) rivian.OTASource { return src }, cache, slog.New(slog.NewTextHandler(logs, nil)),
		func(uuid.UUID) *stateForward { return sf })
	r.Get("/vehicles/{vehicleID}/ota/notes", func(w http.ResponseWriter, req *http.Request) { h(uid, w, req) })
	return r
}

func notesResult(t *testing.T, l otaNotesLinks) forward.Result {
	t.Helper()
	b, _ := json.Marshal(l)
	return forward.Result{Status: 200, Body: b}
}

// The live client's read is made by the owner: the requester gets the checked links, makes no Rivian call and keeps the cache and spacing.
func TestOTANotesForwardedToTheOwner(t *testing.T) {
	fw := &planForwarder{res: notesResult(t, otaNotesLinks{Current: signedNotes})}
	sf := &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", true }}
	var logs bytes.Buffer
	h := otaForwardRouter(rivian.NewLive(), newOTANotesCache(), uuid.New(), sf, &logs)

	rec := getNotes(h, "", "application/json")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "SECRETSIG") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || fw.count() != 1 {
		t.Errorf("cache-control %q, forwards %d", rec.Header().Get("Cache-Control"), fw.count())
	}
	getNotes(h, "", "application/json") // the requester's cache answers the second tap
	if fw.count() != 1 {
		t.Errorf("a cached link was asked of the owner again (%d forwards)", fw.count())
	}
	if strings.Contains(logs.String(), "SECRET") {
		t.Error("a signed link reached the log")
	}
}

// A reply never makes the handler follow a link off Rivian's hosts, whatever the owner sent.
func TestOTANotesForwardedLinkIsCheckedAgain(t *testing.T) {
	fw := &planForwarder{res: notesResult(t, otaNotesLinks{Current: "https://evil.example.com/x"})}
	sf := &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", true }}
	rec := getNotes(otaForwardRouter(rivian.NewLive(), newOTANotesCache(), uuid.New(), sf, &bytes.Buffer{}), "", "application/json")
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "evil") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

// Owner failures keep the handler's fixed classes; no text from the owner is used; an owner that is silent is 503 with Retry-After 5.
func TestOTANotesForwardedFailures(t *testing.T) {
	failure := func(kind string) forward.Result {
		b, _ := json.Marshal(otaFailure{Kind: kind})
		return forward.Result{Status: 502, Class: otaNotesClassName, Body: b}
	}
	for name, tc := range map[string]struct {
		res        forward.Result
		status     int
		retryAfter string
	}{
		"paused":        {failure("paused"), 503, ""},
		"needs reauth":  {failure("needs_reauth"), 401, ""},
		"not signed in": {failure("not_authenticated"), 401, ""},
		"other":         {failure("other"), 502, ""},
		"owner silent":  {forward.Result{Status: forward.StatusUnavailable, Class: forward.ClassUnavailable}, 503, "5"},
	} {
		t.Run(name, func(t *testing.T) {
			fw := &planForwarder{res: tc.res}
			sf := &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", true }}
			rec := getNotes(otaForwardRouter(rivian.NewLive(), newOTANotesCache(), uuid.New(), sf, &bytes.Buffer{}), "", "application/json")
			if rec.Code != tc.status || rec.Header().Get("Retry-After") != tc.retryAfter {
				t.Errorf("status %d Retry-After %q, want %d %q: %s", rec.Code, rec.Header().Get("Retry-After"), tc.status, tc.retryAfter, rec.Body)
			}
		})
	}
}

// Anything but the live client reads here, with no forward, as before.
func TestOTANotesNonLiveClientIsNotForwarded(t *testing.T) {
	fw := &planForwarder{}
	sf := &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", true }}
	src := &fakeOTA{info: &rivian.OTAInfo{Current: &rivian.OTAVersion{ReleaseNotesURL: signedNotes}}}
	rec := getNotes(otaForwardRouter(src, newOTANotesCache(), uuid.New(), sf, &bytes.Buffer{}), "", "application/json")
	if rec.Code != 200 || src.calls != 1 || fw.count() != 0 {
		t.Errorf("status %d, %d reads, %d forwards; want 200, 1, 0", rec.Code, src.calls, fw.count())
	}
}

// The owner's side: the checked links, or a failure kind with no text (the error can carry the signed link).
func TestServeOTANotes(t *testing.T) {
	ctx := context.Background()
	src := &fakeOTA{info: &rivian.OTAInfo{Current: &rivian.OTAVersion{ReleaseNotesURL: signedNotes}, Available: &rivian.OTAVersion{ReleaseNotesURL: "https://evil.example.com/x"}}}
	res := ServeOTANotes(ctx, otaClient{OTASource: src}, "v-fake-1")
	var l otaNotesLinks
	if res.Status != 200 || json.Unmarshal(res.Body, &l) != nil || l.Current != signedNotes || l.Available != "" {
		t.Errorf("links: %d %s", res.Status, res.Body)
	}
	src.err = errors.New("boom " + signedNotes)
	res = ServeOTANotes(ctx, otaClient{OTASource: src}, "v-fake-1")
	if res.Status != 502 || res.Class != otaNotesClassName || strings.Contains(string(res.Body), "SECRET") || strings.Contains(string(res.Body), "boom") {
		t.Errorf("failure: %d %q %s", res.Status, res.Class, res.Body)
	}
	src.err = rivian.ErrUpstreamPaused
	if res = ServeOTANotes(ctx, otaClient{OTASource: src}, "v-fake-1"); !strings.Contains(string(res.Body), "paused") {
		t.Errorf("paused: %s", res.Body)
	}
	if res = ServeOTANotes(ctx, nil, "v-fake-1"); res.Status != 200 || !strings.Contains(string(res.Body), `"current":""`) {
		t.Errorf("no client: %d %s", res.Status, res.Body)
	}
}

// otaClient is a rivian.Client that is also an OTASource.
type otaClient struct {
	rivian.Client
	rivian.OTASource
}
