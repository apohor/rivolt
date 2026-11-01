// Package routepoints audits and repairs stored drive routes that hold a point which is not on the globe.
//
// A drive's stored route (drives.route_polyline) once got one point outside +-90 / +-180 (the recorder spliced an unchecked routing-engine
// shape into the path; see rivian.appendDrivePath), which draws as two segments of thousands of kilometres, out and back. The recorder
// no longer lets such a point in, and the readers skip one already stored; this package finds the routes that still hold one and
// rewrites them without it.
//
// The audit reads every closed drive's route and reports counts only (never a position). The repair drops the off-globe points and
// re-encodes the rest. It does NOT rebuild the route from samples: a stored route also holds the road-snapped fills the recorder
// spliced in, which samples do not have, so a rebuild would turn each fill back into a straight line; dropping the bad point keeps
// everything else exactly as it was. Only drives.route_polyline changes.
package routepoints

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/repair/routegaps"
	"github.com/apohor/rivolt/internal/rivian"
)

// Candidate is a closed drive with a stored route.
type Candidate struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	StartedAt time.Time
	Polyline  string
}

// Select lists every closed drive that started in [since, until) and has a stored route, whatever its source (an imported route can be
// corrupt too). A drive that ended in the last hour may still be written to and is left out.
func Select(ctx context.Context, db *sql.DB, since, until time.Time) ([]Candidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, started_at, route_polyline
		  FROM drives
		 WHERE COALESCE(route_polyline, '') <> ''
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

// OnGlobe reports whether (lat, lon) is a real position: finite and within +-90 / +-180.
func OnGlobe(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

// Strip returns pts without the points that are not on the globe, and how many it removed.
func Strip(pts [][2]float64) ([][2]float64, int) {
	out := make([][2]float64, 0, len(pts))
	for _, p := range pts {
		if OnGlobe(p[0], p[1]) {
			out = append(out, p)
		}
	}
	return out, len(pts) - len(out)
}

// Plan is the audit: how many routes were read and which hold a point off the globe.
type Plan struct {
	Scanned    int // routes read
	Undecoded  int // routes that are not a valid polyline (left alone, counted)
	BadPoints  int // points off the globe, across all routes
	Unfixable  int // routes that would have fewer than two points left (left alone, counted)
	Drives     []Candidate
	monthCount map[string]int
}

// MakePlan audits the candidates. A route is in Drives when it holds a bad point AND dropping it leaves a route of at least two points
// (the repair's work list); a route with nothing left is only counted in Unfixable.
func MakePlan(cands []Candidate) Plan {
	p := Plan{Scanned: len(cands), monthCount: map[string]int{}}
	for _, c := range cands {
		pts, err := routegaps.Decode(c.Polyline)
		if err != nil {
			p.Undecoded++
			continue
		}
		kept, bad := Strip(pts)
		if bad == 0 {
			continue
		}
		p.BadPoints += bad
		if len(kept) < 2 {
			p.Unfixable++
			continue
		}
		p.Drives = append(p.Drives, c)
		p.monthCount[routegaps.Month(c.StartedAt)]++
	}
	return p
}

// IDs are the work list's drive ids, sorted.
func (p Plan) IDs() []uuid.UUID {
	out := make([]uuid.UUID, len(p.Drives))
	for i, d := range p.Drives {
		out[i] = d.ID
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ByMonth counts the work list per month ("2026-10").
func (p Plan) ByMonth() map[string]int { return p.monthCount }

// Digest is the sha256 of the sorted id list, as the other repairs print it.
func Digest(ids []uuid.UUID) string { return routegaps.Digest(ids) }

// ApplyResult counts what Apply did.
type ApplyResult struct {
	Updated int // routes rewritten
	Raced   int // the route changed since the audit; left alone
	Removed int // points dropped, across the routes rewritten
}

// Apply rewrites the work list one drive at a time: the new route is the old one without its off-globe points; the backup row (the old
// route and the hash of the new one) is written and flushed before the update; and the update only applies if the stored route is still
// the one the audit read. routegaps.Restore undoes it from the same rows.
func Apply(ctx context.Context, db *sql.DB, plan Plan, backup func(routegaps.BackupRow) error) (ApplyResult, error) {
	var res ApplyResult
	for _, c := range plan.Drives {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		pts, err := routegaps.Decode(c.Polyline)
		if err != nil {
			continue
		}
		kept, bad := Strip(pts)
		if bad == 0 || len(kept) < 2 {
			continue
		}
		newRoute := rivian.EncodePolyline(kept)
		if err := backup(routegaps.BackupRow{DriveID: c.ID, UserID: c.UserID, Old: c.Polyline, NewSHA: routegaps.HashRoute(newRoute)}); err != nil {
			return res, fmt.Errorf("routepoints: backup: %w", err)
		}
		r, err := db.ExecContext(ctx, `UPDATE drives SET route_polyline = $3 WHERE id = $1 AND user_id = $2 AND route_polyline = $4`,
			c.ID, c.UserID, newRoute, c.Polyline)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n == 1 {
			res.Updated++
			res.Removed += bad
		} else {
			res.Raced++
		}
	}
	return res, nil
}
