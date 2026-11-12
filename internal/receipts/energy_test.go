package receipts

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func f64(v float64) *float64 { return &v }
func bptr(v bool) *bool      { return &v }

// A DC session as in #1885: our feed stopped 41 s before the receipt's end.
func dcCand() EnergyCand {
	end := time.Date(2026, 11, 10, 20, 17, 0, 0, time.UTC)
	return EnergyCand{
		ChargeID: uuid.New(), ReceiptID: uuid.New(), TxnID: "t1", FinalState: "charging_complete",
		ChargeEnd: end, MaxPowerKW: 150, Energy: 26.80, Recorded: 26.80, AvgPower: 94,
		Cost: f64(10.72), CostSource: "observed_estimate", PricePerKWh: 0.40,
		ReceiptStart: end.Add(-17 * time.Minute), ReceiptEnd: end.Add(41 * time.Second),
		ReceiptEnergy: f64(27.42), IsHome: bptr(false), DetailFetched: true,
	}
}

func TestEvaluateEnergyTakesTheReceiptsTail(t *testing.T) {
	c := dcCand()
	out, pct, up := EvaluateEnergy(c)
	if out != OutcomeWouldApply || up == nil {
		t.Fatalf("outcome %q update %v; want would_apply", out, up)
	}
	if pct < 2.2 || pct > 2.4 {
		t.Errorf("delta pct = %.2f, want about 2.31", pct)
	}
	if up.Energy != 27.42 || up.ExpectedNow != 26.80 {
		t.Errorf("update energy %v expected %v", up.Energy, up.ExpectedNow)
	}
	if want := round4(27.42 / (c.ReceiptEnd.Sub(c.ReceiptStart).Hours())); up.AvgPower != want {
		t.Errorf("avg power %v, want receipt energy / receipt duration %v", up.AvgPower, want)
	}
	if up.Cost == nil || *up.Cost != round4(0.40*27.42) {
		t.Errorf("cost %v, want price x energy", up.Cost)
	}
	m := up.Meta
	if m["energy_source"] != SourceReceipt || m["energy_recorded_kwh"] != 26.8 || m["cost_recorded"] != 10.72 || m["avg_power_recorded_kw"] != 94.0 {
		t.Errorf("meta %v", m)
	}
}

func TestEvaluateEnergyRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mod  func(*EnergyCand)
		want string
	}{
		"home charger":     {func(c *EnergyCand) { c.IsHome = bptr(true) }, OutcomeHome},
		"detail missing":   {func(c *EnergyCand) { c.DetailFetched, c.IsHome = false, nil }, OutcomeNoDetail},
		"no receipt kwh":   {func(c *EnergyCand) { c.ReceiptEnergy = nil }, OutcomeNoEnergy},
		"zero receipt kwh": {func(c *EnergyCand) { c.ReceiptEnergy = f64(0) }, OutcomeNoEnergy},
		"nothing recorded": {func(c *EnergyCand) { c.Recorded, c.Energy = 0, 0 }, OutcomeNoEnergy},
		"open charge":      {func(c *EnergyCand) { c.FinalState = "charging_active" }, OutcomeOpen},
		"receipt lower":    {func(c *EnergyCand) { c.ReceiptEnergy = f64(25.0) }, OutcomeBelow},
		"tail too big":     {func(c *EnergyCand) { c.ReceiptEnergy = f64(31.0) }, OutcomeAbove},
		"no power known":   {func(c *EnergyCand) { c.MaxPowerKW = 0; c.ReceiptEnergy = f64(27.42) }, OutcomeAbove},
		"receipt ended before our row": {func(c *EnergyCand) {
			c.ReceiptEnd = c.ChargeEnd.Add(-time.Minute)
			c.ReceiptEnergy = f64(27.42)
		}, OutcomeAbove},
	} {
		c := dcCand()
		tc.mod(&c)
		if out, _, up := EvaluateEnergy(c); out != tc.want || up != nil {
			t.Errorf("%s: outcome %q update %v, want %q and no update", name, out, up, tc.want)
		}
	}
	// Within 1 % below is rounding, not a refusal.
	c := dcCand()
	c.ReceiptEnergy = f64(26.60)
	if out, _, up := EvaluateEnergy(c); out != OutcomeWouldApply || up == nil {
		t.Errorf("0.75%% below: outcome %q", out)
	}
}

func TestEvaluateEnergyKeepsPricesItDoesNotOwn(t *testing.T) {
	for _, src := range []string{"user", "rivian"} {
		c := dcCand()
		c.CostSource = src
		_, _, up := EvaluateEnergy(c)
		if up == nil || up.Cost != nil {
			t.Errorf("cost_source %s: cost should be left alone, got %v", src, up)
		}
	}
	c := dcCand()
	c.PricePerKWh = 0
	if _, _, up := EvaluateEnergy(c); up == nil || up.Cost != nil {
		t.Errorf("no price per kWh: cost should be left alone")
	}
}

// A refresh moves the energy to the receipt's new figure but the first recorded value stays.
func TestEvaluateEnergyRefreshKeepsTheFirstRecorded(t *testing.T) {
	c := dcCand()
	c.FromReceipt, c.Energy, c.Recorded = true, 27.42, 26.80
	c.AvgRecorded, c.CostRecorded = f64(94), f64(10.72)
	c.ReceiptEnergy = f64(27.50)
	out, _, up := EvaluateEnergy(c)
	if out != OutcomeWouldApply || up == nil || up.Energy != 27.50 || up.ExpectedNow != 27.42 {
		t.Fatalf("refresh: %q %+v", out, up)
	}
	if up.Meta["energy_recorded_kwh"] != 26.8 {
		t.Errorf("recorded = %v, want the first original 26.8", up.Meta["energy_recorded_kwh"])
	}
	if _, set := up.Meta["avg_power_recorded_kw"]; set {
		t.Error("avg_power_recorded_kw must not be overwritten")
	}
	if _, set := up.Meta["cost_recorded"]; set {
		t.Error("cost_recorded must not be overwritten")
	}
	// Same receipt energy again: nothing to write.
	c.ReceiptEnergy = f64(27.42)
	if out, _, up := EvaluateEnergy(c); out != OutcomeWouldApply || up != nil {
		t.Errorf("unchanged: %q %v, want would_apply and no update", out, up)
	}
}

type energyFake struct {
	*fakeStore
	cands   []EnergyCand
	written []EnergyUpdate
	since   time.Time
}

func (f *energyFake) EnergyCandidates(_ context.Context, _ uuid.UUID, since time.Time) ([]EnergyCand, error) {
	f.since = since
	return f.cands, nil
}
func (f *energyFake) ApplyEnergy(_ context.Context, _ uuid.UUID, _ EnergyCand, up EnergyUpdate) (bool, error) {
	f.written = append(f.written, up)
	return true, nil
}

func TestSyncerEnergyModes(t *testing.T) {
	for mode, wantWrites := range map[string]int{EnergyOff: 0, "": 0, EnergyObserve: 0, EnergyApply: 1} {
		bad := dcCand()
		bad.IsHome = bptr(true)
		st := &energyFake{fakeStore: &fakeStore{claimOK: true}, cands: []EnergyCand{dcCand(), bad}}
		s := newSyncer(st, fakeRegistry{&fakeAcct{}}, func(uuid.UUID) bool { return true }, nil, nil)
		s.sleep = func(context.Context, time.Duration) {}
		got := map[string]int{}
		s.SetEnergy(mode, func(o string, _ float64) { got[o]++ })
		for i := 0; i < 2; i++ { // the second pass re-reads the same rows
			if err := s.applyEnergy(context.Background(), uuid.New()); err != nil {
				t.Fatal(err)
			}
		}
		if len(st.written) != wantWrites && mode != EnergyApply {
			t.Errorf("mode %q wrote %d, want %d", mode, len(st.written), wantWrites)
		}
		switch mode {
		case EnergyOff, "":
			if len(got) != 0 {
				t.Errorf("mode %q counted %v", mode, got)
			}
		case EnergyObserve:
			if got[OutcomeWouldApply] != 1 || got[OutcomeHome] != 1 || got[OutcomeApplied] != 0 {
				t.Errorf("observe counted %v, want one would_apply and one home (once each)", got)
			}
		case EnergyApply:
			if len(st.written) != 2 || got[OutcomeApplied] != 1 || got[OutcomeHome] != 1 {
				t.Errorf("apply wrote %d counted %v", len(st.written), got)
			}
		}
		if mode != EnergyOff && mode != "" && time.Since(st.since) < energyWindow-time.Minute {
			t.Errorf("window starts %v", st.since)
		}
	}
}
