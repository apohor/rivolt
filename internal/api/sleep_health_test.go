package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/samples"
)

var shNow = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC) // this week = Oct 16-22, baseline = Sep 16-Oct 15

// shDays builds one recorded day (asleep 8 h) per day in [from, to] with the given short sleeps.
func shDays(from, to time.Time, failed func(day time.Time) int) []samples.DayActivity {
	var out []samples.DayActivity
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, samples.DayActivity{Day: d, AsleepH: 8, Sleeps: 4, FailedSleeps: failed(d)})
	}
	return out
}

func day(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

// A steady truck: 1 short sleep a day in the baseline and in the week is normal, not "more".
func TestSleepHealthNormalWeek(t *testing.T) {
	days := shDays(day(9, 16), day(10, 22), func(time.Time) int { return 1 })
	got := buildSleepHealth(days, shNow)
	if got.State != "normal" || got.ThisWeek.Days != 7 || got.BaselineDays != 30 || got.ThisWeek.FailedSleeps != 7 || got.MoreBy != nil {
		t.Errorf("%+v", got)
	}
	if got.UsualFailedSleeps == nil || *got.UsualFailedSleeps != 7 {
		t.Errorf("usual week %v, want 7", got.UsualFailedSleeps)
	}
}

// The usual week is a rate, so a 5-day week is held to a 5-day share of the baseline, not to a 7-day one.
func TestSleepHealthUsualWeekIsARate(t *testing.T) {
	// Baseline 2 short sleeps a day; this week only Oct 18-22 recorded (5 days), 10 short sleeps in all.
	days := shDays(day(9, 16), day(10, 15), func(time.Time) int { return 2 })
	days = append(days, shDays(day(10, 18), day(10, 22), func(time.Time) int { return 2 })...)
	got := buildSleepHealth(days, shNow)
	if got.ThisWeek.Days != 5 || got.UsualFailedSleeps == nil || *got.UsualFailedSleeps != 10 || got.State != "normal" {
		t.Errorf("5-day week at the usual rate: %+v usual=%v", got, got.UsualFailedSleeps)
	}
	// The same 5 days held to a raw 7-day expectation (14) would never read as more; against the rate, 15 in 5 days (5 more, 1.5x) is.
	days2 := shDays(day(9, 16), day(10, 15), func(time.Time) int { return 2 })
	days2 = append(days2, shDays(day(10, 18), day(10, 22), func(d time.Time) int {
		if d.Day() == 22 {
			return 7
		}
		return 2
	})...)
	got = buildSleepHealth(days2, shNow)
	if got.State != "more" || got.MoreBy == nil || *got.MoreBy != 5 {
		t.Errorf("15 vs a usual 10: %+v", got)
	}
}

// Not enough history says so and makes no verdict: under 5 recorded days this week or under 14 in the baseline.
func TestSleepHealthNeedsHistory(t *testing.T) {
	for name, days := range map[string][]samples.DayActivity{
		"nothing":       nil,
		"4-day week":    append(shDays(day(9, 16), day(10, 15), func(time.Time) int { return 1 }), shDays(day(10, 19), day(10, 22), func(time.Time) int { return 20 })...),
		"13-day base":   append(shDays(day(10, 3), day(10, 15), func(time.Time) int { return 1 }), shDays(day(10, 16), day(10, 22), func(time.Time) int { return 20 })...),
		"today ignored": shDays(day(10, 23), day(10, 23), func(time.Time) int { return 30 }),
	} {
		got := buildSleepHealth(days, shNow)
		if got.State != "not_enough_data" || got.UsualFailedSleeps != nil || got.MoreBy != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// Exactly 5 and 14 recorded days are enough.
	days := append(shDays(day(10, 2), day(10, 15), func(time.Time) int { return 1 }), shDays(day(10, 18), day(10, 22), func(time.Time) int { return 1 })...)
	if got := buildSleepHealth(days, shNow); got.State != "normal" || got.ThisWeek.Days != 5 || got.BaselineDays != 14 {
		t.Errorf("exactly enough: %+v", got)
	}
}

// The boundary: at least 3 more and at least 50% more. Usual 7: 10 is 3 more but under 1.5x (10.5), so normal; 11 is both, so more. Usual 3.5:
// 7 is 3.5 more and exactly 2x, so more; 6 is only 2.5 more, so normal.
func TestSleepHealthBoundary(t *testing.T) {
	mk := func(baseEveryOther int, week int) sleepHealthResponse {
		// Baseline: 30 days, `baseEveryOther` short sleeps on every second day. Week: `week` short sleeps on its first day, none after.
		var days []samples.DayActivity
		for d, i := day(9, 16), 0; !d.After(day(10, 15)); d, i = d.AddDate(0, 0, 1), i+1 {
			f := 0
			if i%2 == 0 {
				f = baseEveryOther
			}
			days = append(days, samples.DayActivity{Day: d, AsleepH: 8, Sleeps: 3, FailedSleeps: f})
		}
		for d, i := day(10, 16), 0; !d.After(day(10, 22)); d, i = d.AddDate(0, 0, 1), i+1 {
			f := 0
			if i == 0 {
				f = week
			}
			days = append(days, samples.DayActivity{Day: d, AsleepH: 8, Sleeps: 3, FailedSleeps: f})
		}
		return buildSleepHealth(days, shNow)
	}
	// Baseline of 15 days x 2 = 30 short sleeps / 30 days x 7 = 7 usual.
	if g := mk(2, 10); g.State != "normal" {
		t.Errorf("3 more but under 50%%: %+v", g)
	}
	if g := mk(2, 11); g.State != "more" || *g.MoreBy != 4 {
		t.Errorf("11 vs 7: %+v", g)
	}
	// Baseline 15 x 1 = 15 -> usual 3.5; 7 is 3.5 more and exactly 2x: more. 6 is 2.5 more: normal.
	if g := mk(1, 7); g.State != "more" {
		t.Errorf("7 vs 3.5: %+v", g)
	}
	if g := mk(1, 6); g.State != "normal" {
		t.Errorf("6 vs 3.5: %+v", g)
	}
}

// Days on which the truck never slept don't count (an R2 before its power-state fix: about 21 awake hours, no sleep), so they never make the
// baseline look calmer than it was and a truck that sends none isn't read as "slept well".
func TestSleepHealthUnrecordedDaysDontCount(t *testing.T) {
	days := shDays(day(9, 16), day(10, 22), func(time.Time) int { return 1 })
	for i := range days {
		if i%2 == 0 {
			days[i].Sleeps, days[i].AsleepH, days[i].IdleAwakeH, days[i].FailedSleeps = 0, 0.2, 21, 40
		}
	}
	got := buildSleepHealth(days, shNow)
	if got.ThisWeek.Days > 4 || got.State != "not_enough_data" {
		t.Errorf("%+v", got)
	}
}

// An R2-shaped history: nothing slept before Oct 16 (ready all day), then real days. Seven real days are a week but no baseline, so no verdict;
// and with a longer real history the invalid days still add nothing to the baseline's days or its rate.
func TestSleepHealthR2ShapedHistory(t *testing.T) {
	r2 := func(m time.Month, d int, sleeps, failed int, asleep float64) samples.DayActivity {
		return samples.DayActivity{Day: day(m, d), AsleepH: asleep, IdleAwakeH: 2, Sleeps: sleeps, FailedSleeps: failed}
	}
	var days []samples.DayActivity
	for d := 9; d <= 15; d++ { // before the fix: ready ~21 h, no sleep
		days = append(days, samples.DayActivity{Day: day(10, d), AsleepH: 0, IdleAwakeH: 21})
	}
	// Oct 16-22, R2 A's real shape.
	for i, s := range [][3]int{{7, 0, 4}, {18, 6, 14}, {21, 3, 14}, {30, 10, 20}, {7, 0, 18}, {7, 0, 24}, {7, 0, 24}} {
		days = append(days, r2(10, 16+i, s[0], s[1], float64(s[2])))
	}
	got := buildSleepHealth(days, shNow)
	if got.State != "not_enough_data" || got.BaselineDays != 0 || got.ThisWeek.Days != 7 {
		t.Errorf("a week of real R2 days and no baseline: %+v", got)
	}
	// A month on (Nov 20): the pre-fix days are far outside the window, and a flat history reads as normal.
	var later []samples.DayActivity
	for d := day(10, 16); !d.After(day(11, 19)); d = d.AddDate(0, 0, 1) {
		later = append(later, samples.DayActivity{Day: d, AsleepH: 16, IdleAwakeH: 2, Sleeps: 12, FailedSleeps: 2})
	}
	g := buildSleepHealth(later, time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC))
	if g.State != "normal" || g.BaselineDays != 30 || g.ThisWeek.Days != 7 {
		t.Errorf("R2 with a month of real days: %+v", g)
	}
}

// Through the real router with the full URL: 401 signed out; signed in with no store it answers "not_enough_data".
func TestSleepHealthThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	do := func(method string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://rivolt.dev/api/vehicles/rv-1/sleep-health", nil)
		if signedIn {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := do(http.MethodGet, false); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", w.Code)
	}
	w := do(http.MethodGet, true)
	var out sleepHealthResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.State != "not_enough_data" || out.Version != 1 {
		t.Errorf("signed in: %d %s", w.Code, w.Body.String())
	}
	// An app session may read it (it is not on the read-deny list).
	if appReadDenied("/api/vehicles/rv-1/sleep-health") {
		t.Error("an app session may not read sleep-health")
	}
	// The response has numbers and a state, no text for the client to show.
	if strings.Contains(w.Body.String(), "slept") || strings.Contains(w.Body.String(), "woke") {
		t.Errorf("the response carries wording: %s", w.Body.String())
	}
}

// One truck's verdict is cached per UTC day: the same day shares a key, another truck, user or day does not.
func TestSleepHealthKey(t *testing.T) {
	u, v := uuid.New(), uuid.New()
	d := day(10, 23)
	if sleepHealthKey(u, "a", d) != sleepHealthKey(u, "a", d.Add(15*time.Hour)) {
		t.Error("the same UTC day must share a key")
	}
	for name, k := range map[string]string{
		"vehicle": sleepHealthKey(u, "b", d), "user": sleepHealthKey(v, "a", d), "day": sleepHealthKey(u, "a", d.AddDate(0, 0, 1)),
	} {
		if k == sleepHealthKey(u, "a", d) {
			t.Errorf("a different %s shares the key", name)
		}
	}
}
