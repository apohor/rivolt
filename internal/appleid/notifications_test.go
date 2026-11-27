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
	users     map[string]uuid.UUID
	seen      map[string]bool
	tokenDrop []uuid.UUID
	undeliv   map[uuid.UUID]bool
	mail      string
	failDrop  bool
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
	r.n = &Notifier{ClientID: testClient, Verify: newVerifier(ks, testIssuer), Store: r.st, Sessions: r.ss, Mail: r.ml,
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
		"wrong issuer": r.sign(t, "https://evil.example", testClient, r.now, EventAccountDelete, testSub),
		"wrong key":    forged.sign(t, testIssuer, testClient, r.now, EventAccountDelete, testSub),
		"not a jwt":    "garbage",
	}
	for name, tok := range cases {
		r.logs.Reset()
		if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 400 {
			t.Errorf("%s: code %d, want 400", name, w.Code)
		}
		if !strings.Contains(r.logs.String(), "apple notification refused") || !strings.Contains(r.logs.String(), "reason=") {
			t.Errorf("%s: a 400 must log its reason, got %q", name, r.logs.String())
		}
	}
	for name, c := range map[string][2]string{"empty body": {"", "application/x-www-form-urlencoded"}, "json without payload": {`{}`, "application/json"}, "bad json": {`nope`, "application/json"}} {
		r.logs.Reset()
		if w := post(r.n, c[0], c[1]); w.Code != 400 || !strings.Contains(r.logs.String(), "reason=") {
			t.Errorf("%s: code %d, logs %q", name, w.Code, r.logs.String())
		}
	}
	if len(r.ss.revoked)+len(r.ml.sent)+len(r.st.seen) != 0 {
		t.Fatal("a refused payload must change nothing, not even the replay table")
	}
}

// A payload Apple signed is always a 200 (Apple retries anything else), but one that is not for this deployment acts on nothing and says why.
func TestValidlySignedButNotActedOnIsA200(t *testing.T) {
	r := newRig(t)
	cases := map[string]string{
		"someone else's audience": r.sign(t, testIssuer, "dev.someone.else", r.now, EventAccountDelete, testSub),
		"stale iat":               r.sign(t, testIssuer, testClient, r.now.Add(-2*time.Hour), EventAccountDelete, testSub),
		"future iat":              r.sign(t, testIssuer, testClient, r.now.Add(time.Hour), EventAccountDelete, testSub),
	}
	for name, tok := range cases {
		r.logs.Reset()
		if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
			t.Errorf("%s: code %d, want 200", name, w.Code)
		}
		if !strings.Contains(r.logs.String(), "ignored") || strings.Contains(r.logs.String(), testSub) {
			t.Errorf("%s: want an ignored line without the sub, got %q", name, r.logs.String())
		}
	}
	if len(r.ss.revoked)+len(r.ml.sent)+len(r.st.seen) != 0 {
		t.Fatal("an ignored payload must change nothing")
	}
}

// The shape Apple's server sends: a JSON body {"payload": jwt} with a charset on the content type; `events` a JSON string holding the type, the
// team-scoped sub, an event_time in milliseconds and (for the email events) is_private_email as the string "true". A user we no longer hold
// (deleted in Rivolt a few seconds earlier, which is what makes Apple send consent-revoked) is a 200 with nothing logged.
func TestAppBundleIDAudienceIsActedOn(t *testing.T) {
	r := newRig(t)
	r.n.AppAudiences = []string{"dev.rivolt.app"}
	tok := r.sign(t, testIssuer, "dev.rivolt.app", r.now, EventAccountDelete, testSub)
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if len(r.ss.revoked) != 1 {
		t.Fatal("an event whose audience is the app's bundle id must be acted on like the Services ID's")
	}
}

func TestAppBundleIDsFromConfig(t *testing.T) {
	env := map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": " dev.rivolt.app ", "RIVOLT_IOS_APP_IDS": "ABCDE12345.dev.rivolt.app, ABCDE12345.dev.rivolt.beta,bad"}
	got := AppBundleIDs(func(k string) string { return env[k] })
	if strings.Join(got, ",") != "dev.rivolt.app,dev.rivolt.beta" {
		t.Fatalf("got %v", got)
	}
	if len(AppBundleIDs(func(string) string { return "" })) != 0 {
		t.Fatal("no config, no audiences")
	}
}

// Prod's configuration: no RIVOLT_APPLE_IAP_BUNDLE_ID, and RIVOLT_IOS_APP_IDS in the app-links form TEAMID.bundle. Apple's audience is the bare bundle id.
func TestProdAppIDsFormMatchesTheBareBundleAudience(t *testing.T) {
	env := map[string]string{"RIVOLT_IOS_APP_IDS": "CK383X39S5.dev.rivolt.app"}
	r := newRig(t)
	r.n.AppAudiences = AppBundleIDs(func(k string) string { return env[k] })
	if strings.Join(r.n.AppAudiences, ",") != "dev.rivolt.app" {
		t.Fatalf("audiences %v", r.n.AppAudiences)
	}
	if w := post(r.n, form(r.sign(t, testIssuer, "dev.rivolt.app", r.now, EventAccountDelete, testSub)), "application/x-www-form-urlencoded"); w.Code != 200 || len(r.ss.revoked) != 1 {
		t.Fatalf("code %d, revoked %d", w.Code, len(r.ss.revoked))
	}
	// The team-prefixed form is not an audience Apple sends.
	r2 := newRig(t)
	r2.n.AppAudiences = r.n.AppAudiences
	post(r2.n, form(r2.sign(t, testIssuer, "CK383X39S5.dev.rivolt.app", r2.now, EventAccountDelete, testSub)), "application/x-www-form-urlencoded")
	if len(r2.ss.revoked) != 0 {
		t.Fatal("the TEAMID.bundle form must not match")
	}
}

func TestEmptyClientIDActsOnNothing(t *testing.T) {
	r := newRig(t)
	r.n.ClientID = ""
	tok := r.sign(t, testIssuer, testClient, r.now, EventAccountDelete, testSub)
	if w := post(r.n, form(tok), "application/x-www-form-urlencoded"); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if len(r.ss.revoked)+len(r.ml.sent)+len(r.st.seen) != 0 || !strings.Contains(r.logs.String(), "ignored") {
		t.Fatalf("must ignore and say so: %q", r.logs.String())
	}
}

func TestRealShapedAppleEventForADeletedUserIs200(t *testing.T) {
	r := newRig(t)
	tok := r.sign(t, testIssuer, testClient, r.now, EventConsentRevoked, "001234.0123456789abcdef0123456789abcdef.0412")
	body, _ := json.Marshal(map[string]string{"payload": tok})
	if w := post(r.n, string(body), "application/json; charset=utf-8"); w.Code != 200 {
		t.Fatalf("code %d, want 200", w.Code)
	}
	if r.logs.Len() != 0 || len(r.ss.revoked) != 0 {
		t.Fatalf("an unknown sub must be silent: %q", r.logs.String())
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
