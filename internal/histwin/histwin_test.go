package histwin

import (
	"context"
	"testing"
	"time"
)

func TestFreeWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 4, 5, 0, time.UTC)
	w := Free(now)
	// One cutoff for rows and statements: the first of the month twelve back, so the window is November 2026 and the twelve months before.
	want := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	if !w.Since.Equal(want) || !w.MoneyFrom.Equal(want) {
		t.Errorf("since %v money from %v, want both %v", w.Since, w.MoneyFrom, want)
	}
	if w.Days != 384 {
		t.Errorf("days %d, want 384 (2025-11-01 to 2026-11-20)", w.Days)
	}
	// Across a year boundary.
	if w := Free(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)); !w.Since.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("january: since %v, want 2026-01-01", w.Since)
	}
	// A non-UTC "now" is read in UTC.
	if w := Free(time.Date(2026, 12, 1, 0, 30, 0, 0, time.FixedZone("x", 2*3600))); !w.Since.Equal(want) {
		t.Errorf("zone: since %v, want %v (it is still 30 November 22:30 UTC)", w.Since, want)
	}
	// The window never reaches below a year or above a year and a month, so a client can rely on that range for free_days.
	for d := 0; d < 800; d++ {
		if w := Free(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, d)); w.Days < 365 || w.Days > 396 {
			t.Fatalf("day %d: window of %d days", d, w.Days)
		}
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
