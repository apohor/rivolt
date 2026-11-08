package stretched

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func base() Row {
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	return Row{ID: uuid.New(), UserID: uuid.New(), VehicleID: uuid.New(), Source: "live", FinalState: "charging_user_stopped",
		StartedAt: t0, EndedAt: t0.Add(20 * time.Hour), UpdatedAt: t0.Add(20 * time.Hour),
		FirstDrive: t0.Add(3 * time.Hour), LastPlugged: t0.Add(2 * time.Hour)}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		mut  func(*Row)
		capd bool
		flag bool
	}{
		{"stretched, last plugged before the drive", func(r *Row) {}, true, false},
		{"plugged frames after a drive: flagged, not capped", func(r *Row) { r.PluggedAfterDrive = true }, false, true},
		{"not live", func(r *Row) { r.Source = "import" }, false, false},
		{"open", func(r *Row) { r.FinalState = "charging_active" }, false, false},
		{"written in the last hour", func(r *Row) { r.UpdatedAt = now.Add(-30 * time.Minute) }, false, false},
		{"user price", func(r *Row) { r.CostSource = "user" }, false, false},
		{"restamp", func(r *Row) { r.HasRestamp = true }, false, false},
		{"receipt", func(r *Row) { r.HasReceipt = true }, false, false},
		{"edited", func(r *Row) { r.Edited = true }, false, false},
		{"no drive", func(r *Row) { r.FirstDrive = time.Time{} }, false, false},
		{"no plugged frame", func(r *Row) { r.LastPlugged = time.Time{} }, false, false},
		{"not stretched: ends 30 min after the last plugged frame", func(r *Row) { r.EndedAt = r.LastPlugged.Add(30 * time.Minute) }, false, false},
		{"cap before the start", func(r *Row) { r.LastPlugged = r.StartedAt.Add(-time.Minute) }, false, false},
		{"cap at the end", func(r *Row) { r.LastPlugged = r.EndedAt }, false, false},
	} {
		r := base()
		c.mut(&r)
		v := Classify(r, now, Options{})
		if got := !v.CapTo.IsZero(); got != c.capd || v.Flag != c.flag {
			t.Errorf("%s: cap=%v flag=%v reason=%q, want cap=%v flag=%v", c.name, got, v.Flag, v.Reason, c.capd, c.flag)
		}
		if !v.CapTo.IsZero() && (!v.CapTo.Before(r.EndedAt) || v.CapTo.Before(r.StartedAt)) {
			t.Errorf("%s: cap %v outside [%v, %v)", c.name, v.CapTo, r.StartedAt, r.EndedAt)
		}
	}
}

func TestFlaggerIsSwappable(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	r := base()
	if p := MakePlan([]Row{r}, now, Options{Flag: func(Row) string { return "flagged: a rise" }}); len(p.Cap) != 0 || len(p.Flagged) != 1 {
		t.Fatalf("a custom flagger must flag: cap=%d flagged=%d", len(p.Cap), len(p.Flagged))
	}
	if p := MakePlan([]Row{r}, now, Options{Flag: func(Row) string { return "" }}); len(p.Cap) != 1 {
		t.Fatalf("a flagger that finds nothing must cap: cap=%d", len(p.Cap))
	}
}

func TestDigestCoversTheCapPoint(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a := base()
	b := a
	b.LastPlugged = a.LastPlugged.Add(time.Minute)
	if MakePlan([]Row{a}, now, Options{}).Digest() == MakePlan([]Row{b}, now, Options{}).Digest() {
		t.Fatal("a moved cap point must change the digest")
	}
	if len(MakePlan(nil, now, Options{}).Digest()) != 64 {
		t.Fatal("digest of nothing is not a sha256")
	}
}

// A row with plugged frames after a drive is split when its parked segments hold the energy the row stored.
func TestSplit(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	seg := Segment{Start: t0.Add(5 * time.Hour), FirstActive: t0.Add(5*time.Hour + 5*time.Minute), End: t0.Add(8 * time.Hour),
		SoCStart: 40, SoCEnd: 60, IntegralKWh: 18}
	mk := func() Row {
		r := base()
		r.PluggedAfterDrive = true
		r.LastPluggedBeforeDrive = r.LastPlugged
		r.PackKWh, r.EnergyKWh, r.RivianVehicleID = 91, 25.3, "rv-fake-1"
		r.StartSoC, r.EndSoC = 47, 70
		r.CostSource, r.PricePerKWh = "home_rate", 0.25
		r.Frames = []Frame{{At: t0.Add(time.Hour), SoC: 47, PowerKW: 7, ChargingState: "charging_active"}, {At: t0.Add(65 * time.Minute), SoC: 51, PowerKW: 7, ChargingState: "charging_active"}, {At: t0.Add(2 * time.Hour), SoC: 55}}
		r.Taken = map[string]bool{}
		return r
	}
	defer func() { detectSegments = DetectSegments }()
	detectSegments = func([]Frame, time.Time) []Segment { return []Segment{seg} }

	r := mk()
	p := MakePlan([]Row{r}, now, Options{Split: true})
	sp := p.Splits[r.ID]
	if sp == nil || len(p.Cap) != 1 || len(sp.News) != 1 {
		t.Fatalf("want one split with one new row, got cap=%d splits=%d skipped=%v", len(p.Cap), len(p.Splits), p.Skipped)
	}
	if got := sp.OrigKWh + sp.News[0].KWh; got != r.EnergyKWh {
		t.Errorf("energy not conserved: %v + %v != %v", sp.OrigKWh, sp.News[0].KWh, r.EnergyKWh)
	}
	if sp.News[0].ExternalID != "live_rv-fake-1_c_"+itoa(seg.FirstActive.Unix()) {
		t.Errorf("external id = %q", sp.News[0].ExternalID)
	}
	if sp.OrigCost == nil || *sp.OrigCost < 1.82 || *sp.OrigCost > 1.83 { // 0.25 * (25.3-18)
		t.Errorf("auto-priced original should be re-priced by energy share, got %v", sp.OrigCost)
	}
	if !p.CapTo[r.ID].Equal(r.LastPlugged) || sp.OrigEndSoC != 55 {
		t.Errorf("cap %v end soc %v", p.CapTo[r.ID], sp.OrigEndSoC)
	}
	if sp.OrigFinalState != "charging_complete" || sp.News[0].FinalState != r.FinalState {
		t.Errorf("final states: original %q, last new row %q (want complete / the stretched row's %q)", sp.OrigFinalState, sp.News[0].FinalState, r.FinalState)
	}
	if sp.OrigActiveSec != 300 || sp.OrigMaxKW != 7 || sp.OrigAvgKW != sp.OrigKWh/(300.0/3600) {
		t.Errorf("original figures from its own window: active %v max %v avg %v", sp.OrigActiveSec, sp.OrigMaxKW, sp.OrigAvgKW)
	}
	if n, c, kwh := p.SplitTotals(); n != 1 || c != 1 || kwh != 18 {
		t.Errorf("totals %d %d %v", n, c, kwh)
	}
	// Split off: the same row is only flagged.
	if q := MakePlan([]Row{r}, now, Options{}); len(q.Cap) != 0 || len(q.Flagged) != 1 {
		t.Errorf("without Split the row must be flagged: %v", q.Skipped)
	}
	for _, c := range []struct {
		name string
		mut  func(*Row)
	}{
		{"energy figures disagree", func(r *Row) { r.PackKWh = 20 }},
		{"the original's remainder disagrees with its own SoC gain", func(r *Row) { r.EnergyKWh = 33 }},
		{"remainder would go negative", func(r *Row) { r.EnergyKWh = 10 }},
		{"external id collision", func(r *Row) { r.Taken[liveSessionID(r.RivianVehicleID, seg.FirstActive)] = true }},
		{"no plugged frame before the drive", func(r *Row) { r.LastPluggedBeforeDrive = time.Time{} }},
		{"no frames", func(r *Row) { r.Frames = nil }},
	} {
		r := mk()
		c.mut(&r)
		if q := MakePlan([]Row{r}, now, Options{Split: true}); len(q.Cap) != 0 || len(q.Flagged) != 1 {
			t.Errorf("%s: want flagged, got cap=%d skipped=%v", c.name, len(q.Cap), q.Skipped)
		}
	}
	// A user-priced original keeps its price.
	r = mk()
	r.CostSource = "user"
	if q := MakePlan([]Row{r}, now, Options{Split: true}); len(q.Cap) != 0 {
		t.Error("a user price is refused before any split")
	}
	// Two segments, and the digest covers each new row.
	detectSegments = func([]Frame, time.Time) []Segment {
		s2 := seg
		s2.FirstActive, s2.End = t0.Add(10*time.Hour), t0.Add(12*time.Hour)
		s2.IntegralKWh, s2.SoCStart, s2.SoCEnd = 9, 30, 40
		return []Segment{seg, s2}
	}
	r = mk()
	r.EnergyKWh = 25.3 + 9
	q := MakePlan([]Row{r}, now, Options{Split: true})
	if sp := q.Splits[r.ID]; sp == nil || len(sp.News) != 2 || sp.News[0].FinalState != "charging_complete" || sp.News[1].FinalState != r.FinalState {
		t.Fatalf("two segments: %+v", sp)
	}
	d1 := q.Digest()
	q.Splits[r.ID].News[1].KWh = 8.9
	if q.Digest() == d1 {
		t.Error("the digest must cover each new row's energy")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
