//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// The account's units against real Postgres and the real router: unset reads the placeholder, a client seeds once (first writer
// wins), a person's pick replaces a seed, temperature follows distance until chosen and "auto" returns to that, `units` rides in
// /api/auth/me, and one account's choice never shows on another's.
func TestAccountUnits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "units-a")
	b, _ := db.EnsureUser(ctx, pool, "units-b")
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, DB: pool, Settings: settings.NewFactory(pool)})
	cookie := func(uid uuid.UUID) *http.Cookie {
		rec := httptest.NewRecorder()
		if err := svc.IssueSession(rec, httptest.NewRequest(http.MethodGet, "/", nil), uid); err != nil {
			t.Fatal(err)
		}
		return rec.Result().Cookies()[0]
	}
	ca, cb := cookie(a), cookie(b)
	call := func(c *http.Cookie, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(c)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	want := func(c *http.Cookie, dist, temp, dsrc, tsrc string) {
		t.Helper()
		code, out := call(c, "GET", "/api/settings/units", "")
		if code != 200 || out["distance_unit"] != dist || out["temperature_unit"] != temp || out["distance_source"] != dsrc || out["temperature_source"] != tsrc {
			t.Fatalf("units = %d %v, want %s/%s %s/%s", code, out, dist, temp, dsrc, tsrc)
		}
	}

	want(ca, "mi", "f", "unset", "follows_distance")
	// A client seeds from its region; a second seed does not replace it.
	if code, _ := call(ca, "PATCH", "/api/settings/units", `{"seed":true,"distance_unit":"km"}`); code != 200 {
		t.Fatalf("seed = %d", code)
	}
	want(ca, "km", "c", "region", "follows_distance")
	call(ca, "PATCH", "/api/settings/units", `{"seed":true,"distance_unit":"mi"}`)
	want(ca, "km", "c", "region", "follows_distance")
	// The person's own pick replaces it, and a seed never overrides a pick.
	call(ca, "PATCH", "/api/settings/units", `{"distance_unit":"mi"}`)
	want(ca, "mi", "f", "user", "follows_distance")
	call(ca, "PATCH", "/api/settings/units", `{"seed":true,"distance_unit":"km"}`)
	want(ca, "mi", "f", "user", "follows_distance")
	// Temperature: chosen apart from distance, then back to following it.
	call(ca, "PATCH", "/api/settings/units", `{"temperature_unit":"c"}`)
	want(ca, "mi", "c", "user", "user")
	call(ca, "PATCH", "/api/settings/units", `{"distance_unit":"km"}`)
	want(ca, "km", "c", "user", "user")
	call(ca, "PATCH", "/api/settings/units", `{"temperature_unit":"auto"}`)
	want(ca, "km", "c", "user", "follows_distance")
	call(ca, "PATCH", "/api/settings/units", `{"distance_unit":"mi"}`)
	want(ca, "mi", "f", "user", "follows_distance")
	// Bad input is refused and changes nothing.
	if code, _ := call(ca, "PATCH", "/api/settings/units", `{"distance_unit":"furlongs"}`); code != 400 {
		t.Errorf("bad unit = %d, want 400", code)
	}
	want(ca, "mi", "f", "user", "follows_distance")
	// Another account is untouched.
	want(cb, "mi", "f", "unset", "follows_distance")
	// `units` is in /api/auth/me.
	if _, me := call(ca, "GET", "/api/auth/me", ""); me["units"] == nil {
		t.Errorf("/api/auth/me has no units: %v", me)
	} else if u, _ := me["units"].(map[string]any); u["distance_unit"] != "mi" || u["distance_source"] != "user" {
		t.Errorf("me.units = %v", u)
	}

	// Two clients seeding at once: exactly one writes.
	store := settings.NewFactory(pool).For(b)
	var wg sync.WaitGroup
	wrote := make([]bool, 6)
	for i := range wrote {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unit := "mi"
			if i%2 == 0 {
				unit = "km"
			}
			wrote[i], _ = settings.SeedDistanceUnit(ctx, store, unit)
		}()
	}
	wg.Wait()
	n := 0
	for _, w := range wrote {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d concurrent seeds wrote, want exactly 1", n)
	}
}
