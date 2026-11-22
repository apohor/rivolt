package histwin

import (
	"context"
	"testing"
	"time"
)

// noticeRun sets a 6-month window and the notice far in the past for one test, so every free account has 6 months.
func noticeRun(t *testing.T) {
	t.Helper()
	old, oldM := NoticeAt, Months
	Months = 6
	SetNotice(time.Unix(0, 0))
	t.Cleanup(func() { SetNotice(old); Months = oldM })
}

func TestFreeWindow(t *testing.T) {
	noticeRun(t)
	// After the notice has run, every free account has Months, whenever it was created.
	now := time.Date(2027, 1, 20, 15, 4, 5, 0, time.UTC)
	w := Free(now, time.Time{})
	// One cutoff for rows and statements: the first of the month Months back, so the window is January 2027 and the six months before.
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !w.Since.Equal(want) || !w.MoneyFrom.Equal(want) || w.Months != 6 {
		t.Errorf("since %v money from %v months %d, want both %v and 6", w.Since, w.MoneyFrom, w.Months, want)
	}
	if w.Days != 203 {
		t.Errorf("days %d, want 203 (2026-07-01 to 2027-01-20)", w.Days)
	}
	// Across a year boundary.
	if w := Free(time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC), time.Time{}); !w.Since.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("march: since %v, want 2026-09-01", w.Since)
	}
	// A non-UTC "now" is read in UTC.
	if w := Free(time.Date(2027, 2, 1, 0, 30, 0, 0, time.FixedZone("x", 2*3600)), time.Time{}); !w.Since.Equal(want) {
		t.Errorf("zone: since %v, want %v (it is still 31 January 22:30 UTC)", w.Since, want)
	}
	// The window never reaches below half a year or above half a year and a month, so a client can rely on that range for free_days.
	for d := 0; d < 800; d++ {
		if w := Free(EffectiveAt.AddDate(0, 0, d), time.Time{}); w.Days < 181 || w.Days > 214 {
			t.Fatalf("day %d: window of %d days", d, w.Days)
		}
	}
}

// The 30 days' notice (Terms section 11): an account that existed before the notice keeps LegacyMonths until EffectiveAt; one created
// at or after the notice gets Months at once; nothing unknown shows less history.
func TestMonthsForNotice(t *testing.T) {
	SetNotice(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
	t.Cleanup(func() { SetNotice(time.Time{}) })
	before := NoticeAt.Add(-time.Hour)
	for _, c := range []struct {
		name         string
		created, now time.Time
		want         int
	}{
		{"existing, during the notice", before, NoticeAt.Add(24 * time.Hour), LegacyMonths},
		{"existing, last second of the notice", before, EffectiveAt.Add(-time.Second), LegacyMonths},
		{"existing, on the effective date", before, EffectiveAt, Months},
		{"new at the notice", NoticeAt, NoticeAt, Months},
		{"new during the notice", NoticeAt.Add(time.Hour), NoticeAt.Add(2 * time.Hour), Months},
		{"unknown creation time during the notice", time.Time{}, NoticeAt.Add(time.Hour), LegacyMonths},
		{"unknown creation time after it", time.Time{}, EffectiveAt, Months},
		{"existing before the notice went out", before, before, LegacyMonths},
	} {
		if got := MonthsFor(c.created, c.now); got != c.want {
			t.Errorf("%s: %d months, want %d", c.name, got, c.want)
		}
	}
	if !EffectiveAt.Equal(NoticeAt.AddDate(0, 0, 30)) {
		t.Errorf("effective %v is not 30 days after the notice %v", EffectiveAt, NoticeAt)
	}
	w := Free(NoticeAt.Add(time.Hour), before)
	if w.Months != LegacyMonths || !w.Since.Equal(time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("an existing account during the notice: %+v, want 12 months back from November", w)
	}
}

// Unset (the default) is the code shipping inert: every account, old, new or unknown, keeps 12 months; and the setting is parsed strictly.
func TestNoticeUnset(t *testing.T) {
	old := Months
	Months = 6
	t.Cleanup(func() { Months = old })
	SetNotice(time.Time{})
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, created := range []time.Time{{}, now.AddDate(-3, 0, 0), now.Add(-time.Hour)} {
		if got := MonthsFor(created, now); got != LegacyMonths {
			t.Errorf("created %v, notice unset: %d months, want %d", created, got, LegacyMonths)
		}
	}
	if w := Free(now, time.Time{}); w.Months != LegacyMonths || !w.Since.Equal(time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unset: %+v, want 12 months back from January", w)
	}
	t.Cleanup(func() { SetNotice(time.Time{}) })
	if err := ConfigureNotice(" 2026-12-01T00:00:00Z "); err != nil || !NoticeAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || !EffectiveAt.Equal(NoticeAt.AddDate(0, 0, 30)) {
		t.Errorf("Configure: %v, notice %v effective %v", err, NoticeAt, EffectiveAt)
	}
	if err := ConfigureNotice("2026-12-01"); err == nil {
		t.Error("a date without a time was accepted")
	}
	if err := ConfigureNotice(""); err != nil || !NoticeAt.IsZero() || !EffectiveAt.IsZero() {
		t.Errorf("Configure empty: %v, notice %v", err, NoticeAt)
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
	t.Cleanup(func() { SetNotice(time.Time{}) })
	SetNotice(time.Unix(0, 0)) // the notice has run, so the setting applies to everyone
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
	if w := Free(time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), time.Time{}); w.Months != 6 || !w.Since.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("at 6: %+v", w)
	}
}

// A setting of 12 or more takes nothing away, so it needs no notice; a shorter one is inert until the notice is set.
func TestSettingNeedsNoticeOnlyBelowLegacy(t *testing.T) {
	old := Months
	t.Cleanup(func() { Months = old; SetNotice(time.Time{}) })
	now := time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
	SetNotice(time.Time{})
	for m, want := range map[int]int{12: 12, 18: 18, 6: 12, 3: 12} {
		Months = m
		if got := MonthsFor(now.AddDate(-1, 0, 0), now); got != want {
			t.Errorf("setting %d, no notice: %d months, want %d", m, got, want)
		}
	}
}
