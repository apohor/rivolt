package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/rivian"
)

func rivianStatusFor(t *testing.T, sess rivian.Session, needsReauth bool) map[string]any {
	t.Helper()
	uid := uuid.New()
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient {
		lc := rivian.NewLive()
		lc.Restore(sess)
		if needsReauth {
			lc.SetNeedsReauth(true, "session expired")
		}
		return lc
	})
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil).WithContext(auth.WithUser(httptest.NewRequest("GET", "/", nil).Context(), uid))
	w := httptest.NewRecorder()
	handleRivianStatus(reg, nil, nil, nil, nil, nil)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The status carries when the stored session is expected to end, for the banners that warn
// 14 days ahead.
func TestRivianStatusCarriesTheSessionExpiry(t *testing.T) {
	old := rivian.ConfiguredSessionLifetime
	rivian.ConfiguredSessionLifetime = rivian.SessionLifetime
	t.Cleanup(func() { rivian.ConfiguredSessionLifetime = old })

	signedIn := time.Now().Add(-176*24*time.Hour - time.Hour) // a little under 4 days left
	got := rivianStatusFor(t, rivian.Session{UserSessionToken: "t", AuthenticatedAt: signedIn}, false)
	if d, ok := got["session_days_left"].(float64); !ok || d != 4 {
		t.Fatalf("session_days_left = %v, want 4 (rounded up)", got["session_days_left"])
	}
	exp, err := time.Parse(time.RFC3339, got["session_expires_at"].(string))
	if err != nil || exp.Sub(signedIn.Add(rivian.SessionLifetime)).Abs() > time.Second {
		t.Fatalf("session_expires_at = %v (%v), want sign-in + 180 days", got["session_expires_at"], err)
	}

	// A fresh session says 180.
	if d := rivianStatusFor(t, rivian.Session{UserSessionToken: "t", AuthenticatedAt: time.Now()}, false)["session_days_left"]; d != float64(180) {
		t.Fatalf("fresh session: session_days_left = %v, want 180", d)
	}
	// Past the end: 0, never negative.
	if d := rivianStatusFor(t, rivian.Session{UserSessionToken: "t", AuthenticatedAt: time.Now().Add(-200 * 24 * time.Hour)}, false)["session_days_left"]; d != float64(0) {
		t.Fatalf("expired session: session_days_left = %v, want 0", d)
	}
	// Already flagged needs_reauth, or no known sign-in time: no expiry (the other banner rules).
	flagged := rivianStatusFor(t, rivian.Session{UserSessionToken: "t", AuthenticatedAt: signedIn}, true)
	if _, ok := flagged["session_days_left"]; ok {
		t.Fatalf("a flagged session must not carry an expiry: %v", flagged)
	}
	if _, ok := rivianStatusFor(t, rivian.Session{UserSessionToken: "t"}, false)["session_days_left"]; ok {
		t.Fatal("a session with no sign-in time must not carry an expiry")
	}
}

func TestSessionLifetimeFromEnv(t *testing.T) {
	get := func(v string) func(string) string { return func(string) string { return v } }
	for in, want := range map[string]time.Duration{
		"": rivian.SessionLifetime, "90": 90 * 24 * time.Hour, " 365 ": 365 * 24 * time.Hour,
		"0": rivian.SessionLifetime, "-1": rivian.SessionLifetime, "x": rivian.SessionLifetime, "99999": rivian.SessionLifetime,
	} {
		if got := rivian.SessionLifetimeFromEnv(get(in)); got != want {
			t.Errorf("RIVOLT_RIVIAN_SESSION_DAYS=%q -> %v, want %v", in, got, want)
		}
	}
}
