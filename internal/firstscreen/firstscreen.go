// Package firstscreen reads what the first screen of a new account shows (design first-screen §2): Postgres only, never Rivian. Every
// query filters on user_id and is bounded: an indexed range per user, a LIMIT of 10000 rows for a count, or a LIMIT 1.
package firstscreen

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Data is the endpoint's body.
type Data struct {
	// Vehicles is how many vehicles the account has. Receipts are per account, not per vehicle: with more than one the card says
	// "on your Rivian account".
	Vehicles int `json:"vehicles"`
	// OdometerMi is the last recorded odometer (the earlier owner's miles included on a used truck); nil before any state is recorded.
	OdometerMi *float64 `json:"odometer_mi"`
	Recorded   Counts   `json:"recorded"`
	Imported   Counts   `json:"imported"`
	// FirstRecordedChargeAt is when the first live-recorded charge started, so the client knows when two weeks of charges exist.
	FirstRecordedChargeAt *time.Time `json:"first_recorded_charge_at"`
	Receipts              Receipts   `json:"receipts"`
}

// Counts are capped row counts (at most 10000: the client says "10,000+").
type Counts struct {
	Drives  int `json:"drives"`
	Charges int `json:"charges"`
}

// Receipts is the "billed by Rivian" card. Without the user's opt-in it holds only OptIn false and nothing is read.
type Receipts struct {
	OptIn bool `json:"opt_in"`
	// State: "off" (not opted in), "waiting" (opted in, the first pass has not finished), "error" (it failed and nothing is stored yet),
	// "empty" (it finished and Rivian returned none), "ready".
	State         string       `json:"state"`
	Sessions      int          `json:"sessions"`
	FirstMonth    string       `json:"first_month,omitempty"` // YYYY-MM of the first session
	Currencies    []Currency   `json:"currencies"`
	TopVendors    []NamedCount `json:"top_vendors"`
	TopCities     []NamedCount `json:"top_cities"`
	LastSuccessAt *time.Time   `json:"last_success_at"`
}

// Currency is one currency's totals. A home session with no price counts in HomeSessions and adds nothing to Billed.
type Currency struct {
	Currency     string  `json:"currency"`
	Sessions     int     `json:"sessions"`
	HomeSessions int     `json:"home_sessions"`
	KWh          float64 `json:"kwh"`
	Billed       float64 `json:"billed"`
}

// NamedCount is a vendor or a city and how many sessions.
type NamedCount struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
}

// Store reads the first screen.
type Store struct{ db *sql.DB }

// New wraps a pool.
func New(db *sql.DB) *Store { return &Store{db: db} }

// FirstScreen loads the data. receipts says whether the user opted in and the operator allows it; false reads nothing from the receipts tables.
func (s *Store) FirstScreen(ctx context.Context, uid uuid.UUID, receipts bool) (Data, error) {
	d := Data{Receipts: Receipts{State: "off", Currencies: []Currency{}, TopVendors: []NamedCount{}, TopCities: []NamedCount{}}}
	if s == nil || s.db == nil {
		return d, errors.New("firstscreen: no database")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM vehicles WHERE user_id = $1 LIMIT 20) v`, uid).Scan(&d.Vehicles); err != nil {
		return d, err
	}
	var odo sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `SELECT odometer_mi FROM vehicle_state WHERE user_id = $1 ORDER BY at DESC LIMIT 1`, uid).Scan(&odo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	if odo.Valid {
		v := odo.Float64
		d.OdometerMi = &v
	}
	for _, c := range []struct {
		q   string
		dst *int
	}{
		{`SELECT count(*) FROM (SELECT 1 FROM drives WHERE user_id = $1 AND source = 'live' LIMIT 10000) x`, &d.Recorded.Drives},
		{`SELECT count(*) FROM (SELECT 1 FROM charges WHERE user_id = $1 AND source = 'live' LIMIT 10000) x`, &d.Recorded.Charges},
		{`SELECT count(*) FROM (SELECT 1 FROM drives WHERE user_id = $1 AND source <> 'live' LIMIT 10000) x`, &d.Imported.Drives},
		{`SELECT count(*) FROM (SELECT 1 FROM charges WHERE user_id = $1 AND source <> 'live' LIMIT 10000) x`, &d.Imported.Charges},
	} {
		if err := s.db.QueryRowContext(ctx, c.q, uid).Scan(c.dst); err != nil {
			return d, err
		}
	}
	var first sql.NullTime
	err = s.db.QueryRowContext(ctx, `SELECT started_at FROM charges WHERE user_id = $1 AND source = 'live' ORDER BY started_at ASC LIMIT 1`, uid).Scan(&first)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	if first.Valid {
		t := first.Time.UTC()
		d.FirstRecordedChargeAt = &t
	}
	if !receipts {
		return d, nil
	}
	d.Receipts.OptIn = true
	return d, s.loadReceipts(ctx, uid, &d.Receipts)
}

func (s *Store) loadReceipts(ctx context.Context, uid uuid.UUID, r *Receipts) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(currency, ''), count(*), count(*) FILTER (WHERE is_home_charger),
		       COALESCE(sum(energy_kwh), 0), COALESCE(sum(total_cost), 0)::float8, min(started_at)
		  FROM rivian_receipts WHERE user_id = $1
		 GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 5`, uid)
	if err != nil {
		return err
	}
	var firstAt time.Time
	for rows.Next() {
		var c Currency
		var min time.Time
		if err := rows.Scan(&c.Currency, &c.Sessions, &c.HomeSessions, &c.KWh, &c.Billed, &min); err != nil {
			rows.Close()
			return err
		}
		r.Sessions += c.Sessions
		if firstAt.IsZero() || min.Before(firstAt) {
			firstAt = min
		}
		r.Currencies = append(r.Currencies, c)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !firstAt.IsZero() {
		r.FirstMonth = firstAt.UTC().Format("2006-01")
		for _, q := range []struct {
			col string
			dst *[]NamedCount
		}{{"vendor", &r.TopVendors}, {"city", &r.TopCities}} {
			*q.dst, err = s.top(ctx, uid, q.col)
			if err != nil {
				return err
			}
		}
	}
	var okAt sql.NullTime
	var lastErr sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT last_success_at, last_error FROM rivian_receipt_sync WHERE user_id = $1`, uid).Scan(&okAt, &lastErr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if okAt.Valid {
		t := okAt.Time.UTC()
		r.LastSuccessAt = &t
	}
	switch {
	case r.Sessions > 0:
		r.State = "ready"
	case okAt.Valid:
		r.State = "empty"
	case lastErr.Valid && lastErr.String != "":
		r.State = "error"
	default:
		r.State = "waiting"
	}
	return nil
}

// top is the three most frequent non-empty values of a rivian_receipts text column (col is one of two constants).
func (s *Store) top(ctx context.Context, uid uuid.UUID, col string) ([]NamedCount, error) {
	if col != "vendor" && col != "city" {
		return nil, errors.New("firstscreen: column")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+col+`, count(*) FROM rivian_receipts WHERE user_id = $1 AND `+col+` IS NOT NULL AND `+col+` <> ''
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 3`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NamedCount{}
	for rows.Next() {
		var n NamedCount
		if err := rows.Scan(&n.Name, &n.Sessions); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
