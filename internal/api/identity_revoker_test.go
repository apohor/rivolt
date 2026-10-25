package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/sessions"
)

// fakeIdpAdmin is one fake server standing in for Hydra's and Kratos' admin APIs, recording what was called.
type fakeIdpAdmin struct {
	mu      sync.Mutex
	calls   []string
	failing string // a path substring that answers 500
	ident   string // the Kratos identity id for the account's email ("" = no identity)
}

func (f *fakeIdpAdmin) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if f.failing != "" && strings.Contains(r.URL.Path, f.failing) {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/admin/identities" {
		if f.ident == "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"` + f.ident + `","traits":{"email":"owner@x.test"}}]`))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newRevoker(t *testing.T, f *fakeIdpAdmin) *IdentityRevoker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	h, err := hydra.New(hydra.Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	k, err := kratos.New(kratos.Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return &IdentityRevoker{Hydra: h, Kratos: k, Username: func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil }}
}

func TestIdentityRevokerClearsHydraAndKratos(t *testing.T) {
	f := &fakeIdpAdmin{ident: "kid-1"}
	r := newRevoker(t, f)
	if err := r.RevokeRemembered(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /admin/identities", "DELETE /admin/oauth2/auth/sessions/login", "DELETE /admin/oauth2/auth/sessions/consent", "DELETE /admin/identities/kid-1/sessions"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls %v, want %v", f.calls, want)
	}
}

// All three steps are attempted even when one fails, and the failure is reported (the caller must not say "done").
func TestIdentityRevokerTriesEverythingAndReportsFailure(t *testing.T) {
	f := &fakeIdpAdmin{ident: "kid-1", failing: "/sessions/login"}
	r := newRevoker(t, f)
	if err := r.RevokeRemembered(context.Background(), uuid.New()); err == nil {
		t.Fatal("a failed Hydra revoke must be an error")
	}
	if len(f.calls) != 4 {
		t.Errorf("the other steps must still run: %v", f.calls)
	}
}

// An account that signs in only with Google, GitHub or Apple has no Kratos identity: nothing is remembered, nothing is deleted.
func TestIdentityRevokerNoIdentityIsNothingToDo(t *testing.T) {
	f := &fakeIdpAdmin{}
	r := newRevoker(t, f)
	if err := r.RevokeRemembered(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "DELETE") {
			t.Errorf("deleted something for an account with no identity: %v", f.calls)
		}
	}
	if err := (*IdentityRevoker)(nil).RevokeRemembered(context.Background(), uuid.New()); err != nil {
		t.Errorf("nil revoker: %v", err)
	}
}

type fakeRevoker struct {
	calls int
	err   error
}

func (f *fakeRevoker) RevokeRemembered(context.Context, uuid.UUID) error { f.calls++; return f.err }

// Sign out other devices, and revoking one session, also clear the identity provider's memory, or the revoked browser signs
// straight back in. A failure to clear it is reported, not hidden.
func TestSessionsAPIRevokeAlsoForgetsTheRememberedLogin(t *testing.T) {
	me := uuid.New()
	cur := uuid.New()
	other := uuid.New()
	fs := &fakeSessions{owner: me, rows: nil}
	fs.rows = append(fs.rows, sessions.Session{ID: cur, UserID: me, Kind: sessions.KindWeb}, sessions.Session{ID: other, UserID: me, Kind: sessions.KindWeb})
	for _, c := range []struct {
		name, method, path string
		h                  func(*SessionsAPI) http.HandlerFunc
	}{
		{"revoke one", "DELETE", "/api/auth/sessions/" + other.String(), func(a *SessionsAPI) http.HandlerFunc { return a.Revoke }},
		{"revoke others", "POST", "/api/auth/sessions/revoke-others", func(a *SessionsAPI) http.HandlerFunc { return a.RevokeOthers }},
	} {
		fr := &fakeRevoker{}
		a := &SessionsAPI{Store: fs, Current: func(*http.Request) (uuid.UUID, bool) { return cur, true }, Remembered: fr}
		rec := sessReq(c.h(a), c.method, c.path, me, false)
		if rec.Code >= 300 || fr.calls != 1 {
			t.Errorf("%s: code %d, revoker called %d times; want success and one call", c.name, rec.Code, fr.calls)
		}
		fr = &fakeRevoker{err: errors.New("hydra down")}
		a.Remembered = fr
		if rec := sessReq(c.h(a), c.method, c.path, me, false); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "hydra down") {
			t.Errorf("%s: a failed forget must be a 502 that does not echo the error: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

// The point of all this, end to end against a model of Hydra's remember behavior: a browser Hydra remembers is "skipped" through
// sign-in with no password; after the revoke, the same browser is shown the password form. The fake keeps Hydra's state (the
// subjects it remembers), answers a login request with skip=true while the subject is remembered, and forgets it when the login
// session is deleted, as the real admin API does. The handler under test is the real hydraLoginGET.
func TestRevokedBrowserGetsThePasswordForm(t *testing.T) {
	remembered := map[string]bool{"kid-1": true}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/oauth2/auth/requests/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/cb"}`))
			return
		}
		skip := remembered["kid-1"]
		sub := ""
		if skip {
			sub = "kid-1" // Hydra only reports a subject on a skipped request
		}
		_, _ = w.Write([]byte(`{"challenge":"c","subject":"` + sub + `","skip":` + map[bool]string{true: "true", false: "false"}[skip] + `,"client":{"client_id":"rivolt","client_name":"Rivolt"}}`))
	})
	mux.HandleFunc("/admin/oauth2/auth/requests/login/accept", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/cb"}`))
	})
	mux.HandleFunc("/admin/oauth2/auth/sessions/login", func(w http.ResponseWriter, r *http.Request) {
		delete(remembered, r.URL.Query().Get("subject"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/oauth2/auth/sessions/consent", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/admin/identities", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"kid-1","traits":{"email":"owner@x.test"}}]`))
	})
	mux.HandleFunc("/admin/identities/kid-1/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h, err := hydra.New(hydra.Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	k, err := kratos.New(kratos.Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	login := func() hydraLoginGetResponse {
		rr := httptest.NewRecorder()
		hydraLoginGET(hydraDeps{Hydra: h, Kratos: k, Logger: discardLogger()}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/login?login_challenge=c", nil))
		var out hydraLoginGetResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rr.Body.String())
		}
		return out
	}

	if got := login(); !got.Skip || got.RedirectTo == "" {
		t.Fatalf("before the revoke the remembered browser is signed straight in (the bug): %+v", got)
	}
	rev := &IdentityRevoker{Hydra: h, Kratos: k, Username: func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil }}
	if err := rev.RevokeRemembered(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if got := login(); got.Skip || got.Challenge == "" {
		t.Fatalf("after the revoke the same browser must get the password form, not a skip: %+v", got)
	}
}
