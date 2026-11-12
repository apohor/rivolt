// Package imports is the batch record of a history import (design: history-import-design.md section 4): one row of the imports table per
// commit, the drives and charges it brought in carry its id (import_id), and Undo removes them all in one transaction.
//
// Nothing here parses a file or decides what a row is; it records, finds and undoes batches. The filename column holds "<source>.<ext>",
// never the name the user gave the file (it can carry a name or an email).
package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

var (
	// ErrNotFound: no such batch for this user, or it is already undone.
	ErrNotFound = errors.New("imports: no such import")
	reSource    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	reExt       = regexp.MustCompile(`^[a-z0-9]{1,8}$`)
	reSHA       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// AlreadyImported is returned by Begin when this user has a live (not undone) batch of the same source and file hash.
type AlreadyImported struct {
	ID uuid.UUID
	At time.Time
}

func (e *AlreadyImported) Error() string {
	return fmt.Sprintf("imports: this file was already imported on %s", e.At.UTC().Format("2006-01-02"))
}

// Batch is what a commit records about itself when it starts.
type Batch struct {
	Source    string // "tezlab_import" style source name; a lowercase identifier
	Ext       string // the file's extension without the dot ("csv", "xlsx"); the filename stored is Source + "." + Ext
	SHA256    string // lowercase hex of the uploaded bytes
	Bytes     int64
	VehicleID *uuid.UUID // the vehicles.id the rows go to, nil when the file covers none yet
	Metadata  map[string]any
}

// Counts is what a commit reports when it ends.
type Counts struct {
	Rows, Drives, Charges, Skipped int
}

// Undone is what Undo removed.
type Undone struct {
	Drives, Charges int64
}

// Store records batches for one user.
type Store struct {
	db     *sql.DB
	userID uuid.UUID
}

// New binds a store to a user.
func New(d *sql.DB, userID uuid.UUID) (*Store, error) {
	if d == nil {
		return nil, errors.New("imports: db is nil")
	}
	if userID == uuid.Nil {
		return nil, errors.New("imports: userID is zero")
	}
	return &Store{db: d, userID: userID}, nil
}

// Begin records a new batch and returns its id. A live batch of the same source and file hash for this user refuses it with
// *AlreadyImported (the unique index imports_user_sha_live, which Undo frees): two concurrent commits of one file give exactly one batch.
func (s *Store) Begin(ctx context.Context, b Batch) (uuid.UUID, error) {
	if !reSource.MatchString(b.Source) || !reExt.MatchString(b.Ext) || !reSHA.MatchString(b.SHA256) {
		return uuid.Nil, errors.New("imports: source, extension or hash is malformed")
	}
	meta, err := json.Marshal(b.Metadata)
	if err != nil || b.Metadata == nil {
		meta = []byte("{}")
	}
	var id uuid.UUID
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO imports (user_id, vehicle_id, source, filename, sha256, bytes, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		ON CONFLICT (user_id, source, sha256) WHERE sha256 IS NOT NULL AND undone_at IS NULL DO NOTHING
		RETURNING id`, s.userID, b.VehicleID, b.Source, b.Source+"."+b.Ext, b.SHA256, b.Bytes, string(meta)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var dup AlreadyImported
		err = s.db.QueryRowContext(ctx, `
			SELECT id, started_at FROM imports
			 WHERE user_id = $1 AND source = $2 AND sha256 = $3 AND undone_at IS NULL`, s.userID, b.Source, b.SHA256).Scan(&dup.ID, &dup.At)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Nil, &dup
	}
	return id, err
}

// Finish records the counts and the end time of a batch.
func (s *Store) Finish(ctx context.Context, id uuid.UUID, c Counts) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE imports SET rows = $3, drives = $4, charges = $5, skipped = $6, finished_at = now()
		 WHERE id = $1 AND user_id = $2 AND undone_at IS NULL`, id, s.userID, c.Rows, c.Drives, c.Charges, c.Skipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Undo deletes every drive and charge the batch brought in (with the per-drive rows), and marks the batch undone, in one transaction.
// The same file can then be imported again. Notes or tags the user added to those rows go with them. ErrNotFound when the batch is not
// this user's or is already undone.
func (s *Store) Undo(ctx context.Context, id uuid.UUID) (Undone, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Undone{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// The row lock makes a second Undo of the same batch wait, then find it undone.
	var locked uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM imports WHERE id = $1 AND user_id = $2 AND undone_at IS NULL FOR UPDATE`, id, s.userID).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Undone{}, ErrNotFound
		}
		return Undone{}, err
	}
	var u Undone
	if u.Drives, err = drives.UndoImport(ctx, tx, s.userID, id); err != nil {
		return Undone{}, err
	}
	if u.Charges, err = charges.UndoImport(ctx, tx, s.userID, id); err != nil {
		return Undone{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE imports SET undone_at = now() WHERE id = $1 AND user_id = $2`, id, s.userID); err != nil {
		return Undone{}, err
	}
	return u, tx.Commit()
}
