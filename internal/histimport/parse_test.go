package histimport

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

var (
	la  = mustLoc("America/Los_Angeles")
	now = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
)

func mustLoc(n string) *time.Location {
	l, err := time.LoadLocation(n)
	if err != nil {
		panic(err)
	}
	return l
}

func csvT(t *testing.T, s string) Table {
	t.Helper()
	tb, err := OpenCSV(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

const tezDrives = `Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%),Max Speed (mph)
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20.5,80,70,65
2026-03-02T08:00:00Z,2026-03-02T09:00:00Z,40,70,50,75
`

func TestTezLabDrivesPercentMiles(t *testing.T) {
	r, err := Parse(csvT(t, tezDrives), TezLabDrives, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drives) != 2 || r.Report.Accepted != 2 || r.Report.Rows != 2 {
		t.Fatalf("got %d drives, report %+v", len(r.Drives), r.Report)
	}
	d := r.Drives[0]
	if d.DistanceMi != 20.5 || d.StartSoCPct != 80 || d.EndSoCPct != 70 || d.MaxSpeedMph != 65 || d.Source != "tezlab_import" {
		t.Errorf("drive 0 = %+v", d)
	}
	if got := d.AvgSpeedMph; math.Abs(got-41) > 0.01 {
		t.Errorf("avg speed %v, want 41 (distance over duration)", got)
	}
	if d.EnergyUsedKWh != 0 {
		t.Errorf("energy %v without a pack: want 0", d.EnergyUsedKWh)
	}
	r, _ = Parse(csvT(t, tezDrives), TezLabDrives, Options{Now: now, PackKWh: 135})
	if e := r.Drives[0].EnergyUsedKWh; math.Abs(e-13.5) > 1e-9 || !r.Drives[0].EnergyEstimated {
		t.Errorf("energy = %v estimated=%v, want 13.5 true", e, r.Drives[0].EnergyEstimated)
	}
}

func TestKilometresAreConvertedAndAFileWithoutAUnitAsks(t *testing.T) {
	km := "Start Time,End Time,Distance (km)\n2026-03-01T08:00:00Z,2026-03-01T09:00:00Z,100\n"
	r, err := Parse(csvT(t, km), TezLabDrives, Options{Now: now})
	if err != nil || math.Abs(r.Drives[0].DistanceMi-62.1371) > 1e-6 {
		t.Fatalf("km: %v %+v", err, r)
	}
	bare := "Start Time,End Time,Distance\n2026-03-01T08:00:00Z,2026-03-01T09:00:00Z,100\n"
	_, err = Parse(csvT(t, bare), TezLabDrives, Options{Now: now})
	var nc *NeedChoiceError
	if !errors.As(err, &nc) || len(nc.Choices) != 1 || nc.Choices[0] != "distance_unit" {
		t.Fatalf("want a distance_unit question, got %v", err)
	}
	r, err = Parse(csvT(t, bare), TezLabDrives, Options{Now: now, DistanceUnit: "km"})
	if err != nil || math.Abs(r.Drives[0].DistanceMi-62.1371) > 1e-6 || r.Report.DistanceUnit != "km" {
		t.Fatalf("chosen km: %v %+v", err, r)
	}
}

func TestZonelessTimeAsksForTheZoneAndUsesIt(t *testing.T) {
	in := "Start Time,End Time,Distance (mi)\n2026-07-01 08:00:00,2026-07-01 09:00:00,30\n"
	_, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	var nc *NeedChoiceError
	if !errors.As(err, &nc) || nc.Choices[0] != "timezone" {
		t.Fatalf("want a timezone question, got %v", err)
	}
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now, Location: la})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC); !r.Drives[0].StartedAt.Equal(want) {
		t.Errorf("start = %v, want %v (08:00 PDT)", r.Drives[0].StartedAt, want)
	}
}

func TestFallBackHourIsFlaggedNotDropped(t *testing.T) {
	// 2026-11-01 01:30 happens twice in Los Angeles.
	in := "Start Time,End Time,Distance (mi)\n2026-11-01 01:30:00,2026-11-01 02:30:00,10\n"
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now, Location: la})
	if err != nil || len(r.Drives) != 1 || r.Report.Ambiguous == 0 {
		t.Fatalf("err=%v drives=%d report=%+v", err, len(r.Drives), r.Report)
	}
}

func TestSoCFractionIsDetectedFromTheValues(t *testing.T) {
	in := "Start Time,End Time,Distance (mi),Start Battery,End Battery\n2026-03-01T08:00:00Z,2026-03-01T09:00:00Z,30,0.8,0.55\n"
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	if err != nil || r.Report.SoCScale != "fraction" || math.Abs(r.Drives[0].StartSoCPct-80) > 1e-9 || math.Abs(r.Drives[0].EndSoCPct-55) > 1e-9 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestMissingRequiredColumnRefusesWithHeaderNamesOnly(t *testing.T) {
	in := "When,Miles driven\n2026-03-01,12\n"
	_, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	var me *MissingColumnsError
	if !errors.As(err, &me) {
		t.Fatalf("want MissingColumnsError, got %v", err)
	}
	if strings.Contains(err.Error(), "2026-03-01") || strings.Join(me.Header, ",") != "When,Miles driven" {
		t.Errorf("error leaks a row value or lost the header: %v %v", err, me.Header)
	}
}

func TestRowChecksSkipCountAndRefuseAMostlyBadFile(t *testing.T) {
	in := `Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%)
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20,80,70
2026-03-02T09:00:00Z,2026-03-02T08:00:00Z,20,80,70
2026-03-03T08:00:00Z,2026-03-03T08:10:00Z,500,80,70
2026-03-04T08:00:00Z,2026-03-06T08:00:00Z,20,80,70
2026-03-05T08:00:00Z,2026-03-05T08:30:00Z,20,180,70
2027-03-05T08:00:00Z,2027-03-05T08:30:00Z,20,80,70
2026-03-07T08:00:00Z,2026-03-07T08:30:00Z,abc,80,70
`
	_, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	var tm *TooManyBadRowsError
	if !errors.As(err, &tm) {
		t.Fatalf("6 of 7 rows failing must refuse the file, got %v", err)
	}
	for _, reason := range []string{ReasonEndBeforeBeg, ReasonSpeed, ReasonTooLong, ReasonSoCRange, ReasonFuture, ReasonBadNumber} {
		if tm.Skipped[reason] != 1 {
			t.Errorf("skipped[%s] = %d, want 1 (%v)", reason, tm.Skipped[reason], tm.Skipped)
		}
	}
	// With enough good rows around one bad row the same failure is noise: accepted, every row accounted for.
	var b strings.Builder
	b.WriteString("Start Time,End Time,Distance (mi),Start Battery (%),End Battery (%)\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "2026-03-%02dT08:00:00Z,2026-03-%02dT08:30:00Z,20,80,70\n", i%28+1, i%28+1)
	}
	b.WriteString("2026-04-09T09:00:00Z,2026-04-09T08:00:00Z,20,80,70\n")
	r, err := Parse(csvT(t, b.String()), TezLabDrives, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, n := range r.Report.Skipped {
		skipped += n
	}
	if r.Report.Rows != 51 || len(r.Drives)+skipped != r.Report.Rows || r.Report.Skipped[ReasonEndBeforeBeg] != 1 {
		t.Errorf("rows=%d drives=%d skipped=%v", r.Report.Rows, len(r.Drives), r.Report.Skipped)
	}
}

func TestDuplicateStartKeepsTheFirst(t *testing.T) {
	in := `Start Time,End Time,Distance (mi)
2026-03-06T08:00:00Z,2026-03-06T08:30:00Z,20
2026-03-06T08:00:00Z,2026-03-06T08:40:00Z,25
2026-03-07T08:00:00Z,2026-03-07T08:30:00Z,20
`
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	if err != nil || len(r.Drives) != 2 || r.Drives[0].DistanceMi != 20 || r.Report.Skipped[ReasonDuplicate] != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

const tezCharges = `Start Time,End Time,Start Battery (%),End Battery (%),Energy Added (Wh),Max Power (kW),Cost,Currency,Latitude,Longitude
2026-03-01T08:00:00Z,2026-03-01T09:00:00Z,20,80,60000,150,18.40,USD,34.5,-118.2
2026-03-02T08:00:00Z,2026-03-02T16:00:00Z,50,60,8000,7,,,34.5,-118.2
`

func TestTezLabChargesUnitsBasisAndType(t *testing.T) {
	r, err := Parse(csvT(t, tezCharges), TezLabCharges, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Charges) != 2 {
		t.Fatalf("charges = %d (%v)", len(r.Charges), r.Report)
	}
	dc, ac := r.Charges[0], r.Charges[1]
	if dc.EnergyAddedKWh != 60 || dc.SessionType != "dc" || dc.EnergyBasis != "unknown" || !dc.HasCost || dc.Cost != 18.40 || dc.Currency != "USD" {
		t.Errorf("dc = %+v", dc)
	}
	if ac.EnergyAddedKWh != 8 || ac.SessionType != "ac" || ac.HasCost || ac.Currency != "" {
		t.Errorf("ac = %+v", ac)
	}
	if dc.EnergyEstimated {
		t.Error("energy given by the file must not be marked estimated")
	}
}

func TestChargeWithoutEnergyUsesSoCTimesPackAndSaysSo(t *testing.T) {
	in := "Start Time,End Time,Start Battery (%),End Battery (%)\n2026-03-01T08:00:00Z,2026-03-01T09:00:00Z,20,70\n"
	r, err := Parse(csvT(t, in), TezLabCharges, Options{Now: now, PackKWh: 100})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Charges[0]
	if c.EnergyAddedKWh != 50 || !c.EnergyEstimated || c.EnergyBasis != "battery" {
		t.Errorf("%+v", c)
	}
	r, _ = Parse(csvT(t, in), TezLabCharges, Options{Now: now})
	if c := r.Charges[0]; c.EnergyAddedKWh != 0 || c.EnergyEstimated {
		t.Errorf("without a pack: %+v", c)
	}
}

func TestSeveralVehiclesNeedAChoice(t *testing.T) {
	in := `Start Time,End Time,Distance (mi),Vehicle
2026-03-01T08:00:00Z,2026-03-01T08:30:00Z,20,Truck
2026-03-02T08:00:00Z,2026-03-02T08:30:00Z,20,Van
`
	_, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	var nc *NeedChoiceError
	if !errors.As(err, &nc) || nc.Choices[0] != "vehicle" || len(nc.Vehicles) != 2 {
		t.Fatalf("want a vehicle question, got %v", err)
	}
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now, Vehicle: "Van"})
	if err != nil || len(r.Drives) != 1 || r.Report.Skipped[ReasonOtherVeh] != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestCSVDelimiterBOMAndBlankLines(t *testing.T) {
	in := "\xEF\xBB\xBFStart Time;End Time;Distance (mi)\n\n2026-03-01T08:00:00Z;2026-03-01T08:30:00Z;20\n;;\n"
	r, err := Parse(csvT(t, in), TezLabDrives, Options{Now: now})
	if err != nil || len(r.Drives) != 1 || r.Report.Rows != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestExternalIDIsDeterministic(t *testing.T) {
	a := ExternalID("tezlab_import", "veh1", "d", time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	if a != "tezlab_veh1_d_1772352000" {
		t.Errorf("id = %s", a)
	}
}

// --- xlsx ---

// buildXLSX writes a one-sheet workbook with the given rows; strings go through sharedStrings, numbers are numeric cells.
func buildXLSX(rows [][]any) []byte {
	var shared []string
	idx := map[string]int{}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0"?><worksheet><sheetData>`)
	for r, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, r+1)
		for c, v := range row {
			ref := fmt.Sprintf("%c%d", 'A'+c, r+1)
			switch x := v.(type) {
			case string:
				i, ok := idx[x]
				if !ok {
					i = len(shared)
					idx[x] = i
					shared = append(shared, x)
				}
				fmt.Fprintf(&sheet, `<c r="%s" t="s"><v>%d</v></c>`, ref, i)
			case float64:
				fmt.Fprintf(&sheet, `<c r="%s"><v>%v</v></c>`, ref, x)
			}
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	var ss strings.Builder
	ss.WriteString(`<?xml version="1.0"?><sst>`)
	for _, s := range shared {
		ss.WriteString("<si><t>" + strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(s) + "</t></si>")
	}
	ss.WriteString(`</sst>`)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{{"xl/worksheets/sheet1.xml", sheet.String()}, {"xl/sharedStrings.xml", ss.String()}} {
		w, _ := zw.Create(f.name)
		io.WriteString(w, f.body)
	}
	zw.Close()
	return buf.Bytes()
}

func xlsxT(t *testing.T, b []byte) Table {
	t.Helper()
	tb, err := OpenXLSX(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestXLSXPointsBecomeADriveWithAPolylineAndAChargeApart(t *testing.T) {
	base := float64(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Unix())
	rows := [][]any{{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (mph)"}}
	// 12 minutes of driving, one point a minute, 60 mph north.
	for i := 0; i <= 12; i++ {
		rows = append(rows, []any{base + float64(i*60), 34.0 + float64(i)*0.0145, -118.0, float64(80 - i/2), 60.0})
	}
	// Plugged in 20 minutes later (a gap, so a new run): stationary, SoC rising for 30 minutes.
	cb := base + 12*60 + 20*60
	for i := 0; i <= 6; i++ {
		rows = append(rows, []any{cb + float64(i*300), 34.2, -118.0, float64(40 + i*5), 0.0})
	}
	r, err := Parse(xlsxT(t, buildXLSX(rows)), ABRPPoints, Options{Now: now, DistanceUnit: "mi", PackKWh: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drives) != 1 || len(r.Charges) != 1 {
		t.Fatalf("drives=%d charges=%d report=%+v", len(r.Drives), len(r.Charges), r.Report)
	}
	d := r.Drives[0]
	if d.RoutePolyline == "" || d.MaxSpeedMph != 60 || d.StartSoCPct != 80 || d.EndSoCPct != 74 {
		t.Errorf("drive = %+v", d)
	}
	if d.DistanceMi < 11 || d.DistanceMi > 13 {
		t.Errorf("distance %v, want about 12 mi (12 min at 60 mph)", d.DistanceMi)
	}
	c := r.Charges[0]
	if c.StartSoCPct != 40 || c.EndSoCPct != 70 || c.EnergyAddedKWh != 30 || !c.EnergyEstimated || c.EnergyBasis != "battery" {
		t.Errorf("charge = %+v", c)
	}
	if r.Report.Rows != len(rows)-1 {
		t.Errorf("rows %d", r.Report.Rows)
	}
}

func TestPointsAStopInsideADriveDoesNotSplitIt(t *testing.T) {
	base := float64(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Unix())
	rows := [][]any{{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (mph)"}}
	speeds := []float64{40, 40, 0, 0, 0, 40, 40}
	lat := 34.0
	for i, s := range speeds {
		lat += s / 60 / 69
		rows = append(rows, []any{base + float64(i*60), lat, -118.0, 80.0, s})
	}
	r, err := Parse(xlsxT(t, buildXLSX(rows)), ABRPPoints, Options{Now: now, DistanceUnit: "mi"})
	if err != nil || len(r.Drives) != 1 || len(r.Charges) != 0 {
		t.Fatalf("%v drives=%d charges=%d", err, len(r.Drives), len(r.Charges))
	}
}

func TestPointsParkedRunIsCountedNotKept(t *testing.T) {
	base := float64(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Unix())
	rows := [][]any{{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (mph)"}}
	for i := 0; i < 5; i++ {
		rows = append(rows, []any{base + float64(i*60), 34.0, -118.0, 80.0, 0.0})
	}
	r, err := Parse(xlsxT(t, buildXLSX(rows)), ABRPPoints, Options{Now: now, DistanceUnit: "mi"})
	if err != nil || len(r.Drives)+len(r.Charges) != 0 || r.Report.Skipped[ReasonParked] != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestXLSXRefusesWhatIsNotAWorkbook(t *testing.T) {
	if _, err := OpenXLSX(strings.NewReader("not a zip"), 9); err == nil {
		t.Error("a non-zip must be refused")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	io.WriteString(w, "x")
	zw.Close()
	if _, err := OpenXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
		t.Error("a zip with no worksheet must be refused")
	}
}

func TestXLSXZipBombIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("xl/worksheets/sheet1.xml")
	chunk := bytes.Repeat([]byte("<c/>"), 1<<20)
	for i := 0; i < 60; i++ {
		w.Write(chunk)
	}
	zw.Close()
	_, err := OpenXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("want ErrTooBig, got %v", err)
	}
}

func TestXLSXEntitiesAreNotExpanded(t *testing.T) {
	sheet := `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;">]><worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>&b;</t></is></c></row></sheetData></worksheet>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("xl/worksheets/sheet1.xml")
	io.WriteString(w, sheet)
	zw.Close()
	tb, err := OpenXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err == nil && len(strings.Join(tb.Header(), "")) > 100 {
		t.Fatal("an entity was expanded")
	}
}

func TestTooManyColumnsAreRefused(t *testing.T) {
	h := strings.Repeat("a,", MaxCols+1) + "b\n"
	if _, err := OpenCSV(strings.NewReader(h)); !errors.Is(err, ErrTooBig) {
		t.Errorf("wide header: %v", err)
	}
}

func TestPolyline5MatchesTheReferenceExample(t *testing.T) {
	// The example from Google's polyline algorithm documentation.
	got := polyline5([][2]float64{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}})
	if got != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Errorf("polyline = %q", got)
	}
}

func FuzzOpenAndParse(f *testing.F) {
	f.Add([]byte(tezDrives))
	f.Add([]byte(tezCharges))
	f.Add(buildXLSX([][]any{{"Timestamp", "Latitude", "Longitude", "SoC"}, {1.7e9, 34.0, -118.0, 80.0}}))
	f.Add([]byte("PK\x03\x04garbage"))
	f.Fuzz(func(t *testing.T, b []byte) {
		o := Options{Now: now, Location: time.UTC, DistanceUnit: "mi", EnergyUnit: "kwh", PackKWh: 100}
		for _, fm := range []Format{TezLabDrives, TezLabCharges, ABRPPoints} {
			if tb, err := OpenCSV(bytes.NewReader(b)); err == nil {
				_, _ = Parse(tb, fm, o)
			}
			if tb, err := OpenXLSX(bytes.NewReader(b), int64(len(b))); err == nil {
				_, _ = Parse(tb, fm, o)
			}
		}
	})
}
