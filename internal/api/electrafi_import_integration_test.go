//go:build integration

package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/metrics"
)

const efHeader = "Date,battery_level,odometer,latitude,longitude,speed,shift_state,charging_state,driveNumber,chargeNumber\n"

// efSession is one synthetic drive (08:00 to 08:20) and one charge (18:00 to 20:00) on the given day; n is its drive and charge number.
func efSession(day string, n int) string {
	var b strings.Builder
	for _, r := range []string{
		"08:00:00,80,1000,34.161,-118.168,30,D,,%d,0",
		"08:10:00,78,1005,34.171,-118.158,40,D,,%d,0",
		"08:20:00,76,1010,34.181,-118.148,35,D,,%d,0",
		"18:00:00,40,1010,34.161,-118.168,0,P,Charging,0,%d",
		"19:00:00,50,1010,34.161,-118.168,0,P,Charging,0,%d",
		"20:00:00,60,1010,34.161,-118.168,0,P,Complete,0,%d",
	} {
		fmt.Fprintf(&b, day+" "+r+"\n", n)
	}
	return b.String()
}

// efEvents posts files to the ElectraFi import and returns the NDJSON events.
func (e *hiEnv) efEvents(files ...string) []map[string]any {
	e.t.Helper()
	rec := e.post(e.uid, "/api/import/electrafi", map[string]string{"vehicle_id": "rv-hi-a", "tz": "UTC"}, files...)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("electrafi import = %d %s", rec.Code, rec.Body)
	}
	var out []map[string]any
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			e.t.Fatalf("not NDJSON: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func efFind(events []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, m := range events {
		if m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

// An ElectraFi upload is a batch like TezLab's and ABRP's: one imports row per file (no user filename), its rows carry the import id,
// the same file again is refused with the batch to undo, a re-import never overwrites an edited row and skips what overlaps, a file the
// importer cannot read leaves no batch behind, Undo takes back the batch's rows, and the counters carry source "electrafi".
func TestElectrafiImportIsABatch(t *testing.T) {
	e, _ := newHIEnv(t)
	d := e.api.d
	d.Metrics = metrics.New()
	r := chi.NewRouter()
	r.Post("/api/import/electrafi", handleImportElectrafi(d))
	r.Post("/api/imports/{id}/undo", e.api.undo)
	e.h = r

	fileA := efHeader + efSession("2026-10-01", 1)
	ev := e.efEvents(fileA)
	done := efFind(ev, "done")
	if len(done) != 1 {
		t.Fatalf("events = %v", ev)
	}
	file := efFind(ev, "file_done")[0]["result"].(map[string]any)
	if file["Drives"] != 1.0 || file["Charges"] != 1.0 {
		t.Fatalf("result = %v, want 1 drive and 1 charge", file)
	}
	var batch string
	var drives, charges int
	var filename string
	if err := e.pool.QueryRow(`SELECT id, filename, drives, charges FROM imports WHERE user_id = $1 AND source = 'electrafi_import' AND undone_at IS NULL`, e.uid).
		Scan(&batch, &filename, &drives, &charges); err != nil {
		t.Fatalf("no batch: %v", err)
	}
	if filename != "electrafi_import.csv" || drives != 1 || charges != 1 {
		t.Errorf("batch = %q %d drives %d charges; the upload's own name must not be kept", filename, drives, charges)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND import_id = $2`, e.uid, batch); n != 1 {
		t.Errorf("%d drives carry the import id", n)
	}
	if n := e.count(`SELECT count(*) FROM charges WHERE user_id = $1 AND import_id = $2`, e.uid, batch); n != 1 {
		t.Errorf("%d charges carry the import id", n)
	}

	// The same file again: refused, with the batch to undo, and nothing new.
	ev = e.efEvents(fileA)
	errs := efFind(ev, "error")
	if len(errs) == 0 || errs[0]["code"] != "already_imported" || errs[0]["id"] != batch {
		t.Fatalf("second upload = %v, want already_imported for %s", ev, batch)
	}
	if n := e.count(`SELECT count(*) FROM imports WHERE user_id = $1 AND source = 'electrafi_import'`, e.uid); n != 1 {
		t.Errorf("%d batches after the refused upload", n)
	}

	// The user edits the drive, then a second file covering the same session plus a new day arrives: the edit stays, the overlap is
	// skipped, the new day comes in under its own batch.
	if _, err := e.pool.Exec(`UPDATE drives SET trailer_override = true WHERE user_id = $1 AND import_id = $2`, e.uid, batch); err != nil {
		t.Fatal(err)
	}
	ev = e.efEvents(efHeader + efSession("2026-10-01", 1) + efSession("2026-10-02", 2))
	res := efFind(ev, "file_done")[0]["result"].(map[string]any)
	if res["Drives"] != 1.0 || res["Charges"] != 1.0 || res["skipped_existing"] != 2.0 {
		t.Errorf("overlapping file = %v, want 1 new drive, 1 new charge, 2 skipped as existing", res)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND trailer_override`, e.uid); n != 1 {
		t.Errorf("%d edited drives after the re-import, want the 1 edit kept", n)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND source = 'electrafi_import'`, e.uid); n != 2 {
		t.Errorf("%d drives, want 2", n)
	}

	// A file the importer cannot read: an error event, no batch left, no rows.
	before := e.count(`SELECT count(*) FROM imports WHERE user_id = $1 AND undone_at IS NULL`, e.uid)
	ev = e.efEvents("Date,battery_level\n2026-10-03 08:00:00,80\n")
	if len(efFind(ev, "error")) == 0 {
		t.Fatalf("bad header events = %v", ev)
	}
	if n := e.count(`SELECT count(*) FROM imports WHERE user_id = $1 AND undone_at IS NULL`, e.uid); n != before {
		t.Errorf("%d live batches after a refused file, want %d", n, before)
	}

	// Undo of the first batch takes its rows; the file can then be imported again.
	rec := httptest.NewRecorder()
	e.as(e.uid).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/imports/"+batch+"/undo", nil))
	if rec.Code != 200 {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM drives WHERE user_id = $1 AND import_id = $2`, e.uid, batch); n != 0 {
		t.Errorf("%d drives left after undo", n)
	}
	ev = e.efEvents(fileA)
	if len(efFind(ev, "done")) != 1 {
		t.Fatalf("re-import after undo = %v", ev)
	}

	m := d.Metrics
	if got := testutil.ToFloat64(m.HistoryImportCommits.WithLabelValues("electrafi")); got != 3 {
		t.Errorf("commits = %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.HistoryImportRejected.WithLabelValues("electrafi", "unknown_header")); got != 1 {
		t.Errorf("unknown_header rejections = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.HistoryImportRows.WithLabelValues("electrafi", "drives")); got != 3 {
		t.Errorf("drive rows = %v, want 3", got)
	}
}
