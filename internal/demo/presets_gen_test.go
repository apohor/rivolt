//go:build demotripsgen

// Generates the demo planner's preset trips (internal/demo/trips):
// routes from a Valhalla routing service, charging stops from the
// chargers archive, elevation from the Terrarium tile service, and the
// sample truck's efficiency. Run by hand, never in CI, so the presets
// stay fixed and the demo serves them offline:
//
//	DEMOTRIPS_VALHALLA=http://localhost:18002 \
//	DEMOTRIPS_TILES=http://localhost:18003 \
//	DEMOTRIPS_ELEVATION='https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png' \
//	go test -tags demotripsgen -run TestGeneratePresets ./internal/demo
//
// DEMOTRIPS_ONLY=id,id plans only those genTrips and keeps every other preset exactly as
// it is on disk (its index entry and plan file), so adding a route doesn't re-plan the
// existing ones against today's routes and chargers. New presets are appended to the
// index in genTrips order; a listed id that already exists is replaced in place.
//
// (kubectl port-forward svc/valhalla -n valhalla and svc/tiles -n
// tiles; the in-cluster elevation tiles cover Texas only, so elevation
// comes from the public Terrarium set.) Every point is a public landmark or a
// public charging station; nothing comes from any user or vehicle.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/elevation"
	"github.com/apohor/rivolt/internal/rivian"
)

// The sample truck (rivian/sample_corpus.go) and how a planned route
// is driven.
const (
	genPackKWh     = 131.0
	genMiPerKWh    = 2.3    // on the flat, at highway speed
	genMassKg      = 3100.0 // R1S with two people and luggage
	genClimbEff    = 0.85   // drivetrain efficiency lifting the truck
	genRegenEff    = 0.60   // recovered going down
	genPeakKW      = 220.0  // the truck's DC fast peak
	genTaper       = 0.62   // average ÷ peak over a stop (charge() in the corpus)
	genStepM       = 1000.0 // energy is integrated over 1 km steps
	genCorridorKm  = 1.0    // a station this close to the route is on it
	genMinStopKW   = 50.0
	genMinArrival  = 12.0 // never plan to reach a charger lower
	genBuffer      = 5.0  // charge to this much over what the rest needs
	genMaxDepart   = 90.0
	genFirstStopKm = 40.0    // no stop this close to the origin
	genStopWindowM = 60000.0 // a stop is picked among those this close to the furthest reachable
)

var (
	genUnion    = rivian.SamplePlace{Lat: 34.0562, Lon: -118.2365, Label: "Union Station, Los Angeles"}
	genPasadena = rivian.SamplePlace{Lat: HomeLat, Lon: HomeLon, Label: HomeLabel}
)

type genTrip struct {
	id, name  string
	from, to  rivian.SamplePlace
	startSoC  float64
	targetSoC float64
}

var genTrips = []genTrip{
	{"san-diego", "LA to San Diego", genUnion, rivian.SamplePlace{Lat: 32.7341, Lon: -117.1446, Label: "Balboa Park, San Diego"}, 80, 10},
	{"mammoth", "LA to Mammoth Lakes", genUnion, rivian.SamplePlace{Lat: 37.6511, Lon: -119.0365, Label: "Mammoth Mountain Main Lodge"}, 90, 10},
	{"kettleman-city", "Pasadena to Kettleman City", genPasadena, rivian.SamplePlace{Lat: 35.9894, Lon: -119.9603, Label: "Kettleman City, CA"}, 90, 10},
	{"san-francisco", "Pasadena to San Francisco", genPasadena, rivian.SamplePlace{Lat: 37.7955, Lon: -122.3937, Label: "Ferry Building, San Francisco"}, 90, 10},
	// Sprint 12's route pages: public landmarks only.
	{"seattle-portland", "Seattle to Portland", rivian.SamplePlace{Lat: 47.6097, Lon: -122.3422, Label: "Pike Place Market, Seattle"}, rivian.SamplePlace{Lat: 45.5189, Lon: -122.6790, Label: "Pioneer Courthouse Square, Portland"}, 90, 10},
	{"denver-moab", "Denver to Moab", rivian.SamplePlace{Lat: 39.7527, Lon: -105.0002, Label: "Union Station, Denver"}, rivian.SamplePlace{Lat: 38.5733, Lon: -109.5498, Label: "Main Street, Moab"}, 90, 10},
	{"sf-tahoe", "San Francisco to Lake Tahoe", rivian.SamplePlace{Lat: 37.7955, Lon: -122.3937, Label: "Ferry Building, San Francisco"}, rivian.SamplePlace{Lat: 38.9586, Lon: -119.9400, Label: "Heavenly Village, South Lake Tahoe"}, 90, 10},
	{"la-phoenix", "LA to Phoenix", genUnion, rivian.SamplePlace{Lat: 33.4484, Lon: -112.0740, Label: "Downtown Phoenix"}, 90, 10},
	{"nyc-boston", "New York to Boston", rivian.SamplePlace{Lat: 40.7580, Lon: -73.9855, Label: "Times Square, New York"}, rivian.SamplePlace{Lat: 42.3550, Lon: -71.0656, Label: "Boston Common, Boston"}, 90, 10},
}

func TestGeneratePresets(t *testing.T) {
	vbase, tbase, etmpl := os.Getenv("DEMOTRIPS_VALHALLA"), os.Getenv("DEMOTRIPS_TILES"), os.Getenv("DEMOTRIPS_ELEVATION")
	if vbase == "" || tbase == "" || etmpl == "" {
		t.Skip("DEMOTRIPS_VALHALLA, DEMOTRIPS_TILES and DEMOTRIPS_ELEVATION must be set")
	}
	ctx := context.Background()
	arch := chargers.New(strings.TrimRight(tbase, "/") + "/chargers.pmtiles")
	if err := arch.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	elev := elevation.New(elevation.Config{TileURL: etmpl, MaxTiles: 4096})
	depart, err := presetDepart()
	if err != nil {
		t.Fatal(err)
	}
	if only := os.Getenv("DEMOTRIPS_ONLY"); only != "" {
		generateOnly(t, ctx, vbase, arch, elev, depart, strings.Split(only, ","))
		return
	}

	files, err := vegasPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range genTrips {
		plan, err := genPlan(ctx, vbase, arch, elev, g, depart)
		if err != nil {
			t.Fatalf("%s: %v", g.id, err)
		}
		f, err := newPreset(g.id, g.name, g.from, g.to, g.startSoC, g.targetSoC, genPackKWh, plan)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	for i := range files {
		if files[i].RoutePolyline, err = genShape(vbase, files[i].Plan); err != nil {
			t.Fatalf("%s: shape: %v", files[i].ID, err)
		}
	}
	// The two-day trip drives the two Las Vegas legs' routes: their shapes
	// ride on its days. It follows them in the list.
	two, err := twoDayPreset([]string{files[0].RoutePolyline, files[1].RoutePolyline})
	if err != nil {
		t.Fatal(err)
	}
	files = append(files[:2], append([]presetFile{two}, files[2:]...)...)
	if err := writePresets(PresetsDir, files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		t.Logf("%-38s %-28s %+v", f.ID, f.Name, f.Summary)
	}
}

// genPlan plans g the way the planner would: the Valhalla route, the
// energy along it, and the fewest charging stops (the furthest
// reachable fast station each time, preferring power near the limit).
func genPlan(ctx context.Context, vbase string, arch *chargers.Archive, elev *elevation.Resolver, g genTrip, depart time.Time) (rivian.TripPlan, error) {
	shape, lengthM, durSec, err := genRoute(vbase, g.from, g.to)
	if err != nil {
		return rivian.TripPlan{}, err
	}
	// Resample to 1 km steps with cumulative distance and energy.
	pts := resample(shape, genStepM)
	alt := make([]float64, len(pts))
	for i, p := range pts {
		if alt[i], err = lookupWait(elev, p[0], p[1]); err != nil {
			return rivian.TripPlan{}, err
		}
	}
	dist := make([]float64, len(pts)) // metres from the origin
	kwh := make([]float64, len(pts))  // energy from the origin
	for i := 1; i < len(pts); i++ {
		d := haversineM(pts[i-1], pts[i])
		dist[i] = dist[i-1] + d
		e := d / 1609.344 / genMiPerKWh
		if dh := alt[i] - alt[i-1]; dh > 0 {
			e += genMassKg * 9.81 * dh / genClimbEff / 3.6e6
		} else {
			e -= genRegenEff * genMassKg * 9.81 * -dh / 3.6e6
		}
		kwh[i] = kwh[i-1] + math.Max(e, 0)
	}
	scale := lengthM / dist[len(dist)-1] // the resampled line runs a little short
	for i := range dist {
		dist[i] *= scale
	}
	at := func(m float64) int { // the step at m along
		i := sort.SearchFloat64s(dist, m)
		if i >= len(dist) {
			i = len(dist) - 1
		}
		return i
	}

	pois, err := arch.QueryCorridor(shape, chargers.FilterDCFC, chargers.QueryCorridorOptions{CorridorKm: genCorridorKm, MinPowerKW: genMinStopKW})
	if err != nil {
		return rivian.TripPlan{}, err
	}
	type station struct {
		poi   chargers.POI
		along float64
		tier  int     // genNetworkTier
		kw    float64 // the station's power (genNetworkKW)
	}
	var stations []station
	for _, p := range pois {
		if !p.SocketCCS1 && !p.SocketTesla {
			continue
		}
		best, along := math.Inf(1), 0.0
		for i, q := range pts {
			if d := haversineM(q, [2]float64{p.Lat, p.Lon}); d < best {
				best, along = d, dist[i]
			}
		}
		slug := dcfcrates.MatchNetwork(p.Name + " " + p.EVNetwork).Slug
		stations = append(stations, station{p, along, genNetworkTier[slug], genNetworkKW(slug, p.MaxPowerKW)})
	}
	sort.Slice(stations, func(i, j int) bool { return stations[i].along < stations[j].along })

	secPerM := durSec / lengthM
	reach := func(soc float64) float64 { return math.Round(soc/100*genPackKWh*genMiPerKWh*1609.344*10) / 10 }
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	soc, pos, clock := g.startSoC, 0.0, depart
	var chargeSec float64
	wps := []rivian.PlannedWaypoint{{WaypointType: "ORIGIN", Name: g.from.Label, Latitude: g.from.Lat, Longitude: g.from.Lon,
		DepartureSoC: soc, DepartureReachableMeters: reach(soc), DepartureTimeUTC: stamp(clock)}}
	socAt := func(m float64) float64 { return soc - (kwh[at(m)]-kwh[at(pos)])/genPackKWh*100 }
	for socAt(lengthM) < g.targetSoC {
		var reachable []station
		for _, s := range stations {
			if s.along > pos+genFirstStopKm*1000 && socAt(s.along) >= genMinArrival {
				reachable = append(reachable, s)
			}
		}
		if len(reachable) == 0 {
			return rivian.TripPlan{}, fmt.Errorf("no reachable station after %.0f km", pos/1000)
		}
		// Near the limit of the range, the best network, then the most
		// stalls, then the furthest.
		far := reachable[len(reachable)-1].along
		pick := reachable[len(reachable)-1]
		better := func(a, b station) bool {
			if a.tier != b.tier {
				return a.tier > b.tier
			}
			if a.poi.DCFCCount != b.poi.DCFCCount {
				return a.poi.DCFCCount > b.poi.DCFCCount
			}
			return a.along > b.along
		}
		for _, s := range reachable {
			if s.along >= far-genStopWindowM && better(s, pick) {
				pick = s
			}
		}
		arrive := genRound1(socAt(pick.along))
		clock = clock.Add(time.Duration((pick.along - pos) * secPerM * float64(time.Second)))
		rest := (kwh[len(kwh)-1] - kwh[at(pick.along)]) / genPackKWh * 100
		to := math.Min(math.Ceil(rest+g.targetSoC+genBuffer), genMaxDepart)
		if to <= arrive {
			return rivian.TripPlan{}, fmt.Errorf("stop at %.0f km adds nothing", pick.along/1000)
		}
		kw := math.Min(pick.kw, genPeakKW)
		secs := math.Round((to - arrive) / 100 * genPackKWh / (kw * genTaper) * 3600)
		wp := rivian.PlannedWaypoint{WaypointType: "DC_CHARGE_STATION", Name: genStationName(pick.poi),
			Latitude: pick.poi.Lat, Longitude: pick.poi.Lon, MaxPowerKW: pick.kw, ChargeDurationSec: int(secs),
			ArrivalSoC: arrive, DepartureSoC: to, ArrivalReachableMeters: reach(arrive), DepartureReachableMeters: reach(to),
			ArrivalTimeUTC: stamp(clock), DistanceFromOriginMeters: math.Round(pick.along)}
		clock = clock.Add(time.Duration(secs) * time.Second)
		wp.DepartureTimeUTC = stamp(clock)
		wps = append(wps, wp)
		chargeSec += secs
		soc, pos = to, pick.along
	}
	arrive := genRound1(socAt(lengthM))
	clock = clock.Add(time.Duration((lengthM - pos) * secPerM * float64(time.Second)))
	wps = append(wps, rivian.PlannedWaypoint{WaypointType: "DESTINATION", Name: g.to.Label, Latitude: g.to.Lat, Longitude: g.to.Lon,
		ArrivalSoC: arrive, ArrivalReachableMeters: reach(arrive), ArrivalTimeUTC: stamp(clock), DistanceFromOriginMeters: math.Round(lengthM)})
	return rivian.TripPlan{Status: "OK", ChargeStationsAvailable: true, Routes: []rivian.TripRoute{{
		DestinationReached: true, TotalChargingDurationSec: int(chargeSec), TotalDriveDurationSec: int(math.Round(durSec)),
		TotalTripDurationSec: int(math.Round(durSec + chargeSec)), TotalDriveDistanceMeters: math.Round(lengthM),
		ArrivalSoC: arrive, ArrivalReachableMeters: reach(arrive), EnergyConsumptionKWh: genRound1(kwh[len(kwh)-1]),
		Waypoints: wps}}}, nil
}

// genNetworkTier ranks networks for a planned stop: the big high-power
// networks first.
var genNetworkTier = map[string]int{"ran": 3, "tesla_sc": 3, "ea": 3, "ionna": 3, "evgo": 2}

// genNetworkKW is a station's power: the archive's when it has one,
// else typical for its network.
func genNetworkKW(slug string, archiveKW float64) float64 {
	if archiveKW > 0 {
		return archiveKW
	}
	switch slug {
	case "ionna":
		return 400
	case "ea":
		return 350
	case "tesla_sc":
		return 250
	case "ran":
		return 220
	case "evgo":
		return 150
	}
	return 62.5
}

// genStationName names a station as Rivian's planner does, with its
// network in brackets unless the name already says it.
func genStationName(p chargers.POI) string {
	name := strings.TrimSpace(p.Name)
	if dcfcrates.MatchNetwork(name).Slug != dcfcrates.UnmatchedNetwork.Slug {
		return name
	}
	net := strings.TrimSpace(p.EVNetwork)
	if n := dcfcrates.MatchNetwork(name + " " + net); n.Slug != dcfcrates.UnmatchedNetwork.Slug {
		net = n.DisplayName
	}
	if net == "" {
		return name
	}
	return fmt.Sprintf("%s [%s]", name, net)
}

// genShapeTolM: shape points closer than this to the simplified line
// are dropped (Douglas–Peucker), keeping a preset small.
const genShapeTolM = 25.0

// genShape is the road shape through plan's waypoints (so through its
// charging stops), simplified and encoded as a precision-6 polyline.
func genShape(base string, planJSON json.RawMessage) (string, error) {
	var plan rivian.TripPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return "", err
	}
	var via []rivian.SamplePlace
	for _, w := range plan.Routes[0].Waypoints {
		via = append(via, rivian.SamplePlace{Lat: w.Latitude, Lon: w.Longitude})
	}
	shape, _, _, err := genRouteVia(base, via)
	if err != nil {
		return "", err
	}
	return encodePolyline6(simplify(shape, genShapeTolM)), nil
}

func genRoute(base string, from, to rivian.SamplePlace) ([][2]float64, float64, float64, error) {
	return genRouteVia(base, []rivian.SamplePlace{from, to})
}

// genRouteVia routes through every point in order (one leg per pair)
// and returns the joined shape, length (m) and time (s).
func genRouteVia(base string, via []rivian.SamplePlace) ([][2]float64, float64, float64, error) {
	locs := make([]map[string]float64, len(via))
	for i, p := range via {
		locs[i] = map[string]float64{"lat": p.Lat, "lon": p.Lon}
	}
	body, _ := json.Marshal(map[string]any{
		"locations":       locs,
		"costing":         "auto",
		"directions_type": "none",
	})
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Post(strings.TrimRight(base, "/")+"/route", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Trip struct {
			Summary struct{ Length, Time float64 } // km, s
			Legs    []struct{ Shape string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, 0, err
	}
	if resp.StatusCode != http.StatusOK || len(out.Trip.Legs) != len(via)-1 {
		return nil, 0, 0, fmt.Errorf("valhalla: status %d", resp.StatusCode)
	}
	var shape [][2]float64
	for i, l := range out.Trip.Legs {
		pts := decodePolyline6(l.Shape)
		if i > 0 && len(pts) > 0 {
			pts = pts[1:] // the previous leg's last point
		}
		shape = append(shape, pts...)
	}
	return shape, out.Trip.Summary.Length * 1000, out.Trip.Summary.Time, nil
}

// encodePolyline6 encodes p as a precision-6 polyline.
func encodePolyline6(p [][2]float64) string {
	var b strings.Builder
	enc := func(v int) {
		u := v << 1
		if v < 0 {
			u = ^u
		}
		for u >= 0x20 {
			b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
			u >>= 5
		}
		b.WriteByte(byte(u + 63))
	}
	var lat, lon int
	for _, q := range p {
		la, lo := int(math.Round(q[0]*1e6)), int(math.Round(q[1]*1e6))
		enc(la - lat)
		enc(lo - lon)
		lat, lon = la, lo
	}
	return b.String()
}

// simplify is Douglas–Peucker with tolerance tolM metres, on a local
// equirectangular projection.
func simplify(p [][2]float64, tolM float64) [][2]float64 {
	if len(p) < 3 {
		return p
	}
	keep := make([]bool, len(p))
	keep[0], keep[len(p)-1] = true, true
	kx := 111320 * math.Cos(p[0][0]*math.Pi/180)
	const ky = 110540.0
	var rec func(a, b int)
	rec = func(a, b int) {
		if b-a < 2 {
			return
		}
		ax, ay := p[a][1]*kx, p[a][0]*ky
		bx, by := p[b][1]*kx, p[b][0]*ky
		dx, dy := bx-ax, by-ay
		l2 := dx*dx + dy*dy
		worst, at := 0.0, -1
		for i := a + 1; i < b; i++ {
			x, y := p[i][1]*kx-ax, p[i][0]*ky-ay
			var d float64
			if l2 == 0 {
				d = math.Hypot(x, y)
			} else {
				t := math.Max(0, math.Min(1, (x*dx+y*dy)/l2))
				d = math.Hypot(x-t*dx, y-t*dy)
			}
			if d > worst {
				worst, at = d, i
			}
		}
		if worst > tolM {
			keep[at] = true
			rec(a, at)
			rec(at, b)
		}
	}
	rec(0, len(p)-1)
	var out [][2]float64
	for i, k := range keep {
		if k {
			out = append(out, p[i])
		}
	}
	return out
}

// resample returns points every step metres along p (plus its end).
func resample(path [][2]float64, step float64) [][2]float64 {
	p := append([][2]float64(nil), path...)
	out := [][2]float64{p[0]}
	carry := 0.0
	for i := 1; i < len(p); i++ {
		d := haversineM(p[i-1], p[i])
		for carry+d >= step {
			f := (step - carry) / d
			q := [2]float64{p[i-1][0] + (p[i][0]-p[i-1][0])*f, p[i-1][1] + (p[i][1]-p[i-1][1])*f}
			out = append(out, q)
			d -= step - carry
			p[i-1] = q
			carry = 0
		}
		carry += d
	}
	return append(out, p[len(p)-1])
}

// lookupWait blocks until the tile behind (lat, lon) is fetched.
func lookupWait(r *elevation.Resolver, lat, lon float64) (float64, error) {
	for i := 0; i < 300; i++ {
		if v, ok := r.Lookup(lat, lon); ok {
			return v, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("elevation: no tile for %.4f,%.4f", lat, lon)
}

func genRound1(v float64) float64 { return math.Round(v*10) / 10 }

// writePresets writes index.json and one <id>.plan.json per preset,
// indented, replacing what is there.
func writePresets(dir string, files []presetFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	idx := PresetIndex{Trips: []Preset{}}
	for _, f := range files {
		idx.Trips = append(idx.Trips, f.Preset)
		var buf bytes.Buffer
		if err := json.Indent(&buf, f.Plan, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		if err := os.WriteFile(filepath.Join(dir, f.ID+".plan.json"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json"), append(b, '\n'), 0o644)
}

// generateOnly plans the listed genTrips and keeps every other preset as it is on disk.
func generateOnly(t *testing.T, ctx context.Context, vbase string, arch *chargers.Archive, elev *elevation.Resolver, depart time.Time, ids []string) {
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	b, err := os.ReadFile(filepath.Join(PresetsDir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx PresetIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	fresh := map[string]presetFile{}
	for _, g := range genTrips {
		if !want[g.id] {
			continue
		}
		delete(want, g.id)
		plan, err := genPlan(ctx, vbase, arch, elev, g, depart)
		if err != nil {
			t.Fatalf("%s: %v", g.id, err)
		}
		f, err := newPreset(g.id, g.name, g.from, g.to, g.startSoC, g.targetSoC, genPackKWh, plan)
		if err != nil {
			t.Fatal(err)
		}
		if f.RoutePolyline, err = genShape(vbase, f.Plan); err != nil {
			t.Fatalf("%s: shape: %v", g.id, err)
		}
		fresh[g.id] = f
	}
	if len(want) > 0 {
		t.Fatalf("DEMOTRIPS_ONLY names ids not in genTrips: %v", want)
	}
	var files []presetFile
	for _, p := range idx.Trips {
		if f, ok := fresh[p.ID]; ok {
			files = append(files, f)
			delete(fresh, p.ID)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(PresetsDir, p.ID+".plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		// Compact first: writePresets indents the plan as it did when the file was written,
		// so a kept preset comes out byte for byte the same.
		var plan bytes.Buffer
		if err := json.Compact(&plan, raw); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		files = append(files, presetFile{Preset: p, Plan: plan.Bytes()})
	}
	for _, g := range genTrips {
		if f, ok := fresh[g.id]; ok {
			files = append(files, f)
		}
	}
	if err := writePresets(PresetsDir, files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		t.Logf("%-38s %-28s %+v", f.ID, f.Name, f.Summary)
	}
}
