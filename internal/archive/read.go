package archive

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
)

// Reader reads a user's archive objects back (the backup download, the admin
// restore). It never creates keys or writes.
type Reader struct {
	db     *sql.DB
	store  *s3.Client
	sealer crypto.Sealer
	env    string
	// OnCorrupt, when set, is told about an object that exists but fails to
	// read back: corrupt, truncated, or not this user's. For a thinned month
	// that is the full-resolution data lost, so the caller counts and alerts.
	OnCorrupt func(uid, vehicleID uuid.UUID, month time.Time, err error)
}

// NewReader refuses to run without a real KEK, like the archiver.
func NewReader(db *sql.DB, cfg Config, sealer crypto.Sealer) (*Reader, error) {
	if err := realSealer(sealer); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("archive: no database")
	}
	if !envName.MatchString(cfg.Env) {
		return nil, fmt.Errorf("archive: bad env %q", cfg.Env)
	}
	return &Reader{
		db:     db,
		store:  &s3.Client{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey},
		sealer: sealer,
		env:    cfg.Env,
	}, nil
}

// ObjectRef is one readable object: verified. One waiting to be re-archived
// (needs_rearchive) still holds every row it was written with; the rows that
// came since are in the database, and readers take the union.
type ObjectRef struct {
	VehicleID uuid.UUID
	Month     time.Time
	Rows      int64
}

// Objects lists uid's readable objects, by month then vehicle.
func (r *Reader) Objects(ctx context.Context, uid uuid.UUID) ([]ObjectRef, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT vehicle_id, month, rows FROM archive_months
		 WHERE user_id = $1 AND verified_at IS NOT NULL
		 ORDER BY month, vehicle_id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectRef
	for rows.Next() {
		var o ObjectRef
		if err := rows.Scan(&o.VehicleID, &o.Month, &o.Rows); err != nil {
			return nil, err
		}
		o.Month = time.Date(o.Month.Year(), o.Month.Month(), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Row is one archived vehicle_state row, read by column name. Values are the
// CSV's text: "" for NULL, timestamps in TimeLayout.
type Row struct {
	index map[string]int
	rec   []string
}

// NewRow builds a Row from a header and a record (tests, and callers that
// read CSV themselves).
func NewRow(header, rec []string) Row {
	index := make(map[string]int, len(header))
	for i, h := range header {
		index[h] = i
	}
	return Row{index: index, rec: rec}
}

// Get returns the named column's value and whether the object has it.
func (r Row) Get(name string) (string, bool) {
	i, ok := r.index[name]
	if !ok {
		return "", false
	}
	return r.rec[i], true
}

// EachRow streams the object's rows to fn in `at` order and reads the object
// to the end. An error returned after rows were passed (a corrupt or
// truncated object is detected at its end) means what fn got is incomplete:
// the caller must fail as a whole. A missing object is s3.ErrNotFound before
// any row.
func (r *Reader) EachRow(ctx context.Context, uid, vehicleID uuid.UUID, month time.Time, fn func(Row) error) error {
	err := r.eachRow(ctx, uid, vehicleID, month, fn)
	if err != nil && r.OnCorrupt != nil && corrupt(err) {
		r.OnCorrupt(uid, vehicleID, month, err)
	}
	return err
}

// corrupt reports whether err is the object's fault rather than the store's,
// the network's or the caller's.
func corrupt(err error) bool {
	var ce *csv.ParseError
	return errors.Is(err, ErrCorrupt) || errors.Is(err, ErrMismatch) || errors.Is(err, gzip.ErrHeader) ||
		errors.Is(err, gzip.ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &ce)
}

func (r *Reader) eachRow(ctx context.Context, uid, vehicleID uuid.UUID, month time.Time, fn func(Row) error) error {
	key, err := openKey(ctx, r.db, r.sealer, uid)
	if err != nil {
		return err
	}
	body, err := r.store.Get(ctx, ObjectKey(r.env, uid, vehicleID, month))
	if err != nil {
		return err
	}
	defer body.Close()
	dec, err := NewDecryptReader(body, key, ObjectAAD(uid, vehicleID, MonthString(month)))
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return fmt.Errorf("archive: gzip: %w", err)
	}
	cr := csv.NewReader(gz)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("archive: header: %w", err)
	}
	index := make(map[string]int, len(header))
	for i, h := range header {
		index[h] = i
	}
	if _, ok := index["at"]; !ok {
		return fmt.Errorf("%w: no at column", ErrMismatch)
	}
	cr.FieldsPerRecord = len(header)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("archive: read %s: %w", MonthString(month), err)
		}
		if err := fn(Row{index: index, rec: rec}); err != nil {
			return err
		}
	}
	// Authenticate the final chunk.
	_, err = io.Copy(io.Discard, dec)
	return err
}

// openKey reads uid's data key; a user without one has no objects.
func openKey(ctx context.Context, db *sql.DB, s crypto.Sealer, uid uuid.UUID) ([]byte, error) {
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT sealed_key FROM archive_keys WHERE user_id = $1`, uid).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("archive: key for %s: %w", uid, err)
	}
	return OpenDataKey(ctx, s, uid, sealed)
}
