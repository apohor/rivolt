// Package charges persists derived charge sessions.
package charges

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// openLiveFinalStates is the canonical whitelist of charges.final_state
// values that mean "physically still in the middle of a session".
// Anything not in this set is terminal and must NOT be resumed by
// resumeOpenCharge / janitor predicates.
//
// Notable terminal-but-not-error states deliberately excluded:
//   - charging_user_stopped     (driver pressed stop on the gun)
//   - charging_station_stopped  (station ended the session)
//   - charging_complete         (target SoC reached)
//   - charging_station_err      (hardware fault)
//   - charger_disconnected      (cable pulled)
//   - abandoned                 (recorder janitor closure)
//
// Past incident the whitelist guards against: a brief
// charging_user_stopped frame between two physical sessions was
// being treated as open, so the next charging_active frame
// reattached to the just-closed row and absorbed every subsequent
// drive + charge into one absorber row.
var openLiveFinalStates = []string{
	"charging_active",
	"charging_ready",
	"charging_connecting",
	"waiting_on_charger",
}

// IsOpenLiveFinalState reports whether s names a charges.final_state
// value that means the session is physically still in progress. Kept
// in sync with the inline IN-list in LatestOpenLive et al.
func IsOpenLiveFinalState(s string) bool {
	for _, v := range openLiveFinalStates {
		if v == s {
			return true
		}
	}
	return false
}

// Charge is a single charging session. ID is the stable external id
// ("live_<vid>_c_<unix>", "electrafi_<vid>_c_<unix>") and maps to the
// charges.external_id column. VehicleID is the Rivian gateway string.
type Charge struct {
	ID             string
	VehicleID      string
	StartedAt      time.Time
	EndedAt        time.Time
	StartSoCPct    float64
	EndSoCPct      float64
	EnergyAddedKWh float64
	MilesAdded     float64
	MaxPowerKW     float64
	AvgPowerKW     float64
	FinalState     string
	Lat, Lon       float64
	Source         string
	Cost           float64
	Currency       string
	PricePerKWh    float64
	// ThermalKWh is the energy the BMS spent on pack heating / cooling
	// during the session, surfaced by Rivian's Parallax
	// ChargingSessionLiveData protobuf. Nullable: legacy rows
	// recorded before this column existed leave it nil so the UI
	// can render "—" instead of misleading "0 kWh thermal".
	ThermalKWh *float64
	// ChargeLimitPct is the charge limit the truck was set to during the session
	// (the last reported value, 1 to 100); nil when unknown. A later save that carries
	// none keeps the stored one.
	ChargeLimitPct *float64
	// PlaceOverride is the user's own "home" or "away" for this charge (empty: automatic). When
	// set, Place holds the same value and nothing re-classifies the row.
	PlaceOverride string
	// LocationOverride is "recorded" when the user says the charge happened where the truck was
	// (Lat/Lon), not at the nearest public charger; maps then never snap. Empty: as recorded.
	LocationOverride string
	// ActiveSeconds is the wall-clock time the session actually spent
	// charging (power above the idle floor), as opposed to
	// EndedAt-StartedAt which spans the whole plugged-in period
	// (including charging_ready / battery-conditioning idle). Zero on
	// legacy rows and non-live sources; the UI falls back to the wall
	// span in that case.
	ActiveSeconds float64
	// CostSource says where Cost came from: "rivian" (billed),
	// "home_rate", "network_estimate" (DCFC priced from the matched
	// network's rate), "observed_estimate" (DCFC priced from a typical
	// public price, no rate of the user's) or "user". Empty on legacy rows.
	CostSource string
	// Network is the matched DCFC network's display name for
	// network_estimate rows; empty otherwise.
	Network string
	// Place is where the session happened relative to the user's home
	// when it was recorded: "home", "away", or "" (unknown: no saved
	// home or no position). Migration 0044.
	Place string
	// NetworkState is DC fast charge auto-detection's verify state
	// (migration 0051): "estimated", "needs_confirmation", "confirmed",
	// or "" (not auto-detected: AC, Rivian-billed, user-priced, legacy).
	NetworkState string
	// NetworkConfidence is the detection's confidence, 0..1; nil when
	// there was no detection.
	NetworkConfidence *float64
	// NetworkEvidence is what the detection saw (JSON, contract v1);
	// nil when there was no detection.
	NetworkEvidence json.RawMessage
	// RivianCharger is Rivian's isRivianCharger for the session: nil
	// when not reported. Written by the live recorder; a later upsert
	// without it keeps the stored value. Not read back yet. Migration
	// 0050.
	RivianCharger *bool
	// ReceiptCost is what Rivian billed for the session (its Charging
	// history receipt), kept next to Cost (our estimate), which a receipt
	// never overwrites. Nil when no receipt matched. ReceiptCurrency and
	// ReceiptTxnID go with it. Migration 0055; written only by the
	// receipts sync.
	ReceiptCost     *float64
	ReceiptCurrency string
	ReceiptTxnID    string
	// EstimateMonth and EstimateURL say which public report priced a
	// CostSource "observed_estimate" session ("2026-10", the dcfctracker.com
	// page). Empty on every other row. Migration 0075.
	EstimateMonth string
	EstimateURL   string
	// EnergyEstimated says EnergyAddedKWh was not metered: the recorder's integral had lost energy (a handoff or a
	// restart took it) and the energy was estimated from the SoC gain. Stored in metadata ("energy_estimated"), set
	// and never cleared by an upsert (a row read back and re-saved keeps it).
	EnergyEstimated bool
}

// Store wraps the charges table.
type Store struct {
	db       *sql.DB
	userID   uuid.UUID
	vehicles *db.VehicleResolver
}

// OpenStore binds a pooled connection to a user + vehicle resolver.
func OpenStore(d *sql.DB, userID uuid.UUID, v *db.VehicleResolver) (*Store, error) {
	if d == nil {
		return nil, fmt.Errorf("charges: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("charges: userID is zero")
	}
	if v == nil {
		return nil, fmt.Errorf("charges: vehicle resolver is nil")
	}
	return &Store{db: d, userID: userID, vehicles: v}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Reset deletes every charge for this store's user. See drives.Store.Reset.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM charges WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteByExternalID removes a single charge row owned by this user.
// Returns the number of rows deleted (0 if no match — caller should
// treat that as 404). Scoped by user_id so a malicious external_id
// can't reach into another user's data.
func (s *Store) DeleteByExternalID(ctx context.Context, externalID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM charges WHERE user_id = $1 AND external_id = $2`,
		s.userID, externalID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdatePricing overwrites the cost / currency / price-per-kWh of a
// single charge row, scoped to the current user. Used when the user
// edits a row in the UI to correct a missing or wrong DCFC price
// (e.g. a session paid outside the Rivian app). Zeros clear the
// stored value and let the API-layer fallbacks (home rate, etc.)
// take over again.
func (s *Store) UpdatePricing(ctx context.Context, externalID string, cost float64, currency string, pricePerKWh float64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET cost = $3,
		    currency = NULLIF($4, ''),
		    price_per_kwh = $5,
		    cost_source = CASE WHEN $3::numeric IS NULL THEN NULL ELSE 'user' END,
		    -- A user's price ends auto-detection's verify state (the
		    -- confidence and evidence stay for audit).
		    network_state = NULL,
		    estimate_month = NULL,
		    estimate_url = NULL,
		    legacy_cost = NULL,
		    legacy_price_per_kwh = NULL,
		    updated_at = NOW()
		WHERE user_id = $1 AND external_id = $2
	`, s.userID, externalID, nullIfZero(cost), currency, nullIfZero(pricePerKWh))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkFree records that the user paid nothing for a session: cost and
// price_per_kwh are stored as 0 (not NULL), cost_source = 'user'. It is
// deliberately separate from UpdatePricing, where a zero still means
// "clear the price". Upsert keeps user-priced rows, so a session marked
// free while the recorder still has it open stays free. currency may be
// empty. Returns rows affected (0 → not found for this user).
func (s *Store) MarkFree(ctx context.Context, externalID, currency string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET cost = 0,
		    price_per_kwh = 0,
		    currency = NULLIF($3, ''),
		    cost_source = 'user',
		    network = NULL,
		    network_state = NULL,
		    estimate_month = NULL,
		    estimate_url = NULL,
		    -- A user price ends any reprice undo window for this row:
		    -- 0040's undo selects on legacy_cost IS NOT NULL and would
		    -- otherwise revert the user's edit.
		    legacy_cost = NULL,
		    legacy_price_per_kwh = NULL,
		    updated_at = NOW()
		WHERE user_id = $1 AND external_id = $2
	`, s.userID, externalID, currency)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Upsert inserts or replaces a charge by external_id within the
// (user_id, vehicle_id) scope.
func (s *Store) Upsert(ctx context.Context, c Charge) error {
	vid, err := s.vehicles.Resolve(ctx, c.VehicleID)
	if err != nil {
		return err
	}
	month, url := CleanEstimate(c.EstimateMonth, c.EstimateURL)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO charges (
			user_id, vehicle_id, external_id,
			started_at, ended_at,
			start_soc_pct, end_soc_pct,
			energy_added_kwh, miles_added,
			max_power_kw, avg_power_kw, final_state,
			lat, lon, source,
			cost, currency, price_per_kwh,
			thermal_kwh,
			active_seconds,
			cost_source, network, place,
			network_state, network_confidence, network_evidence,
			rivian_charger,
			estimate_month, estimate_url,
			charge_limit_pct,
			place_override, location_override,
			metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27::boolean,$28,$29,$30,$31,$32,
			CASE WHEN $33::boolean THEN '{"energy_estimated": true}'::jsonb ELSE '{}'::jsonb END)
		ON CONFLICT (vehicle_id, external_id) DO UPDATE SET
			started_at       = EXCLUDED.started_at,
			ended_at         = EXCLUDED.ended_at,
			start_soc_pct    = EXCLUDED.start_soc_pct,
			end_soc_pct      = EXCLUDED.end_soc_pct,
			energy_added_kwh = EXCLUDED.energy_added_kwh,
			miles_added      = EXCLUDED.miles_added,
			max_power_kw     = EXCLUDED.max_power_kw,
			avg_power_kw     = EXCLUDED.avg_power_kw,
			final_state      = EXCLUDED.final_state,
			lat              = EXCLUDED.lat,
			lon              = EXCLUDED.lon,
			source           = EXCLUDED.source,
			-- A price the user entered survives the recorder re-saving
			-- the session (lifecycle upserts keep firing until close).
			cost             = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.cost ELSE EXCLUDED.cost END,
			currency         = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.currency ELSE EXCLUDED.currency END,
			price_per_kwh    = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.price_per_kwh ELSE EXCLUDED.price_per_kwh END,
			-- Only overwrite thermal_kwh when the new row actually
			-- has one. A re-upsert from a path that doesn't track
			-- this metric (REST poller, ElectraFi import) shouldn't
			-- erase a value the Parallax stream already captured.
			thermal_kwh      = COALESCE(EXCLUDED.thermal_kwh, charges.thermal_kwh),
			active_seconds   = COALESCE(EXCLUDED.active_seconds, charges.active_seconds),
			cost_source      = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.cost_source ELSE EXCLUDED.cost_source END,
			-- A network the user confirmed stays with its price, as a
			-- user's own price does, until Rivian's bill arrives. The
			-- detection record survives a re-save that carries none
			-- (only the detector sets it); Rivian's bill ends the state.
			network          = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.network ELSE EXCLUDED.network END,
			network_state      = CASE WHEN charges.cost_source = 'user' THEN charges.network_state
			                          WHEN EXCLUDED.cost_source = 'rivian' THEN NULL
			                          WHEN charges.network_state = 'confirmed' THEN charges.network_state
			                          ELSE COALESCE(EXCLUDED.network_state, charges.network_state) END,
			network_confidence = CASE WHEN charges.cost_source = 'user' OR charges.network_state = 'confirmed' THEN charges.network_confidence ELSE COALESCE(EXCLUDED.network_confidence, charges.network_confidence) END,
			network_evidence   = CASE WHEN charges.cost_source = 'user' OR charges.network_state = 'confirmed' THEN charges.network_evidence ELSE COALESCE(EXCLUDED.network_evidence, charges.network_evidence) END,
			-- The report an estimate came from goes with the price: kept
			-- while the price is kept, replaced with it, cleared when the new
			-- source is not an estimate.
			estimate_month   = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.estimate_month ELSE EXCLUDED.estimate_month END,
			estimate_url     = CASE WHEN charges.cost_source = 'user' OR (charges.network_state = 'confirmed' AND EXCLUDED.cost_source IS DISTINCT FROM 'rivian') THEN charges.estimate_url ELSE EXCLUDED.estimate_url END,
			-- The user's own home/away wins over any re-save (a restore carries it in).
			place            = COALESCE(EXCLUDED.place_override, charges.place_override, EXCLUDED.place, charges.place),
			place_override   = COALESCE(EXCLUDED.place_override, charges.place_override),
			location_override = COALESCE(EXCLUDED.location_override, charges.location_override),
			rivian_charger   = COALESCE(EXCLUDED.rivian_charger, charges.rivian_charger),
			charge_limit_pct = COALESCE(EXCLUDED.charge_limit_pct, charges.charge_limit_pct),
			-- Sticky: an estimated energy stays marked, whatever re-saves the row later.
			metadata         = CASE WHEN $33::boolean THEN charges.metadata || '{"energy_estimated": true}'::jsonb ELSE charges.metadata END,
			updated_at       = NOW()`,
		s.userID, vid, c.ID,
		c.StartedAt.UTC(), c.EndedAt.UTC(),
		nullIfZero(c.StartSoCPct), nullIfZero(c.EndSoCPct),
		nullIfZero(c.EnergyAddedKWh), nullIfZero(c.MilesAdded),
		nullIfZero(c.MaxPowerKW), nullIfZero(c.AvgPowerKW),
		c.FinalState,
		nullIfZero(c.Lat), nullIfZero(c.Lon),
		c.Source,
		storedCost(c), nullIfEmpty(c.Currency), storedPricePerKWh(c),
		nullableFloatPtr(c.ThermalKWh),
		nullIfZero(c.ActiveSeconds),
		nullIfEmpty(c.CostSource), nullIfEmpty(c.Network), nullIfEmpty(storedPlace(c)),
		nullIfEmpty(c.NetworkState), nullableFloatPtr(c.NetworkConfidence), nullIfEmptyJSON(c.NetworkEvidence),
		nullableBool(c.RivianCharger),
		nullIfEmpty(month), nullIfEmpty(url),
		nullableFloatPtr(c.ChargeLimitPct),
		placeOverrideArg(c.PlaceOverride), locationOverrideArg(c.LocationOverride),
		c.EnergyEstimated)
	return err
}

// storedPlace is the user's own place when the row carries one (a restore), else the recorded one.
func storedPlace(c Charge) string {
	if placeOverrideArg(c.PlaceOverride) != nil {
		return c.PlaceOverride
	}
	return c.Place
}

// placeOverrideArg is a valid override or NULL (an unknown value is never stored).
func placeOverrideArg(v string) any {
	if v == "home" || v == "away" {
		return v
	}
	return nil
}

func locationOverrideArg(v string) any {
	if v == LocationRecorded {
		return v
	}
	return nil
}

// EstimateURLPrefix is the only place an estimate's report link may point.
const EstimateURLPrefix = "https://dcfctracker.com/"

// CleanEstimate returns the estimate report month and URL as they may be
// stored: the month only as YYYY-MM, the URL only on dcfctracker.com over
// https. Anything else becomes "" so no writer (the recorder, a restored
// backup) can store a link the app would render.
func CleanEstimate(month, url string) (string, string) {
	if _, err := time.Parse("2006-01", month); err != nil || len(month) != 7 {
		month = ""
	}
	if !strings.HasPrefix(url, EstimateURLPrefix) || len(url) > 300 {
		url = ""
	}
	return month, url
}

// nullableBool maps nil to SQL NULL.
func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// Count returns the total number of charges for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM charges WHERE user_id = $1`, s.userID).Scan(&n)
	return n, err
}

// ListRecent returns the most recent N charges, newest first.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Charge, error) {
	return s.listRecent(ctx, limit, "")
}

// ListRecentForVehicle is ListRecent for one of the user's trucks, by its
// Rivian vehicle id. An id the user doesn't have returns no rows.
func (s *Store) ListRecentForVehicle(ctx context.Context, rivianVehicleID string, limit int) ([]Charge, error) {
	return s.listRecent(ctx, limit, rivianVehicleID)
}

func (s *Store) listRecent(ctx context.Context, limit int, rivianVehicleID string) ([]Charge, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8,
		       c.thermal_kwh, c.charge_limit_pct, COALESCE(c.active_seconds,0)::float8,
		       CASE WHEN c.cost_source = 'user' AND c.cost IS NULL THEN '' ELSE COALESCE(c.cost_source,'') END, COALESCE(c.network,''), COALESCE(c.place,''),
		       COALESCE(c.network_state,''), c.network_confidence, c.network_evidence,
		       c.receipt_cost::float8, COALESCE(c.receipt_currency,''), COALESCE(c.receipt_txn_id,''),
		       COALESCE(c.estimate_month,''), COALESCE(c.estimate_url,''),
		       COALESCE(c.place_override,''), COALESCE(c.location_override,'')
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		  AND ended_at - started_at >= interval '1 minute'
		  AND ($3::text = '' OR v.rivian_vehicle_id = $3::text)
		ORDER BY c.started_at DESC
		LIMIT $2`, s.userID, limit, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		var thermal, limit sql.NullFloat64
		var netConf sql.NullFloat64
		var netEv []byte
		var recCost sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.VehicleID,
			&c.StartedAt, &c.EndedAt,
			&c.StartSoCPct, &c.EndSoCPct,
			&c.EnergyAddedKWh, &c.MilesAdded,
			&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
			&c.Lat, &c.Lon, &c.Source,
			&c.Cost, &c.Currency, &c.PricePerKWh,
			&thermal, &limit, &c.ActiveSeconds, &c.CostSource, &c.Network, &c.Place,
			&c.NetworkState, &netConf, &netEv, &recCost, &c.ReceiptCurrency, &c.ReceiptTxnID, &c.EstimateMonth, &c.EstimateURL,
			&c.PlaceOverride, &c.LocationOverride,
		); err != nil {
			return nil, err
		}
		c.ThermalKWh = floatFromNull(thermal)
		c.ChargeLimitPct = floatFromNull(limit)
		c.NetworkConfidence = floatFromNull(netConf)
		c.NetworkEvidence = netEv
		c.ReceiptCost = floatFromNull(recCost)
		c.StartedAt = c.StartedAt.UTC()
		c.EndedAt = c.EndedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestOpenLive returns the most recent live-sourced charge for this
// vehicle whose final_state is still a non-terminal "charging_*" —
// i.e. the recorder was mid-session when the process stopped.
func (s *Store) LatestOpenLive(ctx context.Context, rivianVehicleID string) (*Charge, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT c.external_id, $2::text,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8,
		       c.thermal_kwh, c.charge_limit_pct, COALESCE(c.active_seconds,0)::float8,
		       CASE WHEN c.cost_source = 'user' AND c.cost IS NULL THEN '' ELSE COALESCE(c.cost_source,'') END, COALESCE(c.network,''), COALESCE(c.place,''),
		       COALESCE(c.network_state,''), c.network_confidence, c.network_evidence,
		       c.receipt_cost::float8, COALESCE(c.receipt_currency,''), COALESCE(c.receipt_txn_id,''),
		       COALESCE(c.estimate_month,''), COALESCE(c.estimate_url,''),
		       COALESCE(c.place_override,''), COALESCE(c.location_override,'')
		FROM charges c
		WHERE c.user_id = $1 AND c.vehicle_id = $3
		  AND c.source = 'live'
		  -- Whitelist of states that mean "physically still in the middle of
		  -- a session". Anything else (charging_user_stopped,
		  -- charging_station_stopped, charging_complete, charging_station_err,
		  -- abandoned, charger_disconnected, blank) is terminal and must NOT
		  -- be resumed. Past bug this guards against: a brief
		  -- charging_user_stopped frame between two physical sessions
		  -- was being treated as open here, so the next
		  -- charging_active frame reattached to the just-closed row
		  -- and absorbed every subsequent drive + charge into one
		  -- absorber.
		  AND c.final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')
		ORDER BY c.started_at DESC
		LIMIT 1`, s.userID, rivianVehicleID, vid)
	var c Charge
	var thermal, limit sql.NullFloat64
	var netConf sql.NullFloat64
	var netEv []byte
	var recCost sql.NullFloat64
	if err := row.Scan(&c.ID, &c.VehicleID,
		&c.StartedAt, &c.EndedAt,
		&c.StartSoCPct, &c.EndSoCPct,
		&c.EnergyAddedKWh, &c.MilesAdded,
		&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
		&c.Lat, &c.Lon, &c.Source,
		&c.Cost, &c.Currency, &c.PricePerKWh,
		&thermal, &limit, &c.ActiveSeconds, &c.CostSource, &c.Network, &c.Place,
		&c.NetworkState, &netConf, &netEv, &recCost, &c.ReceiptCurrency, &c.ReceiptTxnID, &c.EstimateMonth, &c.EstimateURL,
			&c.PlaceOverride, &c.LocationOverride,
	); err != nil {
		return nil, err
	}
	c.ThermalKWh = floatFromNull(thermal)
	c.ChargeLimitPct = floatFromNull(limit)
	c.NetworkConfidence = floatFromNull(netConf)
	c.NetworkEvidence = netEv
	c.ReceiptCost = floatFromNull(recCost)
	c.StartedAt = c.StartedAt.UTC()
	c.EndedAt = c.EndedAt.UTC()
	return &c, nil
}

// CloseStaleOpenLive marks every live-sourced charge for the vehicle
// whose external_id is NOT keepID and whose final_state is still a
// non-terminal "charging_*" as abandoned. Returns rows updated.
func (s *Store) CloseStaleOpenLive(ctx context.Context, rivianVehicleID, keepID string) (int, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET final_state = 'abandoned', updated_at = NOW()
		WHERE user_id = $1 AND vehicle_id = $2
		  AND source = 'live'
		  AND external_id <> $3
		  -- Same whitelist as LatestOpenLive.
		  AND final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
		s.userID, vid, keepID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CloseStaleOpenLiveBefore is the unattended janitor variant: marks
// every open live charge for THIS user whose ended_at is older than
// `before` as abandoned. Used by the recorder's periodic sweep to
// self-heal sessions left open by a previous process that died
// mid-charge and never came back. Returns rows updated.
func (s *Store) CloseStaleOpenLiveBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET final_state = 'abandoned', updated_at = NOW()
		WHERE user_id = $1
		  AND source = 'live'
		  AND ended_at < $2
		  -- Same whitelist as LatestOpenLive.
		  AND final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
		s.userID, before.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// StaleOpenCandidate is one row the janitor is considering for
// abandonment. The rivian-side vehicle ID is denormalised here so
// the caller can REST-poll the gateway without re-resolving from
// the internal vehicle UUID.
type StaleOpenCandidate struct {
	ExternalID      string
	RivianVehicleID string
	FinalState      string
	EndedAt         time.Time
}

// ListStaleOpenLive returns every open live-sourced charge row whose
// ended_at is older than `before`. Same WHERE shape as
// CloseStaleOpenLiveBefore but read-only — used by the smarter
// janitor that wants to REST-confirm the vehicle is actually no
// longer charging before tagging the row abandoned. WS feeds for
// long L1/L2 sessions can go silent for hours without the charge
// having ended.
func (s *Store) ListStaleOpenLive(ctx context.Context, before time.Time) ([]StaleOpenCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       COALESCE(c.final_state,''), c.ended_at
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		  AND c.source = 'live'
		  AND c.ended_at < $2
		  -- Same whitelist as LatestOpenLive.
		  AND c.final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
		s.userID, before.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleOpenCandidate
	for rows.Next() {
		var c StaleOpenCandidate
		if err := rows.Scan(&c.ExternalID, &c.RivianVehicleID, &c.FinalState, &c.EndedAt); err != nil {
			return nil, err
		}
		c.EndedAt = c.EndedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// RefreshOpenLive bumps ended_at = NOW() on a single open live row
// and optionally advances its final_state. Used by the smart
// janitor when a REST poll confirms the vehicle is still actively
// charging — keeps the row live for another sweep window without
// abandoning it. The WHERE clause is intentionally narrow (still
// open, still live) so a concurrent terminal-state push that closed
// the row legitimately can't be undone here.
func (s *Store) RefreshOpenLive(ctx context.Context, externalID, finalState string) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET ended_at = NOW(),
		    final_state = COALESCE(NULLIF($3, ''), final_state),
		    updated_at = NOW()
		WHERE user_id = $1
		  AND external_id = $2
		  AND source = 'live'
		  -- Same whitelist as LatestOpenLive.
		  AND final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
		s.userID, externalID, finalState)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AbandonOpenLive closes one specific open live row as abandoned.
// The smart janitor calls this only after a REST poll has confirmed
// the vehicle is no longer in a charging state. Same guarded WHERE
// as RefreshOpenLive so we never abandon a row that's already been
// closed legitimately by a terminal-state push between the list
// and the close.
func (s *Store) AbandonOpenLive(ctx context.Context, externalID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET final_state = 'abandoned', updated_at = NOW()
		WHERE user_id = $1
		  AND external_id = $2
		  AND source = 'live'
		  -- Same whitelist as LatestOpenLive.
		  AND final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
		s.userID, externalID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Dedupe is a no-op on Postgres — UNIQUE (vehicle_id, external_id)
// prevents the SQLite-era duplicates the old code had to clean up.
func (s *Store) Dedupe(ctx context.Context) (int, error) { return 0, nil }

// EachAll streams EVERY stored charge of this user, newest first, with no
// display filter: the backup and the data export must hold every row, including
// the sub-minute ones the lists hide. Callers that price or show charges use
// EachListed.
func (s *Store) EachAll(ctx context.Context, fn func(Charge) error) error {
	return s.each(ctx, false, fn)
}

// EachListed is EachAll without the charges the lists hide: those under a minute.
func (s *Store) EachListed(ctx context.Context, fn func(Charge) error) error {
	return s.each(ctx, true, fn)
}

func (s *Store) each(ctx context.Context, listedOnly bool, fn func(Charge) error) error {
	minDur := `ended_at - started_at >= interval '1 minute'`
	if !listedOnly {
		minDur = `TRUE`
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8,
		       c.thermal_kwh, c.charge_limit_pct, COALESCE(c.active_seconds,0)::float8,
		       CASE WHEN c.cost_source = 'user' AND c.cost IS NULL THEN '' ELSE COALESCE(c.cost_source,'') END, COALESCE(c.network,''), COALESCE(c.place,''),
		       COALESCE(c.network_state,''), c.network_confidence, c.network_evidence,
		       c.receipt_cost::float8, COALESCE(c.receipt_currency,''), COALESCE(c.receipt_txn_id,''),
		       COALESCE(c.estimate_month,''), COALESCE(c.estimate_url,''),
		       COALESCE(c.place_override,''), COALESCE(c.location_override,'')
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		  AND `+minDur+`
		ORDER BY c.started_at DESC`, s.userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Charge
		var thermal, limit sql.NullFloat64
		var netConf sql.NullFloat64
		var netEv []byte
		var recCost sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.VehicleID,
			&c.StartedAt, &c.EndedAt,
			&c.StartSoCPct, &c.EndSoCPct,
			&c.EnergyAddedKWh, &c.MilesAdded,
			&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
			&c.Lat, &c.Lon, &c.Source,
			&c.Cost, &c.Currency, &c.PricePerKWh,
			&thermal, &limit, &c.ActiveSeconds, &c.CostSource, &c.Network, &c.Place,
			&c.NetworkState, &netConf, &netEv, &recCost, &c.ReceiptCurrency, &c.ReceiptTxnID, &c.EstimateMonth, &c.EstimateURL,
			&c.PlaceOverride, &c.LocationOverride,
		); err != nil {
			return err
		}
		c.ThermalKWh = floatFromNull(thermal)
		c.ChargeLimitPct = floatFromNull(limit)
		c.NetworkConfidence = floatFromNull(netConf)
		c.NetworkEvidence = netEv
		c.ReceiptCost = floatFromNull(recCost)
		c.StartedAt = c.StartedAt.UTC()
		c.EndedAt = c.EndedAt.UTC()
		if err := fn(c); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ListAll returns every stored row; use EachAll for large result sets.
func (s *Store) ListAll(ctx context.Context) ([]Charge, error) {
	var out []Charge
	err := s.EachAll(ctx, func(v Charge) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

// ListAllListed returns the charges the lists show (a minute or longer).
func (s *Store) ListAllListed(ctx context.Context) ([]Charge, error) {
	var out []Charge
	err := s.EachListed(ctx, func(v Charge) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

func nullIfZero(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// floatFromNull lifts a sql.NullFloat64 into a *float64. NULL columns
// become nil, so callers can distinguish "unknown" (legacy / non-
// Parallax sources) from "explicitly zero".
func floatFromNull(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// nullIfEmptyJSON maps an empty JSON value to SQL NULL.
func nullIfEmptyJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// nullableFloatPtr is the inverse: a *float64 → sql.NullFloat64
// suitable as a driver argument. nil pointer → SQL NULL.
func nullableFloatPtr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// storedCost is the cost column Upsert writes. Zero normally means "no
// price" (NULL), but a user-priced row with cost 0 is an explicit Free
// (MarkFree) and must stay 0, or a backup restored through Upsert would
// turn Free into "unknown".
func storedCost(c Charge) any {
	if c.CostSource == "user" {
		return c.Cost
	}
	return nullIfZero(c.Cost)
}

// storedPricePerKWh is storedCost for price_per_kwh.
func storedPricePerKWh(c Charge) any {
	if c.CostSource == "user" {
		return c.PricePerKWh
	}
	return nullIfZero(c.PricePerKWh)
}

// ConfirmNetwork records that the user agreed with an auto-detected DC
// fast charge network: network_state becomes 'confirmed'; cost, network
// and cost_source stay. Only an estimate can be confirmed (cost_source
// 'network_estimate' in an open state); a charge nothing matched has no
// estimate to confirm, so the user picks a network instead. Returns rows
// affected: 0 means not found for this user or not confirmable.
func (s *Store) ConfirmNetwork(ctx context.Context, externalID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET network_state = 'confirmed',
		    updated_at = NOW()
		WHERE user_id = $1 AND external_id = $2
		  AND cost_source = 'network_estimate'
		  AND network_state IN ('estimated', 'needs_confirmation')
	`, s.userID, externalID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SetNetworkPrice records the user's own choice of DC fast charge
// network: cost = pricePerKWh × the session's energy, cost_source
// 'user', network_state 'confirmed' (the user picked it; confidence and
// evidence stay for audit). Returns rows affected (0: not found for this user, or the
// session has no energy to price).
func (s *Store) SetNetworkPrice(ctx context.Context, externalID, network, currency string, pricePerKWh float64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET price_per_kwh = $3,
		    cost = round($3::numeric * energy_added_kwh::numeric, 4),
		    currency = COALESCE(NULLIF($5, ''), currency),
		    network = NULLIF($4, ''),
		    cost_source = 'user',
		    network_state = 'confirmed',
		    estimate_month = NULL,
		    estimate_url = NULL,
		    legacy_cost = NULL,
		    legacy_price_per_kwh = NULL,
		    updated_at = NOW()
		WHERE user_id = $1 AND external_id = $2
		  AND COALESCE(energy_added_kwh, 0) > 0
	`, s.userID, externalID, pricePerKWh, network, currency)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ApplyNetworkRate prices the user's past sessions at their own rate for one
// network, only those that had no rate of theirs: the sessions priced from a
// typical public price for that network (cost_source observed_estimate), and
// the unpriced fast charges the detection put at that network (slug is the
// first network with a slug in network_evidence, as the ledger's rate_slug).
// Their price becomes the rate times the session's energy, cost_source
// network_estimate, the estimate's report cleared; network_state is left as
// the detection set it so the user can still confirm. Sessions with a Rivian
// receipt, a user price (including free) or a Rivian-billed price are never
// touched, and neither is a session still being recorded (it ended within
// the last 30 minutes). Returns the number of sessions priced.
func (s *Store) ApplyNetworkRate(ctx context.Context, slug, network, currency string, pricePerKWh float64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET price_per_kwh = $4,
		    cost = round($4::numeric * energy_added_kwh::numeric, 4),
		    currency = $5,
		    network = $3,
		    cost_source = 'network_estimate',
		    estimate_month = NULL,
		    estimate_url = NULL,
		    legacy_cost = NULL,
		    legacy_price_per_kwh = NULL,
		    updated_at = NOW()
		WHERE user_id = $1
		  AND COALESCE(energy_added_kwh, 0) > 0
		  AND receipt_txn_id IS NULL
		  AND ended_at < NOW() - interval '30 minutes'
		  AND (
		        (cost_source = 'observed_estimate' AND network = $3)
		     OR (COALESCE(cost_source, '') NOT IN ('user', 'rivian', 'home_rate', 'network_estimate', 'observed_estimate')
		         AND COALESCE(cost, 0) = 0
		         AND COALESCE(max_power_kw, 0) >= 50
		         AND energy_added_kwh >= 1
		         AND energy_added_kwh / (GREATEST(COALESCE(NULLIF(active_seconds, 0), EXTRACT(EPOCH FROM ended_at - started_at)), 1) / 3600.0) >= 20
		         AND (SELECT e->>'slug'
		                FROM jsonb_array_elements(COALESCE(network_evidence->'candidates', '[]'::jsonb)) WITH ORDINALITY AS t(e, i)
		               WHERE COALESCE(e->>'slug', '') <> ''
		               ORDER BY i LIMIT 1) = $2)
		      )
	`, s.userID, slug, network, pricePerKWh, currency)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetByExternalID returns one of this user's charges, or (nil, nil).
func (s *Store) GetByExternalID(ctx context.Context, externalID string) (*Charge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8,
		       c.thermal_kwh, c.charge_limit_pct, COALESCE(c.active_seconds,0)::float8,
		       CASE WHEN c.cost_source = 'user' AND c.cost IS NULL THEN '' ELSE COALESCE(c.cost_source,'') END, COALESCE(c.network,''), COALESCE(c.place,''),
		       COALESCE(c.network_state,''), c.network_confidence, c.network_evidence,
		       c.receipt_cost::float8, COALESCE(c.receipt_currency,''), COALESCE(c.receipt_txn_id,''),
		       COALESCE(c.estimate_month,''), COALESCE(c.estimate_url,''),
		       COALESCE(c.place_override,''), COALESCE(c.location_override,'')
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1 AND c.external_id = $2`, s.userID, externalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var c Charge
	var thermal, limit, netConf sql.NullFloat64
	var netEv []byte
	var recCost sql.NullFloat64
	if err := rows.Scan(&c.ID, &c.VehicleID,
		&c.StartedAt, &c.EndedAt,
		&c.StartSoCPct, &c.EndSoCPct,
		&c.EnergyAddedKWh, &c.MilesAdded,
		&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
		&c.Lat, &c.Lon, &c.Source,
		&c.Cost, &c.Currency, &c.PricePerKWh,
		&thermal, &limit, &c.ActiveSeconds, &c.CostSource, &c.Network, &c.Place,
		&c.NetworkState, &netConf, &netEv, &recCost, &c.ReceiptCurrency, &c.ReceiptTxnID, &c.EstimateMonth, &c.EstimateURL,
			&c.PlaceOverride, &c.LocationOverride,
	); err != nil {
		return nil, err
	}
	c.ThermalKWh = floatFromNull(thermal)
	c.ChargeLimitPct = floatFromNull(limit)
	c.NetworkConfidence = floatFromNull(netConf)
	c.NetworkEvidence = netEv
	c.ReceiptCost = floatFromNull(recCost)
	c.StartedAt, c.EndedAt = c.StartedAt.UTC(), c.EndedAt.UTC()
	return &c, rows.Err()
}

// NetworkToConfirm counts this user's auto-detected charges still open
// for verification: toConfirm covers both open states, needsAttention
// only 'needs_confirmation'.
func (s *Store) NetworkToConfirm(ctx context.Context) (toConfirm, needsAttention int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE network_state = 'needs_confirmation')
		FROM charges
		WHERE user_id = $1 AND network_state IN ('estimated', 'needs_confirmation')
	`, s.userID).Scan(&toConfirm, &needsAttention)
	return toConfirm, needsAttention, err
}

// VerifiedNetwork is a DC fast charge of the user's own whose network
// they confirmed or set themselves, and how far it is from a point.
type VerifiedNetwork struct {
	Network string
	DistM   float64
}

// NearestVerifiedNetwork returns the user's nearest DC fast charge within
// radiusM of (lat, lon) whose network is settled: confirmed, or a user
// price that names a network. It is auto-detection's site memory, so a
// co-located site the user already resolved isn't asked about again.
// ok=false when there is none (or no position).
func (s *Store) NearestVerifiedNetwork(ctx context.Context, lat, lon, radiusM float64) (VerifiedNetwork, bool, error) {
	if (lat == 0 && lon == 0) || radiusM <= 0 {
		return VerifiedNetwork{}, false, nil
	}
	// A bounding box on the index-friendly columns, then exact distance.
	dLat := radiusM / 111_320.0
	dLon := dLat / math.Max(math.Cos(lat*math.Pi/180), 0.01)
	rows, err := s.db.QueryContext(ctx, `
		SELECT network, lat, lon
		FROM charges
		WHERE user_id = $1
		  AND COALESCE(network, '') <> ''
		  AND (network_state = 'confirmed' OR cost_source = 'user')
		  AND COALESCE(max_power_kw, 0) >= 50
		  AND lat BETWEEN $2::float8 AND $3::float8
		  AND lon BETWEEN $4::float8 AND $5::float8
		ORDER BY ended_at DESC
		LIMIT 50`, s.userID, lat-dLat, lat+dLat, lon-dLon, lon+dLon)
	if err != nil {
		return VerifiedNetwork{}, false, err
	}
	defer rows.Close()
	best, found := VerifiedNetwork{}, false
	for rows.Next() {
		var n string
		var la, lo float64
		if err := rows.Scan(&n, &la, &lo); err != nil {
			return VerifiedNetwork{}, false, err
		}
		if d := haversineM(lat, lon, la, lo); d <= radiusM && (!found || d < best.DistM) {
			best, found = VerifiedNetwork{Network: n, DistM: d}, true
		}
	}
	return best, found, rows.Err()
}

// LiveOverlap describes the live session another recorder wrote over
// the same time range: where it ends and what it already covers.
type LiveOverlap struct {
	EndedAt   time.Time
	EndSoCPct float64
	EnergyKWh float64
}

// LiveOverlap returns the latest-ending live charge for this vehicle,
// other than excludeID, whose time range overlaps [start, end), or nil.
// The recorder uses it when the overlap guard (migration 0041) rejected
// one of its sessions: the session's tail after that row is still worth
// writing if the other recorder stopped.
func (s *Store) LiveOverlap(ctx context.Context, rivianVehicleID, excludeID string, start, end time.Time) (*LiveOverlap, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	var o LiveOverlap
	err = s.db.QueryRowContext(ctx, `
		SELECT ended_at, COALESCE(end_soc_pct, 0), COALESCE(energy_added_kwh, 0)
		  FROM charges
		 WHERE user_id = $1 AND vehicle_id = $2 AND source = 'live' AND external_id <> $3
		   AND tstzrange(started_at, GREATEST(ended_at, started_at)) && tstzrange($4::timestamptz, GREATEST($5::timestamptz, $4::timestamptz))
		 ORDER BY ended_at DESC
		 LIMIT 1`, s.userID, vid, excludeID, start.UTC(), end.UTC()).Scan(&o.EndedAt, &o.EndSoCPct, &o.EnergyKWh)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.EndedAt = o.EndedAt.UTC()
	return &o, nil
}

// RestampResult counts what RestampPlace changed (or would change).
type RestampResult struct {
	DryRun               bool       `json:"dry_run"`
	Changed              int        `json:"changed"`
	ToHome               int        `json:"to_home"`
	ToAway               int        `json:"to_away"`
	Unchanged            int        `json:"unchanged"`
	SkippedUserPriced    int        `json:"skipped_user_priced"`
	// SkippedOverridden: charges whose home/away the user set by hand, never re-checked.
	SkippedOverridden int `json:"skipped_overridden"`
	WouldPriceAtHomeRate int        `json:"would_price_at_home_rate"`
	TotalCostDelta       float64    `json:"total_cost_delta"`
	Currency             string     `json:"currency"`
	RestampID            *uuid.UUID `json:"restamp_id"`
}

// RestampOptions configures RestampPlace.
type RestampOptions struct {
	HomeLat, HomeLon float64
	RadiusM          float64
	// HomeRate and Currency price AC charges that become home and are
	// unpriced; HomeRate <= 0 prices nothing.
	HomeRate float64
	Currency string
	// IsDCFC is the shared fast-charging rule (rivian.IsDCFCSession);
	// fast charges are never priced at the home rate. nil treats every
	// session as AC.
	IsDCFC func(peakKW, energyKWh, activeSeconds float64, span time.Duration) bool
	DryRun bool
}

// RestampPlace is the "my old home pin was wrong" correction: it
// recomputes the recorded place ('home' within RadiusM of the home,
// else 'away') on this user's charges that have a position, and prices
// the AC charges that become home and were unpriced (no cost, no cost
// source, some energy) at the home rate with cost_source 'home_rate', as
// the recorder would have with the right pin. Rows the user priced are
// never touched; no other price changes. One transaction; each changed
// row is logged in charge_restamps under a new restamp id so
// UndoRestamp can revert it. DryRun counts without writing.
func (s *Store) RestampPlace(ctx context.Context, o RestampOptions) (RestampResult, error) {
	res := RestampResult{DryRun: o.DryRun, Currency: o.Currency}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(place, ''), lat, lon, COALESCE(cost_source, ''), cost IS NOT NULL,
		       COALESCE(energy_added_kwh, 0), COALESCE(max_power_kw, 0), COALESCE(active_seconds, 0)::float8,
		       started_at, ended_at
		  FROM charges
		 WHERE user_id = $1
		   AND place_override IS NULL
		   AND lat IS NOT NULL AND lon IS NOT NULL AND NOT (lat = 0 AND lon = 0)
		 ORDER BY started_at
		   FOR UPDATE`, s.userID)
	if err != nil {
		return res, err
	}
	type change struct {
		id        uuid.UUID
		prev, now string
		cost      float64 // > 0 when priced by this correction
		energy    float64
	}
	var changes []change
	for rows.Next() {
		var (
			id                   uuid.UUID
			place, source        string
			lat, lon             float64
			hasCost              bool
			energy, peak, active float64
			started, ended       time.Time
		)
		if err := rows.Scan(&id, &place, &lat, &lon, &source, &hasCost, &energy, &peak, &active, &started, &ended); err != nil {
			rows.Close()
			return res, err
		}
		if source == "user" {
			continue
		}
		now := "away"
		if haversineM(o.HomeLat, o.HomeLon, lat, lon) <= o.RadiusM {
			now = "home"
		}
		if now == place {
			res.Unchanged++
			continue
		}
		c := change{id: id, prev: place, now: now, energy: energy}
		if now == "home" {
			res.ToHome++
			dcfc := o.IsDCFC != nil && o.IsDCFC(peak, energy, active, ended.Sub(started))
			if o.HomeRate > 0 && source == "" && !hasCost && energy > 0 && !dcfc {
				c.cost = math.Round(o.HomeRate*energy*1e4) / 1e4
				res.WouldPriceAtHomeRate++
				res.TotalCostDelta += c.cost
			}
		} else {
			res.ToAway++
		}
		changes = append(changes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Changed = len(changes)
	res.TotalCostDelta = math.Round(res.TotalCostDelta*100) / 100
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM charges WHERE user_id = $1 AND cost_source = 'user'`, s.userID,
	).Scan(&res.SkippedUserPriced); err != nil {
		return res, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM charges WHERE user_id = $1 AND place_override IS NOT NULL`, s.userID,
	).Scan(&res.SkippedOverridden); err != nil {
		return res, err
	}
	if o.DryRun || len(changes) == 0 {
		return res, nil
	}
	restampID := uuid.New()
	for _, c := range changes {
		if c.cost > 0 {
			_, err = tx.ExecContext(ctx, `
				UPDATE charges SET place = $2::text, cost = $3::numeric, price_per_kwh = $4::numeric,
				       currency = NULLIF($5::text, ''), cost_source = 'home_rate', updated_at = NOW()
				 WHERE id = $1 AND place_override IS NULL`, c.id, c.now, c.cost, o.HomeRate, o.Currency)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE charges SET place = $2::text, updated_at = NOW() WHERE id = $1 AND place_override IS NULL`, c.id, c.now)
		}
		if err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO charge_restamps (restamp_id, user_id, charge_id, prev_place, stamped_cost)
			VALUES ($1, $2, $3, NULLIF($4::text, ''), $5::numeric)`,
			restampID, s.userID, c.id, c.prev, nullIfZero(c.cost)); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.RestampID = &restampID
	return res, nil
}

// ErrNoRestamp is returned by UndoRestamp when there's nothing to undo.
var ErrNoRestamp = errors.New("no restamp to undo")

// ErrRestampNotLatest is returned by UndoRestamp for a correction that
// is still undoable but isn't the user's latest: undoing it would put
// back places a newer correction already changed. Undo the newer ones
// first.
var ErrRestampNotLatest = errors.New("only the latest restamp can be undone")

// UndoResult reports what UndoRestamp reverted.
type UndoResult struct {
	RestampID     uuid.UUID `json:"restamp_id"`
	RestoredPlace int       `json:"restored_place"`
	Unpriced      int       `json:"unpriced"`
}

// UndoRestamp reverts the user's latest correction not yet undone
// (restampID, if given, must be that one: ErrRestampNotLatest for an
// older pending one, ErrNoRestamp for an unknown or undone one): every
// logged charge gets its previous place
// back, and a home-rate price the correction added is removed only if
// the row still carries exactly that price (a price the user set or
// edited since is kept). One transaction; a correction can be undone
// once.
func (s *Store) UndoRestamp(ctx context.Context, restampID *uuid.UUID) (UndoResult, error) {
	var res UndoResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// The latest pending correction, locked so concurrent undos (and
	// the check below) serialize.
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT restamp_id FROM charge_restamps
		 WHERE user_id = $1 AND undone_at IS NULL
		 ORDER BY created_at DESC LIMIT 1
		   FOR UPDATE`, s.userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return res, ErrNoRestamp
	}
	if err != nil {
		return res, err
	}
	if restampID != nil && *restampID != id {
		var pending bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM charge_restamps
			                WHERE restamp_id = $1 AND user_id = $2 AND undone_at IS NULL)`,
			*restampID, s.userID).Scan(&pending); err != nil {
			return res, err
		}
		if pending {
			return res, ErrRestampNotLatest
		}
		return res, ErrNoRestamp
	}
	type entry struct {
		charge  uuid.UUID
		prev    sql.NullString
		stamped sql.NullFloat64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT charge_id, prev_place, stamped_cost::float8 FROM charge_restamps
		 WHERE restamp_id = $1 AND user_id = $2 AND undone_at IS NULL
		   FOR UPDATE`, id, s.userID)
	if err != nil {
		return res, err
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.charge, &e.prev, &e.stamped); err != nil {
			rows.Close()
			return res, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(entries) == 0 {
		return res, ErrNoRestamp
	}
	for _, e := range entries {
		// A row the user has since set by hand keeps their word.
		if _, err := tx.ExecContext(ctx, `UPDATE charges SET place = $2::text, updated_at = NOW() WHERE id = $1 AND user_id = $3 AND place_override IS NULL`,
			e.charge, e.prev, s.userID); err != nil {
			return res, err
		}
		res.RestoredPlace++
		if e.stamped.Valid {
			r, err := tx.ExecContext(ctx, `
				UPDATE charges SET cost = NULL, price_per_kwh = NULL, currency = NULL, cost_source = NULL, updated_at = NOW()
				 WHERE id = $1 AND user_id = $3 AND cost_source = 'home_rate' AND cost = $2::numeric`,
				e.charge, e.stamped.Float64, s.userID)
			if err != nil {
				return res, err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Unpriced++
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE charge_restamps SET undone_at = NOW() WHERE restamp_id = $1 AND user_id = $2`,
		id, s.userID); err != nil {
		return res, err
	}
	res.RestampID = id
	return res, tx.Commit()
}

// haversineM is the great-circle distance in meters.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// LiveCovers reports whether a live (recorder) charge for this vehicle
// overlaps [start, end). Importers use it to leave sessions the
// recorder already captured alone instead of double-counting them.
func (s *Store) LiveCovers(ctx context.Context, rivianVehicleID string, start, end time.Time) (bool, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return false, err
	}
	var ok bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM charges
		   WHERE user_id = $1 AND vehicle_id = $2 AND source = 'live'
		     AND tstzrange(started_at, GREATEST(ended_at, started_at)) && tstzrange($3::timestamptz, GREATEST($4::timestamptz, $3::timestamptz)))`,
		s.userID, vid, start.UTC(), end.UTC()).Scan(&ok)
	return ok, err
}

// ChartCharge is the part of a charge the iPhone's charts need: when and the charge
// at each end. No ids, costs, places or coordinates.
type ChartCharge struct {
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	StartSoCPct float64   `json:"start_soc_pct"`
	EndSoCPct   float64   `json:"end_soc_pct"`
	// ChargeLimitPct is the limit the truck was set to for the charge (1 to 100),
	// null when unknown (older charges are backfilled where the samples still exist).
	ChargeLimitPct *float64 `json:"charge_limit_pct"`
}

// MaxChartRows bounds one chart answer (newest rows win when a window holds more).
const MaxChartRows = 5000

// ListChartPoints returns the user's charges that started in [since, until), oldest
// first, narrowed to ChartCharge; the same rows the list shows (at least a minute).
// rivianVehicleID "" means every truck; at most MaxChartRows, the newest if more.
func (s *Store) ListChartPoints(ctx context.Context, since, until time.Time, rivianVehicleID string) ([]ChartCharge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.started_at, c.ended_at, COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0), c.charge_limit_pct
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		  AND c.ended_at - c.started_at >= interval '1 minute'
		  AND c.started_at >= $2 AND c.started_at < $3
		  AND ($4 = '' OR v.rivian_vehicle_id = $4)
		ORDER BY c.started_at DESC
		LIMIT $5`, s.userID, since, until, rivianVehicleID, MaxChartRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChartCharge
	for rows.Next() {
		var c ChartCharge
		var limit sql.NullFloat64
		if err := rows.Scan(&c.StartedAt, &c.EndedAt, &c.StartSoCPct, &c.EndSoCPct, &limit); err != nil {
			return nil, err
		}
		c.ChargeLimitPct = floatFromNull(limit)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// LocationRecorded is the location override "where the truck was" (the charge's own lat/lon).
const LocationRecorded = "recorded"

// ErrNoSuchCharge: no charge of this user with that id.
var ErrNoSuchCharge = errors.New("no such charge")

// SetPlaceOverride records the user's own "home" or "away" for one of their charges (by its external id) and sets
// place to it; nothing re-classifies the row afterwards. "" clears the override and gives the place back to the
// home location: "home" within radiusM of (homeLat, homeLon), else "away"; with no home (homeSet false) or no
// position, no place.
func (s *Store) SetPlaceOverride(ctx context.Context, externalID, place string, homeSet bool, homeLat, homeLon, radiusM float64) error {
	var res sql.Result
	var err error
	switch place {
	case "home", "away":
		res, err = s.db.ExecContext(ctx, `
			UPDATE charges SET place_override = $3::text, place = $3::text, updated_at = NOW()
			 WHERE user_id = $1 AND external_id = $2`, s.userID, externalID, place)
	case "":
		var lat, lon sql.NullFloat64
		if err := s.db.QueryRowContext(ctx, `SELECT lat, lon FROM charges WHERE user_id = $1 AND external_id = $2`,
			s.userID, externalID).Scan(&lat, &lon); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSuchCharge
			}
			return err
		}
		var derived any
		if homeSet && lat.Valid && lon.Valid && !(lat.Float64 == 0 && lon.Float64 == 0) {
			derived = "away"
			if haversineM(homeLat, homeLon, lat.Float64, lon.Float64) <= radiusM {
				derived = "home"
			}
		}
		res, err = s.db.ExecContext(ctx, `
			UPDATE charges SET place_override = NULL, place = $3::text, updated_at = NOW()
			 WHERE user_id = $1 AND external_id = $2`, s.userID, externalID, derived)
	default:
		return fmt.Errorf("place must be home, away or empty")
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchCharge
	}
	return nil
}

// SetLocationOverride says whether one of the user's charges happened where the truck was (recorded true: maps
// pin its stored lat/lon and never snap to a public charger) or is shown as recorded with the usual snap (false).
func (s *Store) SetLocationOverride(ctx context.Context, externalID string, recorded bool) error {
	var v any
	if recorded {
		v = LocationRecorded
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges SET location_override = $3::text, updated_at = NOW()
		 WHERE user_id = $1 AND external_id = $2`, s.userID, externalID, v)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchCharge
	}
	return nil
}
