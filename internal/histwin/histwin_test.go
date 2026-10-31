package histwin

import (
	"context"
	"testing"
	"time"
)

func TestFreeWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 4, 5, 0, time.UTC)
	w := Free(now)
	if !w.Since.Equal(time.Date(2026, 8, 22, 15, 4, 5, 0, time.UTC)) || w.Days != 90 {
		t.Errorf("since %v days %d", w.Since, w.Days)
	}
	// Current + previous two calendar months: November, October, September.
	if !w.MoneyFrom.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("money from %v, want 2026-09-01", w.MoneyFrom)
	}
	// Across a year boundary.
	if w := Free(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)); !w.MoneyFrom.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("january: money from %v, want 2026-11-01", w.MoneyFrom)
	}
	// A non-UTC "now" is read in UTC.
	if w := Free(time.Date(2026, 12, 1, 0, 30, 0, 0, time.FixedZone("x", 2*3600))); !w.MoneyFrom.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("zone: money from %v, want 2026-09-01 (it is still 30 November 22:30 UTC)", w.MoneyFrom)
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
	wctx := With(ctx, W{Since: since, Days: 90})
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
