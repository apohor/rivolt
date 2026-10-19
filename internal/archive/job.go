package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
)

// ArchiveAfterDays is how old a month must be, all of it, before it is
// archived (and so before it can be thinned). The privacy policy quotes it.
const ArchiveAfterDays = 90

// maintenanceLockID is the advisory lock sample thinning and retention hold
// (internal/samples): one of the three runs at a time, across replicas.
const maintenanceLockID int64 = 0x72765f72657465

// defaultMaxObjectBytes caps one object, which is buffered in memory for the
// signed PUT. A truck-month is a few tens of MB compressed; anything near the
// cap means multipart upload is needed, not a bigger buffer.
const defaultMaxObjectBytes = 256 << 20

// Config is the archive's store and environment.
type Config struct {
	// Env prefixes every key ("prod", "preview"); each environment's MinIO
	// user can only reach its own prefix.
	Env       string
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

// ConfigFromEnv reads RIVOLT_ARCHIVE_*. The archive is on only when
// RIVOLT_ARCHIVE_ENV is set; then both S3 keys are required.
func ConfigFromEnv(getenv func(string) string) (Config, bool, error) {
	c := Config{
		Env:       strings.TrimSpace(getenv("RIVOLT_ARCHIVE_ENV")),
		Endpoint:  strings.TrimSpace(getenv("RIVOLT_ARCHIVE_S3_ENDPOINT")),
		Bucket:    strings.TrimSpace(getenv("RIVOLT_ARCHIVE_S3_BUCKET")),
		Region:    strings.TrimSpace(getenv("RIVOLT_ARCHIVE_S3_REGION")),
		AccessKey: getenv("RIVOLT_ARCHIVE_S3_ACCESS_KEY"),
		SecretKey: getenv("RIVOLT_ARCHIVE_S3_SECRET_KEY"),
	}
	if c.Env == "" {
		return Config{}, false, nil
	}
	if !envName.MatchString(c.Env) {
		return Config{}, false, fmt.Errorf("RIVOLT_ARCHIVE_ENV=%q: want a short lower-case name like prod or preview", c.Env)
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return Config{}, false, errors.New("RIVOLT_ARCHIVE_ENV is set but RIVOLT_ARCHIVE_S3_ACCESS_KEY / RIVOLT_ARCHIVE_S3_SECRET_KEY are not")
	}
	if c.Endpoint == "" {
		c.Endpoint = "http://minio.minio.svc.cluster.local:9000"
	}
	if c.Bucket == "" {
		c.Bucket = "rivolt-telemetry-archive"
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	return c, true, nil
}

// Archiver writes and verifies monthly archive objects.
type Archiver struct {
	db     *sql.DB
	store  *s3.Client
	sealer crypto.Sealer
	env    string
	// Now is the clock (tests set it).
	Now func() time.Time
	// MaxObjectBytes caps one object (0: defaultMaxObjectBytes).
	MaxObjectBytes int64
	// Report, when set, is told about every object and every run.
	Report func(Report)
}

// Report is one object archived (or failed), or with Done set the run's total.
type Report struct {
	Month     string
	UserID    uuid.UUID
	VehicleID uuid.UUID
	Rows      int64
	Bytes     int64
	Err       error
	// Done marks the run's summary: Objects archived, Failed objects.
	Done    bool
	Objects int
	Failed  int
}

// NewArchiver refuses to run without a real KEK.
func NewArchiver(db *sql.DB, cfg Config, sealer crypto.Sealer) (*Archiver, error) {
	if err := realSealer(sealer); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("archive: no database")
	}
	if !envName.MatchString(cfg.Env) {
		return nil, fmt.Errorf("archive: bad env %q", cfg.Env)
	}
	return &Archiver{
		db:     db,
		store:  &s3.Client{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey},
		sealer: sealer,
		env:    cfg.Env,
		Now:    time.Now,
	}, nil
}

// Run archives the oldest month that is wholly older than ArchiveAfterDays
// and has a (user, vehicle) without a verified object, and returns how many
// objects it wrote and verified. One month per run; one replica at a time,
// and never alongside thinning or retention. A failed object is reported and
// the rest of the month continues; the run then returns an error, and the
// month is retried the next day. A month where nothing succeeded (only stuck
// objects left) is passed over so later months still get archived. A store,
// network or database error stops the run at once.
//
// A (user, vehicle) whose object needs re-archiving (rows restored or
// imported into an archived month) is not touched here: its month may be
// thinned already, and only a merge with the existing object (A4) keeps the
// full detail.
func (a *Archiver) Run(ctx context.Context) (int64, error) {
	conn, err := a.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, maintenanceLockID).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, maintenanceLockID) //nolint:errcheck

	months, err := archivableMonths(ctx, conn, a.Now())
	if err != nil {
		return 0, err
	}
	var stuck error
	for _, month := range months {
		pairs, err := pendingPairs(ctx, conn, month)
		if err != nil {
			return 0, err
		}
		if len(pairs) == 0 {
			continue
		}
		cols, err := columns(ctx, conn)
		if err != nil {
			return 0, err
		}
		var done, failed int
		var firstErr error
		for _, p := range pairs {
			rows, n, err := a.archiveOne(ctx, conn, cols, month, p.user, p.vehicle)
			if a.Report != nil {
				a.Report(Report{Month: MonthString(month), UserID: p.user, VehicleID: p.vehicle, Rows: rows, Bytes: n, Err: err})
			}
			if err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
				// Only a fault in this object moves on to the next one; the
				// store, the network or the database being down would fail
				// every object after it too, each after a full COPY and
				// encryption, so the run stops there (DA).
				if !perObject(err) {
					if a.Report != nil {
						a.Report(Report{Month: MonthString(month), Done: true, Objects: done, Failed: failed})
					}
					return int64(done), errors.Join(stuck, fmt.Errorf("archive %s: stopped: %w", MonthString(month), err))
				}
				continue
			}
			done++
		}
		if a.Report != nil {
			a.Report(Report{Month: MonthString(month), Done: true, Objects: done, Failed: failed})
		}
		if firstErr == nil {
			return int64(done), stuck
		}
		err = fmt.Errorf("archive %s: %d of %d objects failed, first: %w", MonthString(month), failed, len(pairs), firstErr)
		// A month whose every remaining object failed (one stuck pair, e.g.
		// over the size cap) doesn't hold the later months back: move on, and
		// still return its error with whatever the next month does.
		if done > 0 || ctx.Err() != nil {
			return int64(done), errors.Join(stuck, err)
		}
		stuck = errors.Join(stuck, err)
	}
	return 0, stuck
}

// perObject reports whether err is a fault of one object (too large, or not
// matching what was written once read back), as opposed to the store, the
// network or the database failing, which would fail the next object as well.
func perObject(err error) bool {
	return errors.Is(err, errTooLarge) || errors.Is(err, ErrMismatch) || errors.Is(err, ErrCorrupt)
}

// archivableMonths lists the months of vehicle_state's attached partitions
// that ended at least ArchiveAfterDays ago, oldest first.
func archivableMonths(ctx context.Context, conn *sql.Conn, now time.Time) ([]time.Time, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT c.relname
		  FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'vehicle_state'::regclass AND NOT i.inhdetachpending
		   AND c.relname ~ '^vehicle_state_y[0-9]{4}_[0-9]{2}$'
		 ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	limit := now.UTC().AddDate(0, 0, -ArchiveAfterDays)
	var out []time.Time
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		m, err := time.Parse("2006_01", strings.TrimPrefix(name, "vehicle_state_y"))
		if err != nil {
			continue
		}
		if !m.AddDate(0, 1, 0).After(limit) {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

type pair struct{ user, vehicle uuid.UUID }

// pendingPairs is every (user, vehicle) with rows in month and no archive_months
// row, or one that was never verified. needs_rearchive rows are left to A4.
func pendingPairs(ctx context.Context, conn *sql.Conn, month time.Time) ([]pair, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT DISTINCT s.user_id, s.vehicle_id
		  FROM vehicle_state s
		 WHERE s.at >= $1 AND s.at < $2
		   AND NOT EXISTS (
		       SELECT 1 FROM archive_months m
		        WHERE m.user_id = s.user_id AND m.vehicle_id = s.vehicle_id AND m.month = $1::date
		          AND (m.verified_at IS NOT NULL OR m.needs_rearchive))
		 ORDER BY 1, 2`, month, month.AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.user, &p.vehicle); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type column struct{ name, typ string }

// columns is vehicle_state's column list in table order: the CSV header.
func columns(ctx context.Context, conn *sql.Conn) ([]column, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = 'vehicle_state'
		 ORDER BY ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("archive: vehicle_state has no columns")
	}
	return out, nil
}

func names(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.name
	}
	return out
}

// tsText formats a timestamptz expression as TimeLayout.
func tsText(expr string) string {
	return `to_char(` + expr + ` AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
}

// archiveOne writes, uploads and verifies one object, then records it. It
// returns the rows and the object's size.
func (a *Archiver) archiveOne(ctx context.Context, conn *sql.Conn, cols []column, month time.Time, uid, vid uuid.UUID) (int64, int64, error) {
	key, err := a.dataKey(ctx, uid)
	if err != nil {
		return 0, 0, err
	}
	aad := ObjectAAD(uid, vid, MonthString(month))
	obj, m, err := a.build(ctx, conn, cols, month, uid, vid, key, aad)
	if err != nil {
		return 0, 0, err
	}
	if m.Rows == 0 {
		return 0, 0, nil
	}
	objKey := ObjectKey(a.env, uid, vid, month)
	if err := a.store.Put(ctx, objKey, obj, "application/octet-stream"); err != nil {
		return m.Rows, m.Bytes, err
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return m.Rows, m.Bytes, err
	}
	if err := a.store.Put(ctx, ManifestKey(a.env, uid, vid, month), mj, "application/json"); err != nil {
		return m.Rows, m.Bytes, err
	}
	if err := a.verify(ctx, objKey, key, aad, m); err != nil {
		return m.Rows, m.Bytes, err
	}
	first, _ := time.Parse(TimeLayout, m.FirstAt)
	last, _ := time.Parse(TimeLayout, m.LastAt)
	// A verified row is never overwritten here (it is excluded from the
	// pending set; the WHERE also holds against a concurrent writer).
	_, err = conn.ExecContext(ctx, `
		INSERT INTO archive_months (user_id, vehicle_id, month, rows, first_at, last_at, sha256_object, bytes, archived_at, verified_at)
		VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8, $9, $9)
		ON CONFLICT (user_id, vehicle_id, month) DO UPDATE
		   SET rows = EXCLUDED.rows, first_at = EXCLUDED.first_at, last_at = EXCLUDED.last_at,
		       sha256_object = EXCLUDED.sha256_object, bytes = EXCLUDED.bytes,
		       archived_at = EXCLUDED.archived_at, verified_at = EXCLUDED.verified_at
		 WHERE archive_months.verified_at IS NULL AND NOT archive_months.needs_rearchive`,
		uid, vid, month, m.Rows, first, last, m.SHA256Object, m.Bytes, a.Now().UTC())
	return m.Rows, m.Bytes, err
}

// build streams the (user, vehicle, month) rows from one REPEATABLE READ
// snapshot into an object, and checks the stream held every row the snapshot
// counts.
func (a *Archiver) build(ctx context.Context, conn *sql.Conn, cols []column, month time.Time, uid, vid uuid.UUID, key, aad []byte) ([]byte, Manifest, error) {
	from, to := month.UTC(), month.UTC().AddDate(0, 1, 0)
	if _, err := conn.ExecContext(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return nil, Manifest{}, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`) //nolint:errcheck
	if _, err := conn.ExecContext(ctx, `SET LOCAL extra_float_digits = 1`); err != nil {
		return nil, Manifest{}, err
	}
	var m Manifest
	var first, last sql.NullString
	if err := conn.QueryRowContext(ctx, `
		SELECT count(*), `+tsText("min(at)")+`, `+tsText("max(at)")+`
		  FROM vehicle_state WHERE user_id = $1 AND vehicle_id = $2 AND at >= $3 AND at < $4`,
		uid, vid, from, to).Scan(&m.Rows, &first, &last); err != nil {
		return nil, Manifest{}, err
	}
	if m.Rows == 0 {
		return nil, m, nil
	}
	m.FirstAt, m.LastAt = first.String, last.String

	sel := make([]string, len(cols))
	for i, c := range cols {
		id := pgx.Identifier{c.name}.Sanitize()
		if c.typ == "timestamp with time zone" {
			sel[i] = tsText(id) + ` AS ` + id
		} else {
			sel[i] = id
		}
	}
	// COPY takes no bind parameters: the literals are a uuid.UUID's String()
	// and RFC 3339 times we formatted.
	copySQL := `COPY (SELECT ` + strings.Join(sel, ", ") + ` FROM vehicle_state` +
		` WHERE user_id = '` + uid.String() + `' AND vehicle_id = '` + vid.String() + `'` +
		` AND at >= '` + from.Format(time.RFC3339) + `'::timestamptz AND at < '` + to.Format(time.RFC3339) + `'::timestamptz` +
		` ORDER BY at) TO STDOUT WITH (FORMAT csv, HEADER true)`

	capBytes := a.MaxObjectBytes
	if capBytes <= 0 {
		capBytes = defaultMaxObjectBytes
	}
	var buf bytes.Buffer
	ow, err := newObjectWriter(&capWriter{w: &buf, left: capBytes}, key, aad)
	if err != nil {
		return nil, Manifest{}, err
	}
	var copied int64
	if err := conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("archive: driver connection is %T, want pgx", dc)
		}
		tag, err := c.Conn().PgConn().CopyTo(ctx, ow, copySQL)
		copied = tag.RowsAffected()
		return err
	}); err != nil {
		return nil, Manifest{}, fmt.Errorf("archive: copy: %w", err)
	}
	if err := ow.Close(); err != nil {
		return nil, Manifest{}, err
	}
	if copied != m.Rows {
		return nil, Manifest{}, fmt.Errorf("%w: copied %d rows, snapshot has %d", ErrMismatch, copied, m.Rows)
	}
	sum := sha256.Sum256(buf.Bytes())
	m.Schema = Schema
	m.Columns = names(cols)
	m.SHA256Plain = ow.plainSHA256()
	m.SHA256Object = hex.EncodeToString(sum[:])
	m.Bytes = int64(buf.Len())
	m.CreatedAt = a.Now().UTC()
	return buf.Bytes(), m, nil
}

// verify reads the stored object back to the end and checks it against what
// was written: its bytes, its rows, first and last at, and the plaintext hash.
func (a *Archiver) verify(ctx context.Context, objKey string, key, aad []byte, m Manifest) error {
	body, err := a.store.Get(ctx, objKey)
	if err != nil {
		return err
	}
	defer body.Close()
	objHash := sha256.New()
	counted := &countWriter{}
	s, err := ReadStats(io.TeeReader(body, io.MultiWriter(objHash, counted)), key, aad, m.Columns)
	if err != nil {
		return fmt.Errorf("archive: verify %s: %w", objKey, err)
	}
	// ReadStats stops at the end of the encrypted stream; anything after it
	// would be extra bytes in the object.
	if _, err := io.Copy(io.MultiWriter(objHash, counted), body); err != nil {
		return err
	}
	got := Manifest{Rows: s.Rows, FirstAt: s.FirstAt, LastAt: s.LastAt, SHA256Plain: s.SHA256Plain,
		SHA256Object: hex.EncodeToString(objHash.Sum(nil)), Bytes: counted.n}
	want := Manifest{Rows: m.Rows, FirstAt: m.FirstAt, LastAt: m.LastAt, SHA256Plain: m.SHA256Plain,
		SHA256Object: m.SHA256Object, Bytes: m.Bytes}
	if got.Rows != want.Rows || got.FirstAt != want.FirstAt || got.LastAt != want.LastAt ||
		got.SHA256Plain != want.SHA256Plain || got.SHA256Object != want.SHA256Object || got.Bytes != want.Bytes {
		return fmt.Errorf("%w: %s read back rows=%d first=%s last=%s bytes=%d, wrote rows=%d first=%s last=%s bytes=%d",
			ErrMismatch, objKey, got.Rows, got.FirstAt, got.LastAt, got.Bytes, want.Rows, want.FirstAt, want.LastAt, want.Bytes)
	}
	return nil
}

// dataKey returns uid's data key, creating and sealing one on first use.
func (a *Archiver) dataKey(ctx context.Context, uid uuid.UUID) ([]byte, error) {
	var sealed []byte
	err := a.db.QueryRowContext(ctx, `SELECT sealed_key FROM archive_keys WHERE user_id = $1`, uid).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		k, kerr := NewDataKey()
		if kerr != nil {
			return nil, kerr
		}
		s, serr := SealDataKey(ctx, a.sealer, uid, k)
		if serr != nil {
			return nil, serr
		}
		// Another replica may have raced us: whichever row landed is the key.
		if _, err := a.db.ExecContext(ctx, `INSERT INTO archive_keys (user_id, sealed_key) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING`, uid, s); err != nil {
			return nil, err
		}
		err = a.db.QueryRowContext(ctx, `SELECT sealed_key FROM archive_keys WHERE user_id = $1`, uid).Scan(&sealed)
	}
	if err != nil {
		return nil, err
	}
	return OpenDataKey(ctx, a.sealer, uid, sealed)
}

// capWriter fails once more than left bytes are written.
type capWriter struct {
	w    io.Writer
	left int64
}

var errTooLarge = errors.New("archive: object over the size cap (needs multipart upload)")

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errTooLarge
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
