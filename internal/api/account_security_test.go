package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/sessions"
)

// secKratos is one fake Kratos: the public login (the current password is "old-pw") and the admin identity API.
type secKratos struct {
	mu        sync.Mutex
	puts      []map[string]any
	hasIdent  bool
	rejectNew string // a new password Kratos refuses (400)
}

func (k *secKratos) handler(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case r.URL.Path == "/self-service/login/api":
		_, _ = w.Write([]byte(`{"id":"f"}`))
	case r.URL.Path == "/self-service/login":
		var b struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b.Password != "old-pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"session":{"identity":{"id":"kid-1","traits":{"email":"owner@x.test"}}}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/admin/identities":
		if !k.hasIdent {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"kid-1","traits":{"email":"owner@x.test"}}]`))
	case r.Method == http.MethodGet && r.URL.Path == "/admin/identities/kid-1":
		_, _ = w.Write([]byte(`{"id":"kid-1","schema_id":"default","state":"active","traits":{"email":"owner@x.test"}}`))
	case r.Method == http.MethodPut && r.URL.Path == "/admin/identities/kid-1":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		if k.rejectNew != "" && strings.Contains(string(raw), k.rejectNew) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"reason":"the password is too common"}}`))
			return
		}
		k.puts = append(k.puts, body)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type secPasskeys struct {
	passkeys.Store // unimplemented methods panic: only these two are used
	creds          []passkeys.Credential
	deleted        int
	listFails      bool
	deleteFails    bool
}

func (s *secPasskeys) Credentials(context.Context, uuid.UUID) ([]passkeys.Credential, error) {
	if s.listFails {
		return nil, errors.New("db down")
	}
	return s.creds, nil
}
func (s *secPasskeys) Delete(_ context.Context, _, _ uuid.UUID) (bool, error) {
	if s.deleteFails {
		return false, errors.New("db down")
	}
	s.deleted++
	return true, nil
}

type secMail struct{ sent []email.Message }

func (m *secMail) Send(_ context.Context, msg email.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}

type secEnv struct {
	api   *accountSecurityAPI
	k     *secKratos
	sess  *fakeSessions
	rev   *fakeRevoker
	pk    *secPasskeys
	mail  *secMail
	uid   uuid.UUID
	curID uuid.UUID
}

func newSecEnv(t *testing.T) *secEnv {
	t.Helper()
	k := &secKratos{hasIdent: true}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	t.Cleanup(srv.Close)
	kc, err := kratos.New(kratos.Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	e := &secEnv{k: k, uid: uuid.New(), curID: uuid.New(), rev: &fakeRevoker{}, mail: &secMail{}}
	e.sess = &fakeSessions{owner: e.uid, rows: []sessions.Session{{ID: e.curID, UserID: e.uid}, {ID: uuid.New(), UserID: e.uid}}}
	e.pk = &secPasskeys{creds: []passkeys.Credential{{ID: uuid.New(), Label: "Phone"}, {ID: uuid.New(), Label: "Laptop"}}}
	e.api = &accountSecurityAPI{
		Kratos: kc, Remembered: e.rev, Sessions: e.sess, Passkeys: e.pk, Mail: e.mail,
		Account:  func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil },
		Failures: newFailLimiter(10, 15*time.Minute),
		Logger:   discardLogger(),
	}
	return e
}

// post calls the handler as the account's own browser session unless mutate says otherwise.
func (e *secEnv) post(h http.HandlerFunc, body string, mutate func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:4000"
	ctx := auth.WithSession(auth.WithUser(req.Context(), e.uid), auth.SessionRef{ID: e.curID, Kind: sessions.KindWeb})
	req = req.WithContext(ctx)
	if mutate != nil {
		req = mutate(req)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestChangePasswordSignsEverythingElseOutAndKeepsPasskeys(t *testing.T) {
	e := newSecEnv(t)
	rec := e.post(e.api.change, `{"current":"old-pw","new":"a-better-pw-9"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(e.k.puts) != 1 || !strings.Contains(secJSON(e.k.puts[0]), "a-better-pw-9") {
		t.Fatalf("the new password did not reach Kratos: %v", e.k.puts)
	}
	if e.rev.calls != 1 || e.sess.keptOther != e.curID {
		t.Errorf("remembered logins cleared %d times, sessions kept %v (want the caller's %v)", e.rev.calls, e.sess.keptOther, e.curID)
	}
	if e.pk.deleted != 0 {
		t.Errorf("Change password must keep the passkeys, deleted %d", e.pk.deleted)
	}
	var out struct {
		OK       bool          `json:"ok"`
		Passkeys []passkeyView `json:"passkeys"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK || len(out.Passkeys) != 2 {
		t.Errorf("the response must list the passkeys so any unfamiliar one can be removed: %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the response must not be cached")
	}
	if len(e.mail.sent) != 1 || e.mail.sent[0].To != "owner@x.test" || !strings.Contains(e.mail.sent[0].Subject, "password was changed") {
		t.Errorf("the address on file must be told: %+v", e.mail.sent)
	}
	if strings.Contains(rec.Body.String(), "a-better-pw-9") || strings.Contains(e.mail.sent[0].Text, "a-better-pw-9") {
		t.Error("the new password must never be echoed")
	}
}

func TestSecureMyAccountAlsoRemovesEveryPasskey(t *testing.T) {
	e := newSecEnv(t)
	rec := e.post(e.api.secure, `{"current":"old-pw","new":"a-better-pw-9"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Removed int `json:"passkeys_removed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if e.pk.deleted != 2 || out.Removed != 2 || e.rev.calls != 1 || e.sess.keptOther != e.curID {
		t.Errorf("deleted %d passkeys, reported %d, remembered cleared %d, kept %v", e.pk.deleted, out.Removed, e.rev.calls, e.sess.keptOther)
	}
	if len(e.mail.sent) != 1 || !strings.Contains(e.mail.sent[0].Subject, "secured") {
		t.Errorf("mail: %+v", e.mail.sent)
	}
}

// The current password is the re-authentication, counted per address and per client, so a stolen session cannot guess it.
func TestChangePasswordRateLimitsWrongCurrentPasswords(t *testing.T) {
	e := newSecEnv(t)
	for i := 0; i < 10; i++ {
		if rec := e.post(e.api.change, `{"current":"wrong","new":"a-better-pw-9"}`, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if rec := e.post(e.api.change, `{"current":"old-pw","new":"a-better-pw-9"}`, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after ten wrong guesses even the right password waits: %d", rec.Code)
	}
	if len(e.k.puts) != 0 || e.rev.calls != 0 {
		t.Error("nothing may change while locked out")
	}
}

func TestChangePasswordRefusedBeforeAnythingChanges(t *testing.T) {
	e := newSecEnv(t)
	e.k.rejectNew = "weak-pw"
	cases := []struct {
		name   string
		body   string
		mutate func(*http.Request) *http.Request
		want   int
	}{
		{"the policy refuses the new password", `{"current":"old-pw","new":"weak-pw"}`, nil, http.StatusUnprocessableEntity},
		{"same password", `{"current":"old-pw","new":"old-pw"}`, nil, http.StatusBadRequest},
		{"missing field", `{"current":"old-pw"}`, nil, http.StatusBadRequest},
		{"the app token", `{"current":"old-pw","new":"a-better-pw-9"}`, func(r *http.Request) *http.Request {
			return r.WithContext(auth.WithSession(r.Context(), auth.SessionRef{ID: uuid.New(), Kind: sessions.KindApp}))
		}, http.StatusForbidden},
		{"an admin viewing as the user", `{"current":"old-pw","new":"a-better-pw-9"}`, func(r *http.Request) *http.Request {
			return r.WithContext(auth.WithImpersonation(r.Context(), uuid.New(), e.uid))
		}, http.StatusForbidden},
		{"cross-site", `{"current":"old-pw","new":"a-better-pw-9"}`, func(r *http.Request) *http.Request {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}, http.StatusForbidden},
		{"not JSON", `{"current":"old-pw","new":"a-better-pw-9"}`, func(r *http.Request) *http.Request {
			r.Header.Set("Content-Type", "text/plain")
			return r
		}, http.StatusUnsupportedMediaType},
		{"no session to keep", `{"current":"old-pw","new":"a-better-pw-9"}`, func(r *http.Request) *http.Request {
			return r.WithContext(auth.WithSession(r.Context(), auth.SessionRef{}))
		}, http.StatusConflict},
	}
	for _, c := range cases {
		if rec := e.post(e.api.change, c.body, c.mutate); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if len(e.k.puts) != 0 || e.rev.calls != 0 || e.pk.deleted != 0 || len(e.mail.sent) != 0 {
		t.Errorf("a refused request changed something: puts=%d forgets=%d deleted=%d mail=%d", len(e.k.puts), e.rev.calls, e.pk.deleted, len(e.mail.sent))
	}
	// An account with no Kratos identity (Google, GitHub or Apple only) has no password to change.
	e.k.hasIdent = false
	if rec := e.post(e.api.change, `{"current":"old-pw","new":"a-better-pw-9"}`, nil); rec.Code != http.StatusConflict {
		t.Errorf("no identity: %d", rec.Code)
	}
}

// If part of the clean-up cannot finish after the password changed, say so plainly and name what is left, so the screen can offer
// each as one tap; the old password no longer works, so repeating this call cannot be the way out. The owner is told by email
// either way, and the steps that can still run do run.
func TestPasswordChangedButCleanUpIncompleteNamesWhatIsLeft(t *testing.T) {
	incompleteOf := func(rec *httptest.ResponseRecorder) []string {
		var out struct {
			Changed    bool     `json:"password_changed"`
			Incomplete []string `json:"incomplete"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusBadGateway || !out.Changed {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return out.Incomplete
	}

	// The remembered-login clear fails: sign_out is left, and the other steps still ran.
	e := newSecEnv(t)
	e.rev.failFirst = 1
	got := incompleteOf(e.post(e.api.secure, `{"current":"old-pw","new":"a-better-pw-9"}`, nil))
	if len(got) != 1 || got[0] != "sign_out" || e.pk.deleted != 2 || e.sess.keptOther != e.curID {
		t.Errorf("incomplete %v, passkeys deleted %d, sessions kept %v: sign_out is left; the passkeys and sessions still went", got, e.pk.deleted, e.sess.keptOther)
	}
	if len(e.k.puts) != 1 || len(e.mail.sent) != 1 {
		t.Errorf("the password changed (%d puts) and the owner must be told (%d mails)", len(e.k.puts), len(e.mail.sent))
	}

	// Secure, and the passkey delete is what fails: only passkeys is left, and "Sign out other devices" would not finish it.
	e = newSecEnv(t)
	e.pk.deleteFails = true
	got = incompleteOf(e.post(e.api.secure, `{"current":"old-pw","new":"a-better-pw-9"}`, nil))
	if len(got) != 1 || got[0] != "passkeys" || e.rev.calls != 1 || e.sess.keptOther != e.curID {
		t.Errorf("incomplete %v: only passkeys should be left (forgets %d, kept %v)", got, e.rev.calls, e.sess.keptOther)
	}

	// Both fail: both are named.
	e = newSecEnv(t)
	e.rev.failFirst, e.pk.deleteFails = 1, true
	if got := incompleteOf(e.post(e.api.secure, `{"current":"old-pw","new":"a-better-pw-9"}`, nil)); len(got) != 2 {
		t.Errorf("incomplete %v, want sign_out and passkeys", got)
	}
}

func secJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
