// Package vehiclelease holds a vehicle's lease terms and computes where the
// driver stands against the mileage allowance. Distances are miles; the
// client converts to the user's unit.
package vehiclelease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	dateLayout = "2006-01-02"
	// staleOdometer is how old the latest odometer reading may be before
	// the used miles are reported as unknown rather than stale.
	staleOdometer = 7 * 24 * time.Hour
	paceWindow    = 90 * 24 * time.Hour
	// minPaceSpan is the least odometer history a pace is computed from.
	minPaceSpan = 14 * 24 * time.Hour
	maxTermDays = 10 * 366
)

// Terms are what the driver enters. Dates are calendar dates (YYYY-MM-DD).
type Terms struct {
	StartDate         string  `json:"start_date"`
	EndDate           string  `json:"end_date"`
	StartOdometerMi   float64 `json:"start_odometer_mi"`
	AllowedMilesTotal float64 `json:"allowed_miles_total"`
	OveragePerMile    float64 `json:"overage_per_mile"`
	Currency          string  `json:"currency"`
}

// Validate normalises the currency and rejects impossible terms.
func (t *Terms) Validate() error {
	s, e, err := t.dates()
	if err != nil {
		return err
	}
	switch {
	case !e.After(s):
		return errors.New("end_date must be after start_date")
	case e.Sub(s) > maxTermDays*24*time.Hour:
		return errors.New("lease term is too long")
	case !finite(t.StartOdometerMi) || t.StartOdometerMi < 0 || t.StartOdometerMi > 2_000_000:
		return errors.New("start_odometer_mi is out of range")
	case !finite(t.AllowedMilesTotal) || t.AllowedMilesTotal <= 0 || t.AllowedMilesTotal > 2_000_000:
		return errors.New("allowed_miles_total must be positive")
	case !finite(t.OveragePerMile) || t.OveragePerMile < 0 || t.OveragePerMile > 1000:
		return errors.New("overage_per_mile is out of range")
	}
	t.Currency = strings.ToUpper(strings.TrimSpace(t.Currency))
	if t.Currency == "" {
		t.Currency = "USD"
	}
	if len(t.Currency) != 3 {
		return errors.New("currency must be a 3-letter code")
	}
	return nil
}

func (t Terms) dates() (start, end time.Time, err error) {
	if start, err = time.Parse(dateLayout, t.StartDate); err != nil {
		return start, end, fmt.Errorf("start_date must be YYYY-MM-DD")
	}
	if end, err = time.Parse(dateLayout, t.EndDate); err != nil {
		return start, end, fmt.Errorf("end_date must be YYYY-MM-DD")
	}
	return start, end, nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Reading is an odometer reading.
type Reading struct {
	Mi float64
	At time.Time
}

// Status is the computed standing. Pointer fields are null when they cannot
// be known (no recent odometer, not enough history for a pace).
type Status struct {
	OdometerMi *float64 `json:"odometer_mi"`
	OdometerAt *string  `json:"odometer_at"`

	UsedMi            *float64 `json:"used_mi"`
	AllowedToDateMi   float64  `json:"allowed_to_date_mi"`
	AheadBehindMi     *float64 `json:"ahead_behind_mi"` // allowed_to_date − used; negative = over the pro-rata allowance
	DaysTotal         int      `json:"days_total"`
	DaysElapsed       int      `json:"days_elapsed"`
	DaysLeft          int      `json:"days_left"`
	PaceMiPerDay      *float64 `json:"pace_mi_per_day"`
	PaceBasis         string   `json:"pace_basis,omitempty"` // last_90_days | lease_to_date
	ProjectedTotalMi  *float64 `json:"projected_total_mi"`
	ProjectedReturnMi *float64 `json:"projected_return_odometer_mi"`
	ProjectedOverMi   *float64 `json:"projected_overage_mi"`
	ProjectedOverCost *float64 `json:"projected_overage_cost"`
	MilesPerDayBudget *float64 `json:"miles_per_day_budget"`
	Currency          string   `json:"currency"`
}

// Compute works out the status at now. latest is the newest odometer
// reading (nil if none); past is the oldest reading inside the pace window
// (nil if none).
func Compute(t Terms, now time.Time, latest, past *Reading) (Status, error) {
	start, end, err := t.dates()
	if err != nil {
		return Status{}, err
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	total := int(end.Sub(start).Hours() / 24)
	elapsed := clampInt(int(today.Sub(start).Hours()/24), 0, total)
	left := total - elapsed
	st := Status{
		DaysTotal: total, DaysElapsed: elapsed, DaysLeft: left, Currency: t.Currency,
		AllowedToDateMi: round1(t.AllowedMilesTotal * float64(elapsed) / float64(total)),
	}
	if latest == nil || now.Sub(latest.At) > staleOdometer {
		return st, nil
	}
	used := math.Max(0, latest.Mi-t.StartOdometerMi)
	at := latest.At.UTC().Format(time.RFC3339)
	st.OdometerMi, st.OdometerAt = ptr(round1(latest.Mi)), &at
	st.UsedMi = ptr(round1(used))
	st.AheadBehindMi = ptr(round1(st.AllowedToDateMi - used))
	if left > 0 {
		st.MilesPerDayBudget = ptr(round1(math.Max(0, (t.AllowedMilesTotal-used)/float64(left))))
	} else {
		st.MilesPerDayBudget = ptr(0.0)
	}

	pace, basis := 0.0, ""
	switch {
	case past != nil && latest.At.Sub(past.At) >= minPaceSpan && latest.Mi >= past.Mi && now.Sub(start) >= paceWindow:
		pace, basis = (latest.Mi-past.Mi)/(latest.At.Sub(past.At).Hours()/24), "last_90_days"
	case elapsed >= int(minPaceSpan.Hours()/24):
		pace, basis = used/float64(elapsed), "lease_to_date"
	case left == 0:
		// Over: the projection is the actual.
	default:
		return st, nil // too early in the lease for a pace
	}
	if basis != "" {
		st.PaceMiPerDay, st.PaceBasis = ptr(round1(pace)), basis
	}
	total2 := used + pace*float64(left)
	over := math.Max(0, total2-t.AllowedMilesTotal)
	st.ProjectedTotalMi = ptr(round1(total2))
	st.ProjectedReturnMi = ptr(round1(t.StartOdometerMi + total2))
	st.ProjectedOverMi = ptr(round1(over))
	st.ProjectedOverCost = ptr(math.Round(over*t.OveragePerMile*100) / 100)
	return st, nil
}

func ptr(f float64) *float64   { return &f }
func round1(f float64) float64 { return math.Round(f*10) / 10 }
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Store is the Postgres side.
type Store struct{ d *sql.DB }

// NewStore wraps the pool.
func NewStore(d *sql.DB) *Store { return &Store{d} }

// Get returns the vehicle's terms, or (nil, nil) when none are set.
func (s *Store) Get(ctx context.Context, uid, vehicleID uuid.UUID) (*Terms, error) {
	var t Terms
	var sd, ed time.Time
	err := s.d.QueryRowContext(ctx, `
		SELECT start_date, end_date, start_odometer_mi, allowed_miles_total, overage_per_mile, currency
		  FROM vehicle_leases WHERE user_id = $1 AND vehicle_id = $2`, uid, vehicleID).
		Scan(&sd, &ed, &t.StartOdometerMi, &t.AllowedMilesTotal, &t.OveragePerMile, &t.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.StartDate, t.EndDate = sd.Format(dateLayout), ed.Format(dateLayout)
	return &t, nil
}

// Put saves validated terms, replacing any existing ones.
func (s *Store) Put(ctx context.Context, uid, vehicleID uuid.UUID, t Terms) error {
	_, err := s.d.ExecContext(ctx, `
		INSERT INTO vehicle_leases (vehicle_id, user_id, start_date, end_date, start_odometer_mi, allowed_miles_total, overage_per_mile, currency)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (vehicle_id) DO UPDATE SET
			start_date = EXCLUDED.start_date, end_date = EXCLUDED.end_date,
			start_odometer_mi = EXCLUDED.start_odometer_mi, allowed_miles_total = EXCLUDED.allowed_miles_total,
			overage_per_mile = EXCLUDED.overage_per_mile, currency = EXCLUDED.currency, updated_at = NOW()
		WHERE vehicle_leases.user_id = EXCLUDED.user_id`,
		vehicleID, uid, t.StartDate, t.EndDate, t.StartOdometerMi, t.AllowedMilesTotal, t.OveragePerMile, t.Currency)
	return err
}

// Delete removes the terms; absent is fine.
func (s *Store) Delete(ctx context.Context, uid, vehicleID uuid.UUID) error {
	_, err := s.d.ExecContext(ctx, `DELETE FROM vehicle_leases WHERE user_id = $1 AND vehicle_id = $2`, uid, vehicleID)
	return err
}

// Odometer returns the newest reading and the oldest one inside the pace
// window (either may be nil).
func (s *Store) Odometer(ctx context.Context, uid, vehicleID uuid.UUID, now time.Time) (latest, past *Reading, err error) {
	one := func(order string, from time.Time) (*Reading, error) {
		var r Reading
		err := s.d.QueryRowContext(ctx, `
			SELECT odometer_mi, at FROM vehicle_state
			 WHERE vehicle_id = $1 AND user_id = $2 AND odometer_mi > 0 AND at >= $3 AND at <= $4
			 ORDER BY at `+order+` LIMIT 1`, vehicleID, uid, from, now).Scan(&r.Mi, &r.At)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return &r, err
	}
	if latest, err = one("DESC", now.Add(-staleOdometer)); err != nil || latest == nil {
		return nil, nil, err
	}
	past, err = one("ASC", now.Add(-paceWindow))
	return latest, past, err
}
