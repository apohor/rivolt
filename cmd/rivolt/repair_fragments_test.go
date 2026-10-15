package main

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/repair"
)

func TestFragmentsBackupCSVHasEveryTouchedRowAndThePlan(t *testing.T) {
	u, v := uuid.New(), uuid.New()
	f := func(x float64) *float64 { return &x }
	t0 := time.Date(2026, 10, 10, 20, 15, 0, 0, time.UTC)
	mk := func(i int, kwh float64, state string) repair.Piece {
		st := t0.Add(time.Duration(i) * 3 * time.Minute)
		return repair.Piece{ID: uuid.New(), ExternalID: "live_c_" + string(rune('a'+i)), UserID: u, Username: "owner", VehicleID: v,
			RivianID: "rv-1", StartedAt: st, EndedAt: st.Add(3 * time.Minute), EnergyKWh: f(kwh), MaxPowerKW: f(200),
			ActiveSeconds: f(180), Cost: f(kwh * 0.1), FinalState: state, Source: "live"}
	}
	g := repair.Group{Pieces: []repair.Piece{mk(0, 5, "abandoned"), mk(1, 20, "abandoned"), mk(2, 41, "charging_complete")}}
	var buf bytes.Buffer
	if err := writeFragmentsCSV(&buf, []repair.Group{g}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows = %d (%v), want header + 3", len(rows), err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	removed, keeper := 0, 0
	for _, r := range rows[1:] {
		switch r[col["role"]] {
		case "removed":
			removed++
			if r[col["planned_energy_kwh"]] != "" {
				t.Errorf("removed row has a plan: %v", r)
			}
		case "keeper":
			keeper++
			if r[col["planned_energy_kwh"]] != "41" || r[col["planned_active_seconds"]] != "540" ||
				r[col["planned_started_at"]] != t0.Format(time.RFC3339) {
				t.Errorf("keeper plan wrong: %v", r)
			}
		}
		if r[col["id"]] == "" || r[col["energy_added_kwh"]] == "" {
			t.Errorf("row lacks the values needed to restore it: %v", r)
		}
	}
	if removed != 2 || keeper != 1 {
		t.Fatalf("roles: removed %d keeper %d", removed, keeper)
	}
}
