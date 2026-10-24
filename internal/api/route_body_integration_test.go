//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/testpg"
)

// POST /api/trips/overnight-candidates through the real router, with a signed-in user and the trip-planner flag on: a route
// body over the group's 1 MiB and under routeBodyMax gets past the body cap, and one over routeBodyMax is refused. (maxBodyBytes matches on the path, so it is tested with the full URL.)
func TestOvernightCandidatesRouteBodyCapThroughTheRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "route-body")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh account must accept the Terms before any data route answers (the server-side gate): this one has.
	if err := db.AcceptTerms(ctx, pool, uid, currentTermsVersion); err != nil {
		t.Fatal(err)
	}
	ss, err := sessions.New(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	authSvc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	authSvc.WithSessionStore(sessions.NewAuthAdapter(ss))
	fl, err := flags.OpenStore(ctx, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fl.SetTripPlanner(ctx, true, "test"); err != nil {
		t.Fatal(err)
	}
	_, tok, err := ss.Create(ctx, uid, time.Hour, sessions.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: authSvc, AuthEnforced: true, DB: pool, Flags: fl, ChargersArchive: emptyArchive(t)})
	post := func(path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: tok})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	body := func(points int) []byte {
		b, _ := json.Marshal(map[string]any{"path": longRoute(points), "drive_sec": 40000, "budget_hours": 8})
		return b
	}
	mid := body(80_000) // about 2 MB
	if len(mid) < 1<<20+100_000 || len(mid) > routeBodyMax {
		t.Fatalf("test body is %d bytes", len(mid))
	}
	if rec := post("/api/trips/overnight-candidates", mid); rec.Code != http.StatusOK {
		t.Errorf("a %d-byte route body: %d %q", len(mid), rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	big := bytes.Repeat([]byte(" "), routeBodyMax+1<<20) // 9 MiB of body, valid JSON after the padding
	big = append(big, mid...)
	if rec := post("/api/trips/overnight-candidates", big); rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("a %d-byte body: %d %q, want refused as too large", len(big), rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
