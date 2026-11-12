//go:build integration

package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

// hiDriveFile is a synthetic TezLab drives export of n drives (ten minutes apart from 2021-06-01), each row padded with pad bytes of
// an unused column.
func hiDriveFile(n, pad int, from time.Time) string {
	var b strings.Builder
	b.WriteString("Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%),Max Speed (mph),Notes\n")
	filler := strings.Repeat("n", pad)
	for i := 0; i < n; i++ {
		s := from.Add(time.Duration(i) * 10 * time.Minute)
		fmt.Fprintf(&b, "%s,%s,12.5,80,70,65,%s\n", s.Format(time.RFC3339), s.Add(5*time.Minute).Format(time.RFC3339), filler)
	}
	return b.String()
}

// heapPeak runs f and returns the highest live heap seen above the heap before it started.
func heapPeak(f func()) uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base := m.HeapAlloc
	var peak atomic.Uint64
	profiled := false
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
			if prof := os.Getenv("RIVOLT_MEASURE_PROFILE"); prof != "" && ms.HeapAlloc > base+150<<20 && !profiled {
				profiled = true
				runtime.GC()
				if f, err := os.Create(prof); err == nil {
					_ = pprof.Lookup("heap").WriteTo(f, 0)
					_ = f.Close()
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	f()
	close(stop)
	<-done
	if p := peak.Load(); p > base {
		return p - base
	}
	return 0
}

// Two files whose drives add up past MaxSessions are refused as too_big, though each file alone is under it.
func TestHistoryImportCapsSessionsAcrossFiles(t *testing.T) {
	e, _ := newHIEnv(t)
	from := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	a, b := hiDriveFile(60_000, 0, from), hiDriveFile(60_000, 0, from.Add(1000*24*time.Hour))
	rec := e.post(e.uid, "/api/import/preview", e.form("rv-hi-a", nil), a, b)
	if rec.Code != http.StatusRequestEntityTooLarge || hiJSON(t, rec)["code"] != "too_big" {
		t.Errorf("preview = %d %.200s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM imports WHERE user_id = $1`, e.uid); n != 0 {
		t.Errorf("%d import rows written", n)
	}
}

// The server's WriteTimeout runs from the request's headers; a commit whose body arrives late still gets its done line, because the
// handler moves the write deadline.
func TestHistoryImportCommitOutlivesWriteTimeout(t *testing.T) {
	e, _ := newHIEnv(t)
	form := e.form("rv-hi-a", nil)
	p := hiJSON(t, e.post(e.uid, "/api/import/preview", form, hiDrives))
	form["sha256"] = p["sha256"].(string)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range form {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "f.csv")
	_, _ = fw.Write([]byte(hiDrives))
	_ = mw.Close()
	body := buf.Bytes()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.h.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), e.uid)))
	}))
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	pr, pw := io.Pipe()
	go func() { // the last bytes arrive after the server's write timeout has passed
		_, _ = pw.Write(body[:len(body)/2])
		time.Sleep(700 * time.Millisecond)
		_, _ = pw.Write(body[len(body)/2:])
		_ = pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/import/commit", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var last map[string]any
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		_ = json.Unmarshal(sc.Bytes(), &last)
	}
	if last["type"] != "done" || last["drives"] != 3.0 {
		t.Errorf("last line = %v (status %d)", last, resp.StatusCode)
	}
}

// RIVOLT_MEASURE_IMPORT=1 measures the heap peak through the handlers (preview and commit) and the 100k-session commit's wall time.
// The budget is 200 MiB per request: two slots on a 512Mi pod with a 400 MiB working budget.
func TestHistoryImportMeasure(t *testing.T) {
	if os.Getenv("RIVOLT_MEASURE_IMPORT") == "" {
		t.Skip("set RIVOLT_MEASURE_IMPORT=1")
	}
	const budget = 200 << 20
	// Garbage the collector has not yet reached is counted in HeapAlloc; with the default GOGC a test process holding its own copy of the
	// upload lets that grow to twice the live heap. A 20% target keeps the figure close to the live heap, as a pod near its memory
	// limit (applyMemoryLimit sets 90% of the container) behaves.
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	from := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		files []string
	}{
		{"one 50 MB file", []string{hiDriveFile(100_000, 400, from)}},
		{"four files at the cap", []string{
			hiDriveFile(25_000, 0, from), hiDriveFile(25_000, 0, from.Add(300*24*time.Hour)),
			hiDriveFile(25_000, 0, from.Add(600*24*time.Hour)), hiDriveFile(25_000, 0, from.Add(900*24*time.Hour))}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newHIEnv(t)
			form := e.form("rv-hi-a", nil)
			var pv *httptest.ResponseRecorder
			t0 := time.Now()
			req := e.request("/api/import/preview", form, c.files...)
			pk := heapPeak(func() { pv = e.send(e.uid, req) })
			if pv.Code != 200 {
				t.Fatalf("preview %d %.300s", pv.Code, pv.Body)
			}
			p := hiJSON(t, pv)
			t.Logf("preview: new_drives %v, heap peak %d MiB, %v", p["new_drives"], pk>>20, time.Since(t0).Round(time.Millisecond))
			if pk > budget {
				t.Errorf("preview peak %d MiB over budget", pk>>20)
			}
			form["sha256"] = p["sha256"].(string)
			var cm *httptest.ResponseRecorder
			t0 = time.Now()
			req = e.request("/api/import/commit", form, c.files...)
			pk = heapPeak(func() { cm = e.send(e.uid, req) })
			lines := strings.Split(strings.TrimSpace(cm.Body.String()), "\n")
			t.Logf("commit: %d, heap peak %d MiB, wall %v, last line %.120s", cm.Code, pk>>20, time.Since(t0).Round(time.Second), lines[len(lines)-1])
			if pk > budget {
				t.Errorf("commit peak %d MiB over budget", pk>>20)
			}
		})
	}
	t.Run("tiny uniquely named fields", func(t *testing.T) {
		e, _ := newHIEnv(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for i := 0; buf.Len() < 49<<20; i++ {
			_ = mw.WriteField(fmt.Sprintf("f%d", i), "x")
		}
		_ = mw.Close()
		var rec *httptest.ResponseRecorder
		pk := heapPeak(func() {
			req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/import/preview", bytes.NewReader(buf.Bytes()))
			req.Header.Set("Content-Type", mw.FormDataContentType())
			rec = httptest.NewRecorder()
			e.as(uuid.New()).ServeHTTP(rec, req)
		})
		t.Logf("49 MB of tiny fields: %d, heap peak %d MiB", rec.Code, pk>>20)
		if rec.Code != 400 || pk > 16<<20 {
			t.Errorf("%d, peak %d MiB", rec.Code, pk>>20)
		}
	})
}
