package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/logging"
)

// kindStore is a SessionStore with one browser cookie and one app token, kept
// apart the way sessions.Store keeps the kinds apart.
type kindStore struct {
	user           uuid.UUID
	webTok, appTok string
	revoked        []string
}

func (k *kindStore) Create(context.Context, uuid.UUID, time.Duration, SessionCreateOpts) (SessionInfo, string, error) {
	return SessionInfo{}, "", errors.New("unused")
}

func (k *kindStore) Lookup(_ context.Context, raw string) (SessionInfo, error) {
	if raw == k.webTok && !k.isRevoked(raw) {
		return SessionInfo{ID: uuid.New(), UserID: k.user}, nil
	}
	return SessionInfo{}, errors.New("invalid")
}

func (k *kindStore) LookupBearer(_ context.Context, raw string) (SessionInfo, error) {
	if raw == k.appTok && !k.isRevoked(raw) {
		return SessionInfo{ID: uuid.New(), UserID: k.user}, nil
	}
	return SessionInfo{}, errors.New("invalid")
}

func (k *kindStore) RevokeByToken(_ context.Context, raw string) error {
	k.revoked = append(k.revoked, raw)
	return nil
}

func (k *kindStore) isRevoked(raw string) bool {
	for _, r := range k.revoked {
		if r == raw {
			return true
		}
	}
	return false
}

func newBearerService(t *testing.T, disabled bool) (*Service, *kindStore) {
	t.Helper()
	store := &kindStore{user: uuid.New(), webTok: "web-token", appTok: "app-token"}
	svc, err := New(Config{
		UserIDFor:   fakeUserIDFor,
		DisabledFor: func(context.Context, uuid.UUID) (bool, error) { return disabled, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithSessionStore(store).AcceptBearer(true)
	return svc, store
}

// seen runs the middleware and reports who the handler saw and how.
func seen(svc *Service, r *http.Request) (*httptest.ResponseRecorder, uuid.UUID, string) {
	var uid uuid.UUID
	var method string
	w := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		uid, _ = UserFromContext(r.Context())
		method = logging.AuthMethodFromContext(r.Context())
	})).ServeHTTP(w, r)
	return w, uid, method
}

func bearerReq(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func TestBearerAuthenticatesAppSessionsOnly(t *testing.T) {
	svc, store := newBearerService(t, false)

	if w, uid, method := seen(svc, bearerReq("app-token")); w.Code != http.StatusOK || uid != store.user || method != logging.AuthBearer {
		t.Fatalf("app token as bearer: %d %v %q", w.Code, uid, method)
	}
	if !ViaBearer(logging.WithAuthMethod(context.Background(), logging.AuthBearer)) {
		t.Fatal("ViaBearer")
	}
	// A browser cookie value is no bearer token, and an app token is no cookie.
	if w, uid, _ := seen(svc, bearerReq("web-token")); w.Code != http.StatusUnauthorized || uid != uuid.Nil {
		t.Fatalf("web cookie value as bearer: %d %v", w.Code, uid)
	}
	cookie := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	cookie.AddCookie(&http.Cookie{Name: "rivolt_session", Value: "app-token"})
	if _, uid, _ := seen(svc, cookie); uid != uuid.Nil {
		t.Fatal("an app token worked as a cookie")
	}
}

func TestBadBearerNeverFallsBackToTheCookie(t *testing.T) {
	svc, _ := newBearerService(t, false)
	r := bearerReq("nope")
	r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: "web-token"})
	w, uid, _ := seen(svc, r)
	if w.Code != http.StatusUnauthorized || uid != uuid.Nil {
		t.Fatalf("bad bearer with a good cookie: %d %v", w.Code, uid)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
		t.Fatalf("WWW-Authenticate %q", got)
	}
	for _, h := range []string{"Bearer", "Bearer ", "bearer app-token"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", h)
		w, _, method := seen(svc, r)
		if h == "bearer app-token" {
			if w.Code != http.StatusOK || method != logging.AuthBearer {
				t.Errorf("%q: case-insensitive scheme refused: %d", h, w.Code)
			}
			continue
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%q: %d", h, w.Code)
		}
	}
}

func TestBearerOffAndDisabledUsers(t *testing.T) {
	svc, _ := newBearerService(t, false)
	svc.AcceptBearer(false)
	if w, uid, _ := seen(svc, bearerReq("app-token")); w.Code != http.StatusUnauthorized || uid != uuid.Nil {
		t.Fatalf("bearer where it's off (admin host): %d %v", w.Code, uid)
	}
	disabled, _ := newBearerService(t, true)
	if w, uid, _ := seen(disabled, bearerReq("app-token")); w.Code != http.StatusUnauthorized || uid != uuid.Nil {
		t.Fatalf("disabled user's bearer: %d %v", w.Code, uid)
	}
}

func TestBearerLogoutRevokesThatToken(t *testing.T) {
	svc, store := newBearerService(t, false)
	w := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(svc.Logout)).ServeHTTP(w, bearerReq("app-token"))
	if w.Code != http.StatusNoContent || len(store.revoked) != 1 || store.revoked[0] != "app-token" {
		t.Fatalf("logout: %d revoked %v", w.Code, store.revoked)
	}
	if w, _, _ := seen(svc, bearerReq("app-token")); w.Code != http.StatusUnauthorized {
		t.Fatalf("token after logout: %d", w.Code)
	}
}
