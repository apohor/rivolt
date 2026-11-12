package histimport

import (
	"errors"
	"io"
	"math"
	"time"
)

// Grouping of points into sessions (design §2): a new run starts at a change of session id, or at a gap longer than runGap. Inside a run,
// moving points are a drive; a stationary stretch where SoC rises is a charge when it lasts chargeMinDur and gains chargeMinGain points;
// any other stationary stretch (a red light, a stop) stays inside the drive around it, and a run that never moves is parked.
const (
	runGap        = 10 * time.Minute
	chargeMinDur  = 5 * time.Minute
	chargeMinGain = 1.0
	movingMph     = 1.0
	maxRunPoints  = 50_000
	maxLinePoints = 1000
	socPeekRows   = 2000
)

type point struct {
	t        time.Time
	lat, lon float64
	soc      float64
	speed    float64 // mph; NaN when the file has none
	odo      float64 // miles; NaN when the file has none
}

// points reads a table with one recorded point per row. Memory is the sessions it produces plus one run of points (capped).
func (p *parser) points(t Table) error {
	pk := &peeked{t: t}
	if err := pk.fill(socPeekRows); err != nil {
		return err
	}
	var max float64
	for _, row := range pk.rows {
		if v, ok, err := p.number(row, FSoC); err == nil && ok {
			max = math.Max(max, v)
		}
	}
	p.socMax = max
	p.socFrac = p.socHint == "fraction" || (p.socHint == "" && max > 0 && max <= 1)
	p.rep.SoCScale = "percent"
	if p.socFrac {
		p.rep.SoCScale = "fraction"
	}

	var run []point
	var runID string
	flush := func() {
		if len(run) > 0 {
			p.run(run)
			run = run[:0]
		}
	}
	for {
		row, err := pk.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		p.rep.Rows++
		if p.noteVehicle(row) {
			p.rep.Skipped[ReasonOtherVeh]++
			continue
		}
		pt, ferr := p.pointRow(row)
		if ferr != nil {
			return ferr
		}
		if pt == nil {
			continue
		}
		id := p.val(row, FSessionID)
		if n := len(run); n > 0 && (id != runID || pt.t.Sub(run[n-1].t) > runGap || pt.t.Before(run[n-1].t)) {
			flush()
		}
		if len(run) >= maxRunPoints {
			p.reject(ReasonRunTooLong, 1)
			continue
		}
		runID = id
		run = append(run, *pt)
	}
	flush()
	if p.overflow {
		return ErrTooBig
	}
	return nil
}

// pointRow parses one point. It returns (nil, nil) for a row that failed (already counted), and an error only for the zone question.
func (p *parser) pointRow(row []string) (*point, error) {
	t, err := p.when(row, FTime)
	if err != nil {
		return nil, p.timeErr(err)
	}
	pt := &point{t: t, speed: math.NaN(), odo: math.NaN()}
	var ok bool
	if pt.lat, ok, err = p.number(row, FLat); err != nil || !ok {
		p.reject(badOrMissing(err), 1)
		return nil, nil
	}
	if pt.lon, ok, err = p.number(row, FLon); err != nil || !ok {
		p.reject(badOrMissing(err), 1)
		return nil, nil
	}
	if pt.soc, ok, err = p.number(row, FSoC); err != nil || !ok {
		p.reject(badOrMissing(err), 1)
		return nil, nil
	}
	if p.socFrac {
		pt.soc *= 100
	}
	if pt.lat < -90 || pt.lat > 90 || pt.lon < -180 || pt.lon > 180 || (pt.lat == 0 && pt.lon == 0) {
		p.reject(ReasonBadNumber, 1)
		return nil, nil
	}
	if badSoC(pt.soc) {
		p.reject(ReasonSoCRange, 1)
		return nil, nil
	}
	if v, ok, e := p.mph(row, FSpeed); e != nil {
		p.reject(ReasonBadNumber, 1)
		return nil, nil
	} else if ok {
		pt.speed = v
	}
	if v, ok, e := p.miles(row, FOdometer); e != nil {
		p.reject(ReasonBadNumber, 1)
		return nil, nil
	} else if ok {
		pt.odo = v
	}
	return pt, nil
}

func badOrMissing(err error) string {
	if err != nil {
		return ReasonBadNumber
	}
	return ReasonMissing
}

type seg struct {
	from, to int // inclusive point indexes
	moving   bool
	charge   bool
}

// run turns one run of points into drives and charges.
func (p *parser) run(pts []point) {
	n := len(pts)
	if n < 2 {
		p.reject(ReasonEmpty, 0)
		return
	}
	// Speed per point: the file's, else from the step to the previous point (the first takes the second's).
	for i := range pts {
		if !math.IsNaN(pts[i].speed) {
			continue
		}
		a, b := i-1, i
		if i == 0 {
			a, b = 0, 1
		}
		hours := pts[b].t.Sub(pts[a].t).Hours()
		if hours <= 0 {
			pts[i].speed = 0
			continue
		}
		pts[i].speed = haversineMi(pts[a].lat, pts[a].lon, pts[b].lat, pts[b].lon) / hours
	}
	var segs []seg
	for i := 0; i < n; i++ {
		m := pts[i].speed >= movingMph
		if k := len(segs); k > 0 && segs[k-1].moving == m {
			segs[k-1].to = i
			continue
		}
		segs = append(segs, seg{from: i, to: i, moving: m})
	}
	for i := range segs {
		s := &segs[i]
		if !s.moving && pts[s.to].t.Sub(pts[s.from].t) >= chargeMinDur && pts[s.to].soc-pts[s.from].soc >= chargeMinGain {
			s.charge = true
		}
	}
	// Merge the non-charge segments between charges into groups; a group that moved is a drive, one that never did is parked.
	i := 0
	for i < len(segs) {
		if segs[i].charge {
			p.charge(pts[segs[i].from : segs[i].to+1])
			i++
			continue
		}
		j, moved := i, false
		for j < len(segs) && !segs[j].charge {
			moved = moved || segs[j].moving
			j++
		}
		from, to := segs[i].from, segs[j-1].to
		if !moved {
			p.reject(ReasonParked, 0)
		} else if to-from < 1 {
			p.reject(ReasonEmpty, 0)
		} else {
			p.drive(pts[from : to+1])
		}
		i = j
	}
}

func (p *parser) drive(pts []point) {
	if len(p.drives) >= MaxSessions {
		p.overflow = true
		return
	}
	a, b := pts[0], pts[len(pts)-1]
	d := Drive{
		Source: p.f.Source, StartedAt: a.t, EndedAt: b.t, StartSoCPct: a.soc, EndSoCPct: b.soc,
		StartLat: a.lat, StartLon: a.lon, EndLat: b.lat, EndLon: b.lon,
	}
	var path float64
	line := make([][2]float64, 0, len(pts))
	for i, q := range pts {
		if i > 0 {
			path += haversineMi(pts[i-1].lat, pts[i-1].lon, q.lat, q.lon)
		}
		if q.speed > d.MaxSpeedMph {
			d.MaxSpeedMph = q.speed
		}
		line = append(line, [2]float64{q.lat, q.lon})
	}
	d.DistanceMi = path
	if !math.IsNaN(a.odo) && !math.IsNaN(b.odo) && b.odo > a.odo {
		d.StartOdometerMi, d.EndOdometerMi = a.odo, b.odo
		d.DistanceMi = b.odo - a.odo
	}
	d.RoutePolyline = polyline5(thin(line, maxLinePoints))
	p.deriveDrive(&d)
	if r := checkDrive(&d, p.o.Now); r != "" {
		p.reject(r, len(pts))
		return
	}
	p.drives = append(p.drives, d)
}

func (p *parser) charge(pts []point) {
	if len(p.charges) >= MaxSessions {
		p.overflow = true
		return
	}
	a, b := pts[0], pts[len(pts)-1]
	c := Charge{
		Source: p.f.Source, StartedAt: a.t, EndedAt: b.t, StartSoCPct: a.soc, EndSoCPct: b.soc, Lat: a.lat, Lon: a.lon,
		EnergyBasis: "battery", EnergyEstimated: true,
	}
	p.deriveCharge(&c)
	if c.EnergyAddedKWh == 0 {
		c.EnergyEstimated, c.EnergyBasis = false, "unknown" // no pack: SoC alone says nothing about energy
	}
	if r := checkCharge(&c, p.o.Now); r != "" {
		p.reject(r, len(pts))
		return
	}
	p.charges = append(p.charges, c)
}

// thin keeps at most max points, evenly spaced, always the first and the last.
func thin(pts [][2]float64, max int) [][2]float64 {
	if len(pts) <= max {
		return pts
	}
	out := make([][2]float64, 0, max)
	step := float64(len(pts)-1) / float64(max-1)
	for i := 0; i < max; i++ {
		out = append(out, pts[int(math.Round(float64(i)*step))])
	}
	return out
}

// peeked holds the first rows so a decision that needs a look ahead (the SoC scale) can be made before they are processed.
type peeked struct {
	t    Table
	rows [][]string
	i    int
}

func (k *peeked) fill(n int) error {
	for len(k.rows) < n {
		row, err := k.t.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		k.rows = append(k.rows, append([]string(nil), row...))
	}
	return nil
}

func (k *peeked) Next() ([]string, error) {
	if k.i < len(k.rows) {
		k.i++
		return k.rows[k.i-1], nil
	}
	k.rows = nil
	return k.t.Next()
}
