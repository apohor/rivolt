package rivian

import (
	"encoding/json"
	"os"
	"testing"
)

type etaVectors struct {
	Pick []struct {
		Name       string  `json:"name"`
		TruckMins  float64 `json:"truck_mins"`
		Validity   string  `json:"validity"`
		SoCPct     float64 `json:"soc_pct"`
		LimitPct   float64 `json:"limit_pct"`
		OursMins   float64 `json:"ours_mins"`
		WantMins   float64 `json:"want_mins"`
		WantSource string  `json:"want_source"`
	} `json:"pick"`
	Own []struct {
		Name     string  `json:"name"`
		SoCPct   float64 `json:"soc_pct"`
		LimitPct float64 `json:"limit_pct"`
		PackKWh  float64 `json:"pack_kwh"`
		PowerKW  float64 `json:"power_kw"`
		WantMins float64 `json:"want_mins"`
	} `json:"own"`
}

// The vectors are shared with web/src/lib/chargeEta.ts and ios/Rivolt/Views/ChargeETA.swift.
func TestChargeETAVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/charge_eta_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v etaVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Pick) < 15 || len(v.Own) < 5 {
		t.Fatalf("vectors thinned out: %d pick, %d own", len(v.Pick), len(v.Own))
	}
	for _, c := range v.Pick {
		mins, src := PickChargeETA(c.TruckMins, c.Validity, c.SoCPct, c.LimitPct, c.OursMins)
		if mins != c.WantMins || src != c.WantSource {
			t.Errorf("%s: got %v %q, want %v %q", c.Name, mins, src, c.WantMins, c.WantSource)
		}
	}
	for _, c := range v.Own {
		if got := OwnChargeETAMins(c.SoCPct, c.LimitPct, c.PackKWh, c.PowerKW); got != c.WantMins {
			t.Errorf("%s: got %v, want %v", c.Name, got, c.WantMins)
		}
	}
}

func TestChargeETAFromState(t *testing.T) {
	st := &State{BatteryLevelPct: 50, ChargeTargetPct: 80, BatteryCapacityKWh: 135, ChargerPowerKW: 7.2, TimeToEndMins: 360, ChargingTimeEstimationValidity: "valid"}
	if m, s := ChargeETAFromState(st); m != 360 || s != ETASourceTruck {
		t.Errorf("truck figure: %v %q", m, s)
	}
	st.ChargingTimeEstimationValidity = "invalid"
	if m, s := ChargeETAFromState(st); m != 338 || s != ETASourceEstimate {
		t.Errorf("invalid: %v %q, want our 338", m, s)
	}
	st.TimeToEndMins = 0
	st.ChargingTimeEstimationValidity = ""
	if m, s := ChargeETAFromState(st); m != 338 || s != ETASourceEstimate {
		t.Errorf("no truck figure: %v %q, want our 338 (the Live Activity used to show none)", m, s)
	}
	if m, s := ChargeETAFromState(nil); m != 0 || s != "" {
		t.Errorf("nil: %v %q", m, s)
	}
}

// The iPhone tests read a copy of the vectors; it must not drift from the shared file.
func TestChargeETAVectorsCopyForIOSIsIdentical(t *testing.T) {
	a, err := os.ReadFile("testdata/charge_eta_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../ios/RivoltTests/Fixtures/charge_eta_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("ios/RivoltTests/Fixtures/charge_eta_vectors.json differs from internal/rivian/testdata/charge_eta_vectors.json: copy it over")
	}
}
