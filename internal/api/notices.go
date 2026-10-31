package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/launchnotice"
)

// The account's service notices (Rivolt Pro, Sprint 14, item 10): GET /api/notices lists the ones still within their window, with their
// text rendered here so the web and the iPhone show the same words as the email; POST /api/notices/dismiss hides the banner. Both are
// free and ungated for every plan. The notices are written by the operator command (rivolt launch-notice), never by a request.

type noticeJSON struct {
	Kind       string              `json:"kind"`
	Title      string              `json:"title"`
	Body       string              `json:"body"`
	Links      []launchnotice.Link `json:"links"`
	ShowsUntil time.Time           `json:"shows_until"`
	// Dismissed: the banner is hidden; the Settings row shows the notice until shows_until all the same.
	Dismissed bool `json:"dismissed"`
}

type noticesAPI struct {
	store launchnotice.Store
	now   func() time.Time
}

func newNoticesAPI(db *sql.DB) *noticesAPI {
	return &noticesAPI{store: launchnotice.Store{DB: db}, now: time.Now}
}

func (a *noticesAPI) list(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	ns, err := a.store.List(r.Context(), uid, a.now())
	if err != nil {
		serverError(w, r, "notices: list", err)
		return
	}
	out := []noticeJSON{}
	for _, n := range ns {
		var t launchnotice.InApp
		switch n.Kind {
		case launchnotice.Kind:
			t = launchnotice.RenderInApp(n.Params)
		case launchnotice.KindFoundingEnd:
			var fp launchnotice.FoundingParams
			if json.Unmarshal(n.Raw, &fp) != nil {
				continue
			}
			t = launchnotice.RenderFoundingInApp(fp)
		default:
			continue
		}
		out = append(out, noticeJSON{Kind: n.Kind, Title: t.Title, Body: t.Body, Links: t.Links, ShowsUntil: n.ShowsUntil, Dismissed: n.Dismissed != nil})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// dismiss: POST /api/notices/dismiss {"kind"}: hides the banner. Idempotent; 404 for a notice the account does not have.
func (a *noticesAPI) dismiss(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil || b.Kind == "" {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	ok, err := a.store.Dismiss(r.Context(), uid, b.Kind)
	if err != nil {
		serverError(w, r, "notices: dismiss", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
