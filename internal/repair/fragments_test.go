package repair

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fp(v float64) *float64 { return &v }

var (
	userA, vehA, vehB = uuid.New(), uuid.New(), uuid.New()
	t0                = time.Date(2026, 10, 10, 20, 15, 0, 0, time.UTC)
)

type spec struct {
	startSec, durSec float64
	kwh              *float64 // nil = no energy recorded
	socFrom, socTo   float64
	state            string
}

func mk(veh uuid.UUID, s spec) Piece {
	st := t0.Add(time.Duration(s.startSec * float64(time.Second)))
	e := 0.0
	if s.kwh != nil {
		e = *s.kwh
	}
	return Piece{
		ID: uuid.New(), ExternalID: "live_c_" + st.Format("150405"), UserID: userA, VehicleID: veh,
		StartedAt: st, EndedAt: st.Add(time.Duration(s.durSec * float64(time.Second))),
		StartSoC: fp(s.socFrom), EndSoC: fp(s.socTo), EnergyKWh: s.kwh, FinalState: s.state, Source: "live",
		MaxPowerKW: fp(200), ActiveSeconds: fp(s.durSec), Cost: fp(0.12 * e), PackKWh: fp(135),
	}
}

// The 2026-10-10 incident: 11 pieces split at resubscribes (gaps 3, 2,
// 178, 3, 178, 178, 2, 3, 175, 2 s), running energy totals, one piece
// without energy, ~5.4% SoC gained per 3-minute piece.
func incident() []Piece {
	kwh := []*float64{fp(0.1), fp(9.4), fp(19.8), fp(40.1), fp(48.9), nil, fp(67.5), fp(71.6), fp(75.6), fp(81.2), fp(82.4)}
	gaps := []float64{3, 2, 178, 3, 178, 178, 2, 3, 175, 2}
	var out []Piece
	start := 0.0
	for i, e := range kwh {
		state := "abandoned"
		if i == len(kwh)-1 {
			state = "charging_complete"
		}
		from := 21 + float64(i)*5.4
		out = append(out, mk(vehA, spec{startSec: start, durSec: 180, kwh: e, socFrom: from, socTo: from + 5.4, state: state}))
		if i < len(gaps) {
			start += 180 + gaps[i]
		}
	}
	return out
}

func TestFindGroups_IncidentMergesIntoTheLastPieceDespiteResubscribeGaps(t *testing.T) {
	ps := incident()
	gs, sk := FindGroups(ps, 200*time.Second)
	if len(sk) != 0 || len(gs) != 1 {
		t.Fatalf("groups=%d skipped=%d", len(gs), len(sk))
	}
	g := gs[0]
	// The 0.1 kWh crumb is not a running-total claim; everything after
	// it is one session (10 pieces, incl. the no-energy one).
	if len(g.Pieces) != 10 || g.Pieces[0].ID != ps[1].ID {
		t.Fatalf("pieces = %d, first = %v", len(g.Pieces), g.Pieces[0].ExternalID)
	}
	if g.Keeper().ID != ps[10].ID || len(g.Removed()) != 9 {
		t.Fatalf("keeper/removed wrong: %d removed", len(g.Removed()))
	}
	m := g.Plan()
	if m.EnergyKWh == nil || *m.EnergyKWh != 82.4 {
		t.Fatalf("energy = %v", m.EnergyKWh)
	}
	if !m.StartedAt.Equal(ps[1].StartedAt) || !m.EndedAt.Equal(ps[10].EndedAt) || m.FinalState != "charging_complete" {
		t.Fatalf("merged span/state: %+v", m)
	}
	if m.Cost == nil || math.Abs(*m.Cost-0.12*82.4) > 1e-9 {
		t.Fatalf("cost must be the keeper's own: %v", m.Cost)
	}
	// counted today (excluding the crumb): everything but 0.1.
	if math.Abs(m.CountedKWh-496.5) > 1e-9 {
		t.Fatalf("counted = %v", m.CountedKWh)
	}
}

// Legitimate unplug/re-plug pairs whose second piece's energy matches
// its OWN SoC gain are independent sessions (DA's audit examples).
func TestFindGroups_LegitReplugsAreNotMerged(t *testing.T) {
	for name, c := range map[string][2]spec{
		"22.5 then 39.8 (own gain 39.6)": {
			{0, 600, fp(22.5), 20, 37, "abandoned"}, {30, 1800, fp(39.8), 37, 66.3, "charging_complete"}},
		"11.2 then 29.0 (own gain 26.8)": {
			{0, 600, fp(11.2), 30, 38, "abandoned"}, {20, 1500, fp(29.0), 38, 57.9, "charging_complete"}},
		"12.6 then 21.2 (own gain 22.1)": {
			{0, 600, fp(12.6), 40, 49, "abandoned"}, {60, 900, fp(21.2), 49, 65.4, "charging_complete"}},
	} {
		gs, _ := FindGroups([]Piece{mk(vehA, c[0]), mk(vehA, c[1])}, 200*time.Second)
		if len(gs) != 0 {
			t.Errorf("%s: merged a real session", name)
		}
	}
}

func TestFindGroups_StaleDuplicateWithFlatSoCIsMerged(t *testing.T) {
	a := mk(vehB, spec{0, 60, fp(36.8), 65, 65, "abandoned"})
	b := mk(vehB, spec{100, 60, fp(36.8), 65, 65, "charging_complete"})
	gs, _ := FindGroups([]Piece{a, b}, 200*time.Second)
	if len(gs) != 1 || len(gs[0].Pieces) != 2 {
		t.Fatalf("stale duplicate not merged: %d groups", len(gs))
	}
}

func TestFindGroups_RulesThatMustNotMerge(t *testing.T) {
	cum := func(i int) spec { // a clear running-total claim
		return spec{float64(i) * 190, 180, fp(float64(20 * (i + 1))), 30, 35, "abandoned"}
	}
	cases := map[string]func() []Piece{
		"previous piece is complete": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			a.FinalState = "charging_complete"
			return []Piece{a, b}
		},
		"gap over the limit": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			b.StartedAt = a.EndedAt.Add(5 * time.Minute)
			b.EndedAt = b.StartedAt.Add(3 * time.Minute)
			return []Piece{a, b}
		},
		"a drive in between": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			b.DriveBefore = true
			return []Piece{a, b}
		},
		"energy goes down": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			b.EnergyKWh = fp(5)
			return []Piece{a, b}
		},
		"missing SoC cannot verify": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			b.StartSoC = nil
			return []Piece{a, b}
		},
		"different vehicles": func() []Piece {
			return []Piece{mk(vehA, cum(0)), mk(vehB, cum(1))}
		},
		"import is not live": func() []Piece {
			a, b := mk(vehA, cum(0)), mk(vehA, cum(1))
			b.Source = "electrafi_import"
			return []Piece{a, b}
		},
		"lone piece": func() []Piece { return []Piece{mk(vehA, cum(0))} },
	}
	for name, build := range cases {
		if gs, sk := FindGroups(build(), 200*time.Second); len(gs) != 0 || len(sk) != 0 {
			t.Errorf("%s: groups=%d skipped=%d", name, len(gs), len(sk))
		}
	}
}

// A run ending in a no-energy piece keeps the last piece WITH energy,
// with its own cost, and never keeps the null one.
func TestPlan_NullEnergyPieceIsNeverTheKeeper(t *testing.T) {
	a := mk(vehA, spec{0, 180, fp(40), 30, 35, "abandoned"})
	b := mk(vehA, spec{183, 180, fp(61), 35, 40, "abandoned"})
	c := mk(vehA, spec{366, 180, nil, 40, 40, "charging_complete"})
	c.Cost = nil
	gs, _ := FindGroups([]Piece{a, b, c}, 200*time.Second)
	if len(gs) != 1 {
		t.Fatalf("groups = %d", len(gs))
	}
	g := gs[0]
	if g.Keeper().ID != b.ID || len(g.Removed()) != 2 {
		t.Fatalf("keeper = %s, want the last piece with energy", g.Keeper().ExternalID)
	}
	m := g.Plan()
	if m.Cost == nil || *m.Cost != *b.Cost || *m.EnergyKWh != 61 {
		t.Fatalf("keeper must keep its own energy and cost: %+v", m)
	}
	if !m.EndedAt.Equal(c.EndedAt) || m.FinalState != "charging_complete" {
		t.Fatalf("merged end/state should come from the last piece: %+v", m)
	}
}

func TestFindGroups_RefusesToDeleteUserPricesOrRestamps(t *testing.T) {
	build := func(mut func(a *Piece)) []Piece {
		a := mk(vehA, spec{0, 180, fp(20), 30, 35, "abandoned"})
		b := mk(vehA, spec{183, 180, fp(45), 35, 40, "charging_complete"})
		mut(&a)
		return []Piece{a, b}
	}
	gs, sk := FindGroups(build(func(a *Piece) { a.CostSource = "user" }), 200*time.Second)
	if len(gs) != 0 || len(sk) != 1 {
		t.Fatalf("user-priced removed piece: groups=%d skipped=%d", len(gs), len(sk))
	}
	gs, sk = FindGroups(build(func(a *Piece) { a.HasRestamp = true }), 200*time.Second)
	if len(gs) != 0 || len(sk) != 1 {
		t.Fatalf("restamped removed piece: groups=%d skipped=%d", len(gs), len(sk))
	}
	// The same flags on the KEEPER are fine (it survives).
	gs, sk = FindGroups([]Piece{
		mk(vehA, spec{0, 180, fp(20), 30, 35, "abandoned"}),
		func() Piece {
			b := mk(vehA, spec{183, 180, fp(45), 35, 40, "charging_complete"})
			b.CostSource, b.HasRestamp = "user", true
			return b
		}(),
	}, 200*time.Second)
	if len(gs) != 1 || len(sk) != 0 {
		t.Fatalf("flags on the keeper must not block: groups=%d skipped=%d", len(gs), len(sk))
	}
}
