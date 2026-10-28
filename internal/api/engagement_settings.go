package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// engagementSettings is GET/PUT /api/settings/engagement: the account-level
// knobs behind the plug-in reminder and the weekly recap. The per-device
// switches (plug-in reminder, milestones) live with the push subscription.
type engagementSettings struct {
	// Timezone is the IANA zone the user's browser last stored; "" until one has.
	// The plug-in reminder never sends without it.
	Timezone string `json:"timezone"`
	// TimezoneStored is false until a zone has been saved with these settings.
	TimezoneStored bool `json:"timezone_stored"`
	WeeklyRecap    bool `json:"weekly_recap"`
	// TripRecap: one notification after driving a saved trip (planned vs actual arrival charge
	// and cost). Off until chosen.
	TripRecap        bool   `json:"trip_recap"`
	PlugThresholdPct int    `json:"plug_threshold_pct"`
	PlugRemindAt     string `json:"plug_remind_at"` // HH:MM local, never after 22:00
}

const (
	defaultPlugThresholdPct = 40
	defaultPlugRemindAt     = "20:00"
	earliestPlugRemindAt    = "07:00"
	latestPlugRemindAt      = "22:00"
)

type engagementAPI struct{ db *sql.DB }

func (a *engagementAPI) load(r *http.Request, uid uuid.UUID) (engagementSettings, error) {
	out := engagementSettings{PlugThresholdPct: defaultPlugThresholdPct, PlugRemindAt: defaultPlugRemindAt}
	var tz sql.NullString
	var at string
	err := a.db.QueryRowContext(r.Context(), `
		SELECT timezone, weekly_recap, trip_recap, plug_threshold_pct, to_char(plug_remind_at, 'HH24:MI')
		  FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&tz, &out.WeeklyRecap, &out.TripRecap, &out.PlugThresholdPct, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Timezone, out.TimezoneStored = tz.String, tz.Valid && tz.String != ""
	out.PlugRemindAt = at
	return out, nil
}

func (a *engagementAPI) get(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	s, err := a.load(r, uid)
	if err != nil {
		serverError(w, r, "engagement settings", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// parseClock reads HH:MM (24 h) as minutes after midnight.
func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// put: PUT /api/settings/engagement. Every field is optional; the zone, when sent,
// is stored (the browser sends its own whenever the user saves any of these).
func (a *engagementAPI) put(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	// Opting in to messages is the user's own act, never an admin's while
	// viewing as them.
	if auth.IsImpersonating(r.Context()) {
		http.Error(w, "not available while viewing as another user", http.StatusForbidden)
		return
	}
	var body struct {
		Timezone         *string `json:"timezone"`
		WeeklyRecap      *bool   `json:"weekly_recap"`
		TripRecap        *bool   `json:"trip_recap"`
		PlugThresholdPct *int    `json:"plug_threshold_pct"`
		PlugRemindAt     *string `json:"plug_remind_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	cur, err := a.load(r, uid)
	if err != nil {
		serverError(w, r, "engagement settings", err)
		return
	}
	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if _, err := time.LoadLocation(tz); err != nil || tz == "" || strings.EqualFold(tz, "local") {
			http.Error(w, "timezone must be an IANA time zone, e.g. America/Chicago", http.StatusBadRequest)
			return
		}
		cur.Timezone, cur.TimezoneStored = tz, true
	}
	if body.WeeklyRecap != nil {
		cur.WeeklyRecap = *body.WeeklyRecap
	}
	if body.TripRecap != nil {
		cur.TripRecap = *body.TripRecap
	}
	if body.PlugThresholdPct != nil {
		if *body.PlugThresholdPct < 5 || *body.PlugThresholdPct > 95 {
			http.Error(w, "plug_threshold_pct must be between 5 and 95", http.StatusBadRequest)
			return
		}
		cur.PlugThresholdPct = *body.PlugThresholdPct
	}
	if body.PlugRemindAt != nil {
		m, ok := parseClock(*body.PlugRemindAt)
		earliest, _ := parseClock(earliestPlugRemindAt)
		latest, _ := parseClock(latestPlugRemindAt)
		if !ok || m < earliest || m > latest {
			http.Error(w, "plug_remind_at must be a time of day (HH:MM) between 07:00 and 22:00", http.StatusBadRequest)
			return
		}
		cur.PlugRemindAt = (time.Time{}).Add(time.Duration(m) * time.Minute).Format("15:04")
	}
	var tz any
	if cur.TimezoneStored {
		tz = cur.Timezone
	}
	if _, err := a.db.ExecContext(r.Context(), `
		INSERT INTO engagement_prefs (user_id, timezone, weekly_recap, trip_recap, plug_threshold_pct, plug_remind_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::time, NOW())
		ON CONFLICT (user_id) DO UPDATE SET timezone = EXCLUDED.timezone, weekly_recap = EXCLUDED.weekly_recap,
			trip_recap = EXCLUDED.trip_recap, plug_threshold_pct = EXCLUDED.plug_threshold_pct,
			plug_remind_at = EXCLUDED.plug_remind_at, updated_at = NOW()`,
		uid, tz, cur.WeeklyRecap, cur.TripRecap, cur.PlugThresholdPct, cur.PlugRemindAt); err != nil {
		serverError(w, r, "engagement settings", err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}
