package archive

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
)

// RethinAfter is how long a restored month keeps its full detail before
// thinning reduces it again: time for whatever prompted the restore.
const RethinAfter = 7 * 24 * time.Hour

// ErrBusy is a restore refused because the archive, thinning or retention
// job is running (they share the maintenance lock); retry shortly.
var ErrBusy = errors.New("archive: a maintenance job is running, try again shortly")

// ErrNoObject is a restore of a (user, vehicle, month) without a verified
// archive object.
var ErrNoObject = errors.New("archive: no verified object for that vehicle and month")

// Restorer puts one archived (user, vehicle, month) back into vehicle_state
// in full detail (the admin site's restore and `rivolt archive restore`).
type Restorer struct {
	db     *sql.DB
	store  *s3.Client
	sealer crypto.Sealer
	env    string
}

// NewRestorer refuses to run without a real KEK.
func NewRestorer(db *sql.DB, cfg Config, sealer crypto.Sealer) (*Restorer, error) {
	if err := realSealer(sealer); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("archive: no database")
	}
	if !envName.MatchString(cfg.Env) {
		return nil, fmt.Errorf("archive: bad env %q", cfg.Env)
	}
	return &Restorer{
		db:     db,
		store:  &s3.Client{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, HTTP: cfg.HTTP},
		sealer: sealer,
		env:    cfg.Env,
	}, nil
}

// RestoreResult is what a restore found and did (or would do, on a dry run).
type RestoreResult struct {
	InObject int64 `json:"in_object"`
	Present  int64 `json:"already_present"`
	Inserted int64 `json:"inserted"`
	// DBOnly is rows the database has that the object lacks; they stay, and
	// the month stays flagged for re-archiving.
	DBOnly      int64      `json:"db_only"`
	RethinAfter *time.Time `json:"rethin_after,omitempty"`
	DryRun      bool       `json:"dry_run"`
}

// Restore reads the object back whole and inserts its rows into the month's
// partition, keeping any row already there. Afterwards a thinned month is
// held at full detail for RethinAfter (rethin_after), after which thinning
// reduces just this vehicle again. The month stays flagged for re-archiving
// if the database has rows the object lacks (DA on A4). It holds the
// maintenance lock (never alongside archiving, thinning or retention: with
// wait false a busy lock is ErrBusy) and the user's archive lock.
func (r *Restorer) Restore(ctx context.Context, uid, vid uuid.UUID, month time.Time, dryRun, wait bool) (RestoreResult, error) {
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	res := RestoreResult{DryRun: dryRun}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	if wait {
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, maintenanceLockID); err != nil {
			return res, err
		}
	} else {
		var ok bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, maintenanceLockID).Scan(&ok); err != nil {
			return res, err
		}
		if !ok {
			return res, ErrBusy
		}
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, maintenanceLockID) //nolint:errcheck
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(`+userLockSQL+`)`, uid); err != nil {
		return res, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+userLockSQL+`)`, uid) //nolint:errcheck

	var verified bool
	err = conn.QueryRowContext(ctx, `SELECT verified_at IS NOT NULL FROM archive_months WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date`,
		uid, vid, month).Scan(&verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !verified) {
		return res, ErrNoObject
	}
	if err != nil {
		return res, err
	}
	key, err := openKey(ctx, r.db, r.sealer, uid)
	if err != nil {
		return res, err
	}
	aad := ObjectAAD(uid, vid, MonthString(month))
	srcKey, err := readableKey(ctx, r.store, ObjectKey(r.env, uid, vid, month), key, aad)
	if err != nil {
		return res, fmt.Errorf("restore %s: %w", MonthString(month), err)
	}
	cols, err := columns(ctx, conn)
	if err != nil {
		return res, err
	}
	if !dryRun {
		if _, err := conn.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, month); err != nil {
			return res, err
		}
	}

	body, err := r.store.Get(ctx, srcKey)
	if err != nil {
		return res, err
	}
	defer body.Close()
	dec, err := NewDecryptReader(body, key, aad)
	if err != nil {
		return res, err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return res, fmt.Errorf("archive: gzip: %w", err)
	}
	plain := io.MultiReader(gz, readerFunc(func([]byte) (int, error) {
		if _, err := io.Copy(io.Discard, dec); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}))

	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return res, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`) //nolint:errcheck
	if err := loadObject(ctx, conn, cols, plain, uid, vid); err != nil {
		return res, err
	}
	from, to := month, month.AddDate(0, 1, 0)
	if err := conn.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM archive_merge),
		       (SELECT count(*) FROM archive_merge o WHERE EXISTS (
		            SELECT 1 FROM vehicle_state v WHERE v.vehicle_id = o.vehicle_id AND v.at = o.at)),
		       (SELECT count(*) FROM vehicle_state v
		         WHERE v.user_id = $1 AND v.vehicle_id = $2 AND v.at >= $3 AND v.at < $4
		           AND NOT EXISTS (SELECT 1 FROM archive_merge o WHERE o.at = v.at))`,
		uid, vid, from, to).Scan(&res.InObject, &res.Present, &res.DBOnly); err != nil {
		return res, err
	}
	if dryRun {
		res.Inserted = res.InObject - res.Present
		return res, nil
	}
	tag, err := conn.ExecContext(ctx, `INSERT INTO vehicle_state SELECT * FROM archive_merge ON CONFLICT (vehicle_id, at) DO NOTHING`)
	if err != nil {
		return res, fmt.Errorf("restore insert: %w", err)
	}
	res.Inserted, _ = tag.RowsAffected()
	// Hold the full detail back from thinning for a while if the month was
	// thinned; never clear a re-archive flag here, and set it if the
	// database has rows the object lacks.
	var rethin sql.NullTime
	if err := conn.QueryRowContext(ctx, `
		UPDATE archive_months
		   SET rethin_after = CASE WHEN EXISTS (SELECT 1 FROM sample_tiers WHERE month = $3::date)
		                           THEN now() + make_interval(secs => $5) END,
		       needs_rearchive = needs_rearchive OR $4 > 0,
		       rearchive_gen = rearchive_gen + CASE WHEN $4 > 0 THEN 1 ELSE 0 END
		 WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date
		RETURNING rethin_after`, uid, vid, month, res.DBOnly, RethinAfter.Seconds()).Scan(&rethin); err != nil {
		return res, err
	}
	if rethin.Valid {
		t := rethin.Time.UTC()
		res.RethinAfter = &t
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return res, err
	}
	return res, nil
}
