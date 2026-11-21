package rivian

import (
	"hash/fnv"
	"math"
	"math/rand"
	"time"
	_ "time/tzdata" // the history is planned in Los Angeles time; a bare image has no zoneinfo
)

// The sample truck's older history.
//
// The replay corpus (sample_corpus.go) is 45 days played at full frame rate; a year and a half of that would be about 2.2 million frames
// per truck and half a day of catch-up. Older history is therefore generated coarser: only the drives and charges the recorder would have
// kept (a start, an end, the battery before and after, the energy, the route, the price), with no samples. It is planned by the same
// sampleBuilder the corpus uses, so the days read like the corpus's (commute, errands, home charging overnight, road trips on the corpus's
// own roads and fast-charging stops), but with the seasons the 45 days cannot show: colder months run a lower mi/kWh and charge more for
// the same miles, and a drive carries the outside temperature it started in (what "range near a temperature" reads).
//
// It is a pure function of the day it ends on: a fixed anchor, one RNG per day, and nothing from the clock, so the same call gives the
// same rows, and a later boundary only appends. It never feeds the replay and never touches NewSampleCorpus, so the public demo's frames
// are exactly what they were (TestSampleCorpusUnchanged pins them).

// SampleHistoryAnchor is the first day of the sample truck's older history, a Monday, in Los Angeles time.
var SampleHistoryAnchor = time.Date(2025, 5, 5, 0, 0, 0, 0, time.UTC)

const (
	sampleHistSeed = 20250505
	// sampleHistTail keeps the last unit's evening charge (it can run to the small hours) before the boundary.
	sampleHistTail = 6 * time.Hour
)

var sampleHistLoc = func() *time.Location {
	l, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PST", -8*3600)
	}
	return l
}()

// SampleHistoryDrive is one drive of the older history.
type SampleHistoryDrive struct {
	Start, End         time.Time
	StartSoC, EndSoC   float64
	Miles, EnergyKWh   float64
	MaxMph, AvgMph     float64
	StartLat, StartLon float64
	EndLat, EndLon     float64
	Polyline           string  // precision 5, as the recorder stores it
	TempC              float64 // outside temperature at the start (drive_weather)
}

// SampleHistoryCharge is one charge session of the older history.
type SampleHistoryCharge struct {
	Start, End       time.Time
	StartSoC, EndSoC float64
	EnergyKWh        float64
	MilesAdded       float64
	MaxKW, AvgKW     float64
	Lat, Lon         float64
	Place            string // home | away
	DCFC             bool
	LimitPct         float64
	Cost             float64
	PricePerKWh      float64
	CostSource       string // home_rate | rivian | network_estimate | user
	Network          string
}

// SampleHistory is the generated older history: drives and charges, oldest first.
type SampleHistory struct {
	Drives  []SampleHistoryDrive
	Charges []SampleHistoryCharge
	// Trips counts the road trips planned.
	Trips int
}

type sampleHistTrip struct {
	week int // Friday of this week from the anchor
	sf   bool
}

// Eight road trips over the first 74 weeks: Las Vegas (3 days) and San Francisco (4 days), through every season.
var sampleHistTrips = []sampleHistTrip{{6, false}, {14, true}, {24, false}, {33, true}, {38, false}, {44, true}, {55, false}, {64, false}, {73, true}}

// sampleHistLocal is the Los Angeles wall time h:m on the anchor's calendar day d.
func sampleHistLocal(d, h, m int) time.Time {
	a := SampleHistoryAnchor
	return time.Date(a.Year(), a.Month(), a.Day()+d, h, m, 0, 0, sampleHistLoc).UTC()
}

// sampleHistNoise is a deterministic -1..1 for a key, so a temperature never depends on RNG order.
func sampleHistNoise(key int64) float64 {
	h := fnv.New64a()
	var b [8]byte
	for i := range b {
		b[i] = byte(uint64(key) >> (8 * i))
	}
	_, _ = h.Write(b[:])
	return float64(h.Sum64()%2001)/1000 - 1
}

// SampleHistoryTempC is the outside temperature on the sample truck's older history at t: a year with a cold winter morning (about 0 °C)
// and a warm summer afternoon (about 30 °C), a daily swing, and a few degrees of weather that day.
func SampleHistoryTempC(t time.Time) float64 {
	lt := t.In(sampleHistLoc)
	base := 14 - 10.5*math.Cos(2*math.Pi*float64(lt.YearDay()-15)/365)
	hr := float64(lt.Hour()) + float64(lt.Minute())/60
	day := sampleHistNoise(int64(lt.Year())*1000 + int64(lt.YearDay()))
	return round1(base + 5*math.Sin(2*math.Pi*(hr-9)/24) + 3*day)
}

// sampleHistEff is the truck's mi/kWh at outside temperature c: the corpus's 2.3 is a mild day; cold costs about 1.6 % per degree under
// 18 °C (cabin heat and a cold pack), and a hot day a little from the air conditioning; the highway is 8 % lower than town.
func sampleHistEff(c, cruise float64) float64 {
	f := 1.0
	if c < 18 {
		f -= 0.0165 * (18 - c)
	}
	if c > 28 {
		f -= 0.006 * (c - 28)
	}
	if cruise >= 55 {
		f *= 0.92
	}
	return 2.55 * f
}

// sampleHistPackKWh is the usable energy a charge of this day puts in per 100 % of battery: a pack that fades about 2 % over the window and
// delivers a little less when cold.
func sampleHistPackKWh(t time.Time) float64 {
	days := t.Sub(sampleHistLocal(0, 0, 0)).Hours() / 24
	cold := math.Max(0, 14-SampleHistoryTempC(t)) * 0.07
	return samplePackKWh*1.015 - 2.3*math.Min(1, days/700) - cold + 0.5*sampleHistNoise(t.Unix()/3600)
}

type sampleHist struct {
	b *sampleBuilder
}

// drive plans one drive at the outside temperature of its start, never below the reserve (a short charge where it stands if the plan
// would have run it flat).
func (h *sampleHist) drive(cruise float64, to ...samplePoint) {
	b := h.b
	eff := sampleHistEff(SampleHistoryTempC(b.t), cruise) * (1 + 0.04*(b.rng.Float64()-0.5))
	way := append([]samplePoint{b.at}, to...)
	_, miles := sampleRoad(way)
	if need := miles/(eff*samplePackKWh)*100 + 8; b.soc < need {
		kw, dc := sampleL2KW, false
		if b.at != SampleHome {
			kw, dc = 150, true
		}
		b.charge(math.Min(95, need+10), kw, dc)
	}
	b.effMi = eff
	b.drive(cruise, to...)
	b.effMi = 0
}

// fast is a fast-charging stop that leaves enough to cover the next miles with a 14 % reserve, between 55 and 92 %.
func (h *sampleHist) fast(nextMiles float64) {
	b := h.b
	eff := sampleHistEff(SampleHistoryTempC(b.t), 66)
	need := nextMiles/(eff*samplePackKWh)*100 + 14
	b.dcfcStop(math.Max(55, math.Min(92, need)))
}

// run drives hops in order at cruise mph and fast-charges at each stop that has a hop after it.
func (h *sampleHist) run(cruise float64, hops []sampleHop) {
	for i, hp := range hops {
		h.drive(cruise, hp.to)
		if hp.stop && i+1 < len(hops) {
			_, miles := sampleRoad([]samplePoint{hp.to, hops[i+1].to})
			h.fast(miles)
		}
	}
}

type sampleHop struct {
	to   samplePoint
	stop bool
}

// evening is the night at home: plugged in when the battery is low (usually to 80, sometimes 90), charging at a varied hour, parked until next.
func (h *sampleHist) evening(d int, next time.Time, target float64) {
	b := h.b
	thr := 52 + 16*b.rng.Float64()
	b.park(b.t.Add(25*time.Minute+time.Duration(b.rng.Intn(20))*time.Minute), false)
	if b.soc < thr || target > 80 {
		if b.rng.Intn(10) < 4 {
			b.park(sampleHistLocal(d, 21, 0).Add(time.Duration(b.rng.Intn(90))*time.Minute), false)
		}
		b.limit = target
		b.charge(target, sampleL2KW, false)
		b.homeL2++
	}
	b.park(next, false)
	b.limit = 80
}

func (h *sampleHist) departure(d int, weekend bool) time.Time {
	r := h.b.rng
	if weekend {
		return sampleHistLocal(d, 9, 30).Add(time.Duration(r.Intn(180)) * time.Minute)
	}
	return sampleHistLocal(d, 7, 25).Add(time.Duration(r.Intn(70)) * time.Minute)
}

func (h *sampleHist) tripFriday(d int) (sampleHistTrip, bool) {
	if d%7 != 4 {
		return sampleHistTrip{}, false
	}
	for _, t := range sampleHistTrips {
		if t.week == d/7 {
			return t, true
		}
	}
	return sampleHistTrip{}, false
}

// NewSampleHistory plans the sample truck's older history up to before (a UTC day boundary: the day the replay's own history begins).
// Whole days only: a day or a road trip that would not end before before is not planned, so a later call with a later boundary
// only adds to what this one returned.
func NewSampleHistory(before time.Time) *SampleHistory {
	end := before.UTC()
	b := &sampleBuilder{rng: rand.New(rand.NewSource(sampleHistSeed)), t: sampleHistLocal(0, 6, 30), soc: 80, odo: 0, at: SampleHome, limit: 80, plugged: true}
	h := &sampleHist{b: b}
	out := &SampleHistory{}
	for d := 0; ; {
		unit := 1
		trip, isTrip := h.tripFriday(d)
		if isTrip {
			unit = 3
			if trip.sf {
				unit = 4
			}
		}
		if sampleHistLocal(d+unit, 0, 0).Add(sampleHistTail).After(end) {
			break
		}
		b.rng = rand.New(rand.NewSource(sampleHistSeed + int64(d)*7919))
		if isTrip {
			h.trip(d, trip)
			out.Trips++
			d += unit
			continue
		}
		h.day(d)
		d++
	}
	h.rows(out)
	return out
}

func (h *sampleHist) day(d int) {
	b := h.b
	wd := sampleHistLocal(d, 12, 0).In(sampleHistLoc).Weekday()
	weekend := wd == time.Saturday || wd == time.Sunday
	b.park(h.departure(d, weekend), false)
	switch {
	case !weekend && b.rng.Intn(100) < 82:
		h.drive(30, sampleOffice)
		b.park(sampleHistLocal(d, 17, 0).Add(time.Duration(b.rng.Intn(80))*time.Minute), false)
		if b.rng.Intn(3) == 0 {
			h.drive(28, sampleErrand[b.rng.Intn(len(sampleErrand))])
			b.park(b.t.Add(time.Duration(20+b.rng.Intn(30))*time.Minute), false)
		}
		h.drive(30, SampleHome)
	case !weekend && b.rng.Intn(100) < 55, weekend && b.rng.Intn(100) < 75:
		if weekend {
			b.park(sampleHistLocal(d, 11, 0).Add(time.Duration(b.rng.Intn(120))*time.Minute), false)
		}
		h.drive(28, sampleErrand[b.rng.Intn(len(sampleErrand))])
		b.park(b.t.Add(time.Duration(30+b.rng.Intn(70))*time.Minute), false)
		h.drive(28, SampleHome)
	}
	h.evening(d, h.nextDeparture(d), 80+10*float64(b.rng.Intn(100)/92))
}

// nextDeparture is the morning departure of day d+1, and tomorrow's charge target is 90 before a road trip (set by the caller).
func (h *sampleHist) nextDeparture(d int) time.Time {
	wd := sampleHistLocal(d+1, 12, 0).In(sampleHistLoc).Weekday()
	return h.departure(d+1, wd == time.Saturday || wd == time.Sunday)
}

func (h *sampleHist) trip(d int, t sampleHistTrip) {
	b := h.b
	b.park(h.departure(d, true), false)
	b.startTrip()
	hotel := func() {
		b.park(b.t.Add(70*time.Minute), false)
		b.charge(90, sampleHotelKW, false)
		b.awayAC++
	}
	if !t.sf {
		h.run(66, []sampleHop{{sampleBarstow, true}, {sampleVegas, false}})
		hotel()
		b.park(sampleHistLocal(d+1, 10, 0), false)
		h.drive(48, sampleHoover, sampleVegas)
		b.park(sampleHistLocal(d+1, 19, 0), false)
		h.drive(28, samplePoint{36.1147, -115.1728}, sampleVegas)
		b.park(sampleHistLocal(d+2, 9, 0), false)
		h.run(66, []sampleHop{{samplePrimm, true}, {sampleBarstow, true}, {SampleHome, false}})
		b.endTrip()
		h.evening(d+2, h.nextDeparture(d+2), 80)
		return
	}
	h.run(66, []sampleHop{{sampleLebec, true}, {sampleKettleman, true}, {sampleGilroy, true}, {sampleSF, false}})
	hotel()
	b.park(sampleHistLocal(d+1, 11, 0), false)
	h.drive(22, samplePoint{37.8080, -122.4177}, samplePoint{37.7694, -122.4862}, sampleSF)
	hotel()
	b.park(sampleHistLocal(d+3, 9, 0), false)
	h.run(66, []sampleHop{{sampleGilroy, true}, {sampleKettleman, true}, {sampleLebec, true}, {SampleHome, false}})
	b.endTrip()
	h.evening(d+3, h.nextDeparture(d+3), 80)
}

// station is the fast-charging stop at p: network, display name and $/kWh. Rivian's own stop is billed through the app; the rest are priced
// from the network's rate, as a recorded session would be.
func sampleHistStation(p samplePoint) (network string, rate float64, billed bool) {
	switch p {
	case sampleBarstow:
		return "Rivian Adventure Network", 0.48, true
	case sampleKettleman:
		return "Tesla Supercharger", 0.43, false
	}
	return "Electrify America", 0.55, false
}

// rows turns the planned segments into drives and charges.
func (h *sampleHist) rows(out *SampleHistory) {
	for _, s := range h.b.segs {
		switch s.kind {
		case segDrive:
			miles := s.odoB - s.odoA
			hours := s.end.Sub(s.start).Hours()
			if miles < 0.2 || hours <= 0 {
				continue
			}
			maxMph := s.cruise * 1.12
			if s.prof != nil {
				maxMph = 0
				for _, v := range s.prof.speed {
					maxMph = math.Max(maxMph, v)
				}
			}
			pts := make([][2]float64, 0, len(s.path))
			for _, p := range s.path {
				pts = append(pts, [2]float64{p.Lat, p.Lon})
			}
			first, last := s.path[0], s.path[len(s.path)-1]
			out.Drives = append(out.Drives, SampleHistoryDrive{
				Start: s.start, End: s.end, StartSoC: round1(s.socA), EndSoC: round1(s.socB),
				Miles: round1(miles), EnergyKWh: round2((s.socA - s.socB) / 100 * samplePackKWh),
				MaxMph: round1(maxMph), AvgMph: round1(miles / hours),
				StartLat: round5(first.Lat), StartLon: round5(first.Lon), EndLat: round5(last.Lat), EndLon: round5(last.Lon),
				Polyline: encodePolyline(pts), TempC: SampleHistoryTempC(s.start),
			})
		case segCharge:
			kwh := (s.socB - s.socA) / 100 * sampleHistPackKWh(s.start)
			hours := s.end.Sub(s.start).Hours()
			if kwh < 0.5 || hours <= 0 {
				continue
			}
			at := s.path[0]
			c := SampleHistoryCharge{
				Start: s.start, End: s.end, StartSoC: round1(s.socA), EndSoC: round1(s.socB),
				EnergyKWh: round2(kwh), MilesAdded: round1(kwh * sampleMiPerKWh), AvgKW: round1(kwh / hours),
				Lat: round5(at.Lat), Lon: round5(at.Lon), Place: "away", DCFC: s.dcfc, LimitPct: s.limit,
			}
			switch {
			case s.dcfc:
				cold := math.Max(0, 12-SampleHistoryTempC(s.start))
				c.MaxKW = round1((175 - 1.3*s.socA) * (1 - 0.006*cold))
				network, rate, billed := sampleHistStation(at)
				c.Network, c.PricePerKWh, c.CostSource = network, rate, "network_estimate"
				if billed {
					c.CostSource = "rivian"
				}
				c.Cost = round2(kwh * rate)
			case at == SampleHome:
				c.Place, c.MaxKW = "home", round1(s.peakKW*1.02)
				c.PricePerKWh, c.CostSource = 0.16, "home_rate"
				c.Cost = round2(kwh * 0.16)
			default: // a hotel's L2, priced by hand
				c.MaxKW = round1(s.peakKW * 1.02)
				c.PricePerKWh, c.CostSource = 0.28, "user"
				c.Cost = round2(kwh * 0.28)
			}
			out.Charges = append(out.Charges, c)
		}
	}
}
