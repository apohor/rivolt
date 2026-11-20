package tripshare

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// Receipt is the data behind the on-device trip receipt image (a saved trip drawn as a till receipt by the browser): built field by field from the
// saved plan, like Snapshot, and never stored. Places are areas only (AreaNamer on the point rounded to areaStepDeg), and every figure that
// could narrow the exact start or destination is rounded. The rules are DA's R1-R11 (trip receipt ruling, Nov 20).
type Receipt struct {
	DepartAt string       `json:"depart_at,omitempty"`
	TZ       string       `json:"tz,omitempty"`
	StartSoC float64      `json:"start_soc"`
	Start    string       `json:"start"`
	End      string       `json:"end"`
	Legs     []ReceiptRow `json:"rows"`
	Totals   ReceiptTotal `json:"totals"`
	// Sketch is the route as polyline6 segments cut at the start, the destination, every added stop and home; only when asked for and only when
	// something is left after the cut. Never the planned polyline.
	Sketch []string `json:"sketch,omitempty"`
}

// ReceiptRow is one line: a drive between two places or a charging stop. Names are areas, or a public charger's own name.
type ReceiptRow struct {
	Kind    string  `json:"kind"` // "drive", "charge" (a public charger) or "stop" (a stop the user added, or a charger inside a private place)
	From    string  `json:"from,omitempty"`
	To      string  `json:"to,omitempty"`
	Name    string  `json:"name,omitempty"`
	DistM   int     `json:"dist_m,omitempty"`
	DurS    int     `json:"dur_s,omitempty"`
	FromSoC float64 `json:"from_soc"`
	ToSoC   float64 `json:"to_soc"`
	MaxKW   float64 `json:"max_kw,omitempty"`
	Rounded bool    `json:"rounded,omitempty"`
}

type ReceiptTotal struct {
	DistM      int     `json:"dist_m"`
	DriveS     int     `json:"drive_s"`
	ChargeS    int     `json:"charge_s"`
	Stops      int     `json:"stops"`
	ArriveAt   string  `json:"arrive_at,omitempty"`
	ArrivalSoC float64 `json:"arrival_soc"`
}

// AreaNamer names the place around a point already rounded to the grid ("Pasadena area"). It is given nothing finer than the grid.
type AreaNamer func(ctx context.Context, lat, lon float64) string

// ReceiptInput is Input plus what the receipt needs: whether the sketch is wanted, the distance unit's step, and the namer.
type ReceiptInput struct {
	Input
	Sketch bool
	Metric bool
	Name   AreaNamer
}

// ErrNoLegs is a plan without the per-stop distances the legs are made from (a plan saved before they were recorded).
var ErrNoLegs = errors.New("tripshare: plan has no leg distances")

const (
	roundMi  = 5 * 1609.344
	roundKm  = 5000.0
	roundSec = 300
)

// roundDist rounds metres to 5 mi, or 5 km when metric.
func roundDist(m float64, metric bool) int {
	step := roundMi
	if metric {
		step = roundKm
	}
	return int(math.Round(math.Round(m/step) * step))
}

// roundSecs rounds seconds to 5 minutes.
func roundSecs(s float64) int { return int(math.Round(s/roundSec)) * roundSec }

func roundTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return time.Unix(int64(math.Round(float64(t.Unix())/roundSec))*roundSec, 0).UTC().Format(time.RFC3339)
}

// areaName names a rounded point, once per point.
func areaName(ctx context.Context, n AreaNamer, lat, lon float64) string {
	if n == nil {
		return "an area"
	}
	if s := strings.TrimSpace(n(ctx, roundArea(lat), roundArea(lon))); s != "" {
		return s
	}
	return "an area"
}

// BuildReceipt makes the receipt, or ErrUnsupported / ErrTooShort (as Build) / ErrNoLegs.
func BuildReceipt(ctx context.Context, in ReceiptInput) (*Receipt, error) {
	snap, err := Build(in.Input)
	if err != nil {
		return nil, err
	}
	var p plan
	_ = json.Unmarshal(in.Plan, &p) // Build has parsed it
	rt := p.Routes[0]

	type place struct {
		w       planWaypoint
		name    string // what a row says for this place: an area, or a public charger's own name
		exact   bool   // a public charger away from every private place: its legs may stay exact
		isCharg bool
		label   string // the row for an added stop ("Stop · <area>", "Overnight · <area>")
	}
	var stops []place
	var origin, dest *planWaypoint
	var mids []planWaypoint
	for i := range rt.Waypoints {
		w := rt.Waypoints[i]
		switch {
		case w.Source == "origin" || strings.EqualFold(w.WaypointType, "origin"):
			origin = &rt.Waypoints[i]
		case w.Source == "destination" || strings.EqualFold(w.WaypointType, "destination"):
			dest = &rt.Waypoints[i]
		default:
			mids = append(mids, w)
		}
	}
	if origin == nil || dest == nil || len(mids) != len(snap.Stops) {
		return nil, ErrUnsupported
	}
	r := &Receipt{DepartAt: in.DepartAt, TZ: snap.TZ, StartSoC: origin.DepartureSoC}
	r.Start = areaName(ctx, in.Name, snap.Start.Lat, snap.Start.Lon)
	r.End = areaName(ctx, in.Name, snap.End.Lat, snap.End.Lon)
	stops = append(stops, place{w: *origin, name: r.Start})
	for i, w := range mids {
		s := snap.Stops[i]
		pl := place{w: w}
		if s.Kind == "charger" {
			pl.name, pl.exact, pl.isCharg = w.Name, true, true
		} else {
			pl.name = areaName(ctx, in.Name, s.Lat, s.Lon)
			pl.label = "Stop · " + pl.name
			if w.Source == "overnight" {
				pl.label = "Overnight · " + pl.name
			}
		}
		stops = append(stops, pl)
	}
	stops = append(stops, place{w: *dest, name: r.End})

	if origin.DistanceFromOriginMeters == 0 && dest.DistanceFromOriginMeters == 0 {
		return nil, ErrNoLegs
	}
	prevDepart := origin.DepartureTimeUTC
	if prevDepart == "" {
		prevDepart = in.DepartAt
	}
	for i := 1; i < len(stops); i++ {
		a, b := stops[i-1], stops[i]
		dist := b.w.DistanceFromOriginMeters - a.w.DistanceFromOriginMeters
		dur := secsBetween(prevDepart, b.w.ArrivalTimeUTC)
		exact := a.exact && b.exact
		row := ReceiptRow{Kind: "drive", From: a.name, To: b.name, FromSoC: a.w.DepartureSoC, ToSoC: b.w.ArrivalSoC, Rounded: !exact}
		if exact {
			row.DistM, row.DurS = int(math.Round(dist)), int(dur)
		} else {
			row.DistM, row.DurS = roundDist(dist, in.Metric), roundSecs(dur)
		}
		r.Legs = append(r.Legs, row)
		r.Totals.DriveS += int(dur)
		prevDepart = b.w.DepartureTimeUTC
		switch {
		case i == len(stops)-1:
		case b.isCharg:
			r.Legs = append(r.Legs, ReceiptRow{Kind: "charge", Name: b.name, DurS: b.w.ChargeDurationSec, FromSoC: b.w.ArrivalSoC, ToSoC: b.w.DepartureSoC, MaxKW: b.w.MaxPowerKW})
			r.Totals.ChargeS += b.w.ChargeDurationSec
			r.Totals.Stops++
		default:
			r.Legs = append(r.Legs, ReceiptRow{Kind: "stop", Name: b.label, FromSoC: b.w.ArrivalSoC, ToSoC: b.w.DepartureSoC})
		}
	}
	r.Totals.DistM = roundDist(dest.DistanceFromOriginMeters-origin.DistanceFromOriginMeters, in.Metric)
	r.Totals.DriveS = roundSecs(float64(r.Totals.DriveS))
	r.Totals.ArrivalSoC = rt.ArrivalSoC
	r.Totals.ArriveAt = roundTime(dest.ArrivalTimeUTC)
	if in.Sketch {
		r.Sketch = snap.Route.Segments
	}
	return r, nil
}

func secsBetween(a, b string) float64 {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil || tb.Before(ta) {
		return 0
	}
	return tb.Sub(ta).Seconds()
}
