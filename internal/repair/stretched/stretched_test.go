package stretched

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
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

// The pricing hook sees the new row's own window and position, its result lands on the new row, and the digest covers it.
func TestSplitPricingHook(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	seg := Segment{Start: t0.Add(5 * time.Hour), FirstActive: t0.Add(5*time.Hour + 5*time.Minute), End: t0.Add(8 * time.Hour), SoCStart: 40, SoCEnd: 60, IntegralKWh: 18}
	r := base()
	r.PluggedAfterDrive = true
	r.LastPluggedBeforeDrive = r.LastPlugged
	r.PackKWh, r.EnergyKWh, r.RivianVehicleID = 91, 25.3, "rv-fake-1"
	r.StartSoC, r.EndSoC = 47, 70
	r.Frames = []Frame{{At: t0.Add(time.Hour), SoC: 47, PowerKW: 7, ChargingState: "charging_active"}, {At: t0.Add(65 * time.Minute), SoC: 51, PowerKW: 7, ChargingState: "charging_active"}, {At: t0.Add(2 * time.Hour), SoC: 55}, {At: seg.FirstActive, SoC: 40, PowerKW: 7, ChargingState: "charging_active"}, {At: seg.FirstActive.Add(5 * time.Minute), SoC: 41, PowerKW: 7, ChargingState: "charging_active"}}
	r.Locs = []Loc{{At: seg.FirstActive, Lat: 1.5, Lon: 2.5}}
	r.Taken = map[string]bool{}
	defer func() { detectSegments = DetectSegments }()
	detectSegments = func([]Frame, time.Time) []Segment { return []Segment{seg} }

	var seen charges.Charge
	var seenUser uuid.UUID
	price := func(rate float64) PriceFn {
		return func(uid uuid.UUID, c *charges.Charge) {
			seen, seenUser = *c, uid
			c.PricePerKWh, c.Currency, c.CostSource, c.Cost, c.Place = rate, "USD", "home_rate", rate*c.EnergyAddedKWh, "home"
		}
	}
	p := MakePlan([]Row{r}, now, Options{Split: true, Price: price(0.25)})
	n := p.Splits[r.ID].News[0]
	if seenUser != r.UserID || seen.Lat != 1.5 || seen.Lon != 2.5 || seen.EnergyAddedKWh != 18 || !seen.StartedAt.Equal(n.Start) || seen.MaxPowerKW != 7 {
		t.Errorf("the hook saw %+v for user %v", seen, seenUser)
	}
	if n.CostSource != "home_rate" || n.PricePerKW != 0.25 || n.Cost != 4.5 || n.Place != "home" {
		t.Errorf("new row not priced: %+v", n)
	}
	if by := p.PricedBySource(); by["home_rate"] != 1 {
		t.Errorf("priced by source %v", by)
	}
	if MakePlan([]Row{r}, now, Options{Split: true, Price: price(0.25)}).Digest() == MakePlan([]Row{r}, now, Options{Split: true, Price: price(0.30)}).Digest() {
		t.Error("a changed rate must change the digest")
	}
	if q := MakePlan([]Row{r}, now, Options{Split: true}); q.Splits[r.ID].News[0].CostSource != "" || q.PricedBySource()["unpriced"] != 1 {
		t.Errorf("without a hook the new row stays unpriced: %+v", q.Splits[r.ID].News[0])
	}
}

// A row whose only charging came after its first drive: the original is kept as a zero-energy abandoned row ended at the
// first drive, the remainder stays stored minus the new rows (conservation exact), and the dry run counts it.
func TestEmptyOriginal(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	seg := Segment{Start: t0.Add(5 * time.Hour), FirstActive: t0.Add(5*time.Hour + 5*time.Minute), End: t0.Add(8 * time.Hour), SoCStart: 40, SoCEnd: 60, IntegralKWh: 18}
	mk := func() Row {
		r := base()
		r.PluggedAfterDrive = true
		r.LastPlugged, r.LastPluggedBeforeDrive = time.Time{}, time.Time{} // nothing plugged before the drive
		r.PackKWh, r.EnergyKWh, r.RivianVehicleID = 91, 18.2, "rv-fake-1"
		r.StartSoC, r.EndSoC, r.Cost, r.PricePerKWh, r.CostSource = 40, 60, 4.55, 0.25, "home_rate"
		r.FirstDrive = t0.Add(3 * time.Hour)
		r.Frames = []Frame{{At: t0.Add(time.Hour), SoC: 40}, {At: t0.Add(2 * time.Hour), SoC: 40.2}, {At: seg.FirstActive, SoC: 40, PowerKW: 7, ChargingState: "charging_active"}, {At: seg.FirstActive.Add(5 * time.Minute), SoC: 41, PowerKW: 7, ChargingState: "charging_active"}}
		r.Taken = map[string]bool{}
		return r
	}
	defer func() { detectSegments = DetectSegments }()
	detectSegments = func([]Frame, time.Time) []Segment { return []Segment{seg} }

	r := mk()
	p := MakePlan([]Row{r}, now, Options{Split: true})
	sp := p.Splits[r.ID]
	if sp == nil || !sp.EmptyOriginal || sp.OrigFinalState != "abandoned" || !p.CapTo[r.ID].Equal(r.FirstDrive) {
		t.Fatalf("want an empty original ended at the first drive, got %+v cap=%v skipped=%v", sp, p.CapTo[r.ID], p.Skipped)
	}
	if got := sp.OrigKWh + sp.News[0].KWh; got < r.EnergyKWh-1e-9 || got > r.EnergyKWh+1e-9 {
		t.Errorf("energy not conserved: %v", got)
	}
	if sp.OrigKWh < 0.19 || sp.OrigKWh > 0.21 {
		t.Errorf("the remainder must stay stored minus new, not be zeroed: %v", sp.OrigKWh)
	}
	if sp.OrigCost == nil || *sp.OrigCost > 0.06 { // 0.25 * 0.2
		t.Errorf("the original goes by energy share: %v", sp.OrigCost)
	}
	if p.EmptyOriginals() != 1 {
		t.Errorf("empty originals = %d", p.EmptyOriginals())
	}
	if o, n := p.CostDelta(); o > -4.4 || n != 0 {
		t.Errorf("cost delta originals %v new %v", o, n)
	}
	// Without --split the row stays skipped, as before.
	if q := MakePlan([]Row{r}, now, Options{}); len(q.Cap) != 0 || q.Skipped["no plugged frame inside"] != 1 {
		t.Errorf("without Split: %v", q.Skipped)
	}
	for _, c := range []struct {
		name string
		mut  func(*Row)
	}{
		{"the SoC rose before the drive", func(r *Row) { r.Frames[1].SoC = 44 }},
		{"the original would keep real energy", func(r *Row) { r.EnergyKWh = 40 }},
		{"no pack size", func(r *Row) { r.PackKWh = 0 }},
	} {
		r := mk()
		c.mut(&r)
		if q := MakePlan([]Row{r}, now, Options{Split: true}); len(q.Cap) != 0 || len(q.Flagged) != 1 {
			t.Errorf("%s: want flagged, got cap=%d skipped=%v", c.name, len(q.Cap), q.Skipped)
		}
	}
}

func TestNewRowTypesAndPerVehicle(t *testing.T) {
	r1, r2 := base(), base()
	r2.VehicleID = r1.VehicleID
	p := Plan{Cap: []Row{r1, r2}, Splits: map[uuid.UUID]*Split{
		r1.ID: {News: []NewRow{{Place: "home", MaxPowerKW: 7}, {Place: "away", MaxPowerKW: 11}, {Place: "", MaxPowerKW: 7}}},
		r2.ID: {News: []NewRow{{Place: "home", MaxPowerKW: 150, KWh: 40, ActiveSeconds: 1440, Start: time.Unix(0, 0), End: time.Unix(1440, 0)}}},
	}}
	if h, a, o, dc := p.NewRowTypes(); h != 1 || a != 1 || o != 1 || dc != 1 {
		t.Errorf("types: home %d away %d other %d dc %d", h, a, o, dc)
	}
	if p.SplitsByVehicle()[r1.VehicleID] != 2 {
		t.Errorf("per vehicle: %v", p.SplitsByVehicle())
	}
}

// Mode A: a row with no plugged frame and no charging after its first drive is capped at the last pre-drive frame where the
// SoC rose (else at the first drive), by the SoC alone.
func TestCapBySoC(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	mk := func() Row {
		r := base()
		r.LastPlugged, r.LastPluggedBeforeDrive = time.Time{}, time.Time{} // nothing plugged
		r.PluggedAfterDrive = false
		r.PackKWh, r.EnergyKWh, r.StartSoC, r.EndSoC = 91, 4.6, 40, 50
		r.FirstDrive = t0.Add(5 * time.Hour)
		r.Frames = []Frame{
			{At: t0.Add(time.Hour), SoC: 40}, {At: t0.Add(90 * time.Minute), SoC: 42}, {At: t0.Add(2 * time.Hour), SoC: 45},
			{At: t0.Add(3 * time.Hour), SoC: 45.05}, {At: t0.Add(4 * time.Hour), SoC: 44.9}, // jitter, not a rise
		}
		return r
	}
	on := Options{CapBySoC: true}
	r := mk()
	p := MakePlan([]Row{r}, now, on)
	if len(p.Cap) != 1 || !p.BySoC[r.ID] || !p.CapTo[r.ID].Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("want a mode-A cap at the last SoC high (2 h), got cap=%d cap-to=%v skipped=%v", len(p.Cap), p.CapTo[r.ID], p.Skipped)
	}
	if n, off := p.SoCModeTotals(); n != 1 || off != 0 {
		t.Errorf("totals %d %d (4.6 kWh stored against 5 points of a 91 kWh pack agree)", n, off)
	}
	// Off by default: the row is skipped exactly as before, and the digest is the plain one.
	if q := MakePlan([]Row{r}, now, Options{}); len(q.Cap) != 0 || q.Skipped["no plugged frame inside"] != 1 {
		t.Errorf("without CapBySoC: %v", q.Skipped)
	}
	// The mode is in the digest.
	if p.Digest() == MakePlan([]Row{r}, now, Options{}).Digest() {
		t.Error("a mode-A cap must change the digest")
	}
	// Stored energy far from the SoC gain: capped, counted as disagreeing.
	d := mk()
	d.EnergyKWh = 50
	if q := MakePlan([]Row{d}, now, on); len(q.Cap) != 1 {
		t.Fatalf("a disagreeing row is still capped: %v", q.Skipped)
	} else if n, off := q.SoCModeTotals(); n != 1 || off != 1 {
		t.Errorf("disagree totals %d %d", n, off)
	}
	for _, c := range []struct {
		name string
		mut  func(*Row)
		cap  time.Time // zero: skipped
	}{
		{"flat SoC: capped at the first drive", func(r *Row) {
			for i := range r.Frames {
				r.Frames[i].SoC = 40
			}
			r.EnergyKWh = 0
		}, t0.Add(5 * time.Hour)},
		{"fewer than two frames: no evidence", func(r *Row) { r.Frames = r.Frames[:1] }, time.Time{}},
		{"charging after the drive: not mode A", func(r *Row) { r.PluggedAfterDrive = true }, time.Time{}},
		{"not stretched: the row ends within an hour of the cap", func(r *Row) { r.EndedAt = t0.Add(2*time.Hour + 30*time.Minute) }, time.Time{}},
		{"a rise only after the drive is ignored", func(r *Row) { r.Frames = append(r.Frames, Frame{At: t0.Add(6 * time.Hour), SoC: 60}) }, t0.Add(2 * time.Hour)},
	} {
		r := mk()
		c.mut(&r)
		q := MakePlan([]Row{r}, now, on)
		switch {
		case c.cap.IsZero() && len(q.Cap) != 0:
			t.Errorf("%s: want skipped, got a cap at %v", c.name, q.CapTo[r.ID])
		case !c.cap.IsZero() && (len(q.Cap) != 1 || !q.CapTo[r.ID].Equal(c.cap)):
			t.Errorf("%s: cap = %v (cap rows %d, skipped %v), want %v", c.name, q.CapTo[r.ID], len(q.Cap), q.Skipped, c.cap)
		}
	}
	// A row that does have a plugged frame is not touched by the mode: same plan and same digest with it on or off.
	pl := base()
	if MakePlan([]Row{pl}, now, Options{}).Digest() != MakePlan([]Row{pl}, now, on).Digest() {
		t.Error("CapBySoC changed the plan of a row with a plugged frame")
	}
}
