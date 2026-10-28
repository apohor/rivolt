package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/sessions"
)

// The iPhone app's expiry banner reads GET /api/settings/rivian/ with an app session. Nothing
// else pins that: a guard added to the route later would hide the banner without a test
// failing (as the passkeys list did). Through the real router, with full URLs: an app session
// and a browser session both get 200 and the expiry fields; a signed-out caller does not.
func TestAppSessionCanReadTheRivianStatusWithTheExpiry(t *testing.T) {
	old := rivian.ConfiguredSessionLifetime
	rivian.ConfiguredSessionLifetime = rivian.SessionLifetime
	t.Cleanup(func() { rivian.ConfiguredSessionLifetime = old })

	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient {
		lc := rivian.NewLive()
		lc.Restore(rivian.Session{
			Email:            "driver@example.invalid",
			UserSessionToken: "t",
			AuthenticatedAt:  time.Now().Add(-176*24*time.Hour - time.Hour), // a little under 4 days left
		})
		return lc
	})
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: reg})
	uid := uuid.New()
	get := func(kind string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/settings/rivian/", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := req.Context()
		if kind != "none" {
			ctx = auth.WithUser(ctx, uid)
			ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: kind})
		}
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}

	for _, kind := range []string{sessions.KindApp, "web"} {
		rec := get(kind)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s session: %d %q, want 200 (the banner reads this route)", kind, rec.Code, rec.Body.String())
		}
		var got struct {
			Authenticated    bool       `json:"authenticated"`
			SessionExpiresAt *time.Time `json:"session_expires_at"`
			SessionDaysLeft  *int       `json:"session_days_left"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Authenticated || got.SessionDaysLeft == nil || *got.SessionDaysLeft != 4 || got.SessionExpiresAt == nil {
			t.Fatalf("%s session: %s, want authenticated with session_days_left 4 and session_expires_at", kind, rec.Body.String())
		}
	}
	// Signed out: not served as a user (the route is behind authentication).
	if rec := get("none"); rec.Code == http.StatusOK && json.Valid(rec.Body.Bytes()) && containsDays(rec.Body.String()) {
		t.Fatalf("a signed-out caller got the expiry: %s", rec.Body.String())
	}
}

func containsDays(s string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return false
	}
	_, ok := m["session_days_left"]
	return ok
}
