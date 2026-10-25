package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

type fakeSessions struct {
	rows      []sessions.Session
	revoked   []uuid.UUID
	owner     uuid.UUID
	keptOther uuid.UUID
}

func (f *fakeSessions) List(_ context.Context, uid uuid.UUID) ([]sessions.Session, error) {
	if uid != f.owner {
		return nil, nil
	}
	return f.rows, nil
}

func (f *fakeSessions) RevokeOwned(_ context.Context, uid, id uuid.UUID) (bool, error) {
	if uid != f.owner {
		return false, nil
	}
	for _, r := range f.rows {
		if r.ID == id {
			f.revoked = append(f.revoked, id)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeSessions) RevokeOthers(_ context.Context, uid, keep uuid.UUID) (int64, error) {
	f.keptOther = keep
	return int64(len(f.rows) - 1), nil
}

func sessReq(h http.HandlerFunc, method, path string, uid uuid.UUID, impersonating bool) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/auth/sessions", h)
	r.Delete("/api/auth/sessions/{id}", h)
	r.Post("/api/auth/sessions/revoke-others", h)
	req := httptest.NewRequest(method, path, nil)
	ctx := req.Context()
	if uid != uuid.Nil {
		ctx = auth.WithUser(ctx, uid)
	}
	if impersonating {
		ctx = auth.WithImpersonation(ctx, uuid.New(), uid)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestSessionsAPI(t *testing.T) {
	me, stranger := uuid.New(), uuid.New()
	now := time.Now()
	web := sessions.Session{ID: uuid.New(), UserID: me, Kind: sessions.KindWeb, UserAgent: "Mozilla/5.0 Safari", IPAddress: "203.0.113.9", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	app := sessions.Session{ID: uuid.New(), UserID: me, Kind: sessions.KindApp, DeviceLabel: "Sam's iPhone", CreatedAt: now, LastSeenAt: now.Add(-time.Hour), ExpiresAt: now.Add(90 * 24 * time.Hour)}
	f := &fakeSessions{rows: []sessions.Session{web, app}, owner: me}
	a := &SessionsAPI{Store: f, Current: func(*http.Request) (uuid.UUID, bool) { return app.ID, true }}

	// List: the caller's sessions, current marked, no IP or hash.
	rec := sessReq(a.List, "GET", "/api/auth/sessions", me, false)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list = %d %v", rec.Code, rec.Header())
	}
	var out struct {
		Sessions []sessionJSON `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Sessions) != 2 {
		t.Fatalf("%v %s", err, rec.Body.String())
	}
	for _, s := range out.Sessions {
		if s.Current != (s.ID == app.ID) || s.Kind == "" {
			t.Errorf("session %+v", s)
		}
	}
	if strings.Contains(rec.Body.String(), "203.0.113.9") || strings.Contains(rec.Body.String(), "hash") || strings.Contains(rec.Body.String(), `"ip`) || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("the list leaks the IP or a hash: %s", rec.Body.String())
	}
	// Nobody else sees them; an admin viewing as the user does not either.
	if rec := sessReq(a.List, "GET", "/api/auth/sessions", stranger, false); !strings.Contains(rec.Body.String(), `"sessions":[]`) {
		t.Errorf("another user's list = %s", rec.Body.String())
	}
	if rec := sessReq(a.List, "GET", "/api/auth/sessions", me, true); rec.Code != http.StatusForbidden {
		t.Errorf("impersonated list = %d", rec.Code)
	}
	if rec := sessReq(a.List, "GET", "/api/auth/sessions", uuid.Nil, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous list = %d", rec.Code)
	}

	// Revoke one: own sessions only; unknown, foreign and malformed ids are the same 404.
	if rec := sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+web.ID.String(), me, false); rec.Code != http.StatusNoContent || len(f.revoked) != 1 {
		t.Errorf("revoke own = %d", rec.Code)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if rec := sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+id, me, false); rec.Code != http.StatusNotFound {
			t.Errorf("revoke %q = %d, want 404", id, rec.Code)
		}
	}
	if rec := sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+web.ID.String(), stranger, false); rec.Code != http.StatusNotFound {
		t.Errorf("revoking someone else's session = %d, want 404", rec.Code)
	}
	if rec := sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+web.ID.String(), me, true); rec.Code != http.StatusForbidden {
		t.Errorf("impersonated revoke = %d", rec.Code)
	}

	// Sign out everywhere else keeps the caller's own session.
	rec = sessReq(a.RevokeOthers, "POST", "/api/auth/sessions/revoke-others", me, false)
	if rec.Code != 200 || f.keptOther != app.ID || !strings.Contains(rec.Body.String(), `"revoked":1`) {
		t.Errorf("revoke others = %d %s (kept %v)", rec.Code, rec.Body.String(), f.keptOther)
	}
	// Without a session to keep it refuses rather than signing the caller out too.
	f.keptOther = uuid.Nil
	a.Current = nil
	if rec := sessReq(a.RevokeOthers, "POST", "/api/auth/sessions/revoke-others", me, false); rec.Code != http.StatusConflict || f.keptOther != uuid.Nil {
		t.Errorf("revoke others without a current session = %d (kept %v)", rec.Code, f.keptOther)
	}
}

// The list is whole by default (a client that does not show "N older browsers not shown" must never get a cut list). A client that
// does ask ?recent=N gets every app session, the current one and the N newest other browsers, with the rest counted. Revocation is
// not affected: "sign out everywhere else" still takes them all.
func TestSessionsListIsWholeUnlessTheClientAsksForRecent(t *testing.T) {
	me := uuid.New()
	now := time.Now()
	var rows []sessions.Session
	for i := 0; i < 40; i++ { // newest first, as the store returns them
		rows = append(rows, sessions.Session{ID: uuid.New(), UserID: me, Kind: sessions.KindWeb, CreatedAt: now, LastSeenAt: now.Add(-time.Duration(i) * time.Hour), ExpiresAt: now.Add(time.Hour)})
	}
	app := sessions.Session{ID: uuid.New(), UserID: me, Kind: sessions.KindApp, DeviceLabel: "Phone", CreatedAt: now, LastSeenAt: now.Add(-100 * time.Hour), ExpiresAt: now.Add(time.Hour)}
	rows = append(rows, app) // older than every web row: it must still be shown
	current := rows[30]      // an old web session that is the caller's own: it must still be shown
	f := &fakeSessions{rows: rows, owner: me}
	a := &SessionsAPI{Store: f, Current: func(*http.Request) (uuid.UUID, bool) { return current.ID, true }}
	list := func(path string) (ids map[uuid.UUID]bool, total, hidden int) {
		rec := sessReq(a.List, "GET", path, me, false)
		var out struct {
			Sessions  []sessionJSON `json:"sessions"`
			Total     int           `json:"total"`
			HiddenWeb int           `json:"hidden_web"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		ids = map[uuid.UUID]bool{}
		for _, s := range out.Sessions {
			ids[s.ID] = true
		}
		return ids, out.Total, out.HiddenWeb
	}
	for _, path := range []string{"/api/auth/sessions", "/api/auth/sessions?recent=", "/api/auth/sessions?recent=x", "/api/auth/sessions?recent=-1", "/api/auth/sessions?recent=51", "/api/auth/sessions?all=1"} {
		if ids, total, hidden := list(path); len(ids) != 41 || total != 41 || hidden != 0 {
			t.Errorf("%s: %d shown, total %d, hidden %d, want the whole list (41, 41, 0)", path, len(ids), total, hidden)
		}
	}
	ids, total, hidden := list("/api/auth/sessions?recent=10")
	if len(ids) != 10+2 || total != 41 || hidden != 40-10-1 {
		t.Errorf("recent=10: %d shown, total %d, hidden %d, want 12 shown, 41, 29", len(ids), total, hidden)
	}
	if !ids[app.ID] || !ids[current.ID] || !ids[rows[0].ID] || ids[rows[20].ID] {
		t.Error("recent must keep the app session, the current one and the newest, and drop old browsers")
	}
	if ids, _, hidden := list("/api/auth/sessions?recent=0"); len(ids) != 2 || hidden != 39 {
		t.Errorf("recent=0: %d shown, hidden %d, want the app session and the current one, 39 hidden", len(ids), hidden)
	}
}
