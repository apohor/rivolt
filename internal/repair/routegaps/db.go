package routegaps

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// Candidate is a live drive with a stored route.
type Candidate struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	StartedAt time.Time
	Polyline  string
}

// Select lists the closed live drives that started in [since, until) and have a stored route. Imported drives are left alone (their
// polylines were never recorded fix by fix), and a drive that ended in the last hour may still be written to.
func Select(ctx context.Context, db *sql.DB, since, until time.Time) ([]Candidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, started_at, route_polyline
		  FROM drives
		 WHERE source = 'live'
		   AND COALESCE(route_polyline, '') <> ''
		   AND started_at >= $1 AND started_at < $2
		   AND ended_at < now() - interval '1 hour'
		 ORDER BY started_at, id`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.UserID, &c.StartedAt, &c.Polyline); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Plan is the drives that have at least one dropout.
type Plan struct {
	Scanned    int
	Drives     []Candidate
	GapsTotal  int
	Undecoded  int
	MinGapM    float64
	gapsByID   map[uuid.UUID]int
	monthCount map[string]int
}

// MakePlan finds the dropouts in each candidate's route.
func MakePlan(cands []Candidate, minGapM float64) Plan {
	p := Plan{Scanned: len(cands), MinGapM: minGapM, gapsByID: map[uuid.UUID]int{}, monthCount: map[string]int{}}
	for _, c := range cands {
		pts, err := Decode(c.Polyline)
		if err != nil {
			p.Undecoded++
			continue
		}
		if n := len(Gaps(pts, minGapM)); n > 0 {
			p.Drives = append(p.Drives, c)
			p.GapsTotal += n
			p.gapsByID[c.ID] = n
			p.monthCount[Month(c.StartedAt)]++
		}
	}
	return p
}

// IDs are the planned drives' ids, sorted.
func (p Plan) IDs() []uuid.UUID {
	out := make([]uuid.UUID, len(p.Drives))
	for i, d := range p.Drives {
		out[i] = d.ID
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ByMonth counts the planned drives per month ("2026-10").
func (p Plan) ByMonth() map[string]int { return p.monthCount }

// Digest is the sha256 of the sorted id list: the dry run prints it and --apply must be given the same one.
func Digest(ids []uuid.UUID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	sort.Strings(s)
	sum := sha256.Sum256([]byte(strings.Join(s, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// BackupRow is what undoes one drive: its route before the fill, and the hash of the route the fill wrote (a restore only touches
// a drive whose route is still that one).
type BackupRow struct {
	DriveID uuid.UUID
	UserID  uuid.UUID
	Old     string
	NewSHA  string
}

// ApplyResult counts what Apply did.
type ApplyResult struct {
	Updated   int // routes rewritten
	Unchanged int // no dropout could be filled
	Raced     int // the route changed since the plan; left alone
	Failed    int // dropouts Valhalla could not fill, across all drives
	Filled    int // dropouts filled
}

// HashRoute is the sha256 a backup row keeps of the route a repair wrote (a restore only touches a drive whose route still hashes to it).
func HashRoute(s string) string { return hashRoute(s) }

func hashRoute(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Apply fills the planned drives one by one. For each: the new route is computed first; the backup row is written (and flushed by
// backup) before the update; the update only applies if the stored route is still the one the plan read. pace waits between drives
// so the Valhalla service is not hammered. It stops with an error when more than half of the dropouts it tried could not be filled
// (Valhalla is down or the area is not covered): better to stop than to burn through the list.
func Apply(ctx context.Context, db *sql.DB, plan Plan, f Filler, backup func(BackupRow) error, pace time.Duration) (ApplyResult, error) {
	var res ApplyResult
	for _, c := range plan.Drives {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		pts, err := Decode(c.Polyline)
		if err != nil {
			continue
		}
		fr := Fill(ctx, pts, f, plan.MinGapM)
		res.Filled += fr.Filled
		res.Failed += fr.Failed
		if tried := res.Filled + res.Failed; tried >= 8 && res.Failed*2 > tried {
			return res, fmt.Errorf("routegaps: %d of %d dropouts could not be filled; stopping (is Valhalla up?)", res.Failed, tried)
		}
		if fr.Filled == 0 {
			res.Unchanged++
			continue
		}
		newRoute := rivian.EncodePolyline(fr.Points)
		if err := backup(BackupRow{DriveID: c.ID, UserID: c.UserID, Old: c.Polyline, NewSHA: hashRoute(newRoute)}); err != nil {
			return res, fmt.Errorf("routegaps: backup: %w", err)
		}
		r, err := db.ExecContext(ctx, `UPDATE drives SET route_polyline = $3 WHERE id = $1 AND user_id = $2 AND route_polyline = $4`,
			c.ID, c.UserID, newRoute, c.Polyline)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n == 1 {
			res.Updated++
		} else {
			res.Raced++
		}
		if pace > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(pace):
			}
		}
	}
	return res, nil
}

// RestoreResult counts what Restore did.
type RestoreResult struct {
	Restored int
	Skipped  int // the route is no longer the one the fill wrote
}

// Restore puts the backed-up routes back, drive by drive, only where the stored route still hashes to the one the fill wrote.
func Restore(ctx context.Context, db *sql.DB, rows []BackupRow) (RestoreResult, error) {
	var res RestoreResult
	for _, r := range rows {
		out, err := db.ExecContext(ctx, `UPDATE drives SET route_polyline = $3
			 WHERE id = $1 AND user_id = $2 AND encode(sha256(convert_to(route_polyline, 'UTF8')), 'hex') = $4`,
			r.DriveID, r.UserID, r.Old, r.NewSHA)
		if err != nil {
			return res, err
		}
		if n, _ := out.RowsAffected(); n == 1 {
			res.Restored++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}
