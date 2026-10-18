package rivian

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Sample truck: the demo's generated vehicle.
//
// A generated, deterministic 45-day corpus for demos and onboarding:
// one R1S whose "home" is a public stadium parking lot (never anyone's
// residence), commuting on weekdays, charging at home on L2 with
// top-ups, two road trips (Las Vegas and San Francisco, 600+ mi each,
// four DC fast charging stops, hotel L2 overnight), mostly asleep when
// parked, and one night of failed sleeps. Nothing is derived from any
// real vehicle.
//
// SampleCorpus implements ReplayCorpus by computing frames on demand
// from a small plan of segments, so nothing large is stored or embedded
// and any time range is cheap to read. The same seed always yields the
// same frames (cmd/sampletruck writes them as JSONL).

// SampleSeed is the seed the demo uses.
const SampleSeed int64 = 20261014

// The sample truck's pack size and efficiency, for planners that model its
// energy use.
const (
	SamplePackKWh  = samplePackKWh
	SampleMiPerKWh = sampleMiPerKWh
)

const (
	sampleDays = 45
	// sampleWrapSoC is the state of charge the corpus starts and ends at.
	sampleWrapSoC   = 80.0
	samplePackKWh   = 131.0
	sampleMiPerKWh  = 2.3
	sampleRoadRatio = 1.2 // road distance ÷ straight-line distance
	sampleL2KW      = 7.6
	sampleHotelKW   = 6.0
)

// sampleSoftwareVersion is the software the sample truck reports
// (Truck tab). No update is ever pending.
const sampleSoftwareVersion = "2026.34.1"

// sampleFlickerDay is the night the truck fails to stay asleep.
const sampleFlickerDay = 24

// sampleStart is the corpus's own (pre-shift) start, a Monday.
var sampleStart = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

type samplePoint struct{ Lat, Lon float64 }

// Places: public landmarks only.
var (
	// SampleHome is the sample truck's "home": the Rose Bowl Stadium's
	// public parking (Pasadena, CA). Non-residential by design.
	SampleHome   = samplePoint{34.1613, -118.1676}
	sampleOffice = samplePoint{34.0562, -118.2365} // Union Station, Los Angeles
	sampleErrand = []samplePoint{
		{34.1394, -118.0447}, // Santa Anita Park
		{34.1184, -118.3004}, // Griffith Observatory
		{34.1478, -118.1445}, // Pasadena City Hall
	}
	sampleVegas  = samplePoint{36.1699, -115.1398}
	sampleHoover = samplePoint{36.0160, -114.7377}
	sampleLebec  = samplePoint{34.9881, -118.9455}
	sampleGilroy = samplePoint{37.0058, -121.5683}
	sampleSF     = samplePoint{37.7955, -122.3937}

	// The DC fast-charging stops sit on real stations from the NREL
	// charger archive (chargers.pmtiles, read 2026-10-14), so the
	// recorder's network pricing (nearest DCFC within 250 m) resolves
	// them as on a real trip, across three networks.
	sampleBarstow   = sampleDCFCStations[0].at
	samplePrimm     = sampleDCFCStations[1].at
	sampleKettleman = sampleDCFCStations[2].at
)

// sampleDCFCStations are the stations the sample truck fast-charges at.
//
// billed is the $/kWh Rivian bills for a session there (the Rivian
// Adventure Network bills through the Rivian app, so its sessions carry a
// live-session price and record as cost_source rivian); 0 elsewhere,
// where the recorder prices them as a network estimate (preview has the
// charger archive). With home L2 at the home rate and the away AC
// sessions left to price, the demo shows every cost source honestly.
var sampleDCFCStations = []struct {
	at            samplePoint
	network, name string // as in the archive (EVNetwork, Name)
	billed        float64
	billedBy      string // the network name Rivian's billed session carries
	// planned is the station's name in a Rivian trip plan ("City, ST
	// [Network]"), for the sample owner's saved plans (sample_plan.go).
	planned string
}{
	{samplePoint{34.851348, -117.082573}, "RIVIAN_ADVENTURE", "Barstow, CA", 0.48, "Rivian Adventure Network", "Barstow, CA [Rivian Adventure Network]"},
	{samplePoint{35.606006, -115.389080}, "Electrify America", "Prizm Outlets (Primm, NV)", 0, "", "Primm, NV [Electrify America]"},
	{samplePoint{35.986104, -119.957772}, "Tesla", "Bravo Farms - Tesla Supercharger", 0, "", "Kettleman City, CA [Tesla]"},
}

type sampleSegKind int

const (
	segParked sampleSegKind = iota
	segDrive
	segCharge
)

// Frame cadence, matched to real trucks (prod vehicle_state, 7 days to
// 2026-10-15): awake, parked or plugged in, p50 5 s / p99 8 s; charging
// p50 3 s; asleep p50 177 s / p99 181 s (the ~3-min connection TTL's
// replay frame). Coarser frames made the demo's Now card read stale.
const (
	sampleAwakeStep  = 5 * time.Second
	sampleAsleepStep = 3 * time.Minute
)

// sampleSeg is a stretch of one activity; frames are every step in
// [start, end).
type sampleSeg struct {
	kind       sampleSegKind
	start, end time.Time
	step       time.Duration
	socA, socB float64
	odoA, odoB float64 // miles
	path       []samplePoint
	cruise     float64        // mph (drive)
	prof       *sampleProfile // drive: the shape within the drive (nil: legacy sine)
	peakKW     float64        // charge
	dcfc       bool
	plugged    bool   // parked with the cable in
	power      string // parked: ready | sleep
	limit      float64
	billedRate float64 // charge: $/kWh Rivian bills (0: not Rivian-billed)
	billedBy   string  // charge: the billed session's network name
}

func (s sampleSeg) frames() int {
	return int((s.end.Sub(s.start) + s.step - 1) / s.step)
}

// SampleCorpus is the generated corpus.
type SampleCorpus struct {
	segs       []sampleSeg
	userPriced int              // segment the owner priced by hand; -1 when none
	legs       [][2]samplePoint // waypoint legs driven (route generator)
	trips      []sampleSpan     // road trips, home to home
	planned    []samplePlanned  // trip legs the owner planned ahead (sample_plan.go)
	stats      SampleSummary    // plan counts; frames and span filled by Summary
}

// sampleSpan is a range of segments, [from, to).
type sampleSpan struct{ from, to int }

// SampleSummary describes what a corpus contains, for tests and the CLI.
type SampleSummary struct {
	Frames          int
	Drives          int
	HomeL2          int
	TopUps          int
	DCFC            int
	AwayAC          int
	FailedSleeps    int
	RoadTripMiles   []float64
	MinSoC, MaxSoC  float64
	Start, End      time.Time
	OdometerStartMi float64
	OdometerEndMi   float64
}

// NewSampleCorpus plans the corpus for seed.
//
// The corpus starts and ends the same way, at home on a weekday morning,
// plugged in at 80 % with the car awake shortly before leaving, so the
// replay's wrap from one loop to the next (sampleLoopGap) reads as just
// another morning, not a jump.
func NewSampleCorpus(seed int64) *SampleCorpus {
	b := &sampleBuilder{rng: rand.New(rand.NewSource(seed)), t: sampleStart.Add(7*time.Hour + 30*time.Minute),
		soc: sampleWrapSoC, odo: 12480, at: SampleHome, limit: 80, plugged: true}
	b.userPriced = -1
	b.plan()
	return &SampleCorpus{segs: b.segs, userPriced: b.userPriced, legs: b.legs, trips: b.trips, planned: b.planned, stats: SampleSummary{HomeL2: b.homeL2, TopUps: b.topUps, DCFC: b.dcfc,
		AwayAC: b.awayAC, FailedSleeps: b.flickers, RoadTripMiles: b.tripMiles}}
}

// ---- planning --------------------------------------------------------

type sampleBuilder struct {
	rng     *rand.Rand
	segs    []sampleSeg
	t       time.Time
	soc     float64
	odo     float64
	at      samplePoint
	plugged bool
	limit   float64

	tripMiles []float64
	inTrip    bool
	tripStart float64
	tripSeg   int // first segment of the trip under way
	trips     []sampleSpan
	planned   []samplePlanned
	homeL2    int
	topUps    int
	dcfc      int
	awayAC    int
	flickers  int
	// userPriced is the segment index of the session the sample owner
	// priced by hand (SampleUserPrice); -1 when none.
	userPriced int
	// legs are the waypoint-to-waypoint stretches driven, for the route
	// generator (sample_routes_gen_test.go).
	legs [][2]samplePoint
}

func (b *sampleBuilder) add(s sampleSeg) {
	if !s.end.After(s.start) {
		return
	}
	s.limit = b.limit
	b.segs = append(b.segs, s)
	b.t = s.end
}

// How long a parked truck stays up before it sleeps, from the R1 fleet
// (prod vehicle_state, 10 days to 2026-10-16): a "ready" run that ends
// in sleep lasts p50 12.9 min, then "standby" p50 1.0 min. 20 min of
// "ready" per park, at five to seven parks a day (top-ups each start
// one), made the demo's idle-awake read high.
const (
	sampleParkReady   = 13 * time.Minute
	sampleParkStandby = 1 * time.Minute
)

// park until `until`: up sampleParkReady, standby, asleep, awake 2 min
// before leaving. flicker adds failed sleeps (asleep < 60 s) through the
// night.
func (b *sampleBuilder) park(until time.Time, flicker bool) {
	if !until.After(b.t) {
		return
	}
	base := sampleSeg{kind: segParked, socA: b.soc, socB: b.soc, odoA: b.odo, odoB: b.odo,
		path: []samplePoint{b.at}, plugged: b.plugged}
	awakeEnd := minTime(b.t.Add(sampleParkReady), until)
	wake := until.Add(-2 * time.Minute)
	seg := base
	seg.start, seg.end, seg.step, seg.power = b.t, awakeEnd, sampleAwakeStep, "ready"
	b.add(seg)
	if wake.After(b.t) {
		seg = base
		seg.start, seg.end, seg.step, seg.power = b.t, minTime(b.t.Add(sampleParkStandby), wake), sampleAwakeStep, "standby"
		b.add(seg)
	}
	if wake.After(b.t) {
		if flicker {
			// Three failed sleeps (asleep ~30 s, then awake again for
			// 20 min), then a normal night's sleep.
			for i := 0; i < 3 && b.t.Add(time.Hour).Before(wake); i++ {
				seg = base
				seg.start, seg.end, seg.step, seg.power = b.t, b.t.Add(30*time.Second), 10*time.Second, "sleep"
				b.add(seg)
				seg = base
				seg.start, seg.end, seg.step, seg.power = b.t, b.t.Add(20*time.Minute), sampleAwakeStep, "ready"
				b.add(seg)
				b.flickers++
			}
		}
		seg = base
		seg.start, seg.end, seg.step, seg.power = b.t, wake, sampleAsleepStep, "sleep"
		b.add(seg)
	}
	seg = base
	seg.start, seg.end, seg.step, seg.power = b.t, until, sampleAwakeStep, "ready"
	b.add(seg)
}

// drive through waypoints at cruise mph.
func (b *sampleBuilder) drive(cruise float64, to ...samplePoint) {
	way := append([]samplePoint{b.at}, to...)
	for i := 1; i < len(way); i++ {
		b.legs = append(b.legs, [2]samplePoint{way[i-1], way[i]})
	}
	path, miles := sampleRoad(way)
	dur := time.Duration(miles / cruise * float64(time.Hour))
	drop := miles / (sampleMiPerKWh * samplePackKWh) * 100
	b.plugged = false
	b.add(sampleSeg{kind: segDrive, start: b.t, end: b.t.Add(dur), step: 5 * time.Second,
		socA: b.soc, socB: b.soc - drop, odoA: b.odo, odoB: b.odo + miles, path: path, cruise: cruise, power: "go",
		prof: newSampleProfile(b.t, miles, dur, 5*time.Second, cruise)})
	b.soc -= drop
	b.odo += miles
	b.at = to[len(to)-1]
	if b.soc < 5 {
		panic("sample corpus: battery ran flat; fix the plan")
	}
}

// charge to target SoC at kw (AC) or peak kW (DCFC).
func (b *sampleBuilder) charge(target, kw float64, dcfc bool) {
	if target <= b.soc {
		return
	}
	// A charge stops at the car's limit, so charging to target means
	// the driver set the limit there.
	b.limit = math.Max(b.limit, target)
	kwh := (target - b.soc) / 100 * samplePackKWh
	avg := kw
	step := sampleAwakeStep
	if dcfc {
		avg = kw * 0.62 // taper
		step = sampleAwakeStep
	}
	dur := time.Duration(kwh / avg * float64(time.Hour))
	billed, billedBy := 0.0, ""
	for _, st := range sampleDCFCStations {
		if dcfc && st.at == b.at {
			billed, billedBy = st.billed, st.billedBy
		}
	}
	b.add(sampleSeg{kind: segCharge, start: b.t, end: b.t.Add(dur), step: step, socA: b.soc, socB: target,
		odoA: b.odo, odoB: b.odo, path: []samplePoint{b.at}, peakKW: kw, dcfc: dcfc, power: "ready", billedRate: billed, billedBy: billedBy})
	b.soc = target
	b.plugged = !dcfc // AC stays plugged in until the car leaves
}

// topUp adds a short AC top-up (the car resuming a completed charge).
// A car holding at its limit resumes only after drifting below it, so
// the park since the last charge drains what the top-up then restores:
// SoC never passes the limit (the Now card reads "holding 90%").
func (b *sampleBuilder) topUp() {
	mins := 3 + b.rng.Intn(4)
	add := float64(mins) / 60 * sampleL2KW / samplePackKWh * 100
	b.drainPark(add)
	b.add(sampleSeg{kind: segCharge, start: b.t, end: b.t.Add(time.Duration(mins) * time.Minute), step: sampleAwakeStep,
		socA: b.soc, socB: b.soc + add, odoA: b.odo, odoB: b.odo, path: []samplePoint{b.at}, peakKW: sampleL2KW, power: "ready"})
	b.soc += add
	b.plugged = true
	b.topUps++
}

func (b *sampleBuilder) clock(day int, h, m int) time.Time {
	return sampleStart.AddDate(0, 0, day).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func (b *sampleBuilder) jitter(maxMin int) time.Duration {
	return time.Duration(b.rng.Intn(2*maxMin+1)-maxMin) * time.Minute
}

// drainPark spreads a drop of pts over the parked segments since the
// last charge or drive, linearly in time, and lowers b.soc to match.
func (b *sampleBuilder) drainPark(pts float64) {
	first := len(b.segs)
	for first > 0 && b.segs[first-1].kind == segParked {
		first--
	}
	if first == len(b.segs) {
		return
	}
	t0, top := b.segs[first].start, b.soc
	span := float64(b.t.Sub(t0))
	at := func(t time.Time) float64 { return top - pts*float64(t.Sub(t0))/span }
	for i := first; i < len(b.segs); i++ {
		b.segs[i].socA, b.segs[i].socB = at(b.segs[i].start), at(b.segs[i].end)
	}
	b.soc = top - pts
}

// evening at home: plug in when low, charge, top up through the night.
func (b *sampleBuilder) eveningAtHome(day int, flicker bool, nextDeparture time.Time, target float64) {
	if b.soc < 68 || target > 80 {
		b.park(b.t.Add(30*time.Minute+b.jitter(10)), false)
		b.limit = target
		b.charge(target, sampleL2KW, false)
		b.homeL2++
		for i, n := 0, 1+b.rng.Intn(3); i < n; i++ {
			next := b.t.Add(time.Duration(70+b.rng.Intn(60)) * time.Minute)
			if !next.Add(time.Hour).Before(nextDeparture) {
				break // never top up into the morning departure
			}
			b.park(next, false)
			b.topUp()
		}
	}
	b.park(nextDeparture, flicker)
	b.limit = 80
}

func (b *sampleBuilder) startTrip() { b.inTrip, b.tripStart, b.tripSeg = true, b.odo, len(b.segs) }
func (b *sampleBuilder) endTrip() {
	b.inTrip = false
	b.tripMiles = append(b.tripMiles, b.odo-b.tripStart)
	b.trips = append(b.trips, sampleSpan{b.tripSeg, len(b.segs)})
}

// planLeg records that the owner planned the leg driven since segment
// from (in the trip under way) ahead of time, as name.
func (b *sampleBuilder) planLeg(name string, from int, origin, dest samplePlace) {
	b.planned = append(b.planned, samplePlanned{name: name, trip: len(b.trips), leg: sampleSpan{from, len(b.segs)},
		origin: origin, dest: dest})
}

// dcfcStop parks, plugs in, fast-charges to t and unplugs before driving
// on, as a real stop does: the car is in P and unplugged for a couple of
// minutes on each side. (Going straight from D to charging and back made
// the recorder close the drive and the charge through its mutual-
// exclusion paths, which don't run the close hooks, so a replay lost
// both.)
func (b *sampleBuilder) dcfcStop(t float64) {
	b.park(b.t.Add(2*time.Minute), false)
	b.charge(t, 150, true)
	b.park(b.t.Add(3*time.Minute), false)
	b.dcfc++
}

func (b *sampleBuilder) plan() {
	b.park(b.clock(0, 7, 45), false)
	for day := 0; day < sampleDays; {
		if day == sampleDays-1 {
			// The last day: a commute, then plugged in to 80 % overnight,
			// ending awake at 07:30 the next morning like the start.
			b.commute(day)
			b.park(b.t.Add(30*time.Minute), false)
			b.limit = 80
			b.charge(sampleWrapSoC, sampleL2KW, false)
			b.homeL2++
			b.plugged = true
			b.park(b.clock(day+1, 7, 30), false)
			break
		}
		weekday := sampleStart.AddDate(0, 0, day).Weekday()
		switch {
		case day == 17: // Thursday before trip 1: charge to 90 overnight.
			b.commute(day)
			b.eveningAtHome(day, false, b.clock(day+1, 7, 40), 90)
			day++
		case day == 18: // Trip 1: Las Vegas, Friday to Sunday.
			b.startTrip()
			out := len(b.segs)
			b.drive(66, sampleBarstow)
			b.dcfcStop(85)
			b.drive(68, sampleVegas) // I-15, through Baker
			b.planLeg(samplePlanVegas, out, samplePlaceHome, samplePlaceVegas)
			b.park(b.t.Add(90*time.Minute), false)
			b.charge(90, sampleHotelKW, false) // hotel L2: away AC
			b.awayAC++
			b.userPriced = len(b.segs) - 1 // the owner enters the hotel's price
			b.park(b.clock(day+1, 10, 0), false)
			b.drive(48, sampleHoover, sampleVegas)
			b.park(b.clock(day+1, 19, 0), false)
			b.drive(28, samplePoint{36.1147, -115.1728}, sampleVegas) // the Strip and back
			b.park(b.clock(day+2, 9, 0), false)
			back := len(b.segs)
			b.drive(66, samplePrimm)
			b.dcfcStop(85)
			b.drive(68, sampleBarstow, SampleHome) // I-15, through Baker
			b.planLeg(samplePlanVegasHome, back, samplePlaceVegas, samplePlaceHome)
			b.endTrip()
			b.eveningAtHome(day+2, false, b.clock(day+3, 7, 45).Add(b.jitter(10)), 80)
			day += 3
		case day == 38: // Thursday before trip 2.
			b.commute(day)
			b.eveningAtHome(day, false, b.clock(day+1, 7, 0), 90)
			day++
		case day == 39: // Trip 2: San Francisco, Friday to Monday.
			b.startTrip()
			b.drive(66, sampleLebec, sampleKettleman)
			b.dcfcStop(85)
			b.drive(68, sampleGilroy, sampleSF)
			b.park(b.t.Add(time.Hour), false)
			b.charge(90, sampleHotelKW, false)
			b.awayAC++
			b.park(b.clock(day+1, 11, 0), false)
			b.drive(22, samplePoint{37.8080, -122.4177}, samplePoint{37.7694, -122.4862}, sampleSF) // Fisherman's Wharf, Golden Gate Park
			b.park(b.t.Add(time.Hour), false)
			b.charge(90, sampleHotelKW, false)
			b.awayAC++
			b.park(b.clock(day+3, 9, 0), false)
			b.drive(66, sampleGilroy, sampleKettleman)
			b.dcfcStop(80)
			b.drive(68, sampleLebec, SampleHome)
			b.endTrip()
			b.eveningAtHome(day+3, false, b.clock(day+4, 7, 45).Add(b.jitter(10)), 80)
			day += 4
		case weekday == time.Saturday || weekday == time.Sunday:
			b.errand(day)
			b.eveningAtHome(day, day == sampleFlickerDay, b.nextMorning(day), 80)
			day++
		default:
			b.commute(day)
			b.eveningAtHome(day, day == sampleFlickerDay, b.nextMorning(day), 80)
			day++
		}
	}
}

func (b *sampleBuilder) nextMorning(day int) time.Time {
	next := sampleStart.AddDate(0, 0, day+1).Weekday()
	if next == time.Saturday || next == time.Sunday {
		return b.clock(day+1, 10, 30).Add(b.jitter(30))
	}
	return b.clock(day+1, 7, 45).Add(b.jitter(10))
}

func (b *sampleBuilder) commute(day int) {
	b.drive(30, sampleOffice)
	b.park(b.clock(day, 17, 30).Add(b.jitter(15)), false)
	if b.rng.Intn(3) == 0 {
		b.drive(28, sampleErrand[b.rng.Intn(len(sampleErrand))])
		b.park(b.t.Add(time.Duration(20+b.rng.Intn(30))*time.Minute), false)
	}
	b.drive(30, SampleHome)
}

func (b *sampleBuilder) errand(day int) {
	if b.t.Before(b.clock(day, 11, 0)) {
		b.park(b.clock(day, 11, 0).Add(b.jitter(30)), false)
	}
	b.drive(28, sampleErrand[b.rng.Intn(len(sampleErrand))])
	b.park(b.t.Add(time.Duration(30+b.rng.Intn(60))*time.Minute), false)
	b.drive(28, SampleHome)
}

// ---- frames ----------------------------------------------------------

func (s sampleSeg) frameAt(i int) replayFrame {
	t := s.start.Add(time.Duration(i) * s.step)
	f := float64(t.Sub(s.start)) / float64(s.end.Sub(s.start))
	soc := s.socA + (s.socB-s.socA)*f
	if s.kind == segCharge && s.dcfc {
		soc = s.socA + (s.socB-s.socA)*(1-math.Pow(1-f, 1.6))
	}
	pos := s.path[0]
	odo := s.odoA + (s.odoB-s.odoA)*f
	if s.kind == segDrive {
		d := f
		if p := s.prof; p != nil && i >= 0 && i < len(p.speed) {
			d = p.dist[i]
			soc = s.socA + (s.socB-s.socA)*p.soc[i]
			odo = s.odoA + (s.odoB-s.odoA)*d
		}
		pos = alongPath(s.path, d)
	}
	fix := t
	fr := replayFrame{
		At: t, ShiftState: "P", BatteryPct: round1(soc), OdometerMi: round2(odo),
		RangeMi: round1(soc / 100 * samplePackKWh * sampleMiPerKWh), Lat: round5(pos.Lat), Lon: round5(pos.Lon),
		ChargingState: "charger_disconnected", PowerState: s.power, LocationFixAt: &fix,
		DriveMode: "everyday", ChargeLimitPct: s.limit, TirePressureBar: 2.9,
		OtaCurrentVersion: sampleSoftwareVersion,
	}
	outside := 14 + 7*math.Sin(2*math.Pi*(float64(t.Hour())+float64(t.Minute())/60-9)/24)
	fr.OutsideTempC = round1(outside)
	fr.InsideTempC = fr.OutsideTempC
	fr.PackTempAvgC, fr.PackTempMinC, fr.PackTempMaxC = 22, 21, 23
	switch s.kind {
	case segDrive:
		fr.ShiftState = "D"
		fr.PowerState = "go"
		if p := s.prof; p != nil && i >= 0 && i < len(p.speed) {
			fr.SpeedMph = p.speed[i]
			if p.modeAt >= 0 && i >= p.modeAt {
				fr.DriveMode = "sport"
			}
		} else {
			ramp := math.Min(1, math.Min(f, 1-f)*40) // ease in/out over the first and last ~2.5 %
			fr.SpeedMph = round1(math.Max(3, s.cruise*(0.88+0.12*math.Sin(float64(i)/9))*math.Max(ramp, 0.15)))
		}
		fr.InsideTempC = 21
		fr.PackTempAvgC, fr.PackTempMinC, fr.PackTempMaxC = 28, 26, 30
	case segCharge:
		fr.ChargingState = "charging_active"
		fr.PowerState = "ready"
		kw := s.peakKW
		if s.dcfc {
			// The power that produces the SoC curve above: the energy
			// delivered over the stop is socΔ × pack, so the power is the
			// derivative of that curve, 1.6·(1-f)^0.6 × the average. (A
			// separate linear taper made the power integral differ from the
			// SoC energy by up to 14%.) Never below the 30 kW the taper
			// bottoms out at on a real stall.
			avgKW := (s.socB - s.socA) / 100 * samplePackKWh / s.end.Sub(s.start).Hours()
			kw = math.Max(30, 1.6*math.Pow(1-f, 0.6)*avgKW)
			fr.PackTempAvgC, fr.PackTempMinC, fr.PackTempMaxC = 34, 32, 36
		} else if f > 0.9 {
			kw = s.peakKW * (1 - (f-0.9)*5)
		}
		fr.ChargerKW = round1(kw)
		if s.billedRate > 0 {
			// The running total Rivian's live session reports.
			kwh := math.Max(0, soc-s.socA) / 100 * samplePackKWh
			fr.SessionPrice, fr.SessionCurrency, fr.SessionNetwork = round2(kwh*s.billedRate), "USD", s.billedBy
		}
	case segParked:
		if s.plugged {
			fr.ChargingState = "charging_complete"
		}
	}
	return fr
}

// Frames implements ReplayCorpus: frames in [from, to), oldest first.
func (c *SampleCorpus) Frames(_ context.Context, from, to time.Time) ([]State, error) {
	var out []State
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(from) })
	for ; i < len(c.segs) && c.segs[i].start.Before(to); i++ {
		s := c.segs[i]
		k := 0
		if from.After(s.start) {
			k = int((from.Sub(s.start) + s.step - 1) / s.step)
		}
		for n := s.frames(); k < n; k++ {
			fr := s.frameAt(k)
			if !fr.At.Before(to) {
				break
			}
			out = append(out, fr.toState())
		}
	}
	return out, nil
}

// LastAtOrBefore implements ReplayCorpus.
func (c *SampleCorpus) LastAtOrBefore(_ context.Context, t time.Time) (*State, error) {
	if len(c.segs) == 0 || t.Before(c.segs[0].start) {
		return nil, nil
	}
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].start.After(t) }) - 1
	s := c.segs[i]
	k := int(t.Sub(s.start) / s.step)
	if n := s.frames(); k >= n {
		k = n - 1
	}
	st := s.frameAt(k).toState()
	return &st, nil
}

// span returns the first and last frame times.
func (c *SampleCorpus) span() (time.Time, time.Time) {
	last := c.segs[len(c.segs)-1]
	return c.segs[0].start, last.frameAt(last.frames() - 1).At
}

// WriteJSONL writes every frame as one replay-format JSON line.
func (c *SampleCorpus) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, s := range c.segs {
		for k, n := 0, s.frames(); k < n; k++ {
			if err := enc.Encode(s.frameAt(k)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Summary describes the corpus.
func (c *SampleCorpus) Summary() SampleSummary {
	sum := c.stats
	sum.MinSoC = 100
	for _, s := range c.segs {
		sum.Frames += s.frames()
		if s.kind == segDrive {
			sum.Drives++
		}
		sum.MinSoC = math.Min(sum.MinSoC, math.Min(s.socA, s.socB))
		sum.MaxSoC = math.Max(sum.MaxSoC, math.Max(s.socA, s.socB))
	}
	sum.Start, sum.End = c.span()
	sum.OdometerStartMi, sum.OdometerEndMi = c.segs[0].odoA, c.segs[len(c.segs)-1].odoB
	return sum
}

// sampleLoopGap is the pause between the end of one loop of the sample
// corpus and the next. The owner-replay's replayLoopGap (6 h) left the
// public demo frozen on its last frame for up to 6 hours a loop ("asleep,
// updated 5 h ago" beside a Live pill); the sample corpus ends where it
// starts, so it needs no overnight park to hide the wrap.
const sampleLoopGap = 5 * time.Minute

// sampleTimeline is how the sample feed loops c over the wall clock.
// SampleUserPrice maps times back through the same timeline.
func sampleTimeline(c *SampleCorpus) replayTimeline {
	start, end := c.span()
	return replayTimeline{start: start, end: end, epoch: replayEpoch, gap: sampleLoopGap,
		odoSpanKm: (c.segs[len(c.segs)-1].odoB - c.segs[0].odoA) / kmToMi}
}

// The price the sample owner entered for the Las Vegas hotel's L2: the
// hotel charged a flat fee for the night's charging.
const (
	sampleUserPriceCost     = 15.00
	sampleUserPriceCurrency = "USD"
)

var (
	sampleTimelineOnce sync.Once
	sampleCorpusShared *SampleCorpus
	sampleTimelineMemo replayTimeline
)

// sampleShared is the demo's corpus and timeline, built once.
func sampleShared() (*SampleCorpus, replayTimeline) {
	sampleTimelineOnce.Do(func() {
		sampleCorpusShared = NewSampleCorpus(SampleSeed)
		sampleTimelineMemo = sampleTimeline(sampleCorpusShared)
	})
	return sampleCorpusShared, sampleTimelineMemo
}

// SampleUserPrice reports whether the sample truck's charge that started
// at startedAt (wall clock, as the replay feed recorded it) is the one
// its owner priced by hand (the Las Vegas hotel's L2, a flat $15), so the
// demo seeder can apply that price through the normal user-pricing path
// after the backfill: a price only a person can enter, which the
// recorder must not invent. Deterministic: startedAt is mapped back onto
// the looping corpus like the replay feed maps it forward.
func SampleUserPrice(startedAt time.Time) (cost float64, currency string, ok bool) {
	c, tl := sampleShared()
	if c.userPriced < 0 {
		return 0, "", false
	}
	k := tl.loopAt(startedAt)
	corpusT := tl.start.Add(startedAt.Sub(tl.loopStart(k)))
	d := corpusT.Sub(c.segs[c.userPriced].start)
	if d < -2*time.Minute || d > 2*time.Minute {
		return 0, "", false
	}
	return sampleUserPriceCost, sampleUserPriceCurrency, true
}

// NewSampleReplayFeed is the replay feed over the sample corpus
// (RIVIAN_CLIENT=replay with RIVOLT_REPLAY_SOURCE=sample). pool, when
// set, provides the resume point (the demo vehicle's latest sample).
func NewSampleReplayFeed(pool *sql.DB, backfill time.Duration, logger *slog.Logger) *ReplayFeed {
	if logger == nil {
		logger = slog.Default()
	}
	c := NewSampleCorpus(SampleSeed)
	f := &ReplayFeed{
		corpus:   c,
		timeline: sampleTimeline(c),
		source:   Vehicle{ID: "sample", Name: "Sample R1S", Model: "R1S", ModelYear: 2025, PackKWh: samplePackKWh},
		backfill: backfill,
		logger:   logger,
	}
	if pool != nil {
		f.lastRecorded = func(ctx context.Context, vehicleID string) (time.Time, error) {
			var at sql.NullTime
			err := pool.QueryRowContext(ctx, `
				SELECT max(vs.at) FROM vehicle_state vs
				JOIN vehicles v ON v.id = vs.vehicle_id
				WHERE v.rivian_vehicle_id = $1`, vehicleID).Scan(&at)
			return at.Time, err
		}
	}
	logger.Info("replay corpus: generated sample truck", "seed", SampleSeed,
		"days", sampleDays, "loop", f.timeline.period().Round(time.Hour).String())
	return f
}

// ---- geometry --------------------------------------------------------

func pathMiles(p []samplePoint) float64 {
	var m float64
	for i := 1; i < len(p); i++ {
		m += haversineMeters(p[i-1].Lat, p[i-1].Lon, p[i].Lat, p[i].Lon) / 1609.344
	}
	return m
}

// alongPath returns the point at fraction f of the path's length.
func alongPath(p []samplePoint, f float64) samplePoint {
	total := pathMiles(p)
	want := f * total
	for i := 1; i < len(p); i++ {
		leg := haversineMeters(p[i-1].Lat, p[i-1].Lon, p[i].Lat, p[i].Lon) / 1609.344
		if want <= leg || i == len(p)-1 {
			g := 0.0
			if leg > 0 {
				g = math.Min(1, want/leg)
			}
			return samplePoint{p[i-1].Lat + (p[i].Lat-p[i-1].Lat)*g, p[i-1].Lon + (p[i].Lon-p[i-1].Lon)*g}
		}
		want -= leg
	}
	return p[len(p)-1]
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round5(f float64) float64 { return math.Round(f*1e5) / 1e5 }
