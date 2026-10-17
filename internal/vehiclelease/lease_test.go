package vehiclelease

import (
	"testing"
	"time"
)

var day = func(s string) time.Time { t, _ := time.Parse(dateLayout, s); return t }

// A 36k-mile, 3-year lease, 700 days in, 21,850 used: the pro-rata
// allowance, the 90-day pace and the projected overage follow from it.
func TestComputeProjectsFromTheNinetyDayPace(t *testing.T) {
	terms := Terms{StartDate: "2024-11-03", EndDate: "2027-11-03", StartOdometerMi: 12, AllowedMilesTotal: 36000, OveragePerMile: 0.25, Currency: "USD"}
	now := day("2026-10-04").Add(12 * time.Hour) // 700 days in
	latest := &Reading{Mi: 12 + 21850, At: now.Add(-time.Hour)}
	past := &Reading{Mi: 12 + 21850 - 2700, At: now.Add(-90 * 24 * time.Hour)}
	st, err := Compute(terms, now, latest, past)
	if err != nil {
		t.Fatal(err)
	}
	if st.DaysTotal != 1095 || st.DaysElapsed != 700 || st.DaysLeft != 395 {
		t.Fatalf("days = %d/%d/%d", st.DaysTotal, st.DaysElapsed, st.DaysLeft)
	}
	if st.AllowedToDateMi != 23013.7 || *st.UsedMi != 21850 || *st.AheadBehindMi != 1163.7 {
		t.Errorf("allowed %v used %v ahead %v", st.AllowedToDateMi, *st.UsedMi, *st.AheadBehindMi)
	}
	if st.PaceBasis != "last_90_days" || *st.PaceMiPerDay < 29.9 || *st.PaceMiPerDay > 30.1 {
		t.Errorf("pace %v %q", st.PaceMiPerDay, st.PaceBasis)
	}
	// 21850 + 30 × 395 = 33700: under 36000, so no overage.
	if *st.ProjectedOverMi != 0 || *st.ProjectedOverCost != 0 || *st.ProjectedReturnMi < 33700 {
		t.Errorf("projection %v over %v return %v", *st.ProjectedTotalMi, *st.ProjectedOverMi, *st.ProjectedReturnMi)
	}
	if want := 35.8; *st.MilesPerDayBudget != want { // (36000-21850)/395
		t.Errorf("budget %v, want %v", *st.MilesPerDayBudget, want)
	}
}

func TestComputeOverageAndBudgetZeroWhenOver(t *testing.T) {
	terms := Terms{StartDate: "2025-01-01", EndDate: "2026-01-01", StartOdometerMi: 0, AllowedMilesTotal: 10000, OveragePerMile: 0.5, Currency: "USD"}
	now := day("2025-07-01") // 181 days in, 6000 used = 33 mi/day
	latest := &Reading{Mi: 6000, At: now.Add(-time.Hour)}
	st, _ := Compute(terms, now, latest, nil)
	if st.PaceBasis != "lease_to_date" {
		t.Fatalf("basis %q", st.PaceBasis)
	}
	if *st.ProjectedOverMi <= 0 || *st.ProjectedOverCost < *st.ProjectedOverMi*0.5-0.05 || *st.ProjectedOverCost > *st.ProjectedOverMi*0.5+0.05 {
		t.Errorf("overage %v cost %v", *st.ProjectedOverMi, *st.ProjectedOverCost)
	}
	latest.Mi = 11000
	st, _ = Compute(terms, now, latest, nil)
	if *st.MilesPerDayBudget != 0 {
		t.Errorf("budget over allowance = %v, want 0", *st.MilesPerDayBudget)
	}
}

// No recent odometer: used and every projection are null, never a stale
// number; the date-based figures still show.
func TestComputeStaleOrMissingOdometerIsNull(t *testing.T) {
	terms := Terms{StartDate: "2025-01-01", EndDate: "2028-01-01", AllowedMilesTotal: 36000, Currency: "USD"}
	now := day("2026-01-01")
	for name, latest := range map[string]*Reading{"none": nil, "stale": {Mi: 9000, At: now.Add(-30 * 24 * time.Hour)}} {
		st, _ := Compute(terms, now, latest, nil)
		if st.UsedMi != nil || st.AheadBehindMi != nil || st.ProjectedTotalMi != nil || st.MilesPerDayBudget != nil || st.DaysElapsed != 365 {
			t.Errorf("%s: %+v", name, st)
		}
	}
}

func TestComputeBeforeStartAfterEndAndEarlyLease(t *testing.T) {
	terms := Terms{StartDate: "2026-06-01", EndDate: "2027-06-01", StartOdometerMi: 100, AllowedMilesTotal: 12000, Currency: "USD"}
	now := day("2026-05-01")
	st, _ := Compute(terms, now, &Reading{Mi: 100, At: now}, nil)
	if st.DaysElapsed != 0 || st.AllowedToDateMi != 0 || *st.UsedMi != 0 || st.ProjectedTotalMi != nil {
		t.Errorf("before start: %+v", st)
	}
	now = day("2026-06-05") // 4 days in: too early for a pace
	st, _ = Compute(terms, now, &Reading{Mi: 300, At: now}, nil)
	if st.PaceMiPerDay != nil || st.ProjectedTotalMi != nil || *st.UsedMi != 200 {
		t.Errorf("early: %+v", st)
	}
	now = day("2027-08-01")
	st, _ = Compute(terms, now, &Reading{Mi: 13100, At: now}, nil)
	if st.DaysLeft != 0 || *st.ProjectedTotalMi != 13000 || *st.ProjectedOverMi != 1000 || *st.MilesPerDayBudget != 0 {
		t.Errorf("after end: total %v over %v", *st.ProjectedTotalMi, *st.ProjectedOverMi)
	}
}

func TestValidate(t *testing.T) {
	ok := Terms{StartDate: "2025-01-01", EndDate: "2028-01-01", AllowedMilesTotal: 36000, OveragePerMile: 0.25}
	if err := ok.Validate(); err != nil || ok.Currency != "USD" {
		t.Fatalf("valid terms rejected: %v %q", err, ok.Currency)
	}
	for name, mut := range map[string]func(*Terms){
		"end before start": func(t *Terms) { t.EndDate = "2024-01-01" },
		"bad date":         func(t *Terms) { t.StartDate = "01/02/2025" },
		"zero allowance":   func(t *Terms) { t.AllowedMilesTotal = 0 },
		"negative rate":    func(t *Terms) { t.OveragePerMile = -1 },
		"negative odo":     func(t *Terms) { t.StartOdometerMi = -5 },
		"too long":         func(t *Terms) { t.EndDate = "2045-01-01" },
		"bad currency":     func(t *Terms) { t.Currency = "DOLLARS" },
	} {
		c := ok
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
