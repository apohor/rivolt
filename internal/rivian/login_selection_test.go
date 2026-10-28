package rivian

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The Login and LoginWithOTP requests ask only for what Rivolt uses. The 3.17.0 app asks for the
// user session token alone; the access and refresh tokens older versions requested were stored
// and never used, so they are no longer requested (data minimisation). This pins the selection
// sets sent to Rivian.
func TestLoginAsksForTheUserSessionTokenOnly(t *testing.T) {
	var mu sync.Mutex
	queries := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			OperationName string `json:"operationName"`
			Query         string `json:"query"`
		}
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		queries[req.OperationName] = req.Query
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.OperationName {
		case "CreateCSRFToken":
			_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
		case "Login":
			_, _ = io.WriteString(w, `{"data":{"login":{"__typename":"MobileLoginResponse","userSessionToken":"u"}}}`)
		case "LoginWithOTP":
			_, _ = io.WriteString(w, `{"data":{"loginWithOTP":{"userSessionToken":"u2"}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	if err := c.Login(context.Background(), Credentials{Email: "a@example.invalid", Password: "pw"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := c.Snapshot().UserSessionToken; got != "u" {
		t.Fatalf("user session token = %q, want u", got)
	}
	// The second leg, from a client holding an MFA challenge.
	c2 := NewLive()
	c2.endpoint = server.URL + "/gateway"
	c2.csrfToken, c2.appSessionToken = "c", "a"
	c2.pendingOTPEmail, c2.pendingOTPToken = "a@example.invalid", "otp"
	if err := c2.Login(context.Background(), Credentials{Email: "a@example.invalid", OTP: "123456"}); err != nil {
		t.Fatalf("Login (OTP): %v", err)
	}
	if got := c2.Snapshot().UserSessionToken; got != "u2" {
		t.Fatalf("OTP user session token = %q, want u2", got)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, op := range []string{"Login", "LoginWithOTP"} {
		q := queries[op]
		if q == "" {
			t.Fatalf("no %s request seen (queries: %v)", op, queries)
		}
		if !strings.Contains(q, "userSessionToken") {
			t.Errorf("%s does not ask for userSessionToken: %s", op, q)
		}
		for _, banned := range []string{"accessToken", "refreshToken"} {
			if strings.Contains(q, banned) {
				t.Errorf("%s still asks for %s: %s", op, banned, q)
			}
		}
	}
}

// A session stored by an older version carries the two legacy fields; it still decodes and the
// fields are simply dropped.
func TestAnOldStoredSessionWithLegacyTokensStillDecodes(t *testing.T) {
	old := `{"email":"a@example.invalid","csrf_token":"c","app_session_token":"a","user_session_token":"u",` +
		`"access_token":"legacy-access","refresh_token":"legacy-refresh","authenticated_at":"2026-05-01T00:47:00Z"}`
	var s Session
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	if s.UserSessionToken != "u" || s.CSRFToken != "c" || s.AppSessionToken != "a" || s.AuthenticatedAt.IsZero() {
		t.Fatalf("decoded %+v", s)
	}
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "legacy-") || strings.Contains(string(out), "access_token") || strings.Contains(string(out), "refresh_token") {
		t.Fatalf("re-encoding a session kept the legacy tokens: %s", out)
	}
}
