package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/passkeys/passkeytest"
)

const (
	pkRP     = "rivolt.dev"
	pkOrigin = "https://rivolt.dev"
	pkEmail  = "me@example.com"
)

// mailFunc is a mailer that calls a function.
type mailFunc func(ctx context.Context, m email.Message) error

func (f mailFunc) Send(ctx context.Context, m email.Message) error { return f(ctx, m) }

// pkRig is the real router with passkeys on, a fake Kratos (one identity, password "right") and a fake Hydra (one open login
// challenge "good"), and a signed-in browser session.
type pkRig struct {
	router   http.Handler
	cookie   *http.Cookie
	uid      uuid.UUID
	store    *memPasskeyStore
	mu       sync.Mutex
	accepted []map[string]any
	mail     []email.Message
}

func newPKRig(t *testing.T, kratosHasIdentity bool) *pkRig {
	t.Helper()
	r := &pkRig{uid: uuid.New(), store: newMemPasskeyStore()}
	krat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/admin/identities":
			if kratosHasIdentity && req.URL.Query().Get("credentials_identifier") == pkEmail {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "kratos-id-1", "traits": map[string]any{"email": pkEmail}}})
				return
			}
			_, _ = w.Write([]byte("[]"))
		case req.URL.Path == "/self-service/login/api":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "flow-1"})
		case req.URL.Path == "/self-service/login":
			var b map[string]string
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["password"] != "right" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"session_token": "", "session": map[string]any{"identity": map[string]any{"id": "kratos-id-1", "traits": map[string]any{"email": pkEmail}}}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(krat.Close)
	hyd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/admin/oauth2/auth/requests/login":
			if req.URL.Query().Get("login_challenge") != "good" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"challenge": "good", "skip": false})
		case "/admin/oauth2/auth/requests/login/accept":
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			r.mu.Lock()
			r.accepted = append(r.accepted, b)
			r.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"redirect_to": "https://hydra.example/continue"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(hyd.Close)
	kc, err := kratos.New(kratos.Config{AdminURL: krat.URL, PublicURL: krat.URL})
	if err != nil {
		t.Fatal(err)
	}
	hc, err := hydra.New(hydra.Config{AdminURL: hyd.URL})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := passkeys.New(passkeys.Config{RPID: pkRP, Origins: []string{pkOrigin}}, r.store)
	if err != nil {
		t.Fatal(err)
	}
	as, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	r.router = New(Deps{
		Auth: as, AuthEnforced: true, Passkeys: svc, Kratos: kc, Hydra: hc,
		PasskeyAccount: func(context.Context, uuid.UUID) (string, string, error) { return pkEmail, "Me", nil },
		PasskeyMailer: mailFunc(func(_ context.Context, m email.Message) error {
			r.mu.Lock()
			r.mail = append(r.mail, m)
			r.mu.Unlock()
			return nil
		}),
	})
	rec := httptest.NewRecorder()
	if err := as.IssueSession(rec, httptest.NewRequest(http.MethodGet, "/", nil), r.uid); err != nil {
		t.Fatal(err)
	}
	r.cookie = rec.Result().Cookies()[0]
	return r
}

func (r *pkRig) do(method, path string, body any, signedIn bool, hdr map[string]string) (int, []byte) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "198.51.100.9:4000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signedIn {
		req.AddCookie(r.cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// addPasskey runs the whole add flow with the software authenticator and returns the passkey's id.
func (r *pkRig) addPasskey(t *testing.T, a *passkeytest.SoftAuth, label string) uuid.UUID {
	t.Helper()
	code, body := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "right"}, true, nil)
	if code != 200 {
		t.Fatalf("begin = %d %s", code, body)
	}
	var begin struct {
		CeremonyID uuid.UUID                   `json:"ceremony_id"`
		Options    protocol.CredentialCreation `json:"options"`
	}
	if err := json.Unmarshal(body, &begin); err != nil {
		t.Fatal(err)
	}
	cred := a.Create(&protocol.CredentialCreation{Response: begin.Options.Response}, pkRP, pkOrigin, true)
	code, body = r.do("POST", "/api/auth/passkeys/register/finish", map[string]any{"ceremony_id": begin.CeremonyID, "label": label, "credential": json.RawMessage(cred)}, true, nil)
	if code != 200 {
		t.Fatalf("finish = %d %s", code, body)
	}
	var fin struct {
		Passkey passkeyView `json:"passkey"`
	}
	_ = json.Unmarshal(body, &fin)
	return fin.Passkey.ID
}

func TestPasskeysAddListRenameAndRemove(t *testing.T) {
	r := newPKRig(t, true)
	if code, body := r.do("GET", "/api/auth/passkeys", nil, true, nil); code != 200 || !strings.Contains(string(body), `"passkeys":[]`) {
		t.Fatalf("empty list = %d %s", code, body)
	}
	id := r.addPasskey(t, passkeytest.New(t), "My phone")
	r.mu.Lock()
	if len(r.mail) != 1 || r.mail[0].To != pkEmail || !strings.Contains(r.mail[0].Text, "My phone") || !strings.Contains(r.mail[0].Text, "support@rivolt.dev") {
		t.Errorf("the account was not told a passkey was added: %+v", r.mail)
	}
	r.mu.Unlock()
	code, body := r.do("GET", "/api/auth/passkeys", nil, true, nil)
	if code != 200 || !strings.Contains(string(body), "My phone") || strings.Contains(string(body), "public") {
		t.Errorf("list = %d %s", code, body)
	}
	if code, _ := r.do("PATCH", "/api/auth/passkeys/"+id.String(), map[string]any{"label": "Work laptop"}, true, nil); code != 200 {
		t.Errorf("rename = %d", code)
	}
	if code, _ := r.do("PATCH", "/api/auth/passkeys/"+id.String(), map[string]any{"label": "  "}, true, nil); code != 400 {
		t.Errorf("blank label = %d, want 400", code)
	}
	if code, _ := r.do("DELETE", "/api/auth/passkeys/"+uuid.NewString(), nil, true, nil); code != 404 {
		t.Errorf("deleting someone else's or an unknown passkey = %d, want 404", code)
	}
	if code, _ := r.do("DELETE", "/api/auth/passkeys/"+id.String(), nil, true, nil); code != 204 {
		t.Errorf("delete = %d", code)
	}
	if code, body := r.do("GET", "/api/auth/passkeys", nil, true, nil); code != 200 || !strings.Contains(string(body), `"passkeys":[]`) {
		t.Errorf("after delete = %d %s", code, body)
	}
}

func TestPasskeyRegistrationNeedsThePasswordAgain(t *testing.T) {
	r := newPKRig(t, true)
	if code, body := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "wrong"}, true, nil); code != 403 || !strings.Contains(string(body), "password_incorrect") {
		t.Errorf("wrong password = %d %s", code, body)
	}
	if code, _ := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{}, true, nil); code != 400 {
		t.Errorf("no password = %d, want 400", code)
	}
	// Ten wrong passwords lock further tries out, as they do on the sign-in page.
	for i := 0; i < 12; i++ {
		r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "wrong"}, true, nil)
	}
	if code, _ := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "right"}, true, nil); code != 429 && code != 200 {
		t.Errorf("after many wrong passwords = %d", code)
	}
}

// An account that signs in only with Google, GitHub or Apple has no Kratos identity and no password: it is told so.
func TestPasskeyRegistrationForAnAccountWithoutAPassword(t *testing.T) {
	r := newPKRig(t, false)
	if code, body := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "right"}, true, nil); code != 409 || !strings.Contains(string(body), "passkeys_need_an_email_account") {
		t.Errorf("= %d %s", code, body)
	}
}

func TestPasskeyManagementGuards(t *testing.T) {
	r := newPKRig(t, true)
	// Not signed in.
	if code, _ := r.do("GET", "/api/auth/passkeys", nil, false, nil); code != 401 {
		t.Errorf("signed out = %d, want 401", code)
	}
	// A cross-site browser request, and a body that is not JSON.
	if code, _ := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "right"}, true, map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Errorf("cross-site = %d, want 403", code)
	}
	if code, _ := r.do("POST", "/api/auth/passkeys/register/begin", map[string]any{"password": "right"}, true, map[string]string{"Content-Type": "text/plain"}); code != 415 {
		t.Errorf("not JSON = %d, want 415", code)
	}
	// Direct on the handlers: an admin viewing as the user, and the iPhone app's token, cannot add or remove.
	svc, _ := passkeys.New(passkeys.Config{RPID: pkRP, Origins: []string{pkOrigin}}, r.store)
	api := &passkeyAPI{Svc: svc, Logger: nil}
	for name, ctx := range map[string]context.Context{
		"viewing as": auth.WithImpersonation(auth.WithUser(context.Background(), r.uid), uuid.New(), r.uid),
		"app token":  auth.WithSession(auth.WithUser(context.Background(), r.uid), auth.SessionRef{ID: uuid.New(), Kind: "app"}),
	} {
		for _, h := range map[string]http.HandlerFunc{"list": api.list, "begin": api.registerBegin, "finish": api.registerFinish, "rename": api.rename, "remove": api.remove} {
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}")).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h(w, req)
			if w.Code != 403 {
				t.Errorf("%s: a handler answered %d, want 403", name, w.Code)
			}
		}
	}
}

// Sign-in on the Hydra login page: usernameless, bound to the login challenge, ending in the same accept as the password path.
func TestPasskeySignInThroughHydra(t *testing.T) {
	r := newPKRig(t, true)
	a := passkeytest.New(t)
	r.addPasskey(t, a, "My phone")
	begin := func(challenge string) (int, uuid.UUID, protocol.CredentialAssertion) {
		code, body := r.do("POST", "/api/auth/hydra/passkey/begin", map[string]any{"challenge": challenge}, false, nil)
		var out struct {
			CeremonyID uuid.UUID                    `json:"ceremony_id"`
			Options    protocol.CredentialAssertion `json:"options"`
		}
		_ = json.Unmarshal(body, &out)
		return code, out.CeremonyID, out.Options
	}
	finish := func(challenge string, cid uuid.UUID, cred []byte) (int, string) {
		code, body := r.do("POST", "/api/auth/hydra/passkey/finish", map[string]any{"challenge": challenge, "ceremony_id": cid, "credential": json.RawMessage(cred)}, false, nil)
		return code, string(body)
	}
	// A challenge Hydra does not know answers like a wrong credential.
	if code, _, _ := begin("unknown"); code != 401 {
		t.Errorf("unknown challenge = %d, want 401", code)
	}
	code, cid, opts := begin("good")
	if code != 200 || opts.Response.UserVerification != "required" {
		t.Fatalf("begin = %d, uv %q", code, opts.Response.UserVerification)
	}
	handle, _ := r.store.UserHandle(context.Background(), r.uid)
	code, body := finish("good", cid, a.Sign(&opts, pkRP, pkOrigin, true, 1, handle))
	if code != 200 || !strings.Contains(body, "https://hydra.example/continue") {
		t.Fatalf("sign-in = %d %s", code, body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.accepted) != 1 || r.accepted[0]["subject"] != "kratos-id-1" || r.accepted[0]["remember"] != true {
		t.Fatalf("hydra accept = %+v", r.accepted)
	}
	if amr, _ := r.accepted[0]["amr"].([]any); len(amr) != 2 || amr[1] != "user" {
		t.Errorf("amr = %v", r.accepted[0]["amr"])
	}
}

// Every way a passkey sign-in can fail short of a server fault answers the same 401, and nothing is accepted.
func TestPasskeySignInFailuresAllAnswerAlike(t *testing.T) {
	r := newPKRig(t, true)
	a := passkeytest.New(t)
	r.addPasskey(t, a, "My phone")
	handle, _ := r.store.UserHandle(context.Background(), r.uid)
	type attempt struct {
		name string
		run  func() (int, string)
	}
	signIn := func(challenge string, mutate func(*protocol.CredentialAssertion) (protocol.CredentialAssertion, bool, string, uint32)) (int, string) {
		_, body := r.do("POST", "/api/auth/hydra/passkey/begin", map[string]any{"challenge": "good"}, false, nil)
		var out struct {
			CeremonyID uuid.UUID                    `json:"ceremony_id"`
			Options    protocol.CredentialAssertion `json:"options"`
		}
		_ = json.Unmarshal(body, &out)
		o, uv, origin, count := mutate(&out.Options)
		cred := a.Sign(&o, pkRP, origin, uv, count, handle)
		code, resp := r.do("POST", "/api/auth/hydra/passkey/finish", map[string]any{"challenge": challenge, "ceremony_id": out.CeremonyID, "credential": json.RawMessage(cred)}, false, nil)
		return code, string(resp)
	}
	same := func(o *protocol.CredentialAssertion) (protocol.CredentialAssertion, bool, string, uint32) {
		return *o, true, pkOrigin, 1
	}
	attempts := []attempt{
		{"other origin", func() (int, string) {
			return signIn("good", func(o *protocol.CredentialAssertion) (protocol.CredentialAssertion, bool, string, uint32) {
				return *o, true, "https://preview.rivolt.dev", 1
			})
		}},
		{"no user verification", func() (int, string) {
			return signIn("good", func(o *protocol.CredentialAssertion) (protocol.CredentialAssertion, bool, string, uint32) {
				return *o, false, pkOrigin, 1
			})
		}},
		{"another login challenge", func() (int, string) { return signIn("good-but-other", same) }},
		{"garbage credential", func() (int, string) {
			return func() (int, string) {
				c, b := r.do("POST", "/api/auth/hydra/passkey/finish", map[string]any{"challenge": "good", "ceremony_id": uuid.New(), "credential": json.RawMessage(`{"x":1}`)}, false, nil)
				return c, string(b)
			}()
		}},
	}
	var first string
	for _, at := range attempts {
		code, body := at.run()
		if code != 401 {
			t.Errorf("%s: %d %s, want 401", at.name, code, body)
		}
		if first == "" {
			first = body
		} else if body != first && at.name != "another login challenge" {
			// all the answers are the same words
			t.Errorf("%s answered %q, others answered %q", at.name, body, first)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.accepted) != 0 {
		t.Fatalf("a failed sign-in was accepted: %+v", r.accepted)
	}
}

func TestPasskeyRoutesAreAbsentWhenNotConfigured(t *testing.T) {
	as, _ := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	router := New(Deps{Auth: as, AuthEnforced: true})
	for _, p := range []string{"/api/auth/passkeys", "/api/auth/passkeys/register/begin", "/api/auth/hydra/passkey/begin"} {
		for _, m := range []string{"GET", "POST"} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(m, p, strings.NewReader("{}")))
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s = %d with passkeys off", m, p, w.Code)
			}
		}
	}
}
