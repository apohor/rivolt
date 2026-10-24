package appleid

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

const (
	testIssuer = "https://appleid.apple.com"
	testClient = "dev.rivolt.web"
	testSub    = "001234.secretsub.5678"
	testRelay  = "abc123@privaterelay.appleid.com"
)

type fakeStore struct {
	users       map[string]uuid.UUID
	seen        map[string]bool
	tokenDrop   []uuid.UUID
	undeliv     map[uuid.UUID]bool
	mail        string
	failDrop    bool
	failSetFlag bool
}

func (f *fakeStore) UserBySub(_ context.Context, sub string) (uuid.UUID, bool, error) {
	id, ok := f.users[sub]
	return id, ok, nil
}
func (f *fakeStore) DropToken(_ context.Context, u uuid.UUID) error {
	if f.failDrop {
		return fmt.Errorf("db down")
	}
	f.tokenDrop = append(f.tokenDrop, u)
	return nil
}
func (f *fakeStore) MarkSeen(_ context.Context, h []byte) (bool, error) {
	if f.seen[string(h)] {
		return false, nil
	}
	f.seen[string(h)] = true
	return true, nil
}
func (f *fakeStore) Unsee(_ context.Context, h []byte) error { delete(f.seen, string(h)); return nil }
func (f *fakeStore) SetEmailUndeliverable(_ context.Context, u uuid.UUID, v bool) error {
	f.undeliv[u] = v
	return nil
}
func (f *fakeStore) DeliverableEmail(context.Context, uuid.UUID) (string, error) { return f.mail, nil }

type fakeSessions struct{ revoked []uuid.UUID }

func (f *fakeSessions) RevokeAll(_ context.Context, u uuid.UUID) (int64, error) {
	f.revoked = append(f.revoked, u)
	return 2, nil
}

type fakeMail struct{ sent []email.Message }

func (f *fakeMail) Send(_ context.Context, m email.Message) error {
	f.sent = append(f.sent, m)
	return nil
}

type rig struct {
	key  *rsa.PrivateKey
	st   *fakeStore
	ss   *fakeSessions
	ml   *fakeMail
	n    *Notifier
	logs *bytes.Buffer
	uid  uuid.UUID
	now  time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}
	uid := uuid.New()
	r := &rig{key: key, uid: uid, now: time.Now(), logs: &bytes.Buffer{},
		st: &fakeStore{users: map[string]uuid.UUID{testSub: uid}, seen: map[string]bool{}, undeliv: map[uuid.UUID]bool{}, mail: "me@example.com"},
		ss: &fakeSessions{}, ml: &fakeMail{}}
	r.n = &Notifier{Verify: newVerifier(ks, testIssuer, testClient), Store: r.st, Sessions: r.ss, Mail: r.ml,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return r.now }}
	return r
}

// sign builds a notification JWT the way Apple does: RS256, `events` a JSON string.
func (r *rig) sign(t *testing.T, iss, aud string, iat time.Time, typ, sub string) string {
	t.Helper()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: r.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := json.Marshal(map[string]any{"type": typ, "sub": sub, "email": testRelay, "is_private_email": "true", "event_time": iat.UnixMilli()})
	tok, err := jwt.Signed(sg).Claims(map[string]any{
		"iss": iss, "aud": aud, "iat": iat.Unix(), "exp": iat.Add(time.Hour).Unix(), "jti": uuid.NewString(), "events": string(ev),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func post(n http.Handler, body, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/apple/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	n.ServeHTTP(w, req)
	return w
}

func form(tok string) string { return url.Values{"payload": {tok}}.Encode() }

func TestConsentRevokedSignsOutEverywhereAndDropsToken(t *testing.T) {
	r := newRig(t)
	tok := r.sign(t, testIssuer, testClient, r.now, EventConsentRevoked, testSub)
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if len(r.ss.revoked) != 1 || r.ss.revoked[0] != r.uid || len(r.st.tokenDrop) != 1 {
		t.Fatalf("sessions %v token %v", r.ss.revoked, r.st.tokenDrop)
	}
	if len(r.ml.sent) != 0 {
		t.Fatal("consent-revoked sends no email")
	}
	assertNoAppleIdentifiers(t, r.logs.String())
	if !strings.Contains(r.logs.String(), r.uid.String()) {
		t.Fatal("the user id should be logged")
	}
}

func TestAccountDeleteAlsoEmailsAndNeverDeletes(t *testing.T) {
	r := newRig(t)
	tok := r.sign(t, testIssuer, testClient, r.now, EventAccountDelete, testSub)
	body, _ := json.Marshal(map[string]string{"payload": tok})
	if w := post(r.n, string(body), "application/json"); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if len(r.ml.sent) != 1 || r.ml.sent[0].To != "me@example.com" {
		t.Fatalf("mail %+v", r.ml.sent)
	}
	if len(r.ss.revoked) != 1 {
		t.Fatal("sessions not revoked")
	}
	assertNoAppleIdentifiers(t, r.logs.String())
}

func TestEmailDisabledAndEnabledToggleTheFlag(t *testing.T) {
	r := newRig(t)
	post(r.n, form(r.sign(t, testIssuer, testClient, r.now, EventEmailDisabled, testSub)), "application/x-www-form-urlencoded")
	if !r.st.undeliv[r.uid] {
		t.Fatal("email-disabled should mark undeliverable")
	}
	post(r.n, form(r.sign(t, testIssuer, testClient, r.now, EventEmailEnabled, testSub)), "application/x-www-form-urlencoded")
	if r.st.undeliv[r.uid] {
		t.Fatal("email-enabled should clear it")
	}
	if len(r.ss.revoked) != 0 {
		t.Fatal("email events must not sign anyone out")
	}
}

func TestUnknownSubIsASilent200(t *testing.T) {
	r := newRig(t)
	tok := r.sign(t, testIssuer, testClient, r.now, EventAccountDelete, "someone.else")
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if len(r.ss.revoked)+len(r.ml.sent) != 0 || r.logs.Len() != 0 {
		t.Fatalf("nothing should happen or be logged: %q", r.logs.String())
	}
}

func TestRefusedPayloads(t *testing.T) {
	r := newRig(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := &rig{key: other}
	cases := map[string]string{
		"wrong issuer":   r.sign(t, "https://evil.example", testClient, r.now, EventAccountDelete, testSub),
		"wrong audience": r.sign(t, testIssuer, "someone.else", r.now, EventAccountDelete, testSub),
		"stale iat":      r.sign(t, testIssuer, testClient, r.now.Add(-2*time.Hour), EventAccountDelete, testSub),
		"future iat":     r.sign(t, testIssuer, testClient, r.now.Add(time.Hour), EventAccountDelete, testSub),
		"wrong key":      forged.sign(t, testIssuer, testClient, r.now, EventAccountDelete, testSub),
		"not a jwt":      "garbage",
	}
	for name, tok := range cases {
		if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 400 {
			t.Errorf("%s: code %d, want 400", name, w.Code)
		}
	}
	if w := post(r.n, "", "application/x-www-form-urlencoded"); w.Code != 400 {
		t.Errorf("empty body: %d", w.Code)
	}
	if len(r.ss.revoked)+len(r.ml.sent)+len(r.st.seen) != 0 {
		t.Fatal("a refused payload must change nothing, not even the replay table")
	}
}

func TestReplayIsIgnored(t *testing.T) {
	r := newRig(t)
	tok := r.sign(t, testIssuer, testClient, r.now, EventConsentRevoked, testSub)
	for i := 0; i < 3; i++ {
		if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
			t.Fatalf("code %d", w.Code)
		}
	}
	if len(r.ss.revoked) != 1 {
		t.Fatalf("acted %d times", len(r.ss.revoked))
	}
	h := sha256.Sum256([]byte(tok))
	if !r.st.seen[string(h[:])] {
		t.Fatal("the replay key should be sha256 of the payload")
	}
}

func TestFailureLetsAppleRetry(t *testing.T) {
	r := newRig(t)
	r.st.failDrop = true
	tok := r.sign(t, testIssuer, testClient, r.now, EventConsentRevoked, testSub)
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 500 {
		t.Fatalf("code %d, want 500", w.Code)
	}
	r.st.failDrop = false
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 || len(r.st.tokenDrop) != 1 {
		t.Fatalf("retry: code %d drops %d", w.Code, len(r.st.tokenDrop))
	}
}

func assertNoAppleIdentifiers(t *testing.T, logs string) {
	t.Helper()
	for _, bad := range []string{testSub, "secretsub", testRelay, "privaterelay", "me@example.com"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("log line carries %q: %s", bad, logs)
		}
	}
}
