package histimport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The synthetic fixtures in testdata/ stand for the layouts a user's export may take (the real column lists are not published, see
// AliasProvenance): units in the header or not, ISO or US dates, a zone or none, "." or "," decimals, SoC in percent or as a fraction, and
// Wh or kWh. They carry made-up numbers and the public sample point (34.161, -118.168). qa uploads the same files on preview.

func fixture(t *testing.T, name string) Table {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	tb, err := OpenCSV(f)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestCSVFixtureVariants(t *testing.T) {
	for _, c := range []struct {
		file    string
		format  Format
		opts    Options
		drives  int
		charges int
		check   func(t *testing.T, r *Result)
	}{
		{"tezlab-drives-miles-utc.csv", TezLabDrives, Options{Now: now}, 3, 0, func(t *testing.T, r *Result) {
			if d := r.Drives[0]; d.DistanceMi != 20.5 || d.StartSoCPct != 80 || d.EndOdometerMi != 10020.5 {
				t.Errorf("drive 0 = %+v", d)
			}
		}},
		{"tezlab-drives-km-local-semicolon.csv", TezLabDrives, Options{Now: now, Location: la}, 2, 0, func(t *testing.T, r *Result) {
			d := r.Drives[0]
			if got := d.DistanceMi; got < 20.4 || got > 20.5 { // 32.9 km
				t.Errorf("distance %v mi, want 32.9 km as miles", got)
			}
			if want := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC); !d.StartedAt.Equal(want) {
				t.Errorf("start %v, want %v (08:00 Los Angeles)", d.StartedAt, want)
			}
			if d.EnergyUsedKWh != 13.4 || d.EnergyEstimated {
				t.Errorf("energy %v estimated=%v, want the file's 13.4", d.EnergyUsedKWh, d.EnergyEstimated)
			}
		}},
		{"tezlab-charges-kwh-cost.csv", TezLabCharges, Options{Now: now}, 0, 2, func(t *testing.T, r *Result) {
			if c := r.Charges[0]; c.EnergyAddedKWh != 60.2 || !c.HasCost || c.Cost != 18.4 || c.Currency != "USD" || c.SessionType != "dc" {
				t.Errorf("charge 0 = %+v", c)
			}
			if c := r.Charges[1]; c.HasCost || c.SessionType != "ac" {
				t.Errorf("charge 1 = %+v: a blank cost must stay unpriced, not zero", c)
			}
		}},
		{"tezlab-charges-wh-fraction-us-dates.csv", TezLabCharges, Options{Now: now, Location: la}, 0, 2, func(t *testing.T, r *Result) {
			if c := r.Charges[0]; c.EnergyAddedKWh != 60.2 || c.StartSoCPct != 15 || c.EndSoCPct != 80 || c.MaxPowerKW != 190 {
				t.Errorf("charge 0 = %+v", c)
			}
			if r.Report.SoCScale != "fraction" {
				t.Errorf("soc scale %q", r.Report.SoCScale)
			}
		}},
	} {
		t.Run(c.file, func(t *testing.T) {
			r, err := Parse(fixture(t, c.file), c.format, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Drives) != c.drives || len(r.Charges) != c.charges || r.Report.BadRows != 0 {
				t.Fatalf("drives=%d charges=%d report=%+v, want %d and %d", len(r.Drives), len(r.Charges), r.Report, c.drives, c.charges)
			}
			c.check(t, r)
		})
	}
}

// A file with no zone, or no unit, is asked about, not guessed: the preview shows the question.
func TestCSVFixturesAskWhatTheyDoNotSay(t *testing.T) {
	_, err := Parse(fixture(t, "tezlab-charges-wh-fraction-us-dates.csv"), TezLabCharges, Options{Now: now})
	var nc *NeedChoiceError
	if !errors.As(err, &nc) || len(nc.Choices) != 1 || nc.Choices[0] != "timezone" {
		t.Errorf("err = %v, want a need for the timezone", err)
	}
}

// The unknown layout is refused by name, and RequiredColumns tells the user what to look for.
func TestUnknownColumnsFixtureIsRefusedAndTheExpectedColumnsAreListed(t *testing.T) {
	for _, f := range []Format{TezLabDrives, TezLabCharges, ABRPPoints} {
		_, err := Parse(fixture(t, "unknown-columns.csv"), f, Options{Now: now})
		var me *MissingColumnsError
		if !errors.As(err, &me) || len(me.Missing) == 0 {
			t.Errorf("%v: err = %v, want MissingColumnsError", f.Kind, err)
		}
	}
	got := RequiredColumns([]Format{TezLabDrives, TezLabCharges})
	if len(got) != 2 || got[0].Kind != "drives" || got[1].Kind != "charges" {
		t.Fatalf("layouts = %+v", got)
	}
	for _, l := range got {
		for _, c := range l.Columns {
			if c.Field == "" || len(c.Examples) == 0 || len(c.Examples) > 3 {
				t.Errorf("%s column %+v", l.Kind, c)
			}
		}
	}
}

// Every alias of every format normalises to a non-empty name, and no two fields of one format share one (a shared name would read one
// column as two fields, in whichever order the header lists them).
func TestAliasesAreDistinctAcrossFields(t *testing.T) {
	for _, f := range []Format{TezLabDrives, TezLabCharges, ABRPPoints} {
		seen := map[string]string{}
		for _, fld := range f.Fields {
			for _, a := range fld.Aliases {
				b := baseName(a)
				if b == "" {
					t.Errorf("%s alias %q normalises to nothing", fld.Name, a)
				}
				if other, ok := seen[b]; ok && other != fld.Name {
					t.Errorf("alias %q is on both %s and %s", a, other, fld.Name)
				}
				seen[b] = fld.Name
			}
		}
	}
}

// ABRP exports points in a workbook; the same drive in kilometres with percent SoC and in miles with a fractional SoC reads the same.
func TestABRPFixtureVariantsReadTheSame(t *testing.T) {
	base := float64(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Unix())
	build := func(head []any, speed, soc func(i int) float64, dist float64) [][]any {
		rows := [][]any{head}
		for i := 0; i <= 12; i++ {
			rows = append(rows, []any{base + float64(i*60), 34.161 + float64(i)*0.0145, -118.168, soc(i), speed(i) * dist})
		}
		return rows
	}
	pct := build([]any{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (km/h)"}, func(int) float64 { return 60 }, func(i int) float64 { return float64(80 - i/2) }, 1.609344)
	frac := build([]any{"Time", "Lat", "Lon", "State of charge", "Speed (mph)"}, func(int) float64 { return 60 }, func(i int) float64 { return float64(80-i/2) / 100 }, 1)
	a, err := Parse(xlsxT(t, buildXLSX(pct)), ABRPPoints, Options{Now: now, DistanceUnit: "km"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(xlsxT(t, buildXLSX(frac)), ABRPPoints, Options{Now: now, DistanceUnit: "mi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Drives) != 1 || len(b.Drives) != 1 {
		t.Fatalf("drives %d and %d", len(a.Drives), len(b.Drives))
	}
	da, db := a.Drives[0], b.Drives[0]
	if da.StartSoCPct != db.StartSoCPct || da.EndSoCPct != db.EndSoCPct || !da.StartedAt.Equal(db.StartedAt) {
		t.Errorf("SoC or start differ: %+v vs %+v", da, db)
	}
	if d := da.DistanceMi - db.DistanceMi; d > 0.05 || d < -0.05 {
		t.Errorf("distance %v vs %v mi", da.DistanceMi, db.DistanceMi)
	}
	if b.Report.SoCScale != "fraction" || a.Report.SoCScale != "percent" {
		t.Errorf("scales %q %q", a.Report.SoCScale, b.Report.SoCScale)
	}
}

// A decimal comma is refused rather than guessed ("1,234" could be a thousands separator); the user sees the bad-row refusal, not a wrong import.
func TestDecimalCommaIsRefusedNotGuessed(t *testing.T) {
	src := "Started At;Ended At;Distance (km);Starting Battery;Ending Battery\n2026-03-01 08:00:00;2026-03-01 08:30:00;32,9;80;70\n"
	tb, err := OpenCSV(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse(tb, TezLabDrives, Options{Now: now, Location: la})
	var bad *TooManyBadRowsError
	if !errors.As(err, &bad) || bad.Skipped[ReasonBadNumber] != 1 {
		t.Errorf("err = %v, want every row refused as bad_number", err)
	}
}

const abrpFixture = "abrp-points-km-percent.xlsx"

func abrpFixtureRows() [][]any {
	base := float64(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Unix())
	rows := [][]any{{"Timestamp", "Latitude", "Longitude", "SoC (%)", "Speed (km/h)"}}
	for i := 0; i <= 12; i++ {
		rows = append(rows, []any{base + float64(i*60), 34.161 + float64(i)*0.0145, -118.168, float64(80 - i/2), 96.5})
	}
	return rows
}

// REGEN_FIXTURES=1 go test -run TestABRPFixtureFile rewrites the workbook that qa uploads; otherwise the committed file is parsed.
func TestABRPFixtureFile(t *testing.T) {
	path := filepath.Join("testdata", abrpFixture)
	if os.Getenv("REGEN_FIXTURES") == "1" {
		if err := os.WriteFile(path, buildXLSX(abrpFixtureRows()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(xlsxT(t, b), ABRPPoints, Options{Now: now, DistanceUnit: "km"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drives) != 1 || r.Drives[0].StartSoCPct != 80 {
		t.Errorf("drives = %+v", r.Drives)
	}
}
