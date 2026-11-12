package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
)

var historyImportRoutes = []struct{ method, url string }{
	{http.MethodPost, "https://rivolt.dev/api/import/preview"},
	{http.MethodPost, "https://rivolt.dev/api/import/commit"},
	{http.MethodGet, "https://rivolt.dev/api/imports"},
	{http.MethodPost, "https://rivolt.dev/api/imports/" + "11111111-1111-1111-1111-111111111111" + "/undo"},
}

func historyImportCall(h http.Handler, method, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, strings.NewReader("not multipart"))
	req.RemoteAddr = "203.0.113.9:4000"
	h.ServeHTTP(rec, req)
	return rec
}

func historyImportAuth(t *testing.T) *auth.Service {
	t.Helper()
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// With RIVOLT_HISTORY_IMPORT off there are no routes: 404 on every path, for a signed-in user too (through the real router, full URLs).
func TestHistoryImportRoutesAbsentWhenOff(t *testing.T) {
	h := asSession(New(Deps{Auth: historyImportAuth(t)}), sessions.KindWeb)
	for _, rt := range historyImportRoutes {
		if rec := historyImportCall(h, rt.method, rt.url); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", rt.method, rt.url, rec.Code)
		}
	}
}

// On, the preview and commit are reached (a body that is not multipart is a 400 before anything else is touched).
func TestHistoryImportRoutesPresentWhenOn(t *testing.T) {
	h := asSession(New(Deps{Auth: historyImportAuth(t), HistoryImport: true}), sessions.KindWeb)
	for _, u := range []string{"https://rivolt.dev/api/import/preview", "https://rivolt.dev/api/import/commit"} {
		if rec := historyImportCall(h, http.MethodPost, u); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_upload") {
			t.Errorf("POST %s = %d %s, want 400 bad_upload", u, rec.Code, rec.Body.String())
		}
	}
}

// R8: a pod parses two requests at once. A third gets 503 Retry-After 10 and is counted; a slot freed lets the next one in.
func TestHistoryImportParseSemaphore(t *testing.T) {
	m := metrics.New()
	a := newHistoryImportAPI(Deps{Metrics: m})
	for i := 0; i < importParseSlots; i++ {
		rec := httptest.NewRecorder()
		if !a.acquire(rec) {
			t.Fatalf("slot %d refused", i)
		}
	}
	rec := httptest.NewRecorder()
	if a.acquire(rec) {
		t.Fatal("a third parse was admitted")
	}
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "10" {
		t.Errorf("got %d Retry-After %q, want 503 and 10", rec.Code, rec.Header().Get("Retry-After"))
	}
	if got := testutil.ToFloat64(m.ImportParseRejected); got != 1 {
		t.Errorf("rejected counter = %v, want 1", got)
	}
	a.release()
	if !a.acquire(httptest.NewRecorder()) {
		t.Error("a freed slot was not reusable")
	}
}

// Previews are limited per user: the 31st inside the hour is a 429, and another user is not affected.
func TestHistoryImportPreviewIsRateLimitedPerUser(t *testing.T) {
	svc := historyImportAuth(t)
	router := New(Deps{Auth: svc, HistoryImport: true})
	as := func(uid uuid.UUID) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithSession(auth.WithUser(r.Context(), uid), auth.SessionRef{ID: uuid.New(), Kind: sessions.KindWeb})
			router.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	one, two := as(uuid.New()), as(uuid.New())
	limited := 0
	for i := 0; i < importPreviewsPerHour+5; i++ {
		if historyImportCall(one, http.MethodPost, historyImportRoutes[0].url).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited < 4 || limited > 5 {
		t.Errorf("%d of %d previews limited, want 5", limited, importPreviewsPerHour+5)
	}
	if rec := historyImportCall(two, http.MethodPost, historyImportRoutes[0].url); rec.Code != http.StatusBadRequest {
		t.Errorf("another user's preview = %d, want 400 (not limited)", rec.Code)
	}
}

func TestLiveOnlyInputDropsImportedAndElectrafiRows(t *testing.T) {
	in := ledgerInputForTest()
	got := liveOnlyInput(in)
	if len(got.Charges) != 1 || got.Charges[0].Source != "live" || len(got.Drives) != 1 || got.Drives[0].Source != "live" {
		t.Errorf("kept %d charges and %d drives, want only the live ones", len(got.Charges), len(got.Drives))
	}
}

func ledgerInputForTest() ledger.Input {
	return ledger.Input{
		Charges: []ledger.Charge{{Source: "live"}, {Source: "tezlab_import"}, {Source: "electrafi_import"}},
		Drives:  []ledger.Drive{{Source: "abrp_import"}, {Source: "live"}, {Source: "electrafi_import"}},
	}
}

// One import in flight per user per pod: a second request from the same user is refused until the first is done, another user is not.
func TestHistoryImportOneInFlightPerUser(t *testing.T) {
	a := newHistoryImportAPI(Deps{})
	req := func(uid uuid.UUID) *http.Request {
		return httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/import/preview", nil).WithContext(auth.WithUser(context.Background(), uid))
	}
	u1, u2 := uuid.New(), uuid.New()
	end1, ok := a.begin(httptest.NewRecorder(), req(u1))
	if !ok {
		t.Fatal("first request refused")
	}
	rec := httptest.NewRecorder()
	if _, ok := a.begin(rec, req(u1)); ok || rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "in_progress") {
		t.Errorf("second request of the same user = %v %d %s", ok, rec.Code, rec.Body)
	}
	end2, ok := a.begin(httptest.NewRecorder(), req(u2))
	if !ok {
		t.Fatal("another user refused")
	}
	end1()
	end2()
	end, ok := a.begin(httptest.NewRecorder(), req(u1))
	if !ok {
		t.Fatal("slot not returned")
	}
	end()
	if len(a.active) != 0 || len(a.sem) != 0 {
		t.Errorf("leaked: %d users, %d slots", len(a.active), len(a.sem))
	}
}

// Only the eight known form fields are read, and at most sixteen non-file parts.
func TestHistoryImportRefusesUnknownAndManyFields(t *testing.T) {
	body := func(names ...string) (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for _, n := range names {
			_ = mw.WriteField(n, "x")
		}
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := newHistoryImportAPI(Deps{})
		a.preview(w, r.WithContext(auth.WithUser(r.Context(), uuid.New())))
	})
	many := make([]string, 17)
	for i := range many {
		many[i] = "tz"
	}
	for name, names := range map[string][]string{"unknown": {"source", "evil"}, "seventeen": many} {
		b, ct := body(names...)
		req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/import/preview", b)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_upload") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
