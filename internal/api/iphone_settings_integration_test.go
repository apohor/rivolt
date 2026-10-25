//go:build integration

package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// Essential Settings on iPhone, through the real router with a real app token: the app may set, restore and search the home
// location and write its own truck's profile, only its own (another user's vehicle is a 404), the writes are rate limited,
// and the export and backup routes stay refused for the token.
func TestAppTokenEssentialSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	owner := acceptedUser(ctx, t, pool, "iphone-owner")
	other := acceptedUser(ctx, t, pool, "iphone-other")
	for _, v := range []struct {
		uid uuid.UUID
		rid string
	}{{owner, "01-IPHONE-OWN"}, {other, "01-IPHONE-OTHER"}} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`, v.uid, v.rid); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := sessions.New(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithSessionStore(sessions.NewAuthAdapter(ss)).AcceptBearer(true)
	_, token, err := ss.Create(ctx, owner, time.Hour, sessions.CreateOpts{Kind: sessions.KindApp, DeviceLabel: "iPhone"})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, DB: pool, Settings: settings.NewFactory(pool)})
	call := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	homeBody := `{"set":true,"latitude":30.27,"longitude":-97.74,"label":"Home","source":"coordinates"}`

	// The truck's profile: its own is written; another account's is a 404, not a 403; a bad spelling is refused as an app write.
	if code, body := call("PUT", "/api/vehicles/01-IPHONE-OWN/profile", `{"wheel_inches":20,"tire_type":"all-season"}`); code != 200 {
		t.Errorf("own profile = %d %s", code, body)
	}
	if code, _ := call("PUT", "/api/vehicles/01-IPHONE-OTHER/profile", `{"wheel_inches":20}`); code != http.StatusNotFound {
		t.Errorf("another user's vehicle = %d, want 404 (not 403, and not written)", code)
	}
	if code, _ := call("PUT", "/api/vehicles/01-IPHONE-OWN/profile/", `{"wheel_inches":20}`); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("trailing slash = %d", code)
	}
	var wheel int
	if err := pool.QueryRowContext(ctx, `SELECT COALESCE((metadata->'profile'->>'wheel_inches')::int, 0) FROM vehicles WHERE rivian_vehicle_id = '01-IPHONE-OTHER'`).Scan(&wheel); err != nil || wheel != 0 {
		t.Errorf("another user's profile was written through the app path: %d, %v", wheel, err)
	}

	// Home: set, read back, restore (nothing yet to restore), clear, search (no address service here).
	if code, body := call("PUT", "/api/settings/home-location", homeBody); code != 200 || !strings.Contains(body, `"set":true`) {
		t.Fatalf("set home = %d %s", code, body)
	}
	if code, body := call("GET", "/api/settings/home-location", ""); code != 200 || !strings.Contains(body, `"set":true`) {
		t.Errorf("read home = %d %s", code, body)
	}
	if code, _ := call("PUT", "/api/settings/home-location", `{"set":true,"latitude":95,"longitude":0}`); code != 400 {
		t.Errorf("an invalid latitude = %d, want 400", code)
	}
	if code, _ := call("POST", "/api/settings/home-location/restore", ""); code != 404 {
		t.Errorf("restore with no previous home = %d, want the handler's 404, not the app refusal", code)
	}
	if code, _ := call("POST", "/api/settings/home-location/search", `{"q":"1 Main Street"}`); code != 404 {
		t.Errorf("search without an address service = %d, want the handler's 404", code)
	}
	if code, _ := call("PUT", "/api/settings/home-location", `{"set":false}`); code != 200 {
		t.Errorf("clear home = %d", code)
	}
	// Other settings writes are still refused for the token, and so are the whole-account reads.
	for _, c := range [][2]string{{"PUT", "/api/settings/charging"}, {"PUT", "/api/settings/ai-consent"}, {"DELETE", "/api/data/account"}} {
		if code, _ := call(c[0], c[1], `{}`); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", c[0], c[1], code)
		}
	}
	for _, p := range []string{"/api/export", "/api/data/backup"} {
		if code, _ := call("GET", p, ""); code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", p, code)
		}
	}
	// Ten a minute for the home writes, then 429.
	limited := false
	for i := 0; i < 12; i++ {
		if code, _ := call("PUT", "/api/settings/home-location", homeBody); code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("12 home writes in a minute were never rate limited")
	}
}
