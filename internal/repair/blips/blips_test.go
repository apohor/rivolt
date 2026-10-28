package blips

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func f(v float64) *float64 { return &v }

func base() Row {
	t0 := time.Date(2026, 10, 28, 7, 56, 59, 0, time.UTC)
	return Row{ID: uuid.New(), UserID: uuid.New(), VehicleID: uuid.New(), Source: "live", FinalState: "charging_complete",
		StartedAt: t0, EndedAt: t0.Add(1600 * time.Millisecond), StartSoC: f(80.1), EndSoC: f(80.1)}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*Row)
		del  bool
	}{
		{"the wake blip", func(r *Row) {}, true},
		{"cached energy and a sticky peak ride along: the energy is not consulted", func(r *Row) {}, true},
		{"19 s", func(r *Row) { r.EndedAt = r.StartedAt.Add(19 * time.Second) }, true},
		{"20 s", func(r *Row) { r.EndedAt = r.StartedAt.Add(20 * time.Second) }, false},
		{"moved 0.4", func(r *Row) { r.EndSoC = f(80.5) }, true},
		{"moved 0.5", func(r *Row) { r.EndSoC = f(80.6) }, false},
		{"dropped 0.6", func(r *Row) { r.EndSoC = f(79.5) }, false},
		{"no soc", func(r *Row) { r.EndSoC = nil }, false},
		{"not live", func(r *Row) { r.Source = "import" }, false},
		{"open: charging_active", func(r *Row) { r.FinalState = "charging_active" }, false},
		{"open: charging_ready", func(r *Row) { r.FinalState = "charging_ready" }, false},
		{"charging_scheduled is not open", func(r *Row) { r.FinalState = "charging_scheduled" }, true},
		{"abandoned", func(r *Row) { r.FinalState = "abandoned" }, true},
		{"user price", func(r *Row) { r.CostSource = "user" }, false},
		{"restamp", func(r *Row) { r.HasRestamp = true }, false},
		{"receipt", func(r *Row) { r.HasReceipt = true }, false},
		{"user override, note or tag", func(r *Row) { r.Edited = true }, false},
		{"a pack-health sample is backed up, not a reason to keep it", func(r *Row) { r.HasPack = true }, true},
	} {
		r := base()
		c.mut(&r)
		if got := Classify(r); got.Delete != c.del {
			t.Errorf("%s: delete=%v reason=%q, want %v", c.name, got.Delete, got.Reason, c.del)
		}
	}
}

func TestDigestIsOrderIndependentAndContentSensitive(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	if Digest([]uuid.UUID{a, b, c}) != Digest([]uuid.UUID{c, a, b}) {
		t.Fatal("the digest depends on the order")
	}
	if Digest([]uuid.UUID{a, b}) == Digest([]uuid.UUID{a, c}) || Digest([]uuid.UUID{a}) == Digest([]uuid.UUID{a, b}) {
		t.Fatal("the digest ignores a difference")
	}
	if len(Digest(nil)) != 64 {
		t.Fatalf("digest of nothing = %q", Digest(nil))
	}
}

func TestPlanCounts(t *testing.T) {
	v1, v2 := uuid.New(), uuid.New()
	mk := func(v uuid.UUID, month time.Month, mut func(*Row)) Row {
		r := base()
		r.VehicleID = v
		r.StartedAt = time.Date(2026, month, 3, 10, 0, 0, 0, time.UTC)
		r.EndedAt = r.StartedAt.Add(2 * time.Second)
		if mut != nil {
			mut(&r)
		}
		return r
	}
	p := MakePlan([]Row{
		mk(v1, 10, nil), mk(v1, 10, nil), mk(v1, 9, nil), mk(v2, 10, nil),
		mk(v2, 10, func(r *Row) { r.CostSource = "user" }),
		mk(v2, 10, func(r *Row) { r.FinalState = "charging_active" }),
	})
	if len(p.Delete) != 4 || p.ByVehicle()[v1] != 3 || p.ByVehicle()[v2] != 1 || p.ByMonth()["2026-10"] != 3 || p.ByMonth()["2026-09"] != 1 {
		t.Fatalf("plan: %d to delete, by vehicle %v, by month %v", len(p.Delete), p.ByVehicle(), p.ByMonth())
	}
	if p.Skipped["carries a user-entered price"] != 1 || p.Skipped["open: final_state charging_active"] != 1 {
		t.Fatalf("skipped: %v", p.Skipped)
	}
}
