//go:build integration

package api

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/imports"
)

const (
	hiDrives = `Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%),Max Speed (mph)
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20.5,80,70,65
2026-03-02T08:00:00Z,2026-03-02T09:00:00Z,40,70,50,75
2026-03-03T08:00:00Z,2026-03-03T08:30:00Z,10,90,85,60
`
	hiCharges = `Start Time,End Time,Start Battery (%),End Battery (%),Energy Added (Wh),Max Power (kW),Cost,Currency,Latitude,Longitude
2026-03-01T10:00:00Z,2026-03-01T11:00:00Z,20,80,60000,150,18.40,USD,34.5,-118.2
2026-03-02T12:00:00Z,2026-03-02T20:00:00Z,50,60,8000,7,,,34.5,-118.2
`
)

type hiEnv struct {
	t    *testing.T
	pool *sql.DB
	h    http.Handler
	uid  uuid.UUID
}

func (e *hiEnv) as(uid uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.h.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uid)))
	})
}

func (e *hiEnv) post(uid uuid.UUID, path string, fields map[string]string, files ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for i, f := range files {
		fw, _ := mw.CreateFormFile("file", fmt.Sprintf("f%d.csv", i))
		_, _ = fw.Write([]byte(f))
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev"+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	e.as(uid).ServeHTTP(rec, req)
	return rec
}

func (e *hiEnv) form(rivianID string, extra map[string]string) map[string]string {
	m := map[string]string{"source": "tezlab", "vehicle_id": rivianID, "distance_unit": "mi"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func hiJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

func (e *hiEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func newHIEnv(t *testing.T) (*hiEnv, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	d := backupTestDepsFor(t, pool)
	d.HistoryImport = true
	a := newHistoryImportAPI(d)
	r := chi.NewRouter()
	r.Post("/api/import/preview", a.preview)
	r.Post("/api/import/commit", a.commit)
	r.Get("/api/imports", a.list)
	r.Post("/api/imports/{id}/undo", a.undo)
	mk := func(name string) uuid.UUID {
		uid, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-'||$2,'R1S')`, uid, name); err != nil {
			t.Fatal(err)
		}
		return uid
	}
	e := &hiEnv{t: t, pool: pool, h: r}
	e.uid = mk("hi-a")
	return e, mk("hi-b")
}

// Preview counts equal what the commit writes, every skip reason is counted, a re-upload is refused, and undo takes the batch away.
func TestHistoryImportPreviewEqualsCommitAndUndo(t *testing.T) {
	e, other := newHIEnv(t)
	form := e.form("rv-hi-a", nil)

	pv := e.post(e.uid, "/api/import/preview", form, hiDrives, hiCharges)
	if pv.Code != 200 {
		t.Fatalf("preview %d %s", pv.Code, pv.Body)
	}
	p := hiJSON(t, pv)
	if p["new_drives"] != 3.0 || p["new_charges"] != 2.0 {
		t.Fatalf("preview counts %v/%v, want 3/2: %s", p["new_drives"], p["new_charges"], pv.Body)
	}
	if e.count(`SELECT count(*) FROM drives WHERE user_id = $1`, e.uid) != 0 {
		t.Fatal("preview wrote rows")
	}

	// A different file than the one previewed is refused.
	if rec := e.post(e.uid, "/api/import/commit", e.form("rv-hi-a", map[string]string{"sha256": "00"}), hiDrives, hiCharges); rec.Code != 409 || hiJSON(t, rec)["code"] != "preview_stale" {
		t.Errorf("sha mismatch = %d %s", rec.Code, rec.Body)
	}
	// Another user's vehicle is a 404.
	if rec := e.post(other, "/api/import/preview", e.form("rv-hi-a", nil), hiDrives); rec.Code != 404 {
		t.Errorf("not my vehicle = %d", rec.Code)
	}

	form["sha256"] = p["sha256"].(string)
	cm := e.post(e.uid, "/api/import/commit", form, hiDrives, hiCharges)
	if cm.Code != 200 || !strings.HasPrefix(cm.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("commit %d %s", cm.Code, cm.Header().Get("Content-Type"))
	}
	var lines []map[string]any
	sc := bufio.NewScanner(cm.Body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line is not JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) < 2 || lines[0]["type"] != "start" || lines[0]["total"] != 5.0 || lines[len(lines)-1]["type"] != "done" {
		t.Fatalf("lines = %v", lines)
	}
	done := lines[len(lines)-1]
	if done["drives"] != p["new_drives"] || done["charges"] != p["new_charges"] {
		t.Errorf("commit wrote %v/%v, preview said %v/%v", done["drives"], done["charges"], p["new_drives"], p["new_charges"])
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND source = 'tezlab_import'`, e.uid); n != 3 {
		t.Errorf("drives stored = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM charges WHERE user_id = $1 AND source = 'tezlab_import'`, e.uid); n != 2 {
		t.Errorf("charges stored = %d", n)
	}
	// An import is a bulk write: no notification, engagement or hook rows come of it.
	if n := e.count(`SELECT count(*) FROM push_notifications WHERE user_id = $1`, e.uid); n != 0 {
		t.Errorf("%d notifications from an import", n)
	}

	// The same files again: the preview says so and the commit refuses with 409.
	if again := hiJSON(t, e.post(e.uid, "/api/import/preview", form, hiDrives, hiCharges)); again["already_imported"] == nil {
		t.Errorf("preview of the same files has no already_imported: %v", again)
	}
	if rec := e.post(e.uid, "/api/import/commit", form, hiCharges, hiDrives); rec.Code != 409 || hiJSON(t, rec)["code"] != "already_imported" {
		t.Errorf("second commit (files in another order) = %d %s", rec.Code, rec.Body)
	}
	// Part of it again, in a new file: the stored rows are skipped, none doubled.
	part := hiJSON(t, e.post(e.uid, "/api/import/preview", e.form("rv-hi-a", nil), hiDrives[:strings.Index(hiDrives, "\n2026-03-02")]+"\n"))
	if part["new_drives"] != 0.0 {
		t.Errorf("rows already stored are offered again: %v", part)
	}

	// The list shows the batch; another user sees nothing and cannot undo it.
	list := func(uid uuid.UUID) []any {
		rec := httptest.NewRecorder()
		e.as(uid).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/imports", nil))
		return hiJSON(t, rec)["imports"].([]any)
	}
	if got := list(e.uid); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if len(list(other)) != 0 {
		t.Error("another user sees the batch")
	}
	id := done["id"].(string)
	undo := func(uid uuid.UUID) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.as(uid).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/imports/"+id+"/undo", nil))
		return rec
	}
	if rec := undo(other); rec.Code != 404 {
		t.Errorf("another user's undo = %d", rec.Code)
	}
	if rec := undo(e.uid); rec.Code != 200 || hiJSON(t, rec)["drives"] != 3.0 || hiJSON(t, rec)["charges"] != 2.0 {
		t.Errorf("undo = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND source = 'tezlab_import'`, e.uid); n != 0 {
		t.Errorf("%d drives left after undo", n)
	}
	if rec := undo(e.uid); rec.Code != 404 {
		t.Errorf("second undo = %d", rec.Code)
	}
}

// A live session the file also has is skipped as overlaps_live; any other stored row as overlaps_existing. Preview and commit agree.
func TestHistoryImportSkipsLiveAndExisting(t *testing.T) {
	e, _ := newHIEnv(t)
	var vid uuid.UUID
	if err := e.pool.QueryRow(`SELECT id FROM vehicles WHERE user_id = $1`, e.uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	ins := func(ext, src, start, end string) {
		if _, err := e.pool.Exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4,$5,5,$6)`,
			e.uid, vid, ext, start, end, src); err != nil {
			t.Fatal(err)
		}
	}
	ins("live-1", "live", "2026-03-01T08:05:00Z", "2026-03-01T08:25:00Z")
	ins("old-1", "electrafi_import", "2026-03-02T08:00:00Z", "2026-03-02T09:00:00Z")

	form := e.form("rv-hi-a", nil)
	p := hiJSON(t, e.post(e.uid, "/api/import/preview", form, hiDrives))
	sk, _ := p["skipped"].(map[string]any)
	if p["new_drives"] != 1.0 || len(sk) != 2 {
		t.Fatalf("preview = %v", p)
	}
	form["sha256"] = p["sha256"].(string)
	cm := e.post(e.uid, "/api/import/commit", form, hiDrives)
	if cm.Code != 200 {
		t.Fatalf("commit %d %s", cm.Code, cm.Body)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1`, e.uid); n != 3 {
		t.Errorf("drives after = %d, want 2 existing + 1 new", n)
	}
}

// Ten commits an hour; the eleventh is a 429 with Retry-After, counted from the imports rows.
func TestHistoryImportCommitIsRateLimited(t *testing.T) {
	e, _ := newHIEnv(t)
	ctx := context.Background()
	is, err := imports.New(e.pool, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < importCommitsPerHour; i++ {
		if _, err := is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: fmt.Sprintf("%064d", i), Bytes: 1}); err != nil {
			t.Fatal(err)
		}
	}
	form := e.form("rv-hi-a", nil)
	p := hiJSON(t, e.post(e.uid, "/api/import/preview", form, hiDrives))
	form["sha256"] = p["sha256"].(string)
	rec := e.post(e.uid, "/api/import/commit", form, hiDrives)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("commit = %d %s", rec.Code, rec.Body)
	}
}
