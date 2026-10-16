package ledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Load reads the user's charges and drives whose time range intersects
// [from, to), optionally narrowed to one Rivian vehicle id. Read-only:
// plain SELECTs filtered by user_id (RLS is the backstop, not the
// filter). Rows keep NULLs as nil so the ledger can tell "not recorded"
// from zero.
func Load(ctx context.Context, d *sql.DB, userID uuid.UUID, vehicleID string, from, to time.Time) ([]Charge, []Drive, error) {
	if d == nil {
		return nil, nil, errors.New("ledger: nil db")
	}
	if userID == uuid.Nil {
		return nil, nil, errors.New("ledger: zero user id")
	}
	cs, err := loadCharges(ctx, d, userID, vehicleID, from, to)
	if err != nil {
		return nil, nil, err
	}
	ds, err := loadDrives(ctx, d, userID, vehicleID, from, to)
	if err != nil {
		return nil, nil, err
	}
	return cs, ds, nil
}

// pricingColumnsSQL reports whether migration 0040's charges.cost_source
// and charges.network exist. The ledger ships independently of that
// migration, so it reads them only when present; without them every row
// is treated as legacy (cost_source derived at query time).
const pricingColumnsSQL = `
	SELECT count(*) = 2 FROM information_schema.columns
	WHERE table_schema = current_schema()
	  AND table_name = 'charges'
	  AND column_name IN ('cost_source', 'network')`

const networkStateColumnsSQL = `SELECT count(*) = 3 FROM information_schema.columns
	WHERE table_schema = current_schema()
	  AND table_name = 'charges'
	  AND column_name IN ('network_state', 'network_confidence', 'network_evidence')`

// receiptColumnsSQL reports whether migration 0055's charges.receipt_cost and
// rivian_receipts exist, like the other optional pricing columns.
const receiptColumnsSQL = `SELECT (SELECT count(*) = 2 FROM information_schema.columns
	WHERE table_schema = current_schema() AND table_name = 'charges'
	  AND column_name IN ('receipt_cost', 'receipt_currency'))
	AND EXISTS (SELECT 1 FROM information_schema.tables
	WHERE table_schema = current_schema() AND table_name = 'rivian_receipts')`

func loadCharges(ctx context.Context, d *sql.DB, userID uuid.UUID, vehicleID string, from, to time.Time) ([]Charge, error) {
	var havePricing bool
	if err := d.QueryRowContext(ctx, pricingColumnsSQL).Scan(&havePricing); err != nil {
		return nil, err
	}
	pricingCols := `NULL::text, NULL::text`
	if havePricing {
		pricingCols = `c.cost_source, c.network`
	}
	var haveNetState bool
	if err := d.QueryRowContext(ctx, networkStateColumnsSQL).Scan(&haveNetState); err != nil {
		return nil, err
	}
	pricingCols += `, ` + networkStateCols(haveNetState)
	var haveReceipts bool
	if err := d.QueryRowContext(ctx, receiptColumnsSQL).Scan(&haveReceipts); err != nil {
		return nil, err
	}
	billedCols, billedJoin := `, NULL::float8, NULL::text, NULL::text, NULL::text, NULL::boolean`, ``
	if haveReceipts {
		// The receipt row carries the vendor, charger name and the home flag.
		// Joined on the receipt's transaction id (unique per user) through the
		// charge's own receipt_txn_id, never on rivian_receipts.charge_id: that
		// column is not unique, and a second receipt pointing at the same
		// charge would make the join return the charge twice.
		billedCols = `, c.receipt_cost::float8, c.receipt_currency, rr.vendor, rr.charger_name, rr.is_home_charger`
		billedJoin = ` LEFT JOIN rivian_receipts rr ON rr.user_id = c.user_id AND rr.transaction_id = c.receipt_txn_id`
	}
	rows, err := d.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       c.start_soc_pct, c.end_soc_pct,
		       c.energy_added_kwh, c.max_power_kw,
		       c.cost::float8, COALESCE(c.currency, ''), c.price_per_kwh::float8,
		       COALESCE(c.final_state, ''), c.source,
		       c.lat, c.lon, c.created_at, c.active_seconds::float8, COALESCE(c.place, ''), `+pricingCols+billedCols+`
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id`+billedJoin+`
		WHERE c.user_id = $1
		  AND c.started_at < $3
		  AND c.ended_at >= $2
		  AND ($4 = '' OR v.rivian_vehicle_id = $4)
		ORDER BY c.started_at, c.external_id`,
		userID, from, to, vehicleID)
	if err != nil {
		return nil, err
	}
	return scanCharges(rows)
}

func scanCharges(rows *sql.Rows) ([]Charge, error) {
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		var startSoC, endSoC, energy, peak, cost, ppk, lat, lon, active sql.NullFloat64
		var costSource, network, netState sql.NullString
		var netConf sql.NullFloat64
		var netEv []byte
		var billed sql.NullFloat64
		var billedCur, billedVendor, billedCharger sql.NullString
		var billedHome sql.NullBool
		if err := rows.Scan(&c.ID, &c.VehicleID,
			&c.StartedAt, &c.EndedAt,
			&startSoC, &endSoC,
			&energy, &peak,
			&cost, &c.Currency, &ppk,
			&c.FinalState, &c.Source,
			&lat, &lon, &c.CreatedAt, &active, &c.Place,
			&costSource, &network,
			&netState, &netConf, &netEv,
			&billed, &billedCur, &billedVendor, &billedCharger, &billedHome,
		); err != nil {
			return nil, err
		}
		c.BilledCost, c.BilledCurrency = ptr(billed), billedCur.String
		c.BilledVendor, c.BilledCharger, c.BilledHome = billedVendor.String, billedCharger.String, billedHome.Valid && billedHome.Bool
		c.NetworkState, c.NetworkConfidence, c.NetworkEvidence = netState.String, ptr(netConf), netEv
		c.StartedAt, c.EndedAt, c.CreatedAt = c.StartedAt.UTC(), c.EndedAt.UTC(), c.CreatedAt.UTC()
		c.StartSoC, c.EndSoC = ptr(startSoC), ptr(endSoC)
		c.EnergyKWh, c.PeakKW = ptr(energy), ptr(peak)
		c.Cost, c.PricePerKWh = ptr(cost), ptr(ppk)
		c.Lat, c.Lon = ptr(lat), ptr(lon)
		c.ActiveSeconds = ptr(active)
		c.CostSource, c.Network = costSource.String, network.String
		out = append(out, c)
	}
	return out, rows.Err()
}

func loadDrives(ctx context.Context, d *sql.DB, userID uuid.UUID, vehicleID string, from, to time.Time) ([]Drive, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT dr.external_id, v.rivian_vehicle_id,
		       dr.started_at, dr.ended_at,
		       dr.distance_mi, dr.energy_used_kwh, dr.source, dr.created_at,
		       dr.start_lat, dr.start_lon, dr.end_lat, dr.end_lon,
		       dr.start_soc_pct, dr.end_soc_pct
		FROM drives dr
		JOIN vehicles v ON v.id = dr.vehicle_id
		WHERE dr.user_id = $1
		  AND dr.started_at < $3
		  AND dr.ended_at >= $2
		  AND ($4 = '' OR v.rivian_vehicle_id = $4)
		ORDER BY dr.started_at, dr.external_id`,
		userID, from, to, vehicleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var dr Drive
		var dist, energy, sLat, sLon, eLat, eLon, sSoC, eSoC sql.NullFloat64
		if err := rows.Scan(&dr.ID, &dr.VehicleID, &dr.StartedAt, &dr.EndedAt,
			&dist, &energy, &dr.Source, &dr.CreatedAt, &sLat, &sLon, &eLat, &eLon, &sSoC, &eSoC); err != nil {
			return nil, err
		}
		dr.StartSoC, dr.EndSoC = ptr(sSoC), ptr(eSoC)
		dr.StartLat, dr.StartLon, dr.EndLat, dr.EndLon = ptr(sLat), ptr(sLon), ptr(eLat), ptr(eLon)
		dr.StartedAt, dr.EndedAt, dr.CreatedAt = dr.StartedAt.UTC(), dr.EndedAt.UTC(), dr.CreatedAt.UTC()
		dr.DistanceMi, dr.EnergyKWh = ptr(dist), ptr(energy)
		out = append(out, dr)
	}
	return out, rows.Err()
}

func ptr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// networkStateCols is the select list for the verify-state columns, or
// typed NULLs on a database that hasn't applied migration 0051.
func networkStateCols(have bool) string {
	if have {
		return `c.network_state, c.network_confidence::float8, c.network_evidence`
	}
	return `NULL::text, NULL::float8, NULL::jsonb`
}

// CountToConfirm counts the user's auto-detected charges still open
// for verification, across all time and vehicles: open is both open
// states, attention only needs_confirmation. Zero on a database
// without migration 0051.
func CountToConfirm(ctx context.Context, d *sql.DB, userID uuid.UUID) (open, attention int, err error) {
	var have bool
	if err := d.QueryRowContext(ctx, networkStateColumnsSQL).Scan(&have); err != nil || !have {
		return 0, 0, err
	}
	err = d.QueryRowContext(ctx, `
		SELECT count(*), count(*) FILTER (WHERE network_state = 'needs_confirmation')
		FROM charges
		WHERE user_id = $1 AND network_state IN ('estimated', 'needs_confirmation')`,
		userID).Scan(&open, &attention)
	return open, attention, err
}
