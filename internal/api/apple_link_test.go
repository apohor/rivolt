package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/oidc"
)

// Link Apple to my account, the start and the sign-out hook (design: _shared/apple-link-design.md; DA's conditions A and C).

const appleLinkMail = "me@example.com"

// fakeBegin stands in for the OIDC service: it records the request exactly as oidc.BeginAppleLink keys it (sha256 of the state and of
// the session id's text), so the sign-out hook is tested against the same hashes.
type fakeBegin struct {
	intents map[string]uuid.UUID // state hash → user
	bySess  map[string][]string  // session hash → state hashes
	err     error
}

func (f *fakeBegin) BeginAppleLink(w http.ResponseWriter, _ *http.Request, uid, sessionID uuid.UUID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	state := uuid.NewString()
	f.intents[string(oidc.LinkHash(state))] = uid
	sh := string(oidc.LinkHash(sessionID.String()))
	f.bySess[sh] = append(f.bySess[sh], string(oidc.LinkHash(state)))
	http.SetCookie(w, &http.Cookie{Name: "__Secure-rivolt_oidc_apple", Value: "x", Path: "/api/auth/oidc/apple", Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode})
	return "https://appleid.apple.example/authorize?state=" + state, nil
}

// DropSessionLinkIntents is the store's sign-out delete, over the same map.
func (f *fakeBegin) DropSessionLinkIntents(_ context.Context, sessionHash []byte) error {
	for _, st := range f.bySess[string(sessionHash)] {
		delete(f.intents, st)
	}
	delete(f.bySess, string(sessionHash))
	return nil
}

type fakeLive struct {
	created time.Time
	live    bool
}

func (f fakeLive) Live(context.Context, uuid.UUID, uuid.UUID) (time.Time, bool, error) {
	return f.created, f.live, nil
}

type linkRig struct {
	api      *appleLinkAPI
	begin    *fakeBegin
	outcomes []string
	uid, sid uuid.UUID
}

// newLinkRig makes the API over a fake Kratos with one identity (password "right") when hasIdentity, and a session created `age` ago.
func newLinkRig(t *testing.T, age time.Duration, hasIdentity bool, account string) *linkRig {
	t.Helper()
	krat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/admin/identities":
			if hasIdentity && req.URL.Query().Get("credentials_identifier") == appleLinkMail {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "kid", "traits": map[string]any{"email": appleLinkMail}}})
				return
			}
			_, _ = w.Write([]byte("[]"))
		case "/self-service/login/api":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "flow-1"})
		case "/self-service/login":
			var b map[string]string
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["password"] != "right" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"session_token": "", "session": map[string]any{"identity": map[string]any{"id": "kid", "traits": map[string]any{"email": appleLinkMail}}}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(krat.Close)
	kc, err := kratos.New(kratos.Config{AdminURL: krat.URL, PublicURL: krat.URL})
	if err != nil {
		t.Fatal(err)
	}
	r := &linkRig{begin: &fakeBegin{intents: map[string]uuid.UUID{}, bySess: map[string][]string{}}, uid: uuid.New(), sid: uuid.New()}
	r.api = &appleLinkAPI{
		OIDC:     r.begin,
		Sessions: fakeLive{created: time.Now().Add(-age), live: true},
		Kratos:   kc,
		Account:  func(context.Context, uuid.UUID) (string, error) { return account, nil },
		Failures: newFailLimiter(10, 15*time.Minute),
		Logger:   discardLogger(),
		Observe:  func(o string) { r.outcomes = append(r.outcomes, o) },
	}
	return r
}

func (r *linkRig) post(body string, mut func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/apple-link/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.WithSession(auth.WithUser(req.Context(), r.uid), auth.SessionRef{ID: r.sid, Kind: "web"})
	req = req.WithContext(ctx)
	if mut != nil {
		req = mut(req)
	}
	rec := httptest.NewRecorder()
	r.api.start(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	b, _ := io.ReadAll(rec.Body)
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &out)
	return out.Error
}

// A session created a moment ago is no proof of a typed credential (Hydra's remembered login makes one silently), so the password is
// asked whatever the session's age.
func TestAppleLinkStartAsksForThePasswordEvenOnAFreshSession(t *testing.T) {
	for name, age := range map[string]time.Duration{"just created": time.Second, "an hour old": time.Hour, "three days old": 72 * time.Hour} {
		r := newLinkRig(t, age, true, appleLinkMail)
		if rec := r.post("{}", nil); rec.Code != http.StatusForbidden || errCode(t, rec) != "password_required" {
			t.Errorf("%s, no password: %d %s", name, rec.Code, rec.Body)
		}
		if rec := r.post(`{"password":"wrong"}`, nil); rec.Code != http.StatusForbidden || errCode(t, rec) != "password_incorrect" {
			t.Errorf("%s, wrong password: %d %s", name, rec.Code, rec.Body)
		}
		if len(r.begin.intents) != 0 {
			t.Fatalf("%s: a request was recorded without the password", name)
		}
		rec := r.post(`{"password":"right"}`, nil)
		if rec.Code != http.StatusOK || len(r.begin.intents) != 1 || len(rec.Result().Cookies()) != 1 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s, right password: %d %s, intents %d", name, rec.Code, rec.Body, len(r.begin.intents))
		}
		var out struct{ URL string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if !strings.HasPrefix(out.URL, "https://appleid.apple.example/authorize") {
			t.Errorf("%s: url %q", name, out.URL)
		}
		want := []string{"reauth_refused", "reauth_refused", "started"}
		if strings.Join(r.outcomes, ",") != strings.Join(want, ",") {
			t.Errorf("%s: outcomes %v, want %v", name, r.outcomes, want)
		}
	}
}

// An account that signs in only with Google or GitHub has no password to type: it cannot link yet, on any session.
func TestAppleLinkStartWithoutAPasswordIsRefused(t *testing.T) {
	for name, r := range map[string]*linkRig{
		"no email":                   newLinkRig(t, time.Second, true, ""),
		"no identity":                newLinkRig(t, time.Second, false, appleLinkMail),
		"no identity, older session": newLinkRig(t, 3*time.Hour, false, appleLinkMail),
	} {
		if rec := r.post(`{"password":"right"}`, nil); rec.Code != http.StatusForbidden || errCode(t, rec) != "password_needed_to_link" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if len(r.begin.intents) != 0 {
			t.Errorf("%s: a request was recorded", name)
		}
		if len(r.outcomes) != 1 || r.outcomes[0] != "password_needed" {
			t.Errorf("%s: outcomes %v, want [password_needed]", name, r.outcomes)
		}
	}
}

func TestAppleLinkStartRefusals(t *testing.T) {
	r := newLinkRig(t, time.Minute, true, appleLinkMail)
	pw := `{"password":"right"}`
	cases := map[string]func(*http.Request) *http.Request{
		"cross-site": func(q *http.Request) *http.Request { q.Header.Set("Sec-Fetch-Site", "cross-site"); return q },
		"not json":   func(q *http.Request) *http.Request { q.Header.Set("Content-Type", "text/plain"); return q },
		"app token": func(q *http.Request) *http.Request {
			return q.WithContext(auth.WithSession(q.Context(), auth.SessionRef{ID: r.sid, Kind: "app"}))
		},
		"no user": func(q *http.Request) *http.Request {
			return q.WithContext(auth.WithSession(httptest.NewRequest("GET", "/", nil).Context(), auth.SessionRef{ID: r.sid, Kind: "web"}))
		},
		"no session row": func(q *http.Request) *http.Request {
			return q.WithContext(auth.WithUser(httptest.NewRequest("GET", "/", nil).Context(), r.uid))
		},
	}
	for name, mut := range cases {
		if rec := r.post(pw, mut); rec.Code < 400 || len(r.begin.intents) != 0 {
			t.Errorf("%s: status %d, intents %d", name, rec.Code, len(r.begin.intents))
		}
	}
	r.api.Sessions = fakeLive{created: time.Now(), live: false}
	if rec := r.post(pw, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("an ended session: %d", rec.Code)
	}
	r.api.Sessions = fakeLive{created: time.Now(), live: true}
	r.begin.err = oidc.ErrAppleNotConfigured
	if rec := r.post(pw, nil); rec.Code != http.StatusNotFound || errCode(t, rec) != "apple_not_available" {
		t.Errorf("no Apple provider: %d %s", rec.Code, rec.Body)
	}
}

// DA's condition A, end to end: the request the start recorded for a session is gone after that session signs out through the real
// sign-out handler, and only that session's.
func TestSigningOutDropsTheLinkRequestsOfThatSession(t *testing.T) {
	r := newLinkRig(t, time.Minute, true, appleLinkMail)
	if rec := r.post(`{"password":"right"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("start: %d", rec.Code)
	}
	otherUser, otherSession := uuid.New(), uuid.New()
	if _, err := r.begin.BeginAppleLink(httptest.NewRecorder(), nil, otherUser, otherSession); err != nil {
		t.Fatal(err)
	}
	if len(r.begin.intents) != 2 {
		t.Fatalf("intents = %d, want 2", len(r.begin.intents))
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = req.WithContext(auth.WithSession(auth.WithUser(req.Context(), r.uid), auth.SessionRef{ID: r.sid, Kind: "web"}))
	rec := httptest.NewRecorder()
	Deps{Auth: svc, AppleLinkIntents: r.begin}.logoutHandler()(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if len(r.begin.intents) != 1 {
		t.Errorf("intents after sign-out = %d, want 1 (the other session's)", len(r.begin.intents))
	}
	for _, u := range r.begin.intents {
		if u != otherUser {
			t.Errorf("the surviving request belongs to %v, want the other session's user", u)
		}
	}
}

// The store's hash and the OIDC service's are one function, or the hook would drop nothing.
func TestTheStoreAndTheServiceHashTheSameWay(t *testing.T) {
	for _, in := range []string{"", "state", uuid.NewString()} {
		if string(appleid.Hash(in)) != string(oidc.LinkHash(in)) {
			t.Errorf("appleid.Hash and oidc.LinkHash differ for %q", in)
		}
	}
}
