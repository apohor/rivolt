package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
)

type fakeAllSessions struct {
	mu    sync.Mutex
	users []uuid.UUID
	fail  bool
}

func (f *fakeAllSessions) RevokeAll(_ context.Context, uid uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, errors.New("db down")
	}
	f.users = append(f.users, uid)
	return 3, nil
}

type secureEnv struct {
	a    *adminSecure
	k    *secKratos
	sess *fakeAllSessions
	rev  *fakeRevoker
	pk   *secPasskeys
	mail *secMail
}

func newSecureEnv(t *testing.T) *secureEnv {
	t.Helper()
	k := &secKratos{hasIdent: true}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	t.Cleanup(srv.Close)
	kc, err := kratos.New(kratos.Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	e := &secureEnv{k: k, sess: &fakeAllSessions{}, rev: &fakeRevoker{}, pk: &secPasskeys{creds: []passkeys.Credential{{ID: uuid.New()}, {ID: uuid.New()}}}, mail: &secMail{}}
	e.a = &adminSecure{
		Account: func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil },
		Kratos:  kc, Remembered: e.rev, Sessions: e.sess, Passkeys: e.pk, Mail: e.mail, Logger: discardLogger(),
	}
	return e
}

func (e *secureEnv) post(target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/users/"+target+"/secure", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", target)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	e.a.handle(rec, req)
	return rec
}

func TestAdminSecureDoesEverythingAndReturnsThePasswordOnce(t *testing.T) {
	e := newSecureEnv(t)
	target := uuid.New()
	rec := e.post(target.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Password string `json:"temporary_password"`
		Sessions int64  `json:"sessions_revoked"`
		Removed  int    `json:"passkeys_removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Password) < 20 || out.Sessions != 3 || out.Removed != 2 {
		t.Errorf("response %+v", out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the temporary password must not be cacheable")
	}
	// Everything happened: the password went to Kratos (the same one returned), the remembered logins were cleared, every session
	// revoked, every passkey deleted, the account emailed.
	if len(e.k.puts) != 1 || !strings.Contains(secJSON(e.k.puts[0]), out.Password) {
		t.Errorf("Kratos did not receive the returned password: %v", e.k.puts)
	}
	if e.rev.calls != 1 || len(e.sess.users) != 1 || e.sess.users[0] != target || e.pk.deleted != 2 {
		t.Errorf("forgets=%d sessions=%v passkeys deleted=%d", e.rev.calls, e.sess.users, e.pk.deleted)
	}
	if len(e.mail.sent) != 1 || e.mail.sent[0].To != "owner@x.test" || !strings.Contains(e.mail.sent[0].Subject, "support secured") {
		t.Fatalf("mail %+v", e.mail.sent)
	}
	// The password is in the response and nowhere else: not in the email, not in the logs.
	if strings.Contains(e.mail.sent[0].Text, out.Password) || strings.Contains(e.mail.sent[0].Subject, out.Password) {
		t.Error("the temporary password must not be emailed")
	}
	// A second run gives a different one (it is generated each time and never stored).
	if rec2 := e.post(target.String()); !strings.Contains(rec2.Body.String(), "temporary_password") || strings.Contains(rec2.Body.String(), out.Password) {
		t.Errorf("the second run must produce a new password")
	}
}

// On any failure no password is returned and the answer names the step: the account is half done, and running it again finishes it.
func TestAdminSecureReturnsNoPasswordWhenAStepFails(t *testing.T) {
	for name, breakIt := range map[string]func(*secureEnv){
		"forget":   func(e *secureEnv) { e.rev.failFirst = 1 },
		"sessions": func(e *secureEnv) { e.sess.fail = true },
		"passkeys": func(e *secureEnv) { e.pk.listFails = true },
	} {
		e := newSecureEnv(t)
		breakIt(e)
		rec := e.post(uuid.NewString())
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "temporary_password") {
			t.Errorf("%s failing: %d %s, want a 502 with no password", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"step"`) {
			t.Errorf("%s failing: the answer must name the step: %s", name, rec.Body.String())
		}
		if len(e.mail.sent) != 0 {
			t.Errorf("%s failing: the 'secured' notice must wait for success", name)
		}
	}
}

func TestAdminSecureRefusals(t *testing.T) {
	e := newSecureEnv(t)
	if rec := e.post("not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
	e.a.Account = func(context.Context, uuid.UUID) (string, error) { return "", sql.ErrNoRows }
	if rec := e.post(uuid.NewString()); rec.Code != http.StatusNotFound {
		t.Errorf("no such user: %d", rec.Code)
	}
	e.a.Account = func(context.Context, uuid.UUID) (string, error) { return "", nil }
	if rec := e.post(uuid.NewString()); rec.Code != http.StatusConflict {
		t.Errorf("no email: %d", rec.Code)
	}
	e.a.Account = func(context.Context, uuid.UUID) (string, error) { return "owner@x.test", nil }
	e.k.hasIdent = false // Google, GitHub or Apple only: nothing to reset
	if rec := e.post(uuid.NewString()); rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "temporary_password") {
		t.Errorf("no password account: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.k.puts) != 0 || len(e.sess.users) != 0 || e.pk.deleted != 0 || len(e.mail.sent) != 0 {
		t.Error("a refused request must change nothing")
	}
}

// The audit row for this action holds the action and the target and nothing else: its spec names no body keys, no field names
// and no before-values, so even a request that carried a password would leave none of it in the log.
func TestAdminSecureAuditRowHoldsNoSecret(t *testing.T) {
	spec := auditSpec{action: "user.secure", targetType: "user", idParam: "id", userTarget: true}
	got := auditDetails(spec, map[string]any{"password": "hunter2-hunter2", "temporary_password": "x"})
	if len(got) != 0 {
		t.Errorf("the audit details must be empty for this action, got %v", got)
	}
}

// Passkeys off on the admin deployment (no WebAuthn service) must not skip the deletion: the store comes from the database, not
// from the sign-in service (qa's attacker story on admin-preview: "removed 0 passkeys" with one credential left).
func TestAdminSecureTakesThePasskeyStoreWithoutWebAuthn(t *testing.T) {
	e := newSecureEnv(t)
	d := Deps{PasskeyStore: e.pk, Kratos: e.a.Kratos, SessionRevoker: e.sess, Logger: discardLogger()} // Passkeys (the service) is nil
	d.Passkeys = nil
	sec := d.adminSecure()
	if sec.Passkeys != passkeys.Store(e.pk) {
		t.Fatal("the reset did not take the passkey store from Deps.PasskeyStore")
	}
	sec.Account = e.a.Account
	e.a = sec
	rec := e.post(uuid.NewString())
	if rec.Code != http.StatusOK || e.pk.deleted != 2 || !strings.Contains(rec.Body.String(), `"passkeys_removed":2`) {
		t.Fatalf("%d %s deleted=%d, want both credentials removed and reported", rec.Code, rec.Body.String(), e.pk.deleted)
	}
	if got := (Deps{}).adminSecure().Passkeys; got != nil {
		t.Errorf("no database must leave a nil store (not a typed nil), got %v", got)
	}
}

// With no way to look at the passkeys, the reset refuses before it changes anything, instead of reporting success.
func TestAdminSecureWithoutAPasskeyStoreChangesNothing(t *testing.T) {
	e := newSecureEnv(t)
	e.a.Passkeys = nil
	rec := e.post(uuid.NewString())
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "temporary_password") {
		t.Fatalf("%d %s, want 503 and no password", rec.Code, rec.Body.String())
	}
	if len(e.k.puts) != 0 || len(e.sess.users) != 0 || len(e.mail.sent) != 0 {
		t.Error("a refused reset must change nothing")
	}
}
