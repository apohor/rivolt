package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
)

// Migration checksums (migrations v2, step 2). migrations.checksum holds the sha256 of each file. After a run the runner
//   - fills the checksum of every applied migration that has none (rows written by older binaries, and the ones just applied), and
//   - compares the rest with the files this binary ships, logs each difference and counts them (rivolt_migration_checksum_mismatch).
//
// A mismatch is a finding, never a boot failure: the schema is what it is and the pods must still start.
//
// A checksum filled afterwards attests the file as it is today, not the bytes that were applied: before the immutability check (step 1)
// a file could change after it ran, and nothing recorded that. From the day this runs, a later edit shows as a mismatch.
var checksumMismatches atomic.Int64

// ChecksumMismatches is how many applied migrations differ from the file this binary ships, as of the last check in this process.
func ChecksumMismatches() int64 { return checksumMismatches.Load() }

func fileChecksum(sqlText string) string {
	sum := sha256.Sum256([]byte(sqlText))
	return hex.EncodeToString(sum[:])
}

// hasChecksumColumn is false until migration 0120 has run.
func hasChecksumColumn(ctx context.Context, d *sql.DB) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'migrations' AND column_name = 'checksum'`).Scan(&n)
	return n > 0, err
}

// reconcileChecksums fills missing checksums when fill is true (the migrator holds the lock and writes), and always counts mismatches.
func reconcileChecksums(ctx context.Context, d *sql.DB, fill bool) ([]string, error) {
	ok, err := hasChecksumColumn(ctx, d)
	if err != nil || !ok {
		checksumMismatches.Store(0)
		return nil, err
	}
	steps, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	want := make(map[string]string, len(steps))
	for _, m := range steps {
		want[m.id] = fileChecksum(m.sql)
	}
	rows, err := d.QueryContext(ctx, `SELECT id, checksum FROM migrations`)
	if err != nil {
		return nil, err
	}
	var missing, bad []string
	for rows.Next() {
		var id string
		var sum sql.NullString
		if err := rows.Scan(&id, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		w, shipped := want[id]
		switch {
		case !shipped: // applied by a newer binary: not ours to judge
		case !sum.Valid:
			missing = append(missing, id)
		case sum.String != w:
			bad = append(bad, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if fill {
		for _, id := range missing {
			if _, err := d.ExecContext(ctx, `UPDATE migrations SET checksum = $2 WHERE id = $1 AND checksum IS NULL`, id, want[id]); err != nil {
				return nil, fmt.Errorf("record checksum of %s: %w", id, err)
			}
		}
	}
	sort.Strings(bad)
	for _, id := range bad {
		slog.Warn("migration checksum mismatch: the file differs from what was recorded", "id", id)
	}
	checksumMismatches.Store(int64(len(bad)))
	return bad, nil
}

// CheckChecksums is the read-only form for a server that does not migrate: it counts mismatches and changes nothing. Errors are logged by the caller's
// choice; boot never depends on this.
func CheckChecksums(ctx context.Context, d *sql.DB) ([]string, error) {
	return reconcileChecksums(ctx, d, false)
}
