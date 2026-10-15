package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
)

// Plan vs Actual endpoints (B12; contract
// scratchpad/plan-vs-actual-contract.md). Matching and comparison are
// the ledger's pure functions (ParsePlan / MatchPlan / ComparePlan /
// PlanWindow); this file owns the HTTP shape, the links table, the
// plan snapshot taken at link time, and loading the ledger input for
// the plan's window. Behind money_tab_enabled, user-scoped.

// planLink is one trip_plan_links row.
type planLink struct {
	TripID     string
	Status     string // linked | unlinked
	Confidence *float64
	StartedAt  time.Time
	EndedAt    time.Time
	Snapshot   planSnapshot
	LinkedAt   time.Time
}

// planSnapshot is the {"inputs": ..., "plan": ...} stored on a link.
type planSnapshot struct {
	Inputs json.RawMessage `json:"inputs"`
	Plan   json.RawMessage `json:"plan"`
}

// planLinkStore is the links table, scoped by user.
type planLinkStore interface {
	// List returns every row for the saved trip.
	List(ctx context.Context, uid, savedTripID uuid.UUID) ([]planLink, error)
	// Link makes tripID the (only) linked outing, turning any other
	// linked row 'unlinked', in one transaction.
	Link(ctx context.Context, uid, savedTripID uuid.UUID, l planLink) error
	// Unlink marks the outing unlinked (inserting the row if needed).
	Unlink(ctx context.Context, uid, savedTripID uuid.UUID, l planLink) error
}

// planActualAPI holds the handlers' seams so tests run without
// Postgres or the matching code.
type planActualAPI struct {
	getTrip func(ctx context.Context, uid, id uuid.UUID) (*trips.SavedTrip, error)
	links   planLinkStore
	input   func(ctx context.Context, uid uuid.UUID, from, to time.Time) (ledger.Input, error)
	parse   func(inputs, plan json.RawMessage, createdAt time.Time) (ledger.Plan, error)
	match   func(p ledger.Plan, in ledger.Input) []ledger.PlanCandidate
	compare func(p ledger.Plan, in ledger.Input, tripID string) (ledger.PlanComparison, bool)
	window  func(p ledger.Plan) (time.Time, time.Time)
	now     func() time.Time
}

func newPlanActualAPI(pool *sql.DB, tf *trips.Factory, sf *settings.Factory) *planActualAPI {
	return &planActualAPI{
		getTrip: func(ctx context.Context, uid, id uuid.UUID) (*trips.SavedTrip, error) {
			return tf.For(uid).Get(ctx, id)
		},
		links: pgPlanLinks{db: pool},
		input: func(ctx context.Context, uid uuid.UUID, from, to time.Time) (ledger.Input, error) {
			var st *settings.Store
			if sf != nil {
				st = sf.For(uid)
			}
			cfg, err := ledgerConfig(ctx, st)
			if err != nil {
				return ledger.Input{}, err
			}
			cs, ds, err := ledger.Load(ctx, pool, uid, "", from, to)
			if err != nil {
				return ledger.Input{}, err
			}
			return ledger.Input{Charges: cs, Drives: ds, Config: cfg, Now: time.Now()}, nil
		},
		parse:   ledger.ParsePlan,
		match:   ledger.MatchPlan,
		compare: ledger.ComparePlan,
		window:  ledger.PlanWindow,
		now:     time.Now,
	}
}

// ---- response ---------------------------------------------------------

type planActualLink struct {
	TripID     string    `json:"trip_id"`
	Confidence *float64  `json:"confidence"`
	Band       string    `json:"band"`
	LinkedAt   time.Time `json:"linked_at"`
	LinkStale  bool      `json:"link_stale"`
}

type planActualResponse struct {
	Version     int                    `json:"version"`
	SavedTripID string                 `json:"saved_trip_id"`
	Status      string                 `json:"status"` // linked | suggested | none
	Link        *planActualLink        `json:"link"`
	Candidates  []ledger.PlanCandidate `json:"candidates"`
	Comparison  *ledger.PlanComparison `json:"comparison"`
}

func bandFor(conf float64) string {
	switch {
	case conf >= 0.80:
		return "high"
	case conf >= 0.50:
		return "medium"
	}
	return "low"
}

// ---- shared computation ----------------------------------------------

func (a *planActualAPI) loadTrip(w http.ResponseWriter, r *http.Request, uid uuid.UUID) (*trips.SavedTrip, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	st, err := a.getTrip(r.Context(), uid, id)
	if err != nil {
		if errors.Is(err, trips.ErrNotFound) {
			http.NotFound(w, r)
			return nil, false
		}
		http.Error(w, "trip lookup failed", http.StatusInternalServerError)
		return nil, false
	}
	return st, true
}

// build computes the §4 body for a saved trip. Pure apart from the
// store and ledger reads.
func (a *planActualAPI) build(ctx context.Context, uid uuid.UUID, st *trips.SavedTrip) (planActualResponse, error) {
	out := planActualResponse{Version: 1, SavedTripID: st.ID.String(), Status: "none", Candidates: []ledger.PlanCandidate{}}
	rows, err := a.links.List(ctx, uid, st.ID)
	if err != nil {
		return out, err
	}
	var linked *planLink
	unlinked := map[string]bool{}
	for i := range rows {
		switch rows[i].Status {
		case "linked":
			linked = &rows[i]
		case "unlinked":
			unlinked[rows[i].TripID] = true
		}
	}
	// Current plan: candidates and suggestions come from it.
	cur, err := a.parse(st.Inputs, st.Plan, st.CreatedAt)
	if err != nil {
		// A saved trip with no usable plan (ledger.ErrPlanUnusable:
		// no origin or destination, never planned, or an older
		// shape) has nothing to compare: status none, not a 500.
		return out, nil
	}
	// Load one window covering the current plan (and the snapshot's,
	// when linked).
	from, to := a.window(cur)
	var snap ledger.Plan
	if linked != nil {
		if snap, err = a.parse(linked.Snapshot.Inputs, linked.Snapshot.Plan, st.CreatedAt); err != nil {
			return out, err
		}
		sf, stt := a.window(snap)
		if !sf.IsZero() && (from.IsZero() || sf.Before(from)) {
			from = sf
		}
		if stt.After(to) {
			to = stt
		}
	}
	in, err := a.input(ctx, uid, from, to)
	if err != nil {
		return out, err
	}
	for _, c := range a.match(cur, in) {
		if !unlinked[c.TripID] {
			out.Candidates = append(out.Candidates, c)
		}
	}
	if linked != nil {
		tripID, stale := linked.TripID, false
		cmp, ok := a.compare(snap, in, tripID)
		if !ok {
			// The outing's first drive changed (re-import / dedupe):
			// re-find it by time overlap among the snapshot's candidates.
			for _, c := range a.match(snap, in) {
				if c.StartedAt.Before(linked.EndedAt) && linked.StartedAt.Before(c.EndedAt) {
					if cmp, ok = a.compare(snap, in, c.TripID); ok {
						tripID, stale = c.TripID, true
						break
					}
				}
			}
		}
		if !ok {
			// Neither the linked id nor an overlapping outing can be
			// rebuilt: say so, so the UI can explain.
			stale = true
		}
		l := &planActualLink{TripID: tripID, Confidence: linked.Confidence, LinkedAt: linked.LinkedAt, LinkStale: stale}
		if linked.Confidence != nil {
			l.Band = bandFor(*linked.Confidence)
		}
		out.Status, out.Link = "linked", l
		if ok {
			cmp.PlanFrom = "snapshot" // compared against the plan as linked
			out.Comparison = &cmp
		}
		return out, nil
	}
	// No link: preview the top candidate when it is high confidence.
	if len(out.Candidates) > 0 && out.Candidates[0].Band == "high" {
		if cmp, ok := a.compare(cur, in, out.Candidates[0].TripID); ok {
			cmp.PlanFrom = "current" // a suggestion uses the plan as saved now
			out.Status, out.Comparison = "suggested", &cmp
		}
	}
	return out, nil
}

// ---- handlers -----------------------------------------------------------

// GET /api/trips/saved/{id}/actual
func (a *planActualAPI) actual(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	st, ok := a.loadTrip(w, r, uid)
	if !ok {
		return
	}
	out, err := a.build(r.Context(), uid, st)
	if err != nil {
		a.fail(w, r, "plan vs actual", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/trips/saved/{id}/link {"trip_id": "..."}
func (a *planActualAPI) link(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	st, ok := a.loadTrip(w, r, uid)
	if !ok {
		return
	}
	var body struct {
		TripID string `json:"trip_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.TripID) == "" {
		http.Error(w, "trip_id is required", http.StatusBadRequest)
		return
	}
	tripID := strings.TrimSpace(body.TripID)
	cur, err := a.parse(st.Inputs, st.Plan, st.CreatedAt)
	if err != nil {
		http.Error(w, "plan: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	from, to := a.window(cur)
	in, err := a.input(r.Context(), uid, from, to)
	if err != nil {
		a.fail(w, r, "plan vs actual", err)
		return
	}
	var picked *ledger.PlanCandidate
	for _, c := range a.match(cur, in) {
		if c.TripID == tripID {
			c := c
			picked = &c
			break
		}
	}
	if picked == nil {
		http.Error(w, "trip_id is not a candidate for this plan", http.StatusConflict)
		return
	}
	conf := picked.Confidence
	err = a.links.Link(r.Context(), uid, st.ID, planLink{
		TripID: tripID, Status: "linked", Confidence: &conf,
		StartedAt: picked.StartedAt, EndedAt: picked.EndedAt,
		Snapshot: planSnapshot{Inputs: st.Inputs, Plan: st.Plan},
	})
	if err != nil {
		a.fail(w, r, "link", err)
		return
	}
	out, err := a.build(r.Context(), uid, st)
	if err != nil {
		a.fail(w, r, "plan vs actual", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/trips/saved/{id}/unlink [{"trip_id": "..."}]
//
//   - No body: unlinks the current link; with nothing linked it is a
//     no-op that returns 200 (so a double-click can't dismiss
//     anything else).
//   - {"trip_id"}: rejects that outing ("Not this trip"). An unlinked
//     row with a snapshot is written so it is never suggested again
//     for this plan. 409 when it isn't one of the plan's candidates
//     (and isn't the linked outing).
func (a *planActualAPI) unlink(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	st, ok := a.loadTrip(w, r, uid)
	if !ok {
		return
	}
	var body struct {
		TripID string `json:"trip_id"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	}
	rows, err := a.links.List(r.Context(), uid, st.ID)
	if err != nil {
		a.fail(w, r, "unlink", err)
		return
	}
	var linked *planLink
	for i := range rows {
		if rows[i].Status == "linked" {
			linked = &rows[i]
		}
	}
	tripID := strings.TrimSpace(body.TripID)
	var target *planLink
	switch {
	case tripID == "":
		target = linked // nil: a no-op
	case linked != nil && linked.TripID == tripID:
		target = linked
	default:
		cur, perr := a.parse(st.Inputs, st.Plan, st.CreatedAt)
		if perr != nil {
			http.Error(w, "trip_id is not a candidate for this plan", http.StatusConflict)
			return
		}
		from, to := a.window(cur)
		in, ierr := a.input(r.Context(), uid, from, to)
		if ierr != nil {
			a.fail(w, r, "unlink", ierr)
			return
		}
		for _, c := range a.match(cur, in) {
			if c.TripID == tripID {
				conf := c.Confidence
				target = &planLink{TripID: c.TripID, Confidence: &conf, StartedAt: c.StartedAt, EndedAt: c.EndedAt,
					Snapshot: planSnapshot{Inputs: st.Inputs, Plan: st.Plan}}
				break
			}
		}
		if target == nil {
			http.Error(w, "trip_id is not a candidate for this plan", http.StatusConflict)
			return
		}
	}
	if target != nil {
		target.Status = "unlinked"
		if err := a.links.Unlink(r.Context(), uid, st.ID, *target); err != nil {
			a.fail(w, r, "unlink", err)
			return
		}
	}
	out, err := a.build(r.Context(), uid, st)
	if err != nil {
		a.fail(w, r, "plan vs actual", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// fail logs the error and answers a generic 500 (store and SQL errors
// stay out of response bodies).
func (a *planActualAPI) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "plan vs actual", "op", what, "err", err.Error(), "path", r.URL.Path)
	http.Error(w, "plan vs actual unavailable", http.StatusInternalServerError)
}

// ---- Postgres store -----------------------------------------------------

type pgPlanLinks struct{ db *sql.DB }

func (p pgPlanLinks) List(ctx context.Context, uid, savedTripID uuid.UUID) ([]planLink, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT trip_id, status, confidence::float8, started_at, ended_at, plan_snapshot, updated_at
		  FROM trip_plan_links
		 WHERE user_id = $1 AND saved_trip_id = $2
		 ORDER BY created_at`, uid, savedTripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []planLink
	for rows.Next() {
		var l planLink
		var conf sql.NullFloat64
		var snap []byte
		if err := rows.Scan(&l.TripID, &l.Status, &conf, &l.StartedAt, &l.EndedAt, &snap, &l.LinkedAt); err != nil {
			return nil, err
		}
		if conf.Valid {
			l.Confidence = &conf.Float64
		}
		if err := json.Unmarshal(snap, &l.Snapshot); err != nil {
			return nil, err
		}
		l.StartedAt, l.EndedAt, l.LinkedAt = l.StartedAt.UTC(), l.EndedAt.UTC(), l.LinkedAt.UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

func (p pgPlanLinks) Link(ctx context.Context, uid, savedTripID uuid.UUID, l planLink) error {
	snap, err := json.Marshal(l.Snapshot)
	if err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE trip_plan_links SET status = 'unlinked', updated_at = NOW()
		 WHERE user_id = $1 AND saved_trip_id = $2 AND status = 'linked' AND trip_id <> $3`,
		uid, savedTripID, l.TripID); err != nil {
		return err
	}
	// The saved trip must be the user's: the insert selects it by
	// (id, user_id) so another user's id inserts nothing.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO trip_plan_links (saved_trip_id, user_id, trip_id, status, confidence, started_at, ended_at, plan_snapshot)
		SELECT s.id, s.user_id, $3, 'linked', $4, $5::timestamptz, $6::timestamptz, $7::jsonb
		  FROM saved_trips s WHERE s.id = $2 AND s.user_id = $1
		ON CONFLICT (saved_trip_id, trip_id) DO UPDATE SET
		       -- Re-linking the outing that is already linked (a retry,
		       -- a double submit, or Link after re-planning) must not
		       -- rewrite the snapshot: contract §3.
		       confidence    = CASE WHEN trip_plan_links.status = 'linked' THEN trip_plan_links.confidence    ELSE EXCLUDED.confidence    END,
		       started_at    = CASE WHEN trip_plan_links.status = 'linked' THEN trip_plan_links.started_at    ELSE EXCLUDED.started_at    END,
		       ended_at      = CASE WHEN trip_plan_links.status = 'linked' THEN trip_plan_links.ended_at      ELSE EXCLUDED.ended_at      END,
		       plan_snapshot = CASE WHEN trip_plan_links.status = 'linked' THEN trip_plan_links.plan_snapshot ELSE EXCLUDED.plan_snapshot END,
		       status        = 'linked',
		       updated_at    = CASE WHEN trip_plan_links.status = 'linked' THEN trip_plan_links.updated_at ELSE NOW() END`,
		uid, savedTripID, l.TripID, nullFloat(l.Confidence), l.StartedAt.UTC(), l.EndedAt.UTC(), string(snap))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (p pgPlanLinks) Unlink(ctx context.Context, uid, savedTripID uuid.UUID, l planLink) error {
	snap, err := json.Marshal(l.Snapshot)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO trip_plan_links (saved_trip_id, user_id, trip_id, status, confidence, started_at, ended_at, plan_snapshot)
		SELECT s.id, s.user_id, $3, 'unlinked', $4, $5::timestamptz, $6::timestamptz, $7::jsonb
		  FROM saved_trips s WHERE s.id = $2 AND s.user_id = $1
		ON CONFLICT (saved_trip_id, trip_id) DO UPDATE SET status = 'unlinked', updated_at = NOW()`,
		uid, savedTripID, l.TripID, nullFloat(l.Confidence), l.StartedAt.UTC(), l.EndedAt.UTC(), string(snap))
	return err
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
