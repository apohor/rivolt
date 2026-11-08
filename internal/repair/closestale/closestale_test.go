package closestale

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func f(v float64) *float64 { return &v }

func base(now time.Time) Row {
	return Row{ID: uuid.New(), UserID: uuid.New(), VehicleID: uuid.New(), FinalState: "charging_ready",
		StartedAt: now.Add(-100 * 24 * time.Hour), EndedAt: now.Add(-99 * 24 * time.Hour), UpdatedAt: now.Add(-99 * 24 * time.Hour),
		StartSoC: f(28), EndSoC: f(78), LastFrameAt: now.Add(-99 * 24 * time.Hour)}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		mut  func(*Row)
		want string // new state, or "" for skipped
	}{
		{"a dormant account's open row, SoC rose", func(r *Row) {}, "charging_complete"},
		{"SoC did not rise", func(r *Row) { r.EndSoC = f(20) }, "abandoned"},
		{"no SoC recorded", func(r *Row) { r.StartSoC = nil }, "abandoned"},
		{"no frame ever", func(r *Row) { r.LastFrameAt = time.Time{} }, "charging_complete"},
		{"charging_active is open too", func(r *Row) { r.FinalState = "charging_active" }, "charging_complete"},
		{"already closed", func(r *Row) { r.FinalState = "charging_complete" }, ""},
		{"written in the last day", func(r *Row) { r.EndedAt = now.Add(-23 * time.Hour) }, ""},
		{"updated in the last day", func(r *Row) { r.UpdatedAt = now.Add(-2 * time.Hour) }, ""},
		{"the vehicle is reporting", func(r *Row) { r.LastFrameAt = now.Add(-2 * time.Hour) }, ""},
		{"user price", func(r *Row) { r.CostSource = "user" }, ""},
		{"restamp", func(r *Row) { r.HasRestamp = true }, ""},
		{"receipt", func(r *Row) { r.HasReceipt = true }, ""},
		{"edited", func(r *Row) { r.Edited = true }, ""},
	} {
		r := base(now)
		c.mut(&r)
		if got := Classify(r, now).NewState; got != c.want {
			t.Errorf("%s: new state %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDigestCoversTheDecisionAndTheRow(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a := base(now)
	b := a
	b.EndSoC = f(20) // a different decision
	c := a
	c.EndedAt = a.EndedAt.Add(-time.Hour) // a row that moved
	da, db, dc := MakePlan([]Row{a}, now).Digest(), MakePlan([]Row{b}, now).Digest(), MakePlan([]Row{c}, now).Digest()
	if da == db || da == dc {
		t.Fatalf("the digest must change with the decision (%v) and with the row's ended_at (%v)", da != db, da != dc)
	}
	if len(MakePlan(nil, now).Digest()) != 64 {
		t.Fatal("digest of nothing is not a sha256")
	}
	p := MakePlan([]Row{a, b}, now)
	if got := p.ByState(); got["charging_complete"] != 1 && got["abandoned"] != 1 && len(p.IDs()) != 2 {
		t.Errorf("by state %v", got)
	}
}
