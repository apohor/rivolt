package histimport

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Files that fit under every byte cap and still try to make the decoders hold a lot: each must be refused with ErrTooBig, fast and with a small
// peak heap, because a parser that runs out of memory takes the pod's vehicle leases with it.
const adversarialPeakMax = 32 << 20

// zipOf builds a workbook whose parts are written by the given functions.
func zipOf(parts map[string]func(w io.Writer)) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, fn := range parts {
		w, _ := zw.Create(name)
		fn(w)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func repeat(w io.Writer, s string, n int) {
	chunk := strings.Repeat(s, 4096)
	for left := n; left > 0; left -= 4096 {
		k := min(4096, left)
		_, _ = io.WriteString(w, chunk[:k*len(s)])
	}
}

func openAndDrain(b []byte) error {
	tb, err := OpenXLSX(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return err
	}
	for {
		if _, err := tb.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func wantTooBig(t *testing.T, name string, fn func() error) {
	t.Helper()
	var err error
	pk, took := peak(func() { err = fn() })
	t.Logf("%s: %v, peak %.1f MB, %v", name, err, float64(pk)/1e6, took.Round(time.Millisecond))
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("%s: want ErrTooBig, got %v", name, err)
	}
	if pk > adversarialPeakMax {
		t.Fatalf("%s: peak %d bytes", name, pk)
	}
}

func scaled(small, big int) int {
	if os.Getenv("HISTIMPORT_MEASURE") != "" {
		return big
	}
	return small
}

func TestAdversarialXMLNesting(t *testing.T) {
	n := scaled(10<<20, 66<<20) // "<a>" repeats: 30 MB or 200 MB of XML
	b := zipOf(map[string]func(io.Writer){"xl/worksheets/sheet1.xml": func(w io.Writer) {
		_, _ = io.WriteString(w, "<worksheet><sheetData><row>")
		repeat(w, "<a>", n)
	}})
	wantTooBig(t, fmt.Sprintf("nesting %d MB xml in %d KB zip", n*3>>20, len(b)>>10), func() error { return openAndDrain(b) })
}

func TestAdversarialSharedStringsNesting(t *testing.T) {
	n := scaled(10<<20, 66<<20)
	b := zipOf(map[string]func(io.Writer){
		"xl/sharedStrings.xml":     func(w io.Writer) { _, _ = io.WriteString(w, "<sst><si>"); repeat(w, "<a>", n) },
		"xl/worksheets/sheet1.xml": func(w io.Writer) { _, _ = io.WriteString(w, "<worksheet/>") },
	})
	wantTooBig(t, "shared strings nesting", func() error { return openAndDrain(b) })
}

func TestAdversarialSharedStringsTotal(t *testing.T) {
	m := scaled(40, 190) // MB of strings: over the 16 MB budget
	b := zipOf(map[string]func(io.Writer){
		"xl/sharedStrings.xml": func(w io.Writer) {
			_, _ = io.WriteString(w, "<sst>")
			repeat(w, "<si><t>"+strings.Repeat("x", 1000)+"</t></si>", m<<10) // m*1000*1030 B
			_, _ = io.WriteString(w, "</sst>")
		},
		"xl/worksheets/sheet1.xml": func(w io.Writer) { _, _ = io.WriteString(w, "<worksheet/>") },
	})
	wantTooBig(t, fmt.Sprintf("shared strings %d MB", m), func() error { return openAndDrain(b) })
}

func TestAdversarialOneHugeTextNode(t *testing.T) {
	b := zipOf(map[string]func(io.Writer){"xl/worksheets/sheet1.xml": func(w io.Writer) {
		_, _ = io.WriteString(w, "<worksheet><sheetData><row><c><v>")
		repeat(w, "a", scaled(8<<20, 190<<20))
	}})
	wantTooBig(t, "one text node", func() error { return openAndDrain(b) })
}

func TestAdversarialCellOfManyCommentsStaysSmall(t *testing.T) {
	b := zipOf(map[string]func(io.Writer){"xl/worksheets/sheet1.xml": func(w io.Writer) {
		_, _ = io.WriteString(w, "<worksheet><sheetData><row><c><v>")
		repeat(w, "x<!---->", scaled(2<<20, 24<<20))
	}})
	wantTooBig(t, "cell built of many text pieces", func() error { return openAndDrain(b) })
}

func TestAdversarialCSVLineOfCommas(t *testing.T) {
	n := scaled(20<<20, 50<<20)
	var err error
	pk, took := peak(func() {
		r := io.MultiReader(strings.NewReader("a,b\n"), &zeroCommas{n: n})
		var tb Table
		if tb, err = OpenCSV(r); err != nil {
			return
		}
		for err == nil {
			_, err = tb.Next()
		}
	})
	t.Logf("csv line of %d MB of commas: %v, peak %.1f MB, %v", n>>20, err, float64(pk)/1e6, took.Round(time.Millisecond))
	if !errors.Is(err, ErrTooBig) || pk > adversarialPeakMax {
		t.Fatalf("%v peak %d", err, pk)
	}
}

type zeroCommas struct{ n int }

func (z *zeroCommas) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), z.n)
	for i := 0; i < k; i++ {
		p[i] = ','
	}
	z.n -= k
	return k, nil
}

func TestAdversarialCSVHugeQuotedRecord(t *testing.T) {
	// One quoted field across many short lines, never closed: the record would grow to the whole file.
	var err error
	pk, _ := peak(func() {
		r := io.MultiReader(strings.NewReader("a,b\n\"start\n"), &lines{n: scaled(20<<20, 100<<20)})
		var tb Table
		if tb, err = OpenCSV(r); err != nil {
			return
		}
		for err == nil {
			_, err = tb.Next()
		}
	})
	if !errors.Is(err, ErrTooBig) || pk > adversarialPeakMax {
		t.Fatalf("%v peak %d", err, pk)
	}
}

type lines struct{ n int }

func (l *lines) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), l.n)
	for i := 0; i < k; i++ {
		p[i] = "abcdefg\n"[i%8]
	}
	l.n -= k
	return k, nil
}

func TestAdversarialZipDirectory(t *testing.T) {
	entries := scaled(5000, 60000) // under 65,535 so the count is real; Go writes zip64 past it
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < entries; i++ {
		_, _ = zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f%07d", i), Method: zip.Store})
	}
	_ = zw.Close()
	b := buf.Bytes()
	wantTooBig(t, fmt.Sprintf("zip with %d entries", entries), func() error { return openAndDrain(b) })

	// The same directory with its entry count and size forged down to look small: the distance to the record still gives it away.
	i := bytes.LastIndex(b, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint16(b[i+10:], 3)
	binary.LittleEndian.PutUint32(b[i+12:], 100)
	if len(b)-int(binary.LittleEndian.Uint32(b[i+16:])) > maxZipDirectory {
		wantTooBig(t, "forged zip directory", func() error { return openAndDrain(b) })
	}
}

func TestAdversarialHugeEntryZip(t *testing.T) {
	if os.Getenv("HISTIMPORT_MEASURE") == "" {
		t.Skip("set HISTIMPORT_MEASURE=1")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 400_000; i++ {
		_, _ = zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f%07d", i), Method: zip.Store})
	}
	_ = zw.Close()
	b := buf.Bytes()
	wantTooBig(t, fmt.Sprintf("zip with 400k entries (%d MB)", len(b)>>20), func() error { return openAndDrain(b) })
}

func TestSessionCoordinatesOffTheGlobeAreDropped(t *testing.T) {
	in := `Start Time,End Time,Distance (mi),Start Latitude,Start Longitude
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20,95.5,10
2026-03-02T08:00:00Z,2026-03-02T08:30:00Z,20,34.1,-118.2
2026-03-03T08:00:00Z,2026-03-03T08:30:00Z,20,0,0
`
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	if err != nil || len(r.Drives) != 3 || r.Report.DroppedCoords != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if r.Drives[0].StartLat != 0 || r.Drives[0].StartLon != 0 || r.Drives[1].StartLat != 34.1 {
		t.Fatalf("%+v", r.Drives)
	}
}

func TestRowsBefore2021AreBad(t *testing.T) {
	in := `Start Time,End Time,Distance (mi)
1970-01-01T00:00:00Z,1970-01-01T00:30:00Z,20
2026-03-02T08:00:00Z,2026-03-02T08:30:00Z,20
2026-03-03T08:00:00Z,2026-03-03T08:30:00Z,20
2026-03-04T08:00:00Z,2026-03-04T08:30:00Z,20
2026-03-05T08:00:00Z,2026-03-05T08:30:00Z,20
2026-03-06T08:00:00Z,2026-03-06T08:30:00Z,20
2026-03-07T08:00:00Z,2026-03-07T08:30:00Z,20
`
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	if err != nil || len(r.Drives) != 6 || r.Report.Skipped[ReasonTooOld] != 1 || r.Report.BadRows != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestBlankVehicleLabelIsAnotherVehicleOnceOneIsChosen(t *testing.T) {
	in := `Start Time,End Time,Distance (mi),Vehicle
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20,Truck
2026-03-02T08:00:00Z,2026-03-02T08:30:00Z,20,Van
2026-03-03T08:00:00Z,2026-03-03T08:30:00Z,20,
`
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now, Vehicle: "Van"})
	if err != nil || len(r.Drives) != 1 || r.Report.Skipped[ReasonOtherVeh] != 2 {
		t.Fatalf("%v %+v", err, r)
	}
}
