package histimport

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// MaxBadRowFraction: a file with more failing rows than this is refused as a wrong mapping, not noise (design §2).
const MaxBadRowFraction = 0.20

// Skip reasons. The first group counts toward MaxBadRowFraction (the row or session is wrong); the second is expected noise.
const (
	ReasonBadNumber    = "bad_number"
	ReasonBadTime      = "bad_time"
	ReasonMissing      = "missing_value"
	ReasonEndBeforeBeg = "ended_before_started"
	ReasonFuture       = "ended_in_future"
	ReasonTooOld       = "before_2021"
	ReasonTooLong      = "too_long"
	ReasonSoCRange     = "soc_out_of_range"
	ReasonDistance     = "bad_distance"
	ReasonSpeed        = "speed_too_high"
	ReasonEnergy       = "energy_out_of_range"
	ReasonPower        = "power_out_of_range"
	ReasonRunTooLong   = "run_too_long"

	ReasonDuplicate = "duplicate_in_file"
	ReasonOtherVeh  = "other_vehicle"
	ReasonParked    = "parked"
	ReasonEmpty     = "empty_session"
)

var noiseReasons = map[string]bool{ReasonDuplicate: true, ReasonOtherVeh: true, ReasonParked: true, ReasonEmpty: true}

// Options are what the user confirmed in the preview. A zero value is "not stated": the parser asks (NeedChoiceError) when the file does not
// state it either.
type Options struct {
	Location     *time.Location // zone of times that carry none
	DistanceUnit string         // "km" or "mi", for headers that state no unit
	EnergyUnit   string         // "kwh" or "wh", for energy and power headers that state none
	PackKWh      float64        // usable pack, for energy derived from SoC; 0 leaves such energy out
	Vehicle      string         // the file's own label of the vehicle to keep, when the file holds several
	Now          time.Time      // for the no-future check; zero means time.Now()
}

// Report says what happened to every row, so the preview can show that new + skipped = rows.
type Report struct {
	Rows      int            // data rows read
	Accepted  int            // sessions produced
	Skipped   map[string]int // sessions or rows left out, by reason (a points file counts points for parse failures and sessions for the rest)
	BadRows   int            // rows behind failing sessions: what MaxBadRowFraction is measured on
	Ambiguous int            // local times inside a DST fall-back hour, kept as the first instant
	// DroppedCoords counts session positions that were off the globe (or 0,0) and were left out; the session itself is kept.
	DroppedCoords int
	// Assumptions the numbers rest on, shown in the preview.
	SoCScale     string // "percent" or "fraction" (file said, or every value was ≤ 1)
	DistanceUnit string
	EnergyUnit   string
	Vehicles     []string // distinct vehicle labels in the file (at most 20)
	First, Last  time.Time
}

// Result is a parsed file.
type Result struct {
	Drives  []Drive
	Charges []Charge
	Report  Report
}

// TooManyBadRowsError refuses a file whose rows mostly fail: its columns do not mean what the format says.
type TooManyBadRowsError struct {
	Bad, Rows int
	Skipped   map[string]int
}

func (e *TooManyBadRowsError) Error() string {
	return fmt.Sprintf("histimport: %d of %d rows failed the checks", e.Bad, e.Rows)
}

// Parse reads a whole table in the given format. Memory is bounded by the sessions it produces, plus one run of points for KindPoints.
func Parse(t Table, f Format, o Options) (*Result, error) {
	cols, err := resolve(t.Header(), f)
	if err != nil {
		return nil, err
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	p := &parser{f: f, o: o, cols: cols, rep: Report{Skipped: map[string]int{}}, vehicles: map[string]bool{}}
	if err := p.units(); err != nil {
		return nil, err
	}
	switch f.Kind {
	case KindPoints:
		err = p.points(t)
	default:
		err = p.sessions(t)
	}
	if err != nil {
		return nil, err
	}
	return p.finish()
}

type parser struct {
	f    Format
	o    Options
	cols map[string]column
	rep  Report

	dist, energy string // resolved units
	socFrac      bool
	socHint      string
	socMax       float64
	overflow     bool // more sessions than MaxSessions

	drives   []Drive
	charges  []Charge
	vehicles map[string]bool
}

// units resolves what the headers do not say: a column's own unit wins, else the user's choice, else ask. Nothing is guessed.
func (p *parser) units() error {
	var need []string
	p.dist, p.energy = p.o.DistanceUnit, p.o.EnergyUnit
	if p.dist != "" && p.dist != "km" && p.dist != "mi" || p.energy != "" && p.energy != "kwh" && p.energy != "wh" {
		return fmt.Errorf("histimport: unknown unit choice")
	}
	for _, c := range p.cols {
		switch c.f.Dim {
		case DimDistance:
			if c.unit != "km" && c.unit != "mi" && p.dist == "" {
				need = append(need, "distance_unit")
			}
		case DimSpeed:
			if c.unit != "kph" && c.unit != "mph" && p.dist == "" {
				need = append(need, "distance_unit")
			}
		case DimEnergy:
			if c.unit != "kwh" && c.unit != "wh" && p.energy == "" {
				need = append(need, "energy_unit")
			}
		case DimPower:
			if c.unit != "kw" && c.unit != "w" && p.energy == "" {
				need = append(need, "energy_unit")
			}
		case DimSoC:
			switch c.unit {
			case "frac":
				p.socHint = "fraction"
			case "pct":
				p.socHint = "percent"
			}
		}
	}
	if len(need) > 0 {
		sort.Strings(need)
		need = dedupeStrings(need)
		return &NeedChoiceError{Choices: need}
	}
	p.rep.DistanceUnit, p.rep.EnergyUnit = p.dist, p.energy
	return nil
}

func dedupeStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// val returns the trimmed cell of a field, or "" when the file has no such column or the row is short.
func (p *parser) val(row []string, name string) string {
	c, ok := p.cols[name]
	if !ok || c.idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[c.idx])
}

// label reads a short text field (a vehicle name, a currency) as a copy cut to maxLabel bytes: a kept string would otherwise pin the whole CSV
// record it was sliced from, and a hostile row can make that a quarter of a megabyte.
func (p *parser) label(row []string, name string) string {
	v := p.val(row, name)
	if len(v) > maxLabel {
		v = v[:maxLabel]
	}
	return strings.Clone(v)
}

func (p *parser) has(name string) bool { _, ok := p.cols[name]; return ok }

// number reads a field as a float. ok is false for an empty cell; err for text that is not a number.
func (p *parser) number(row []string, name string) (float64, bool, error) {
	return num(p.val(row, name))
}

// miles reads a distance in the file's unit as miles.
func (p *parser) miles(row []string, name string) (float64, bool, error) {
	v, ok, err := p.number(row, name)
	if err != nil || !ok {
		return 0, ok, err
	}
	if p.unitOf(name, p.dist) == "km" {
		v *= kmToMi
	}
	return v, true, nil
}

// mph reads a speed as mph. A header that says km/h or mph decides; else the distance unit does.
func (p *parser) mph(row []string, name string) (float64, bool, error) {
	v, ok, err := p.number(row, name)
	if err != nil || !ok {
		return 0, ok, err
	}
	switch p.cols[name].unit {
	case "kph":
		v *= kmToMi
	case "mph":
	default:
		if p.dist == "km" {
			v *= kmToMi
		}
	}
	return v, true, nil
}

// kwh reads an energy as kWh; kw reads a power as kW.
func (p *parser) kwh(row []string, name string) (float64, bool, error) {
	return p.scaled(row, name, "kwh", "wh")
}
func (p *parser) kw(row []string, name string) (float64, bool, error) {
	return p.scaled(row, name, "kw", "w")
}

func (p *parser) scaled(row []string, name, big, small string) (float64, bool, error) {
	v, ok, err := p.number(row, name)
	if err != nil || !ok {
		return 0, ok, err
	}
	u := p.cols[name].unit
	if u != big && u != small {
		u = big
		if p.energy == "wh" {
			u = small
		}
	}
	if u == small {
		v /= 1000
	}
	return v, true, nil
}

func (p *parser) unitOf(name, fallback string) string {
	if u := p.cols[name].unit; u == "km" || u == "mi" {
		return u
	}
	return fallback
}

// when reads a timestamp field.
func (p *parser) when(row []string, name string) (time.Time, error) {
	pt, needLoc, err := parseTime(p.val(row, name), p.o.Location)
	if err != nil {
		return time.Time{}, errBadValue
	}
	if needLoc {
		return time.Time{}, errNeedZone
	}
	if pt.ambigu {
		p.rep.Ambiguous++
	}
	return pt.t, nil
}

var errNeedZone = &NeedChoiceError{Choices: []string{"timezone"}}

// reject counts a skipped session or row. rows is how many file rows stand behind it.
func (p *parser) reject(reason string, rows int) {
	p.rep.Skipped[reason]++
	if !noiseReasons[reason] {
		p.rep.BadRows += rows
	}
}

func (p *parser) noteVehicle(row []string) (skip bool) {
	if !p.has(FVehicle) {
		return false
	}
	v := p.label(row, FVehicle)
	if v == "" {
		// A blank label in a file that names vehicles belongs to none of them once the user has picked one.
		return p.o.Vehicle != ""
	}
	if len(p.vehicles) < 20 {
		p.vehicles[v] = true
	}
	return p.o.Vehicle != "" && v != p.o.Vehicle
}

func (p *parser) finish() (*Result, error) {
	rep := &p.rep
	if rep.Rows > 0 && float64(rep.BadRows) > MaxBadRowFraction*float64(rep.Rows) {
		return nil, &TooManyBadRowsError{Bad: rep.BadRows, Rows: rep.Rows, Skipped: rep.Skipped}
	}
	for v := range p.vehicles {
		rep.Vehicles = append(rep.Vehicles, v)
	}
	sort.Strings(rep.Vehicles)
	if len(rep.Vehicles) > 1 && p.o.Vehicle == "" {
		return nil, &NeedChoiceError{Choices: []string{"vehicle"}, Vehicles: rep.Vehicles}
	}
	p.dedupe()
	sort.Slice(p.drives, func(i, j int) bool { return p.drives[i].StartedAt.Before(p.drives[j].StartedAt) })
	sort.Slice(p.charges, func(i, j int) bool { return p.charges[i].StartedAt.Before(p.charges[j].StartedAt) })
	rep.Accepted = len(p.drives) + len(p.charges)
	for _, d := range p.drives {
		rep.widen(d.StartedAt, d.EndedAt)
	}
	for _, c := range p.charges {
		rep.widen(c.StartedAt, c.EndedAt)
	}
	return &Result{Drives: p.drives, Charges: p.charges, Report: *rep}, nil
}

func (r *Report) widen(a, b time.Time) {
	if r.First.IsZero() || a.Before(r.First) {
		r.First = a
	}
	if b.After(r.Last) {
		r.Last = b
	}
}

// dedupe keeps the first of two sessions of one kind that start in the same second.
func (p *parser) dedupe() {
	seen := map[int64]bool{}
	out := p.drives[:0]
	for _, d := range p.drives {
		if seen[d.StartedAt.Unix()] {
			p.rep.Skipped[ReasonDuplicate]++
			continue
		}
		seen[d.StartedAt.Unix()] = true
		out = append(out, d)
	}
	p.drives = out
	seen = map[int64]bool{}
	outc := p.charges[:0]
	for _, c := range p.charges {
		if seen[c.StartedAt.Unix()] {
			p.rep.Skipped[ReasonDuplicate]++
			continue
		}
		seen[c.StartedAt.Unix()] = true
		outc = append(outc, c)
	}
	p.charges = outc
}

// readAll feeds each row to fn until EOF.
func readAll(t Table, fn func([]string)) error {
	for {
		row, err := t.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		fn(row)
	}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
