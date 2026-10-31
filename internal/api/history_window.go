package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/histwin"
)

// The free plan's history window (Rivolt Pro, Sprint 14, item 2).
//
// A free account sees the last 90 days of drives and charges and the current and previous two months of statements. Nothing is deleted or
// hidden in storage: the recorder, the jobs, the export and every privacy path see everything. The window is a property of a request: a
// route that returns history is registered behind historyWindowMiddleware, which stamps the free window on the request's context for a
// free account (nothing for Pro, billing off, the demo, or when the plan lookup failed). Handlers then narrow their RESPONSE:
//
//   - Anything derived from the whole history (cost rates, charge classification, clusters) is computed over everything and only the rows
//     returned are narrowed, so a free account's numbers are the numbers a Pro account sees for the same rows, and nothing computed under
//     a window is ever stored or cached.
//   - A single row outside the window (a drive or charge by id) answers 402 {"error":"outside_free_window","free_days":90}; a Pro-only
//     insight answers 402 {"error":"pro_required"}. 402, not 403: the clients read 403 as "terms required" or "signed out".
//   - A client-supplied range (?since, ?month, ?from) is clamped to the window, never trusted.
//
// history_window_test.go classifies every route of the API: windowed, Pro-only, or exempt with a reason.

// HistoryWindowComplete is true only when every route that returns history is windowed or Pro-only (the todo map below is empty) AND the
// privacy deletes work outside the window. main.go refuses to start with RIVOLT_BILLING on while it is false: selling Pro before the free
// plan's limits exist would sell nothing, and before the delete-by-range exists would trap data outside the window.
const HistoryWindowComplete = false

// historyWindowMiddleware stamps the free window on the request when the signed-in account is free. e nil: no window.
func historyWindowMiddleware(e *entitlements.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if e == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := auth.UserFromContext(r.Context()); ok {
				if win, on := e.Window(r.Context(), uid); on {
					r = r.WithContext(histwin.With(r.Context(), win))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// moneyWindowStart is the first instant of the oldest month a free account's Money statements show: the current month and the two before,
// cut in the zone the request asked for. on is false when the request has no window (Pro, billing off, or a background context).
func moneyWindowStart(ctx context.Context, now time.Time, loc *time.Location) (from time.Time, on bool) {
	if _, ok := histwin.From(ctx); !ok {
		return time.Time{}, false
	}
	cur := now.In(loc)
	return time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -2, 0), true
}

// windowRows narrows rows (newest first or any order) to the request's window; with no window it returns rows unchanged.
func windowRows[T any](ctx context.Context, rows []T, started func(T) time.Time) []T {
	if _, ok := histwin.Since(ctx); !ok {
		return rows
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		if histwin.Allows(ctx, started(r)) {
			out = append(out, r)
		}
	}
	return out
}

// paywallBody is the body of a 402: error is "outside_free_window" (with free_days) or "pro_required". Clients act on the code.
type paywallBody struct {
	Error    string `json:"error"`
	FreeDays int    `json:"free_days,omitempty"`
}

// writeOutsideFreeWindow is the answer for a single row older than the free window.
func writeOutsideFreeWindow(w http.ResponseWriter) {
	writeJSON(w, http.StatusPaymentRequired, paywallBody{Error: "outside_free_window", FreeDays: histwin.FreeDays})
}

// writeProRequired is the answer for a read that is Pro-only on a free account (a CSV of a past month, trends, range near a temperature,
// capacity): the data is not "too old", the feature is Pro.
func writeProRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusPaymentRequired, paywallBody{Error: "pro_required"})
}

// driveInWindow reports whether the drive with this external id is inside the request's window. A drive that does not exist counts as
// inside, so the handler's own 404 still answers; with no window it is always inside and nothing is read.
func driveInWindow(ctx context.Context, pool *sql.DB, uid uuid.UUID, driveID string) (bool, error) {
	if _, ok := histwin.Since(ctx); !ok {
		return true, nil
	}
	var started time.Time
	err := pool.QueryRowContext(ctx, `SELECT started_at FROM drives WHERE user_id = $1 AND external_id = $2 ORDER BY started_at DESC LIMIT 1`, uid, driveID).Scan(&started)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return histwin.Allows(ctx, started), nil
}

// requireDriveInWindow writes the 402 (or a 500) and returns false when the drive may not be served on this request.
func requireDriveInWindow(w http.ResponseWriter, r *http.Request, pool *sql.DB, uid uuid.UUID, driveID string) bool {
	ok, err := driveInWindow(r.Context(), pool, uid, driveID)
	if err != nil {
		serverError(w, r, "history window: drive lookup", err) // the error text stays in the log, not in the response
		return false
	}
	if !ok {
		writeOutsideFreeWindow(w)
		return false
	}
	return true
}
