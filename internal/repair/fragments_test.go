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

func piece(veh uuid.UUID, startMin, durMin float64, kwh *float64, state string) Piece {
	st := t0.Add(time.Duration(startMin * float64(time.Minute)))
	return Piece{
		ID: uuid.New(), ExternalID: "live_c_" + st.Format("150405"), UserID: userA, VehicleID: veh,
		StartedAt: st, EndedAt: st.Add(time.Duration(durMin * float64(time.Minute))),
		EnergyKWh: kwh, FinalState: state, Source: "live", MaxPowerKW: fp(100), ActiveSeconds: fp(durMin * 60),
		Cost: fp(0.12 * func() float64 {
			if kwh == nil {
				return 0
			}
			return *kwh
		}()),
	}
}

// The 2026-10-10 incident shape: pieces split at resubscribes, energy
// is a running total (one piece has none), the last is complete.
func incident() []Piece {
	e := []*float64{fp(0.1), fp(9.4), fp(19.8), fp(40.1), fp(48.9), nil, fp(67.5), fp(71.6), fp(75.6), fp(81.2), fp(82.4)}
	var out []Piece
	for i, kwh := range e {
		state := "abandoned"
		if i == len(e)-1 {
			state = "charging_complete"
		}
		out = append(out, piece(vehA, float64(i)*3, 3, kwh, state))
	}
	return out
}

func TestFindGroups_IncidentIsOneGroupKeepingTheLastPiece(t *testing.T) {
	ps := incident()
	gs := FindGroups(ps, 15*time.Second)
	if len(gs) != 1 || len(gs[0].Pieces) != 11 {
		t.Fatalf("groups = %d (pieces %v)", len(gs), len(gs))
	}
	g := gs[0]
	if g.Keeper().ID != ps[10].ID || len(g.Removed()) != 10 {
		t.Fatalf("keeper/removed wrong")
	}
	m := g.Plan()
	if m.EnergyKWh == nil || *m.EnergyKWh != 82.4 {
		t.Fatalf("energy = %v, want 82.4", m.EnergyKWh)
	}
	if math.Abs(m.CountedKWh-496.6) > 1e-9 {
		t.Fatalf("counted kWh = %v, want 496.6", m.CountedKWh)
	}
	if !m.StartedAt.Equal(ps[0].StartedAt) || m.MaxPowerKW == nil || *m.MaxPowerKW != 100 {
		t.Fatalf("merged start/power: %+v", m)
	}
	if m.ActiveSeconds == nil || *m.ActiveSeconds != 11*180 {
		t.Fatalf("active seconds = %v, want sum of pieces", m.ActiveSeconds)
	}
	if m.Cost == nil || math.Abs(*m.Cost-0.12*82.4) > 1e-9 {
		t.Fatalf("cost = %v, want the keeper's", m.Cost)
	}
}

func TestFindGroups_DoesNotMergeUnrelatedSessions(t *testing.T) {
	var ps []Piece
	// Two genuine sessions an hour apart (a complete one, then another).
	ps = append(ps, piece(vehA, 0, 60, fp(30), "charging_complete"), piece(vehA, 120, 60, fp(25), "charging_complete"))
	// Back to back but the first is complete, not abandoned: two sessions.
	ps = append(ps, piece(vehB, 0, 10, fp(5), "charging_complete"), piece(vehB, 10, 10, fp(7), "charging_complete"))
	// Abandoned then a later real session far outside max-gap.
	ps = append(ps, piece(vehB, 100, 10, fp(5), "abandoned"), piece(vehB, 200, 10, fp(7), "charging_complete"))
	if gs := FindGroups(ps, 15*time.Second); len(gs) != 0 {
		t.Fatalf("merged %d groups that are separate sessions", len(gs))
	}
	// Energy going DOWN means a new session, not a running total.
	ps = []Piece{piece(vehA, 0, 3, fp(40), "abandoned"), piece(vehA, 3, 3, fp(2), "charging_complete")}
	if gs := FindGroups(ps, 15*time.Second); len(gs) != 0 {
		t.Fatal("a drop in energy is a new session")
	}
}

func TestFindGroups_SeparateVehiclesAndNonLiveAreNeverJoined(t *testing.T) {
	a := piece(vehA, 0, 3, fp(10), "abandoned")
	b := piece(vehB, 3, 3, fp(20), "charging_complete")
	if gs := FindGroups([]Piece{a, b}, 15*time.Second); len(gs) != 0 {
		t.Fatal("pieces of different vehicles joined")
	}
	imp := piece(vehA, 3, 3, fp(20), "charging_complete")
	imp.Source = "electrafi_import"
	if gs := FindGroups([]Piece{a, imp}, 15*time.Second); len(gs) != 0 {
		t.Fatal("an import joined a live fragment")
	}
}

func TestPlan_KeeperWithoutEnergyUsesLargestPieceTotal(t *testing.T) {
	ps := []Piece{piece(vehA, 0, 3, fp(20), "abandoned"), piece(vehA, 3, 3, nil, "charging_complete")}
	g := FindGroups(ps, 15*time.Second)
	if len(g) != 1 {
		t.Fatalf("groups = %d", len(g))
	}
	if m := g[0].Plan(); m.EnergyKWh == nil || *m.EnergyKWh != 20 {
		t.Fatalf("energy = %v, want 20", m.EnergyKWh)
	}
}
