package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/settings"
)

// Pro prompts the person has dismissed ("Not now", Pro entry D6). Per account, so a dismissal follows the person across browsers and the
// phone. The first "Not now" hides that prompt for proPromptHide (90 days); a second hides it for good. The header "Pro tier" link is not a
// prompt and never hides.
//
// State is one per-user setting, keyPromptState, in the same store as the primary vehicle: a JSON object {where: {dismissed_at, count}}. It
// is read from Postgres on every request (no process cache), so every replica agrees. The only writer is the person's own POST, which
// takes a per-account lock so two replicas cannot lose a count. Whether a prompt is hidden is worked out when it is read (no job): a
// dismissal made before the account's latest plan change (the end of a Pro period) no longer counts, so a lapse shows its prompts again.
// A failed read hides every prompt (it fails closed: no prompt is better than one the person already turned away). Account deletion
// removes the row with the user (user_settings cascades), and the key is in the export.

const (
	keyPromptState = "pro.prompts"
	proPromptHide  = 90 * 24 * time.Hour
)

// proPromptWheres is the fixed list of places a prompt can be dismissed from. The path value is checked against it; anything else is 404.
var proPromptWheres = []string{"lapse_banner", "planner"}

type promptMark struct {
	DismissedAt time.Time `json:"dismissed_at"`
	Count       int       `json:"count"`
}

func validPromptWhere(w string) bool {
	for _, k := range proPromptWheres {
		if k == w {
			return true
		}
	}
	return false
}

func parsePromptState(raw string) map[string]promptMark {
	m := map[string]promptMark{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
			return map[string]promptMark{}
		}
	}
	return m
}

// promptHidden says whether a prompt is hidden at now. changedAt is the account's latest plan change (zero: none); a mark older than it is void.
func promptHidden(m promptMark, changedAt, now time.Time) bool {
	if m.Count <= 0 || (!changedAt.IsZero() && m.DismissedAt.Before(changedAt)) {
		return false
	}
	return m.Count >= 2 || now.Before(m.DismissedAt.Add(proPromptHide))
}

// hiddenPrompts is the sorted list of prompt places hidden for the account. On a failed read it is every place.
func hiddenPrompts(ctx context.Context, db *sql.DB, uid uuid.UUID, changedAt, now time.Time) []string {
	st, err := settings.OpenStore(db, uid)
	var all map[string]string
	if err == nil {
		all, err = st.GetAll(ctx)
	}
	if err != nil {
		return append([]string{}, proPromptWheres...)
	}
	marks := parsePromptState(all[keyPromptState])
	out := []string{}
	for _, w := range proPromptWheres {
		if promptHidden(marks[w], changedAt, now) {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// dismissPrompt records one "Not now" for where, under a per-account lock, and returns the new mark.
func dismissPrompt(ctx context.Context, db *sql.DB, uid uuid.UUID, where string, changedAt, now time.Time) (promptMark, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return promptMark{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('pro.prompts:' || $1::text))`, uid.String()); err != nil {
		return promptMark{}, err
	}
	var cur sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT value->>$3 FROM user_settings WHERE user_id = $1 AND namespace = $2`, uid, "misc", keyPromptState).Scan(&cur); err != nil && err != sql.ErrNoRows {
		return promptMark{}, err
	}
	marks := parsePromptState(cur.String)
	m := marks[where]
	if !changedAt.IsZero() && m.DismissedAt.Before(changedAt) {
		m = promptMark{} // a mark from before the plan change starts over
	}
	if m.Count < 2 {
		m.Count++
	}
	m.DismissedAt = now.UTC()
	marks[where] = m
	doc, err := json.Marshal(marks)
	if err != nil {
		return promptMark{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_settings (user_id, namespace, value)
		VALUES ($1, 'misc', jsonb_build_object($2::text, to_jsonb($3::text)))
		ON CONFLICT (user_id, namespace) DO UPDATE
			SET value = user_settings.value || jsonb_build_object($2::text, to_jsonb($3::text)), updated_at = NOW()`,
		uid, keyPromptState, string(doc)); err != nil {
		return promptMark{}, err
	}
	return m, tx.Commit()
}

// handleProPromptDismiss: POST /api/pro/prompts/{where}/dismiss. 204 on success, 404 for a place not on the list. The body is ignored.
func handleProPromptDismiss(d Deps) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		where := chi.URLParam(r, "where")
		if !validPromptWhere(where) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		if d.DB == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		var changed time.Time
		if d.Entitlements != nil {
			if pf := planFor(r.Context(), d.Entitlements, uid); pf != nil && pf.PlanEndedAt != nil {
				changed = *pf.PlanEndedAt
			}
		}
		if _, err := dismissPrompt(r.Context(), d.DB, uid, where, changed, time.Now()); err != nil {
			serverError(w, r, "pro prompts: dismiss", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
