package histwin

import (
	"context"
	"testing"
	"time"
)

// newAccount is the creation time of an account after the cutoff set by cutoffRun.
var newAccount = time.Unix(0, 0).Add(time.Hour)

// cutoffRun sets a 6-month window with the cutoff far in the past for one test, so an account of newAccount's age has 6 months.
func cutoffRun(t *testing.T) {
	t.Helper()
	old, oldM := Cutoff, Months
	Months = 6
	SetCutoff(time.Unix(0, 0))
	t.Cleanup(func() { SetCutoff(old); Months = oldM })
}

func TestFreeWindow(t *testing.T) {
	cutoffRun(t)
	// An account created after the cutoff has Months.
	now := time.Date(2027, 1, 20, 15, 4, 5, 0, time.UTC)
	w := Free(now, newAccount)
	// One cutoff for rows and statements: the first of the month Months back, so the window is January 2027 and the six months before.
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !w.Since.Equal(want) || !w.MoneyFrom.Equal(want) || w.Months != 6 {
		t.Errorf("since %v money from %v months %d, want both %v and 6", w.Since, w.MoneyFrom, w.Months, want)
	}
	if w.Days != 203 {
		t.Errorf("days %d, want 203 (2026-07-01 to 2027-01-20)", w.Days)
	}
	// Across a year boundary.
	if w := Free(time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC), newAccount); !w.Since.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("march: since %v, want 2026-09-01", w.Since)
	}
	// A non-UTC "now" is read in UTC.
	if w := Free(time.Date(2027, 2, 1, 0, 30, 0, 0, time.FixedZone("x", 2*3600)), newAccount); !w.Since.Equal(want) {
		t.Errorf("zone: since %v, want %v (it is still 31 January 22:30 UTC)", w.Since, want)
	}
	// The window never reaches below half a year or above half a year and a month, so a client can rely on that range for free_days.
	for d := 0; d < 800; d++ {
		if w := Free(time.Unix(0, 0).AddDate(0, 0, d), newAccount); w.Days < 181 || w.Days > 214 {
			t.Fatalf("day %d: window of %d days", d, w.Days)
		}
	}
}

// The cutoff: an account created before it, or of unknown age, keeps LegacyMonths with no end date; one created at or after it gets Months.
func TestMonthsForCutoff(t *testing.T) {
	old := Months
	Months = 6
	SetCutoff(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
	t.Cleanup(func() { SetCutoff(time.Time{}); Months = old })
	before := Cutoff.Add(-time.Hour)
	for _, c := range []struct {
		name    string
		created time.Time
		want    int
	}{
		{"existing", before, LegacyMonths},
		{"existing, long ago", Cutoff.AddDate(-3, 0, 0), LegacyMonths},
		{"new at the cutoff", Cutoff, Months},
		{"new after it", Cutoff.Add(time.Hour), Months},
		{"unknown creation time", time.Time{}, LegacyMonths},
	} {
		if got := MonthsFor(c.created); got != c.want {
			t.Errorf("%s: %d months, want %d", c.name, got, c.want)
		}
	}
	// A year on, the existing account still has 12 and the new one 6.
	now := Cutoff.AddDate(1, 0, 0)
	if w := Free(now, before); w.Months != LegacyMonths || !w.Since.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("existing a year on: %+v, want 12 months back from November 2027", w)
	}
	if w := Free(now, Cutoff.Add(time.Hour)); w.Months != Months || !w.Since.Equal(time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("new a year on: %+v, want 6 months back from November 2027", w)
	}
	if w := Free(now, time.Time{}); w.Months != LegacyMonths {
		t.Errorf("unknown a year on: %+v, want 12 months", w)
	}
}

// Unset (the default) is the code shipping inert: every account, old, new or unknown, keeps 12 months; and the setting is parsed strictly.
func TestCutoffUnset(t *testing.T) {
	old := Months
	Months = 6
	t.Cleanup(func() { Months = old; SetCutoff(time.Time{}) })
	SetCutoff(time.Time{})
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, created := range []time.Time{{}, now.AddDate(-3, 0, 0), now.Add(-time.Hour)} {
		if got := MonthsFor(created); got != LegacyMonths {
			t.Errorf("created %v, cutoff unset: %d months, want %d", created, got, LegacyMonths)
		}
	}
	if w := Free(now, time.Time{}); w.Months != LegacyMonths || !w.Since.Equal(time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unset: %+v, want 12 months back from January", w)
	}
	if err := ConfigureCutoff(" 2026-12-01T00:00:00Z "); err != nil || !Cutoff.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ConfigureCutoff: %v, cutoff %v", err, Cutoff)
	}
	if err := ConfigureCutoff("2026-12-01"); err == nil {
		t.Error("a date without a time was accepted")
	}
	if err := ConfigureCutoff(""); err != nil || !Cutoff.IsZero() {
		t.Errorf("ConfigureCutoff empty: %v, cutoff %v", err, Cutoff)
	}
}

func TestContextWindow(t *testing.T) {
	ctx := context.Background()
	if _, ok := From(ctx); ok {
		t.Fatal("a window in a bare context")
	}
	if !Allows(ctx, time.Unix(0, 0)) {
		t.Error("no window must allow everything")
	}
	since := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	wctx := With(ctx, W{Since: since, Days: 384})
	if got, ok := Since(wctx); !ok || !got.Equal(since) {
		t.Errorf("Since = %v %v", got, ok)
	}
	if Allows(wctx, since.Add(-time.Second)) || !Allows(wctx, since) || !Allows(wctx, since.Add(time.Hour)) {
		t.Error("Allows: the window start is inclusive, earlier is out")
	}
	if got := Clamp(wctx, since.Add(-24*time.Hour)); !got.Equal(since) {
		t.Errorf("Clamp earlier = %v, want the window start", got)
	}
	if got := Clamp(wctx, time.Time{}); !got.Equal(since) {
		t.Errorf("Clamp zero = %v, want the window start", got)
	}
	if later := since.Add(48 * time.Hour); !Clamp(wctx, later).Equal(later) {
		t.Error("Clamp moved a bound that was already inside the window")
	}
	if got := Clamp(ctx, time.Time{}); !got.IsZero() {
		t.Errorf("Clamp with no window changed a zero bound: %v", got)
	}
	// A W with no Since is not a window (a zero value must not hide everything).
	if _, ok := From(With(ctx, W{})); ok {
		t.Error("a zero W counts as a window")
	}
}

// The one setting: unset is 12, a whole number from 1 to 24 is taken, anything else is refused (the server will not start on it).
func TestConfigure(t *testing.T) {
	t.Cleanup(func() { SetCutoff(time.Time{}) })
	SetCutoff(time.Unix(0, 0))
	t.Cleanup(func() { Months = 12 })
	for raw, want := range map[string]int{"": 12, "  ": 12, "6": 6, " 24 ": 24, "1": 1} {
		if err := Configure(raw); err != nil || Months != want {
			t.Errorf("Configure(%q): err %v, months %d, want %d", raw, err, Months, want)
		}
	}
	for _, raw := range []string{"0", "25", "-3", "six", "6.5"} {
		Months = 12
		if err := Configure(raw); err == nil || Months != 12 {
			t.Errorf("Configure(%q): err %v, months %d, want an error and 12", raw, err, Months)
		}
	}
	if err := Configure("6"); err != nil {
		t.Fatal(err)
	}
	if w := Free(time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), newAccount); w.Months != 6 || !w.Since.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("at 6: %+v", w)
	}
}

// A setting of 12 or more takes nothing away, so it needs no cutoff; a shorter one is inert until the cutoff is set.
func TestSettingNeedsNoticeOnlyBelowLegacy(t *testing.T) {
	old := Months
	t.Cleanup(func() { Months = old; SetCutoff(time.Time{}) })
	now := time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
	SetCutoff(time.Time{})
	for m, want := range map[int]int{12: 12, 18: 18, 6: 12, 3: 12} {
		Months = m
		if got := MonthsFor(now.AddDate(-1, 0, 0)); got != want {
			t.Errorf("setting %d, no cutoff: %d months, want %d", m, got, want)
		}
	}
}

// The 3-month rule: the cutoff moved from November 22 to November 26, 2026, so an account created in between (6 months under the old
// cutoff) keeps 12, and only one created at or after the new cutoff gets 3.
func TestThreeMonthsFromNewCutoff(t *testing.T) {
	old := Months
	Months = 3
	SetCutoff(time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC))
	t.Cleanup(func() { SetCutoff(time.Time{}); Months = old })
	now := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		created time.Time
		want    int
		since   time.Time
	}{
		{"before November 22", time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), 12, time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"between November 22 and the cutoff", time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), 12, time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"just before the cutoff", Cutoff.Add(-time.Second), 12, time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"at the cutoff", Cutoff, 3, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"after the cutoff", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), 3, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	} {
		w := Free(now, c.created)
		if w.Months != c.want || !w.Since.Equal(c.since) {
			t.Errorf("%s: %+v, want %d months from %v", c.name, w, c.want, c.since)
		}
	}
}
