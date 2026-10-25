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

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
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
	// failFirst makes the first n calls fail (a Hydra that is briefly down).
	failFirst int
}

func (f *fakeRevoker) RevokeRemembered(context.Context, uuid.UUID) error {
	f.calls++
	if f.calls <= f.failFirst {
		return errors.New("hydra down")
	}
	return nil
}

// Sign out other devices, and revoking one session, also clear the identity provider's memory, or the revoked browser signs
// straight back in. The memory is cleared FIRST: when that fails the answer is a 502 and nothing was revoked, so the retry runs
// the whole thing again (cleared after, the retry would find the row already revoked, answer 404, and never clear it).
func TestSessionsAPIRevokeForgetsTheRememberedLoginFirstAndRetries(t *testing.T) {
	me := uuid.New()
	cur := uuid.New()
	other := uuid.New()
	for _, c := range []struct {
		name, method, path string
		h                  func(*SessionsAPI) http.HandlerFunc
		done               func(*fakeSessions) bool
	}{
		{"revoke one", "DELETE", "/api/auth/sessions/" + other.String(), func(a *SessionsAPI) http.HandlerFunc { return a.Revoke }, func(f *fakeSessions) bool { return len(f.revoked) == 1 }},
		{"revoke others", "POST", "/api/auth/sessions/revoke-others", func(a *SessionsAPI) http.HandlerFunc { return a.RevokeOthers }, func(f *fakeSessions) bool { return f.keptOther == cur }},
	} {
		fs := &fakeSessions{owner: me, rows: []sessions.Session{{ID: cur, UserID: me, Kind: sessions.KindWeb}, {ID: other, UserID: me, Kind: sessions.KindWeb}}}
		fr := &fakeRevoker{failFirst: 1}
		a := &SessionsAPI{Store: fs, Current: func(*http.Request) (uuid.UUID, bool) { return cur, true }, Remembered: fr}

		rec := sessReq(c.h(a), c.method, c.path, me, false)
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "hydra down") {
			t.Errorf("%s: a failed forget must be a 502 that does not echo the error: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if c.done(fs) {
			t.Errorf("%s: nothing may be revoked when the forget failed (the retry must start from scratch)", c.name)
		}

		rec = sessReq(c.h(a), c.method, c.path, me, false)
		if rec.Code >= 300 || fr.calls != 2 || !c.done(fs) {
			t.Errorf("%s retry: code %d, forget called %d times, revoked=%v; want success on the second call", c.name, rec.Code, fr.calls, c.done(fs))
		}
	}

	// No revoker configured (no Hydra): the revoke works as before.
	fs := &fakeSessions{owner: me, rows: []sessions.Session{{ID: other, UserID: me, Kind: sessions.KindWeb}}}
	a := &SessionsAPI{Store: fs, Current: func(*http.Request) (uuid.UUID, bool) { return cur, true }}
	if rec := sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+other.String(), me, false); rec.Code != http.StatusNoContent {
		t.Errorf("no revoker: %d", rec.Code)
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

// Plain "Sign out" must end the browser's Hydra login too, or the shared-computer case is open: Rivolt's session is gone but the next
// "Continue with Email" is skipped through with no password. The real logout handler and the real login handler, against a model of
// Hydra's remember state.
func TestSignOutAlsoEndsTheRememberedLogin(t *testing.T) {
	remembered := map[string]bool{"kid-1": true}
	var deletes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/oauth2/auth/requests/login", func(w http.ResponseWriter, r *http.Request) {
		skip := remembered["kid-1"]
		sub := ""
		if skip {
			sub = "kid-1"
		}
		_, _ = w.Write([]byte(`{"challenge":"c","subject":"` + sub + `","skip":` + map[bool]string{true: "true", false: "false"}[skip] + `,"client":{"client_id":"rivolt","client_name":"Rivolt"}}`))
	})
	mux.HandleFunc("/admin/oauth2/auth/requests/login/accept", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/cb"}`))
	})
	mux.HandleFunc("/admin/oauth2/auth/sessions/login", func(w http.ResponseWriter, r *http.Request) {
		deletes = append(deletes, r.URL.Path)
		delete(remembered, r.URL.Query().Get("subject"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/oauth2/auth/sessions/consent", func(w http.ResponseWriter, r *http.Request) {
		deletes = append(deletes, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/identities", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"kid-1","traits":{"email":"owner@x.test"}}]`))
	})
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
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	rev := &IdentityRevoker{Hydra: h, Kratos: k, Username: func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil }}
	uid := uuid.New()
	loginSkips := func() bool {
		rr := httptest.NewRecorder()
		hydraLoginGET(hydraDeps{Hydra: h, Kratos: k, Logger: discardLogger()}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/login?login_challenge=c", nil))
		var out hydraLoginGetResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Skip
	}
	logout := func(d Deps, mutate func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req = req.WithContext(auth.WithUser(req.Context(), uid))
		if mutate != nil {
			req = mutate(req)
		}
		rec := httptest.NewRecorder()
		d.logoutHandler()(rec, req)
		return rec
	}
	d := Deps{Auth: svc, LoginForgetter: rev}

	if !loginSkips() {
		t.Fatal("before sign-out the remembered browser is skipped through with no password (the bug)")
	}
	if rec := logout(d, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if loginSkips() {
		t.Error("after Sign out the same browser must get the password form, not a skip")
	}
	if len(deletes) != 1 || deletes[0] != "/admin/oauth2/auth/sessions/login" {
		t.Errorf("sign-out must forget the login only, not the consent grants: %v", deletes)
	}

	// An admin viewing as the user signs out of nothing of the target's (the app's bearer sign-out is skipped the same way).
	remembered["kid-1"] = true
	deletes = nil
	if rec := logout(d, func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithImpersonation(r.Context(), uuid.New(), uid))
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("view-as logout = %d", rec.Code)
	}
	if len(deletes) != 0 || !remembered["kid-1"] {
		t.Errorf("a viewing-as admin's sign-out must not touch the target's remembered login: %v", deletes)
	}

	// A Hydra failure never keeps anyone signed in: the Rivolt sign-out still happens.
	srv.Close()
	if rec := logout(d, nil); rec.Code != http.StatusNoContent {
		t.Errorf("logout with Hydra down = %d, want the Rivolt sign-out to go ahead", rec.Code)
	}
	// And with nothing configured it is the old behavior.
	if rec := logout(Deps{Auth: svc}, nil); rec.Code != http.StatusNoContent {
		t.Errorf("logout without a forgetter = %d", rec.Code)
	}
}
