package api

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/samples"
)

// Sleep health (Sprint 8 item 5): this week's quick re-wakes against this truck's own last 30 days.
// Display only, and only ever this truck against itself: nothing here compares trucks or users, stores
// anything or alerts. It reads the same per-day rows as /api/sleep-activity (so the Sleep card and this
// verdict agree), and a sleep that ended within a minute is a "failed" (short) sleep.
//
// This week is the 7 complete UTC days before today; the baseline is the 30 days before that. A day counts
// only if the truck's power state was recorded for it. Because the week can have 5 to 7 such days and the
// baseline 14 to 30, the usual week is a rate: the baseline's short sleeps per recorded day times this
// week's recorded days.
const (
	sleepHealthWeekDays     = 7
	sleepHealthBaselineDays = 30
	// A day is recorded with a sleep in it, or with this many hours of asleep or idle-awake time (the drain
	// card's own "observed power state" minimum).
	sleepHealthMinObsHours = 3.0
	sleepHealthMinWeekDays = 5
	sleepHealthMinBaseDays = 14
	// "More than usual": at least this many more short sleeps than the usual week, and at least this share more.
	sleepHealthMoreBy    = 3.0
	sleepHealthMoreRatio = 1.5
)

type sleepHealthResponse struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	// State is "normal", "more" or "not_enough_data". Numbers only: the words are the client's.
	State        string  `json:"state"`
	ThisWeek     weekSum `json:"this_week"`
	BaselineDays int     `json:"baseline_days"`
	// UsualFailedSleeps is the baseline's rate scaled to this week's recorded days; absent without enough data.
	UsualFailedSleeps *float64 `json:"usual_failed_sleeps,omitempty"`
	// MoreBy is how many more short sleeps than usual (rounded), set only when State is "more".
	MoreBy *int `json:"more_by,omitempty"`
}

type weekSum struct {
	Days         int `json:"days"`
	Sleeps       int `json:"sleeps"`
	FailedSleeps int `json:"failed_sleeps"`
}

func sleepDayRecorded(d samples.DayActivity) bool {
	return d.Sleeps > 0 || d.AsleepH+d.IdleAwakeH >= sleepHealthMinObsHours
}

// buildSleepHealth is pure: the per-UTC-day rows and the clock -> the verdict.
func buildSleepHealth(days []samples.DayActivity, now time.Time) sleepHealthResponse {
	out := sleepHealthResponse{Version: 1, GeneratedAt: now.UTC(), State: "not_enough_data"}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	weekStart := today.AddDate(0, 0, -sleepHealthWeekDays)
	baseStart := weekStart.AddDate(0, 0, -sleepHealthBaselineDays)
	var baseFailed int
	for _, d := range days {
		day := d.Day.UTC()
		if !sleepDayRecorded(d) {
			continue
		}
		switch {
		case !day.Before(weekStart) && day.Before(today):
			out.ThisWeek.Days++
			out.ThisWeek.Sleeps += d.Sleeps
			out.ThisWeek.FailedSleeps += d.FailedSleeps
		case !day.Before(baseStart) && day.Before(weekStart):
			out.BaselineDays++
			baseFailed += d.FailedSleeps
		}
	}
	if out.ThisWeek.Days < sleepHealthMinWeekDays || out.BaselineDays < sleepHealthMinBaseDays {
		return out
	}
	usual := float64(baseFailed) / float64(out.BaselineDays) * float64(out.ThisWeek.Days)
	u := math.Round(usual*10) / 10
	out.UsualFailedSleeps = &u
	this := float64(out.ThisWeek.FailedSleeps)
	if this-usual >= sleepHealthMoreBy && this >= sleepHealthMoreRatio*usual {
		out.State = "more"
		n := int(math.Round(this - usual))
		out.MoreBy = &n
		return out
	}
	out.State = "normal"
	return out
}

// handleSleepHealth serves GET /api/vehicles/{vehicleID}/sleep-health. Read-only, from vehicle_state only (never
// Rivian, so it can't wake a truck), through the same per-process cache as /api/sleep-activity: the window is
// whole UTC days, so the answer is computed once per truck per day-bucket however often the page asks.
func handleSleepHealth(uid uuid.UUID, store *samples.Store, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := now().UTC()
		if store == nil {
			writeJSON(w, http.StatusOK, buildSleepHealth(nil, t))
			return
		}
		vehicle := chi.URLParam(r, "vehicleID")
		today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		since := today.AddDate(0, 0, -(sleepHealthWeekDays + sleepHealthBaselineDays))
		key := sleepActivityKey(uid, vehicle, since, today)
		days, err := sleepActivityCache.get(key, func() ([]samples.DayActivity, error) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Second)
			defer cancel()
			res, err := store.SleepActivity(ctx, vehicle, since, today)
			if res == nil {
				res = []samples.DayActivity{}
			}
			return res, err
		})
		if err != nil {
			http.Error(w, "could not read sleep history", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, buildSleepHealth(days, t))
	}
}
