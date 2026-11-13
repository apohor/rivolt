package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A file with no header row has a data row in the first line: none of it comes back in the refusal.
func TestHistoryImportRefusalCarriesNoDataRow(t *testing.T) {
	req := &importRequest{
		formats: importSources["tezlab"],
		files:   []importFile{{name: "x.csv", data: []byte("2026-03-01T08:00:00Z,34.0522,-118.2437,12.5,80,Home,7,1/2/2026,a-very-long-cell-that-goes-past-the-forty-character-limit-for-names\n2026-03-02T08:00:00Z,35.1,-119.1,13,70,Home,7,1/3/2026,x\n")}},
	}
	_, pf := req.parse(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if pf == nil || pf.code != "unknown_columns" {
		t.Fatalf("parse = %+v", pf)
	}
	for _, c := range pf.header {
		if c != "Home" {
			t.Errorf("header carries %q", c)
		}
	}
	rec := httptest.NewRecorder()
	pf.write(rec)
	for _, banned := range []string{"34.05", "118.24", "2026", "very-long"} {
		if strings.Contains(rec.Body.String(), banned) {
			t.Errorf("response carries %q: %s", banned, rec.Body)
		}
	}
	got := nameLikeCells([]string{"Start Time", " Distance (mi) ", "12.5", "7,5", "2026-03-01", "x"})
	if strings.Join(got, "|") != "Start Time|Distance (mi)|x" {
		t.Errorf("nameLikeCells = %q", got)
	}
	many := make([]string, 100)
	for i := range many {
		many[i] = "col" + strings.Repeat("a", i%3)
	}
	if n := len(nameLikeCells(many)); n != 60 {
		t.Errorf("%d cells kept, want 60", n)
	}
}

// Preview and commit refuse a browser request from another site or another subdomain; same-origin, no page and non-browser callers pass.
func TestHistoryImportSecFetchSite(t *testing.T) {
	a := newHistoryImportAPI(Deps{})
	for sfs, want := range map[string]bool{"": true, "same-origin": true, "none": true, "same-site": false, "cross-site": false} {
		r := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/import/preview", nil).WithContext(auth.WithUser(context.Background(), uuid.New()))
		if sfs != "" {
			r.Header.Set("Sec-Fetch-Site", sfs)
		}
		rec := httptest.NewRecorder()
		end, ok := a.begin(rec, r)
		if ok != want || (!ok && rec.Code != http.StatusForbidden) {
			t.Errorf("Sec-Fetch-Site %q: admitted %v (%d), want %v", sfs, ok, rec.Code, want)
		}
		if ok {
			end()
		}
	}
}

// Undo deletes rows, so it gets the same check; a refused request never reaches the store.
func TestHistoryImportUndoSecFetchSite(t *testing.T) {
	a := newHistoryImportAPI(Deps{})
	for _, sfs := range []string{"same-site", "cross-site"} {
		r := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/imports/"+uuid.NewString()+"/undo", nil).WithContext(auth.WithUser(context.Background(), uuid.New()))
		r.Header.Set("Sec-Fetch-Site", sfs)
		rec := httptest.NewRecorder()
		a.undo(rec, r)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross_site_request") {
			t.Errorf("Sec-Fetch-Site %q: %d %s", sfs, rec.Code, rec.Body.String())
		}
	}
}

// A refused file is counted by source and reason, and logged with the header names it had: no value from any row.
func TestHistoryImportRefusalCountsAndLogsHeaderNamesOnly(t *testing.T) {
	m := metrics.New()
	var logs bytes.Buffer
	a := newHistoryImportAPI(Deps{Metrics: m, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	req := &importRequest{
		source:  "tezlab",
		formats: importSources["tezlab"],
		files:   []importFile{{name: "x.csv", data: []byte("Date,Odometer,Notes\n2026-03-01,12345.6,Home 34.0522 -118.2437\n")}},
	}
	_, pf := req.parse(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if pf == nil || pf.code != "unknown_columns" {
		t.Fatalf("parse = %+v", pf)
	}
	a.refused("tezlab", pf)
	if got := testutil.ToFloat64(m.HistoryImportRejected.WithLabelValues("tezlab", "unknown_header")); got != 1 {
		t.Errorf("unknown_header = %v, want 1", got)
	}
	out := logs.String()
	for _, want := range []string{"Date", "Odometer", "Notes", "unknown_header"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
	for _, banned := range []string{"12345", "34.05", "118.24", "2026-03-01", "Home"} {
		if strings.Contains(out, banned) {
			t.Errorf("log carries %q: %s", banned, out)
		}
	}
	// The user-facing message names what the export needs and quotes nothing from the file.
	rec := httptest.NewRecorder()
	pf.write(rec)
	body := rec.Body.String()
	if !strings.Contains(body, `"expected"`) || !strings.Contains(body, "Nothing was imported") {
		t.Errorf("body lacks the expected columns: %s", body)
	}
	for _, banned := range []string{"12345", "34.05", "Home"} {
		if strings.Contains(body, banned) {
			t.Errorf("body carries %q: %s", banned, body)
		}
	}
}

// Each failure code lands in one of the four reasons; need_choice is a question, not a refusal, so it is not counted.
func TestHistoryImportRefusedReasons(t *testing.T) {
	m := metrics.New()
	a := newHistoryImportAPI(Deps{Metrics: m, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for code, reason := range map[string]string{"unknown_columns": "unknown_header", "too_many_bad_rows": "bad_row", "too_big": "size", "bad_file": "parse"} {
		a.refused("abrp", &parseFailure{code: code})
		if got := testutil.ToFloat64(m.HistoryImportRejected.WithLabelValues("abrp", reason)); got != 1 {
			t.Errorf("%s -> %s = %v, want 1", code, reason, got)
		}
	}
	a.refused("abrp", &parseFailure{code: "need_choice"})
	total := 0.0
	for _, r := range metrics.ImportRejectReasons {
		total += testutil.ToFloat64(m.HistoryImportRejected.WithLabelValues("abrp", r))
	}
	if total != 4 {
		t.Errorf("total refusals %v, want 4 (need_choice must not count)", total)
	}
	// Every source and reason exports at 0 before the first file, so a rate() has a series to start from.
	if got := testutil.CollectAndCount(m.HistoryImportRejected); got != len(metrics.ImportSources)*len(metrics.ImportRejectReasons) {
		t.Errorf("%d rejected series, want %d", got, len(metrics.ImportSources)*len(metrics.ImportRejectReasons))
	}
}

// A first line that is a data row (a contact list: name, email, street) never reaches the log; only a count does.
func TestHistoryImportRefusalLogDropsDataRows(t *testing.T) {
	var logs bytes.Buffer
	a := newHistoryImportAPI(Deps{Metrics: metrics.New(), Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	req := &importRequest{
		source:  "tezlab",
		formats: importSources["tezlab"],
		files:   []importFile{{name: "x.csv", data: []byte("Maria Gonzalez,maria.g@example.com,12 Elm Street\nJohn Smith,js@example.org,4 Oak Avenue\n")}},
	}
	_, pf := req.parse(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if pf == nil || pf.code != "unknown_columns" {
		t.Fatalf("parse = %+v", pf)
	}
	a.refused("tezlab", pf)
	out := logs.String()
	for _, banned := range []string{"Maria", "Gonzalez", "example.com", "Elm", "@"} {
		if strings.Contains(out, banned) {
			t.Errorf("log carries %q: %s", banned, out)
		}
	}
	if !strings.Contains(out, "3 cells, not logged") {
		t.Errorf("log lacks the count: %s", out)
	}
}

// Words that carry no digit or "@" still do not reach the log unless the line has an EV-export word.
func TestHistoryImportRefusalLogNeedsAnExportWord(t *testing.T) {
	var logs bytes.Buffer
	a := newHistoryImportAPI(Deps{Metrics: metrics.New(), Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	req := &importRequest{
		source:  "tezlab",
		formats: importSources["tezlab"],
		files:   []importFile{{name: "x.csv", data: []byte("Maria Gonzalez,Springfield,USA\nJohn Smith,Portland,USA\n")}},
	}
	_, pf := req.parse(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if pf == nil || pf.code != "unknown_columns" {
		t.Fatalf("parse = %+v", pf)
	}
	a.refused("tezlab", pf)
	for _, banned := range []string{"Maria", "Springfield", "USA"} {
		if strings.Contains(logs.String(), banned) {
			t.Errorf("log carries %q: %s", banned, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "3 cells, not logged") {
		t.Errorf("log lacks the count: %s", logs.String())
	}
	for _, c := range []string{"Smith", "Brenda", "Miami"} {
		if hasExportWord([]string{c}) {
			t.Errorf("%q passes as an export word", c)
		}
	}
	for _, c := range []string{"Start Time", "Distance (mi)", "Charging Energy", "Odometer"} {
		if !hasExportWord([]string{c}) {
			t.Errorf("%q should pass", c)
		}
	}
}
