//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
)

// kratosImage is the Kratos the test runs against: the version prod and preview run, by digest. Bump it with the Ory chart; the
// pre-pull step in build.yml names the same image.
const kratosImage = "oryd/kratos:v26.2.0@sha256:2a13bb8d362c7a7ae33bd7c0f5168aee46921f15c916a06346db91c06dc76643"

// kratosIdentitySchema is prod's identity schema (apps/platform/ory/kratos/values.yaml): email is the password identifier.
const kratosIdentitySchema = `{"$id":"https://rivolt.dev/schemas/user.json","$schema":"http://json-schema.org/draft-07/schema#","title":"Rivolt User","type":"object","properties":{"traits":{"type":"object","properties":{"email":{"type":"string","format":"email","title":"E-Mail","ory.sh/kratos":{"credentials":{"password":{"identifier":true}}}},"display_name":{"type":"string","title":"Display Name"}},"required":["email"],"additionalProperties":false}}}`

// startKratos runs a real Kratos (in-memory store, dev mode) with prod's schema and password method, and returns its public and
// admin base URLs.
func startKratos(ctx context.Context, t *testing.T) (public, admin string) {
	t.Helper()
	pubPort, admPort := freePort(t), freePort(t)
	public, admin = "http://127.0.0.1:"+pubPort, "http://127.0.0.1:"+admPort
	cfg := `
dsn: memory
serve:
  public: {base_url: ` + public + `/, host: 0.0.0.0, port: 4433}
  admin: {base_url: ` + admin + `/, host: 0.0.0.0, port: 4434}
selfservice:
  default_browser_return_url: ` + public + `/
  flows:
    login: {ui_url: ` + public + `/login}
    registration: {enabled: false}
  methods:
    password: {enabled: true, config: {haveibeenpwned_enabled: false}}
identity:
  default_schema_id: rivolt_user
  schemas:
    - {id: rivolt_user, url: "file:///etc/kratos/user.json"}
hashers:
  algorithm: bcrypt
  bcrypt: {cost: 4}
secrets:
  default: ["rivolt-test-secret-0123456789abcdef"]
  cookie: ["rivolt-test-cookie-0123456789abcdef"]
courier:
  smtp: {connection_uri: "smtp://127.0.0.1:1/?disable_starttls=true"}
`
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        kratosImage,
			Cmd:          []string{"serve", "--dev", "--watch-courier=false", "-c", "/etc/kratos/kratos.yml"},
			ExposedPorts: []string{"4433/tcp", "4434/tcp"},
			Files: []testcontainers.ContainerFile{
				{Reader: bytes.NewReader([]byte(cfg)), ContainerFilePath: "/etc/kratos/kratos.yml", FileMode: 0o644},
				{Reader: bytes.NewReader([]byte(kratosIdentitySchema)), ContainerFilePath: "/etc/kratos/user.json", FileMode: 0o644},
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.PortBindings = network.PortMap{
					network.MustParsePort("4433/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: pubPort}},
					network.MustParsePort("4434/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: admPort}},
				}
			},
			WaitingFor: wait.ForHTTP("/health/ready").WithPort("4434/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start kratos: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	return public, admin
}

// kratosSessionToken signs in through Kratos's API flow and returns the session token, the way the browser flow's cookie would be
// minted. (kratos.Client.LoginByPassword drops the token; the test needs it to see whether the session is still alive.)
func kratosSessionToken(ctx context.Context, t *testing.T, public, email, password string) string {
	t.Helper()
	get := func(method, url string, body any, header map[string]string) (int, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequestWithContext(ctx, method, url, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	_, flow := get(http.MethodGet, public+"/self-service/login/api", nil, nil)
	id, _ := flow["id"].(string)
	if id == "" {
		t.Fatalf("no login flow: %v", flow)
	}
	code, out := get(http.MethodPost, public+"/self-service/login?flow="+id,
		map[string]any{"method": "password", "identifier": email, "password": password}, nil)
	tok, _ := out["session_token"].(string)
	if code != http.StatusOK || tok == "" {
		t.Fatalf("login: %d %v", code, out)
	}
	return tok
}

func kratosWhoami(ctx context.Context, t *testing.T, public, token string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, public+"/sessions/whoami", nil)
	req.Header.Set("X-Session-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The Kratos half of sign-out and "Secure my account", against a real Kratos and a real Hydra (the Hydra half is
// TestHydraRevokeForgetsRememberedLogin): IdentityRevoker.ForgetLogin ends the identity's Kratos sessions and forgets Hydra's
// remembered login for the same identity id, another identity is untouched, and SetPassword changes the credential for real.
func TestIdentityRevokerAgainstRealKratosAndHydra(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	kPublic, kAdmin := startKratos(ctx, t)
	hPublic, hAdmin := startHydra(ctx, t)
	createHydraClient(ctx, t, hAdmin)
	kc, err := kratos.New(kratos.Config{AdminURL: kAdmin, PublicURL: kPublic})
	if err != nil {
		t.Fatal(err)
	}
	hc, err := hydra.New(hydra.Config{AdminURL: hAdmin})
	if err != nil {
		t.Fatal(err)
	}
	const alice, bob, pw = "alice@example.test", "bob@example.test", "correct-horse-battery-1"
	for _, e := range []string{alice, bob} {
		if err := kc.CreateIdentity(ctx, e, "", "user", pw); err != nil {
			t.Fatalf("create %s: %v", e, err)
		}
	}
	aliceID, err := kc.IdentityIDByEmail(ctx, alice)
	if err != nil || aliceID == "" {
		t.Fatalf("alice id: %q %v", aliceID, err)
	}
	bobID, _ := kc.IdentityIDByEmail(ctx, bob)

	// Two Kratos sessions for alice (two browsers), one for bob; Hydra remembers alice and bob, as the login bridge does with the
	// Kratos identity id as the subject.
	a1, a2, b1 := kratosSessionToken(ctx, t, kPublic, alice, pw), kratosSessionToken(ctx, t, kPublic, alice, pw), kratosSessionToken(ctx, t, kPublic, bob, pw)
	aliceBrowser, bobBrowser := newHydraBrowser(t, hPublic), newHydraBrowser(t, hPublic)
	aliceBrowser.signIn(ctx, hc, aliceID)
	bobBrowser.signIn(ctx, hc, bobID)
	for name, tok := range map[string]string{"alice 1": a1, "alice 2": a2, "bob": b1} {
		if c := kratosWhoami(ctx, t, kPublic, tok); c != http.StatusOK {
			t.Fatalf("control: %s whoami = %d before any revoke", name, c)
		}
	}
	if !aliceBrowser.nextLogin(ctx, hc).Skip {
		t.Fatal("control: alice's browser is not remembered by Hydra before the revoke")
	}

	revoker := &api.IdentityRevoker{Hydra: hc, Kratos: kc,
		Username: func(context.Context, uuid.UUID) (string, error) { return alice, nil }}
	if err := revoker.ForgetLogin(ctx, uuid.New()); err != nil {
		t.Fatalf("ForgetLogin: %v", err)
	}
	for name, tok := range map[string]string{"alice 1": a1, "alice 2": a2} {
		if c := kratosWhoami(ctx, t, kPublic, tok); c != http.StatusUnauthorized {
			t.Errorf("%s: whoami = %d after ForgetLogin, want 401 (the Kratos session must be gone)", name, c)
		}
	}
	if aliceBrowser.nextLogin(ctx, hc).Skip {
		t.Error("alice's browser is still remembered by Hydra after ForgetLogin")
	}
	if c := kratosWhoami(ctx, t, kPublic, b1); c != http.StatusOK {
		t.Errorf("bob's Kratos session = %d after alice's revoke, want 200", c)
	}
	if !bobBrowser.nextLogin(ctx, hc).Skip {
		t.Error("bob's remembered login was forgotten by revoking alice's")
	}

	// Plain sign-out (POST /api/auth/logout) with the real revoker: alice signs in again (a new Kratos session, remembered by Hydra),
	// then signs out. Her Kratos sessions are gone, Hydra no longer skips her login, and the response expires the Kratos cookie.
	a3 := kratosSessionToken(ctx, t, kPublic, alice, pw)
	aliceBrowser.signIn(ctx, hc, aliceID)
	if kratosWhoami(ctx, t, kPublic, a3) != http.StatusOK || !aliceBrowser.nextLogin(ctx, hc).Skip {
		t.Fatal("control: alice's new session is not live and remembered before the sign-out")
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "a-browser-cookie"})
	rec := httptest.NewRecorder()
	// The real router; the wrapper stands in for the session middleware by putting the signed-in user on the context.
	router := api.New(api.Deps{Auth: svc, LoginForgetter: revoker})
	http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uuid.New())))
	}).ServeHTTP(rec, logoutReq)
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout = %d, want 204", rec.Code)
	}
	if c := kratosWhoami(ctx, t, kPublic, a3); c != http.StatusUnauthorized {
		t.Errorf("after logout: alice's Kratos session = %d, want 401", c)
	}
	if aliceBrowser.nextLogin(ctx, hc).Skip {
		t.Error("after logout: Hydra still skips alice's login")
	}
	expired := false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "ory_kratos_session" && ck.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Error("logout did not expire the ory_kratos_session cookie")
	}

	// SetPassword is real: the old password stops working, the new one works, and Kratos' own policy is not what refuses a short one
	// (the admin API skips it), our client is.
	const newPw = "a-different-horse-battery-2"
	if err := kc.SetPassword(ctx, aliceID, newPw); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err := kc.LoginByPassword(ctx, alice, pw); !errors.Is(err, kratos.ErrInvalidCredentials) {
		t.Errorf("old password after SetPassword: %v, want ErrInvalidCredentials", err)
	}
	if _, err := kc.LoginByPassword(ctx, alice, newPw); err != nil {
		t.Errorf("new password after SetPassword: %v", err)
	}
	var rej *kratos.RejectedError
	if err := kc.SetPassword(ctx, aliceID, "short1"); !errors.As(err, &rej) {
		t.Errorf("a 6-character password: %v, want a RejectedError", err)
	}
	if _, err := kc.LoginByPassword(ctx, alice, newPw); err != nil {
		t.Errorf("a refused password change must leave the current one working: %v", err)
	}
}
