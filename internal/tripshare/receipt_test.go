package tripshare

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// receiptWps is a trip 120 km east: an origin with a street address, an added stop, a public charger in the middle, an added overnight is not
// single-day so it is a via here, and a destination with its own street address. Cumulative distances are exact on purpose, to see the rounding.
func receiptWps(line [][2]float64) []rivian.PlannedWaypoint {
	at := func(f float64) (float64, float64) {
		i := int(f * float64(len(line)-1))
		return line[i][0], line[i][1]
	}
	vl, vo := at(0.3)
	cl, co := at(0.6)
	last := line[len(line)-1]
	t := func(min int) string { return time.Date(2026, 11, 21, 15, min, 0, 0, time.UTC).Format(time.RFC3339) }
	return []rivian.PlannedWaypoint{
		{WaypointType: "origin", Source: "origin", Latitude: line[0][0], Longitude: line[0][1], DepartureSoC: 90, DepartureTimeUTC: t(0), Name: "123 Home Street"},
		{WaypointType: "waypoint", Source: "via", Latitude: vl, Longitude: vo, ArrivalSoC: 70, DepartureSoC: 70, ArrivalTimeUTC: t(31), DepartureTimeUTC: t(36), DistanceFromOriginMeters: 36111, Name: "Grandma's Motel"},
		{WaypointType: "charger", Source: "planner", Latitude: cl, Longitude: co, ArrivalSoC: 40, DepartureSoC: 80, ChargeDurationSec: 1080, MaxPowerKW: 150, ArrivalTimeUTC: t(66), DepartureTimeUTC: t(84), DistanceFromOriginMeters: 71234, Name: "Barstow, CA · Rivian Adventure Network"},
		{WaypointType: "destination", Source: "destination", Latitude: last[0], Longitude: last[1], ArrivalSoC: 30, ArrivalTimeUTC: t(127), DistanceFromOriginMeters: 118400, Name: "9 Far Road"},
	}
}

func receiptFor(t *testing.T, sketch, metric bool, home *Circle) (*Receipt, string) {
	t.Helper()
	line := eastLine(36.9, -104.5, 1000, 121)
	namer := func(_ context.Context, lat, lon float64) string { return fmt.Sprintf("Area %.2f,%.2f", lat, lon) }
	rec, err := BuildReceipt(context.Background(), ReceiptInput{
		Input:  Input{Title: "Trip", Plan: planJSON(t, line, receiptWps(line), nil), DepartAt: "2026-11-21T15:00:00Z", PlannedAt: time.Unix(1, 0), Home: home, CutM: 4000},
		Sketch: sketch, Metric: metric, Name: namer,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	return rec, string(b)
}

// R1, R2: places are areas of the rounded point; the stored labels (street addresses, the added stop's business name) never reach the receipt.
func TestReceiptHasNoStoredLabels(t *testing.T) {
	rec, js := receiptFor(t, true, false, nil)
	for _, bad := range []string{"123 Home Street", "9 Far Road", "Grandma", "Motel", "e1", "e2"} {
		if strings.Contains(js, bad) {
			t.Errorf("receipt carries %q: %s", bad, js)
		}
	}
	if !strings.HasPrefix(rec.Start, "Area 36.90,") || !strings.HasPrefix(rec.End, "Area 36.90,") {
		t.Errorf("ends %q %q are not areas of the rounded points", rec.Start, rec.End)
	}
	var stop, charge *ReceiptRow
	for i := range rec.Legs {
		switch rec.Legs[i].Kind {
		case "stop":
			stop = &rec.Legs[i]
		case "charge":
			charge = &rec.Legs[i]
		}
	}
	if stop == nil || !strings.HasPrefix(stop.Name, "Stop · Area ") {
		t.Errorf("added stop row %+v: want \"Stop · <area>\"", stop)
	}
	if stop != nil && stop.DurS != 300 {
		t.Errorf("added stop length %d s, want 300 (5 min, rounded)", stop.DurS)
	}
	if charge == nil || charge.Name != "Barstow, CA · Rivian Adventure Network" {
		t.Errorf("public charger row %+v", charge)
	}
}

// R3: legs touching the start, the destination or an added stop, the totals and the arrival time are rounded to 5 mi and 5 min; legs between two
// public chargers stay exact.
func TestReceiptRounding(t *testing.T) {
	rec, _ := receiptFor(t, false, false, nil)
	var drives []ReceiptRow
	for _, r := range rec.Legs {
		if r.Kind == "drive" {
			drives = append(drives, r)
		}
	}
	if len(drives) != 3 {
		t.Fatalf("drive legs %d, want 3", len(drives))
	}
	for _, d := range drives {
		if !d.Rounded || roundDist(float64(d.DistM), false) != d.DistM || d.DurS%300 != 0 {
			t.Errorf("leg %+v is not rounded to 5 mi / 5 min", d)
		}
	}
	// 36111 m = 22.4 mi -> 20 mi; 118400 m = 73.6 mi -> 75 mi.
	if drives[0].DistM != 32187 {
		t.Errorf("first leg %d m, want 32187 (20 mi)", drives[0].DistM)
	}
	if rec.Totals.DistM != 120701 {
		t.Errorf("total %d m, want 120701 (75 mi)", rec.Totals.DistM)
	}
	if rec.Totals.DriveS%300 != 0 {
		t.Errorf("total drive %d s is not a multiple of 5 min", rec.Totals.DriveS)
	}
	at, err := time.Parse(time.RFC3339, rec.Totals.ArriveAt)
	if err != nil || at.Unix()%300 != 0 {
		t.Errorf("arrival %q is not on a 5 minute mark", rec.Totals.ArriveAt)
	}
	km, _ := receiptFor(t, false, true, nil)
	if km.Totals.DistM != 120000 {
		t.Errorf("metric total %d m, want 120000 (120 km)", km.Totals.DistM)
	}
}

// Two public chargers in a row: the leg between them stays exact.
func TestReceiptChargerToChargerLegStaysExact(t *testing.T) {
	line := eastLine(36.9, -104.5, 1000, 121)
	wps := receiptWps(line)
	extra := wps[2]
	extra.Latitude, extra.Longitude = line[80][0], line[80][1]
	extra.DistanceFromOriginMeters, extra.ArrivalTimeUTC, extra.DepartureTimeUTC = 95432, "2026-11-21T16:50:07Z", "2026-11-21T17:05:07Z"
	wps = append(wps[:3], append([]rivian.PlannedWaypoint{extra}, wps[3:]...)...)
	rec, err := BuildReceipt(context.Background(), ReceiptInput{Input: Input{Plan: planJSON(t, line, wps, nil), PlannedAt: time.Unix(1, 0), CutM: 4000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.Legs {
		if r.Kind == "drive" && !r.Rounded && r.DistM != 24198 {
			t.Errorf("exact leg %+v, want 24198 m", r)
		}
	}
}

// R6: no sketch unless asked for; when asked, only the server-cut segments, none of which comes near the start, the destination or the added stop.
func TestReceiptSketch(t *testing.T) {
	if rec, _ := receiptFor(t, false, false, nil); rec.Sketch != nil {
		t.Errorf("sketch sent without being asked for")
	}
	rec, js := receiptFor(t, true, false, nil)
	if len(rec.Sketch) == 0 {
		t.Fatalf("no sketch: %s", js)
	}
	snap := &Snapshot{}
	snap.Route.Segments = rec.Sketch
	line := eastLine(36.9, -104.5, 1000, 121)
	for _, p := range []struct {
		name     string
		lat, lon float64
	}{{"start", line[0][0], line[0][1]}, {"end", line[120][0], line[120][1]}, {"stop", line[36][0], line[36][1]}} {
		if d := minDistTo(snap, p.lat, p.lon); d < 3900 {
			t.Errorf("sketch comes within %.0f m of the %s", d, p.name)
		}
	}
}

// A home inside the trip widens the cut and the receipt still names only areas.
func TestReceiptHomeStartIsAnArea(t *testing.T) {
	line := eastLine(36.9, -104.5, 1000, 121)
	_, js := receiptFor(t, true, false, &Circle{Lat: line[0][0], Lon: line[0][1], RadiusM: 250})
	if strings.Contains(js, "Home") || strings.Contains(js, "36.9000") {
		t.Errorf("home start leaks: %s", js)
	}
}

func TestReceiptRefusesWhatBuildRefuses(t *testing.T) {
	line := eastLine(36.9, -104.5, 1000, 4)
	_, err := BuildReceipt(context.Background(), ReceiptInput{Input: Input{Plan: planJSON(t, line, basicWps(line), nil), PlannedAt: time.Unix(1, 0), CutM: 4000}})
	if err != ErrTooShort {
		t.Errorf("short trip: %v, want ErrTooShort", err)
	}
	_, err = BuildReceipt(context.Background(), ReceiptInput{Input: Input{Plan: json.RawMessage(`{"kind":"multiday"}`), PlannedAt: time.Unix(1, 0)}})
	if err != ErrUnsupported {
		t.Errorf("multi-day: %v, want ErrUnsupported", err)
	}
}

// A trip that starts at home: the planner's charge inside home and the drive to it are no trip. The first row starts at the start area, "start
// with" is the battery leaving the last in-home point, the stops count only what is shown, and the totals keep all driving and charging time.
func TestReceiptFoldsTheHomeStart(t *testing.T) {
	line := eastLine(36.9, -104.5, 1000, 121)
	t0 := func(min int) string { return time.Date(2026, 11, 21, 15, min, 0, 0, time.UTC).Format(time.RFC3339) }
	cl, co := line[72][0], line[72][1]
	last := line[len(line)-1]
	wps := []rivian.PlannedWaypoint{
		{WaypointType: "origin", Source: "origin", Latitude: line[0][0], Longitude: line[0][1], DepartureSoC: 90, DepartureTimeUTC: t0(0)},
		{WaypointType: "charger", Source: "planner", Latitude: line[3][0], Longitude: line[3][1], ArrivalSoC: 85, DepartureSoC: 95, ChargeDurationSec: 600, MaxPowerKW: 11, ArrivalTimeUTC: t0(5), DepartureTimeUTC: t0(15), DistanceFromOriginMeters: 3000, Name: "Home Wallbox"},
		{WaypointType: "charger", Source: "planner", Latitude: cl, Longitude: co, ArrivalSoC: 40, DepartureSoC: 80, ChargeDurationSec: 1080, MaxPowerKW: 150, ArrivalTimeUTC: t0(66), DepartureTimeUTC: t0(84), DistanceFromOriginMeters: 71234, Name: "Barstow, CA · Rivian Adventure Network"},
		{WaypointType: "destination", Source: "destination", Latitude: last[0], Longitude: last[1], ArrivalSoC: 30, ArrivalTimeUTC: t0(127), DistanceFromOriginMeters: 118400},
	}
	namer := func(_ context.Context, lat, lon float64) string { return fmt.Sprintf("Area %.2f,%.2f", lat, lon) }
	rec, err := BuildReceipt(context.Background(), ReceiptInput{
		Input: Input{Title: "Trip", Plan: planJSON(t, line, wps, nil), DepartAt: "2026-11-21T15:00:00Z", PlannedAt: time.Unix(1, 0),
			Home: &Circle{Lat: line[0][0], Lon: line[0][1], RadiusM: 9000}, CutM: 4000},
		Name: namer,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	js := string(b)
	if strings.Contains(js, "Home Wallbox") || strings.Contains(js, "Charging ·") {
		t.Errorf("a charge inside home is shown: %s", js)
	}
	if rec.StartSoC != 95 {
		t.Errorf("start with %v%%, want 95 (leaving the last in-home point)", rec.StartSoC)
	}
	if len(rec.Legs) != 3 || rec.Legs[0].Kind != "drive" || rec.Legs[0].From != rec.Start || rec.Legs[0].FromSoC != 95 {
		t.Fatalf("rows %+v: want drive from the start area, then the public charger, then the last drive", rec.Legs)
	}
	if rec.Totals.Stops != 1 {
		t.Errorf("stops %d, want 1 (only the shown charge)", rec.Totals.Stops)
	}
	if rec.Totals.ChargeS != 1080 {
		t.Errorf("charge time %d, want only the shown charge (1080): the folded home charge is not in the Charging line", rec.Totals.ChargeS)
	}
	if rec.Totals.TripS != 127*60-120 || rec.Totals.TripS%300 != 0 {
		t.Errorf("trip time %d, want all 127 min rounded to 5 min (7500)", rec.Totals.TripS)
	}
	dep, _ := time.Parse(time.RFC3339, rec.DepartAt)
	arr, _ := time.Parse(time.RFC3339, rec.Totals.ArriveAt)
	if !dep.Add(time.Duration(rec.Totals.TripS) * time.Second).Equal(arr) {
		t.Errorf("Leave %s + total %d s != Arrive %s", rec.DepartAt, rec.Totals.TripS, rec.Totals.ArriveAt)
	}
	if rec.DepartAt != "2026-11-21T15:00:00Z" {
		t.Errorf("leave moved to %q", rec.DepartAt)
	}
}

// The same at the other end: arriving home, the drive between home-area points and a charge inside home are left out.
func TestReceiptFoldsTheHomeEnd(t *testing.T) {
	line := eastLine(36.9, -104.5, 1000, 121)
	t0 := func(min int) string { return time.Date(2026, 11, 21, 15, min, 0, 0, time.UTC).Format(time.RFC3339) }
	n := len(line) - 1
	last := line[n]
	wps := []rivian.PlannedWaypoint{
		{WaypointType: "origin", Source: "origin", Latitude: line[0][0], Longitude: line[0][1], DepartureSoC: 90, DepartureTimeUTC: t0(0)},
		{WaypointType: "charger", Source: "planner", Latitude: line[60][0], Longitude: line[60][1], ArrivalSoC: 40, DepartureSoC: 80, ChargeDurationSec: 1080, MaxPowerKW: 150, ArrivalTimeUTC: t0(66), DepartureTimeUTC: t0(84), DistanceFromOriginMeters: 60000, Name: "Barstow, CA · Rivian Adventure Network"},
		{WaypointType: "charger", Source: "planner", Latitude: line[n-3][0], Longitude: line[n-3][1], ArrivalSoC: 20, DepartureSoC: 30, ChargeDurationSec: 600, ArrivalTimeUTC: t0(120), DepartureTimeUTC: t0(130), DistanceFromOriginMeters: 117000, Name: "Home Wallbox"},
		{WaypointType: "destination", Source: "destination", Latitude: last[0], Longitude: last[1], ArrivalSoC: 28, ArrivalTimeUTC: t0(135), DistanceFromOriginMeters: 120000},
	}
	rec, err := BuildReceipt(context.Background(), ReceiptInput{
		Input: Input{Title: "Trip", Plan: planJSON(t, line, wps, nil), DepartAt: "2026-11-21T15:00:00Z", PlannedAt: time.Unix(1, 0),
			Home: &Circle{Lat: last[0], Lon: last[1], RadiusM: 9000}, CutM: 4000},
		Name: func(_ context.Context, lat, lon float64) string { return fmt.Sprintf("Area %.2f,%.2f", lat, lon) },
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "Home Wallbox") || strings.Contains(string(b), "Charging ·") {
		t.Errorf("a charge inside home is shown: %s", b)
	}
	if len(rec.Legs) != 3 || rec.Legs[2].Kind != "drive" || rec.Legs[2].To != rec.End {
		t.Fatalf("rows %+v: want drive, the public charge, a drive that ends at the end area", rec.Legs)
	}
	if rec.Totals.Stops != 1 || rec.Totals.ChargeS != 1080 || rec.Totals.TripS%300 != 0 {
		t.Errorf("stops %d charge %d", rec.Totals.Stops, rec.Totals.ChargeS)
	}
}
