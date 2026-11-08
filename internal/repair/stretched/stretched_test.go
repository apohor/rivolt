package stretched

import (
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
		v := Classify(r, now, DefaultFlagger)
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
	if p := MakePlan([]Row{r}, now, func(Row) string { return "flagged: a rise" }); len(p.Cap) != 0 || len(p.Flagged) != 1 {
		t.Fatalf("a custom flagger must flag: cap=%d flagged=%d", len(p.Cap), len(p.Flagged))
	}
	if p := MakePlan([]Row{r}, now, func(Row) string { return "" }); len(p.Cap) != 1 {
		t.Fatalf("a flagger that finds nothing must cap: cap=%d", len(p.Cap))
	}
}

func TestDigestCoversTheCapPoint(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a := base()
	b := a
	b.LastPlugged = a.LastPlugged.Add(time.Minute)
	if MakePlan([]Row{a}, now, nil).Digest() == MakePlan([]Row{b}, now, nil).Digest() {
		t.Fatal("a moved cap point must change the digest")
	}
	if len(MakePlan(nil, now, nil).Digest()) != 64 {
		t.Fatal("digest of nothing is not a sha256")
	}
}
