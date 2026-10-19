package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
)

// fakeHydraKratos stands in for Hydra's admin API and Kratos's public API, counting
// what the login bridge calls.
type fakeHydraKratos struct {
	goodChallenge string
	hydraStatus   int // for GET login requests with the good challenge (0: 200)
	password      string

	kratosLogins, revoked, accepts atomic.Int32
	revokedToken                   atomic.Value
}

func (f *fakeHydraKratos) hydraHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/oauth2/auth/requests/login":
			if r.URL.Query().Get("login_challenge") != f.goodChallenge {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			if f.hydraStatus != 0 {
				http.Error(w, "boom", f.hydraStatus)
				return
			}
			_, _ = io.WriteString(w, `{"challenge":"c","client":{"client_id":"argocd"},"requested_scope":["openid"]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/admin/oauth2/auth/requests/login/accept":
			f.accepts.Add(1)
			_, _ = io.WriteString(w, `{"redirect_to":"https://hydra.example/continue"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeHydraKratos) kratosHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/self-service/login/api":
			_, _ = io.WriteString(w, `{"id":"flow1"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/self-service/login":
			f.kratosLogins.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != f.password {
				http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"session_token":"tok-123","session":{"identity":{"id":"ident-1"}}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/self-service/logout/api":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.revokedToken.Store(body["session_token"])
			f.revoked.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func newBridge(t *testing.T, f *fakeHydraKratos) http.Handler {
	t.Helper()
	hs, ks := httptest.NewServer(f.hydraHandler()), httptest.NewServer(f.kratosHandler())
	t.Cleanup(hs.Close)
	t.Cleanup(ks.Close)
	hc, err := hydra.New(hydra.Config{AdminURL: hs.URL})
	if err != nil {
		t.Fatal(err)
	}
	kc, err := kratos.New(kratos.Config{AdminURL: ks.URL, PublicURL: ks.URL})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	mountHydraBridge(r, hydraDeps{Hydra: hc, Kratos: kc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return r
}

func login(h http.Handler, challenge, password, ip string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"challenge": challenge, "email": "a@x.test", "password": password})
	req := httptest.NewRequest(http.MethodPost, "/hydra/login", bytes.NewReader(body))
	req.RemoteAddr = ip + ":4000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A wrong or unknown challenge is checked before the password: the password
// is never tried, and the answer is the same as for a wrong password.
func TestHydraLoginChecksTheChallengeFirst(t *testing.T) {
	f := &fakeHydraKratos{goodChallenge: "good", password: "right"}
	h := newBridge(t, f)

	bad := login(h, "unknown", "right", "203.0.113.1")
	wrongPw := login(h, "good", "wrong", "203.0.113.2")
	if bad.Code != http.StatusUnauthorized || wrongPw.Code != http.StatusUnauthorized || bad.Body.String() != wrongPw.Body.String() {
		t.Fatalf("bad challenge %d %q, wrong password %d %q: want the same 401", bad.Code, bad.Body, wrongPw.Code, wrongPw.Body)
	}
	if got := f.kratosLogins.Load(); got != 1 {
		t.Errorf("Kratos saw %d password attempts, want 1 (only the one with a real challenge)", got)
	}
	if f.accepts.Load() != 0 {
		t.Error("a login was accepted without a valid password")
	}

	// A real challenge and password go through, and the session Kratos opened is closed.
	ok := login(h, "good", "right", "203.0.113.3")
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	if ok.Code != 200 || json.Unmarshal(ok.Body.Bytes(), &out) != nil || out.RedirectTo != "https://hydra.example/continue" {
		t.Fatalf("good login: %d %s", ok.Code, ok.Body)
	}
	if f.accepts.Load() != 1 {
		t.Errorf("accepts %d", f.accepts.Load())
	}
	if f.revoked.Load() != 1 || f.revokedToken.Load() != "tok-123" {
		t.Errorf("the Kratos session was not revoked: %d %v", f.revoked.Load(), f.revokedToken.Load())
	}
}

func TestHydraOutageIsNotAWrongPassword(t *testing.T) {
	f := &fakeHydraKratos{goodChallenge: "good", password: "right", hydraStatus: http.StatusInternalServerError}
	h := newBridge(t, f)
	if rec := login(h, "good", "right", "203.0.113.4"); rec.Code != http.StatusBadGateway {
		t.Errorf("Hydra down: %d, want 502", rec.Code)
	}
	if f.kratosLogins.Load() != 0 {
		t.Error("the password was tried while Hydra was down")
	}
}

// The login routes are limited per client address: the eleventh POST in a
// window is refused, another address is not, and the GET has its own bucket.
func TestHydraLoginIsRateLimitedPerIP(t *testing.T) {
	f := &fakeHydraKratos{goodChallenge: "good", password: "right"}
	h := newBridge(t, f)
	for i := 0; i < 10; i++ {
		if rec := login(h, "unknown", "x", "198.51.100.7"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if rec := login(h, "unknown", "x", "198.51.100.7"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("11th attempt: %d, want 429", rec.Code)
	}
	if rec := login(h, "unknown", "x", "198.51.100.8"); rec.Code != http.StatusUnauthorized {
		t.Errorf("another address: %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/hydra/login?login_challenge=good", nil)
	req.RemoteAddr = "198.51.100.7:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "challenge") {
		t.Errorf("GET from the same address: %d %s", rec.Code, rec.Body)
	}
}

// Wrong passwords are counted per email address across client addresses: the
// eleventh failure for one address is a 429 even from a new IP, another address
// is not affected, a right password is never counted and clears the count, and
// the window rolls.
func TestHydraLoginLimitsFailuresPerEmail(t *testing.T) {
	f := &fakeHydraKratos{goodChallenge: "good", password: "right"}
	hs, ks := httptest.NewServer(f.hydraHandler()), httptest.NewServer(f.kratosHandler())
	t.Cleanup(hs.Close)
	t.Cleanup(ks.Close)
	hc, _ := hydra.New(hydra.Config{AdminURL: hs.URL})
	kc, _ := kratos.New(kratos.Config{AdminURL: ks.URL, PublicURL: ks.URL})
	fl := newFailLimiter(10, 15*time.Minute)
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	fl.now = func() time.Time { return clock }
	h := hydraLoginPOST(hydraDeps{Hydra: hc, Kratos: kc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Failures: fl})

	post := func(email, password, ip string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"challenge": "good", "email": email, "password": password})
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
		req.RemoteAddr = ip + ":1"
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	for i := 0; i < 10; i++ { // ten different client addresses, one account
		if rec := post("Victim@X.test", "wrong", "198.51.100."+strconv.Itoa(i+1)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i+1, rec.Code)
		}
	}
	if rec := post("victim@x.test", "wrong", "203.0.113.99"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("11th guess (new address, lower case): %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	attempts := f.kratosLogins.Load()
	if rec := post("victim@x.test", "right", "203.0.113.99"); rec.Code != http.StatusTooManyRequests || f.kratosLogins.Load() != attempts {
		t.Errorf("while blocked even the right password is refused without trying Kratos: %d", rec.Code)
	}
	if rec := post("someone-else@x.test", "right", "203.0.113.99"); rec.Code != http.StatusOK {
		t.Errorf("another account: %d", rec.Code)
	}
	clock = clock.Add(16 * time.Minute) // the window rolled
	if rec := post("victim@x.test", "right", "203.0.113.99"); rec.Code != http.StatusOK {
		t.Errorf("after the window: %d", rec.Code)
	}
	// A right password clears the count: nine wrong ones then one right one, and nine more are allowed.
	for i := 0; i < 9; i++ {
		post("careful@x.test", "wrong", "198.51.100.200")
	}
	if rec := post("careful@x.test", "right", "198.51.100.200"); rec.Code != http.StatusOK {
		t.Fatalf("the 10th try with the right password: %d", rec.Code)
	}
	for i := 0; i < 9; i++ {
		if rec := post("careful@x.test", "wrong", "198.51.100.200"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("after a success, wrong #%d: %d", i+1, rec.Code)
		}
	}
	// The key is a hash, not the address.
	if k := emailLimitKey(" Victim@X.test "); strings.Contains(k, "victim") || k != emailLimitKey("victim@x.test") {
		t.Errorf("key %q", k)
	}
}
