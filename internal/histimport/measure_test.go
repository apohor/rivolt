package histimport

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// peak runs fn and returns the highest live heap it saw above the heap before it, sampled every millisecond.
func peak(fn func()) (peakBytes uint64, took time.Duration) {
	// A tight GC target, so the sampled heap is close to what is live, not what the collector has not yet swept.
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var hi atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > hi.Load() {
				hi.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	start := time.Now()
	fn()
	took = time.Since(start)
	close(stop)
	<-done
	if h := hi.Load(); h > base.HeapAlloc {
		return h - base.HeapAlloc, took
	}
	return 0, took
}

// syntheticDrivesCSV is a TezLab-shaped drives file of n rows, all valid.
func syntheticDrivesCSV(n int) []byte {
	var b bytes.Buffer
	b.WriteString("Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%),Max Speed (mph),Start Latitude,Start Longitude\n")
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		s := t0.Add(time.Duration(i) * time.Minute)
		fmt.Fprintf(&b, "%s,%s,%.1f,%d,%d,%d,34.%04d,-118.%04d\n", s.Format(time.RFC3339), s.Add(30*time.Minute).Format(time.RFC3339), 5+float64(i%40), 80, 70, 40+i%30, i%10000, i%10000)
	}
	return b.Bytes()
}

// syntheticPointsXLSX is an ABRP-shaped workbook of n points: drives of 400 points, a minute apart.
func syntheticPointsXLSX(n int) []byte {
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0"?><worksheet><sheetData><row r="1">`)
	for c, h := range []string{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (mph)"} {
		fmt.Fprintf(&sheet, `<c r="%c1" t="inlineStr"><is><t>%s</t></is></c>`, 'A'+c, h)
	}
	sheet.WriteString(`</row>`)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for i := 0; i < n; i++ {
		run, k := i/400, i%400
		ts := t0 + int64(run)*7200 + int64(k)*60
		fmt.Fprintf(&sheet, `<row r="%d"><c><v>%d</v></c><c><v>%.5f</v></c><c><v>-118.0</v></c><c><v>%d</v></c><c><v>55</v></c></row>`, i+2, ts, 34+float64(k)*0.0005, 90-k/10)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("xl/worksheets/sheet1.xml")
	_, _ = io.WriteString(w, sheet.String())
	_ = zw.Close()
	return buf.Bytes()
}

// TestMeasureParseOf500kRows (a drives CSV at the session cap, 100k rows, and a 500k-point xlsx) is the slice-2 measurement: HISTIMPORT_MEASURE=1 go test -run Measure -v. It logs file size, time and peak
// live heap for a 500k-row CSV of drives and a 500k-point xlsx, which is what sizes the per-pod parse semaphore (design §6, R8).
func TestMeasureParseOf500kRows(t *testing.T) {
	if os.Getenv("HISTIMPORT_MEASURE") == "" {
		t.Skip("set HISTIMPORT_MEASURE=1")
	}
	o := Options{Now: time.Now(), DistanceUnit: "mi", Location: time.UTC, PackKWh: 135}
	csvB := syntheticDrivesCSV(MaxSessions)
	var rc *Result
	pk, took := peak(func() {
		tb, err := OpenCSV(bytes.NewReader(csvB))
		if err != nil {
			t.Fatal(err)
		}
		if rc, err = Parse(tb, TezLabDrives, o); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("csv drives: %d rows, file %.1f MB, %d drives, %v, peak extra heap %.0f MB (file itself held by the caller: %.1f MB)",
		rc.Report.Rows, float64(len(csvB))/1e6, len(rc.Drives), took.Round(time.Millisecond), float64(pk)/1e6, float64(len(csvB))/1e6)
	rc = nil

	xl := syntheticPointsXLSX(500_000)
	var rx *Result
	pk, took = peak(func() {
		tb, err := OpenXLSX(bytes.NewReader(xl), int64(len(xl)))
		if err != nil {
			t.Fatal(err)
		}
		if rx, err = Parse(tb, ABRPPoints, o); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("xlsx points: %d rows, file %.1f MB, %d drives %d charges, %v, peak extra heap %.0f MB (file held by the caller: %.1f MB)",
		rx.Report.Rows, float64(len(xl))/1e6, len(rx.Drives), len(rx.Charges), took.Round(time.Millisecond), float64(pk)/1e6, float64(len(xl))/1e6)
}
