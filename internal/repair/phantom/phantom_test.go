package phantom

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func f(v float64) *float64 { return &v }

func base() Row {
	return Row{ID: uuid.New(), Source: "live", EnergyKWh: 36.8, StartSoC: f(80), EndSoC: f(80), PackKWh: f(135), PowerKWh: 0}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Row)
		del  bool
	}{
		{"phantom", func(r *Row) {}, true},
		{"soc fell", func(r *Row) { r.EndSoC = f(79.9) }, true},
		{"not live", func(r *Row) { r.Source = "import" }, false},
		{"user price", func(r *Row) { r.CostSource = "user" }, false},
		{"restamp", func(r *Row) { r.HasRestamp = true }, false},
		{"under 5 kWh", func(r *Row) { r.EnergyKWh = 4.9 }, false},
		{"exactly 5 kWh", func(r *Row) { r.EnergyKWh = 5 }, true},
		{"no soc", func(r *Row) { r.EndSoC = nil }, false},
		{"soc accounts", func(r *Row) { r.EndSoC = f(90) }, false},
		{"power accounts", func(r *Row) { r.PowerKWh = 10 }, false},
		{"little power", func(r *Row) { r.PowerKWh = 0.01 }, true},
		{"no pack, soc rose", func(r *Row) { r.PackKWh = nil; r.EndSoC = f(81) }, false},
		{"no pack, flat", func(r *Row) { r.PackKWh = nil }, true},
	}
	for _, c := range cases {
		r := base()
		c.mut(&r)
		if got := Classify(r); got.Delete != c.del {
			t.Errorf("%s: delete=%v reason=%q, want %v", c.name, got.Delete, got.Reason, c.del)
		}
	}
}

func TestSummarise(t *testing.T) {
	a, b, c := base(), base(), base()
	a.StartedAt = time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	b.StartedAt = time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	c.StartedAt = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a.Cost, b.Cost = f(5), f(2)
	v := map[uuid.UUID]Verdict{a.ID: {Delete: true}, b.ID: {Delete: true}, c.ID: {}}
	got := Summarise([]Row{a, b, c}, v)
	if len(got) != 1 || got[0].Month != "2026-07" || got[0].Rows != 2 || got[0].KWh != 73.6 || got[0].Cost != 7 {
		t.Fatalf("%+v", got)
	}
}
