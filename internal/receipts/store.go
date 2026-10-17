package receipts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// Store is the Postgres side: rivian_receipts, rivian_receipt_sync and the
// charges.receipt_* columns (migration 0055). It never touches a charge's
// cost, cost_source or price_per_kwh.
type Store struct{ db *sql.DB }

// NewStore wraps a pool.
func NewStore(d *sql.DB) *Store { return &Store{db: d} }

// ErrOptedOut: the user isn't (or is no longer) opted in to receipts, so
// nothing was written.
var ErrOptedOut = errors.New("receipts: user not opted in")

// optedIn runs fn in a transaction that first checks, and share-locks, the
// user's opt-in (settings.RivianReceiptsOptInLocked). Every write of the
// sync goes through it, so a sync already running when the user opts out
// can't write after the opt-out's Purge.
func (s *Store) optedIn(ctx context.Context, uid uuid.UUID, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	on, err := settings.RivianReceiptsOptInLocked(ctx, tx, uid)
	if err != nil {
		return err
	}
	if !on {
		return ErrOptedOut
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimSummaries records an attempt to fetch the user's summaries and
// reports whether this caller won it: no attempt in the last minInterval,
// across pods and restarts.
func (s *Store) ClaimSummaries(ctx context.Context, uid uuid.UUID, minInterval time.Duration) (bool, error) {
	return s.claim(ctx, uid, minInterval, true)
}

// ClaimDetails is the same for a details-only pass (no summaries call).
func (s *Store) ClaimDetails(ctx context.Context, uid uuid.UUID, minInterval time.Duration) (bool, error) {
	return s.claim(ctx, uid, minInterval, false)
}

func (s *Store) claim(ctx context.Context, uid uuid.UUID, minInterval time.Duration, summaries bool) (bool, error) {
	col := "last_attempt_at"
	if summaries {
		col = "last_summary_at"
	}
	// The conditional upsert returns a row only when it inserted or the
	// guard held; a user claimed within the interval returns none.
	q := fmt.Sprintf(`
		INSERT INTO rivian_receipt_sync (user_id, last_attempt_at, last_summary_at)
		VALUES ($1, NOW(), CASE WHEN $3::boolean THEN NOW() END)
		ON CONFLICT (user_id) DO UPDATE
		   SET last_attempt_at = NOW(),
		       last_summary_at = CASE WHEN $3::boolean THEN NOW() ELSE rivian_receipt_sync.last_summary_at END
		 WHERE rivian_receipt_sync.%s IS NULL
		    OR rivian_receipt_sync.%s < NOW() - $2::interval
		RETURNING 1`, col, col)
	won := false
	err := s.optedIn(ctx, uid, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, q, uid, fmt.Sprintf("%d microseconds", minInterval.Microseconds()), summaries).Scan(&one)
		if err == sql.ErrNoRows {
			return nil
		}
		won = err == nil
		return err
	})
	if errors.Is(err, ErrOptedOut) {
		return false, nil
	}
	return won, err
}

// MarkResult stores the outcome of a claimed attempt. The error text is
// kept short and is only ever our own classification, never a response
// body.
func (s *Store) MarkResult(ctx context.Context, uid uuid.UUID, runErr error) error {
	return s.optedIn(ctx, uid, func(tx *sql.Tx) error {
		if runErr == nil {
			_, err := tx.ExecContext(ctx, `UPDATE rivian_receipt_sync SET last_success_at = NOW(), last_error = NULL WHERE user_id = $1`, uid)
			return err
		}
		msg := runErr.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		_, err := tx.ExecContext(ctx, `UPDATE rivian_receipt_sync SET last_error = $2 WHERE user_id = $1`, uid, msg)
		return err
	})
}

// UpsertReceipts stores the summaries, refreshing amounts of rows we
// already have (a receipt can finalize later) and keeping detail and
// match state.
func (s *Store) UpsertReceipts(ctx context.Context, uid uuid.UUID, in []rivian.SessionSummary) error {
	return s.optedIn(ctx, uid, func(tx *sql.Tx) error { return upsertReceipts(ctx, tx, uid, in) })
}

func upsertReceipts(ctx context.Context, tx *sql.Tx, uid uuid.UUID, in []rivian.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency, energy_kwh, range_added_km, vendor, city)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8, NULLIF($9,''), NULLIF($10,''))
		ON CONFLICT (user_id, transaction_id) DO UPDATE SET
		    started_at = EXCLUDED.started_at, ended_at = EXCLUDED.ended_at,
		    total_cost = EXCLUDED.total_cost, currency = EXCLUDED.currency,
		    energy_kwh = EXCLUDED.energy_kwh, range_added_km = EXCLUDED.range_added_km,
		    vendor = EXCLUDED.vendor, city = EXCLUDED.city, fetched_at = NOW()`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range in {
		if _, err := stmt.ExecContext(ctx, uid, r.TransactionID, r.StartedAt, r.EndedAt,
			nullFloat(r.TotalCost), r.Currency, nullFloat(r.EnergyKWh), nullFloat(r.RangeAddedKm), r.Vendor, r.City); err != nil {
			return err
		}
	}
	return nil
}

// Unmatched lists the user's receipts that no charge claims yet.
func (s *Store) Unmatched(ctx context.Context, uid uuid.UUID) ([]Receipt, error) {
	rows, err := s.db.QueryContext(ctx, receiptSelect+` WHERE user_id = $1 AND charge_id IS NULL ORDER BY started_at`, uid)
	if err != nil {
		return nil, err
	}
	return scanReceipts(rows)
}

// CandidateCharges lists the user's charges without a receipt whose
// window touches [from, to].
func (s *Store) CandidateCharges(ctx context.Context, uid uuid.UUID, from, to time.Time) ([]ChargeRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, started_at, ended_at, COALESCE(energy_added_kwh, 0)::float8
		  FROM charges
		 WHERE user_id = $1 AND receipt_txn_id IS NULL
		   AND ended_at >= $2 AND started_at <= $3
		 ORDER BY started_at`, uid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeRef
	for rows.Next() {
		var c ChargeRef
		if err := rows.Scan(&c.ID, &c.StartedAt, &c.EndedAt, &c.EnergyKWh); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Apply links each pair: the receipt gets charge_id, the charge gets the
// billed amount in receipt_*. A charge's cost, cost_source and
// price_per_kwh are left alone.
func (s *Store) Apply(ctx context.Context, uid uuid.UUID, pairs []Pair) error {
	if len(pairs) == 0 {
		return nil
	}
	return s.optedIn(ctx, uid, func(tx *sql.Tx) error { return applyPairs(ctx, tx, uid, pairs) })
}

func applyPairs(ctx context.Context, tx *sql.Tx, uid uuid.UUID, pairs []Pair) error {
	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE rivian_receipts SET charge_id = $3, matched_at = NOW()
			 WHERE id = $1 AND user_id = $2 AND charge_id IS NULL`, p.ReceiptID, uid, p.ChargeID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE charges c
			   SET receipt_cost = r.total_cost, receipt_currency = r.currency,
			       receipt_txn_id = r.transaction_id, receipt_source = 'rivian_receipt', updated_at = NOW()
			  FROM rivian_receipts r
			 WHERE r.id = $1 AND r.user_id = $2 AND c.id = $3 AND c.user_id = $2 AND c.receipt_txn_id IS NULL`,
			p.ReceiptID, uid, p.ChargeID); err != nil {
			return err
		}
	}
	return nil
}

// RefreshMatched copies a changed amount or currency from a receipt to the
// charge it is linked to.
func (s *Store) RefreshMatched(ctx context.Context, uid uuid.UUID) error {
	return s.optedIn(ctx, uid, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
		UPDATE charges c
		   SET receipt_cost = r.total_cost, receipt_currency = r.currency, updated_at = NOW()
		  FROM rivian_receipts r
		 WHERE r.user_id = $1 AND r.charge_id = c.id AND c.user_id = $1
		   AND (c.receipt_cost IS DISTINCT FROM r.total_cost OR c.receipt_currency IS DISTINCT FROM r.currency)`, uid)
		return err
	})
}

// NeedDetail returns up to limit transaction ids of matched receipts whose
// charger details were never fetched.
func (s *Store) NeedDetail(ctx context.Context, uid uuid.UUID, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT transaction_id FROM rivian_receipts
		 WHERE user_id = $1 AND charge_id IS NOT NULL AND detail_fetched_at IS NULL
		 ORDER BY started_at DESC LIMIT $2`, uid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveDetail stores the charger fields. For a home charger only the flag
// is kept: its name can be the owner's own place. A nil charger still marks
// the row fetched so it is not asked again.
func (s *Store) SaveDetail(ctx context.Context, uid uuid.UUID, txn string, c *rivian.SessionCharger) error {
	var name sql.NullString
	var home sql.NullBool
	if c != nil {
		if c.IsHome != nil {
			home = sql.NullBool{Bool: *c.IsHome, Valid: true}
		}
		if c.Name != "" && (c.IsHome == nil || !*c.IsHome) {
			name = sql.NullString{String: c.Name, Valid: true}
		}
	}
	vendor := sql.NullString{}
	if c != nil && c.Vendor != "" {
		vendor = sql.NullString{String: c.Vendor, Valid: true}
	}
	return s.optedIn(ctx, uid, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE rivian_receipts
			   SET charger_name = $3, is_home_charger = $4, vendor = COALESCE($5, vendor), detail_fetched_at = NOW()
			 WHERE user_id = $1 AND transaction_id = $2`, uid, txn, name, home, vendor)
		return err
	})
}

// ListAll is every receipt of the user, for the data export.
func (s *Store) ListAll(ctx context.Context, uid uuid.UUID) ([]Receipt, error) {
	rows, err := s.db.QueryContext(ctx, receiptSelect+` WHERE user_id = $1 ORDER BY started_at`, uid)
	if err != nil {
		return nil, err
	}
	return scanReceipts(rows)
}

const receiptSelect = `
	SELECT id, transaction_id, started_at, ended_at, total_cost::float8, COALESCE(currency,''),
	       energy_kwh, range_added_km, COALESCE(vendor,''), COALESCE(city,''),
	       COALESCE(charger_name,''), is_home_charger, detail_fetched_at IS NOT NULL, charge_id
	  FROM rivian_receipts`

func scanReceipts(rows *sql.Rows) ([]Receipt, error) {
	defer rows.Close()
	var out []Receipt
	for rows.Next() {
		var r Receipt
		var cost, kwh, km sql.NullFloat64
		var home sql.NullBool
		var charge uuid.NullUUID
		if err := rows.Scan(&r.ID, &r.TransactionID, &r.StartedAt, &r.EndedAt, &cost, &r.Currency,
			&kwh, &km, &r.Vendor, &r.City, &r.ChargerName, &home, &r.DetailFetched, &charge); err != nil {
			return nil, err
		}
		r.StartedAt, r.EndedAt = r.StartedAt.UTC(), r.EndedAt.UTC()
		r.TotalCost, r.EnergyKWh, r.RangeAddedKm = floatPtr(cost), floatPtr(kwh), floatPtr(km)
		if home.Valid {
			b := home.Bool
			r.IsHome = &b
		}
		if charge.Valid {
			id := charge.UUID
			r.ChargeID = &id
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// Purge removes everything the receipts sync stored for a user: the
// receipts, the sync clock, and the receipt_* columns on their charges. The
// estimate on the charge is untouched. Used when the user opts out.
func (s *Store) Purge(ctx context.Context, uid uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, q := range []string{
		`UPDATE charges SET receipt_cost = NULL, receipt_currency = NULL, receipt_txn_id = NULL, receipt_source = NULL, updated_at = NOW()
		  WHERE user_id = $1 AND receipt_source IS NOT NULL`,
		`DELETE FROM rivian_receipts WHERE user_id = $1`,
		`DELETE FROM rivian_receipt_sync WHERE user_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, q, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeNotOptedIn is the retention backstop: it deletes everything the
// receipts sync stored for every user who isn't opted in (the opt-out
// handler purges at once; this catches anything that slipped past, e.g. a
// failed purge). The operator flag isn't consulted: switching it off
// pauses the sync but leaves an opted-in user's data. It returns how many
// receipts it deleted.
func (s *Store) PurgeNotOptedIn(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `
		UPDATE charges SET receipt_cost = NULL, receipt_currency = NULL, receipt_txn_id = NULL, receipt_source = NULL, updated_at = NOW()
		 WHERE receipt_source IS NOT NULL AND `+"NOT "+settings.RivianReceiptsOptInSQL("charges.user_id")); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM rivian_receipts r WHERE `+"NOT "+settings.RivianReceiptsOptInSQL("r.user_id"))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rivian_receipt_sync rs WHERE `+"NOT "+settings.RivianReceiptsOptInSQL("rs.user_id")); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
