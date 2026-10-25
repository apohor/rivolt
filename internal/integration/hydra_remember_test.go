//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/hydra"
)

// hydraImage is the Hydra the test runs against: the version prod and preview run (docker.io/oryd/hydra). Bump it with the Ory
// chart appVersion; the pre-pull step in build.yml names the same tag.
const hydraImage = "oryd/hydra:v26.2.0@sha256:ff67c7fb5f95074fa53374d41151713554960504b340cd3f95b09e65deaea2a9"

const (
	hydraClientID    = "rivolt-test"
	hydraRedirectURI = "http://127.0.0.1:1/callback"
	hydraRemember    = 24 * 60 * 60
)

// freePort asks the kernel for a port nobody holds. Hydra puts its public URL into every redirect, so the container has to be
// reachable on the same port it is told its issuer is; a mapped random port would not match.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

// startHydra runs a real Hydra (in-memory store, dev mode so plain http) and returns its public and admin base URLs.
func startHydra(ctx context.Context, t *testing.T) (public, admin string) {
	t.Helper()
	pubPort, admPort := freePort(t), freePort(t)
	public, admin = "http://127.0.0.1:"+pubPort, "http://127.0.0.1:"+admPort
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        hydraImage,
			Cmd:          []string{"serve", "all", "--dev"},
			ExposedPorts: []string{"4444/tcp", "4445/tcp"},
			Env: map[string]string{
				"DSN":                       "memory",
				"URLS_SELF_ISSUER":          public,
				"URLS_LOGIN":                "http://127.0.0.1:1/login",
				"URLS_CONSENT":              "http://127.0.0.1:1/consent",
				"SECRETS_SYSTEM":            "rivolt-test-secret-0123456789abcdef",
				"LOG_LEAK_SENSITIVE_VALUES": "false",
			},
			// Pin the host ports: Hydra puts its issuer URL into every redirect, so it must be reachable on the port it is told.
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.PortBindings = network.PortMap{
					network.MustParsePort("4444/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: pubPort}},
					network.MustParsePort("4445/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: admPort}},
				}
			},
			WaitingFor: wait.ForHTTP("/health/ready").WithPort("4445/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start hydra: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	return public, admin
}

// hydraBrowser is one browser: its own cookie jar, and no automatic redirects, so each hop Hydra answers can be read.
type hydraBrowser struct {
	t      *testing.T
	public string
	hc     *http.Client
}

func newHydraBrowser(t *testing.T, public string) *hydraBrowser {
	jar, _ := cookiejar.New(nil)
	return &hydraBrowser{t: t, public: public, hc: &http.Client{
		Jar:           jar,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// hop GETs a URL and returns where Hydra redirects next.
func (b *hydraBrowser) hop(ctx context.Context, target string) *url.URL {
	b.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	resp, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatalf("GET %s: %v", target, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("GET %s: status %d, want a redirect", target, resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		b.t.Fatalf("GET %s: no Location: %v", target, err)
	}
	return loc
}

// authorize starts an OAuth2 authorization request, as the app or Argo would, and returns the login challenge.
func (b *hydraBrowser) authorize(ctx context.Context) string {
	b.t.Helper()
	q := url.Values{"client_id": {hydraClientID}, "response_type": {"code"}, "scope": {"openid"},
		"redirect_uri": {hydraRedirectURI}, "state": {"state-0123456789"}}
	loc := b.hop(ctx, b.public+"/oauth2/auth?"+q.Encode())
	ch := loc.Query().Get("login_challenge")
	if ch == "" {
		b.t.Fatalf("authorize redirected to %s, want a login challenge", loc)
	}
	return ch
}

// signIn is a full sign-in with "remember" on, the way the Rivolt login bridge does it: accept login, follow to consent, accept
// consent. It returns once the browser has been sent back to the client with a code.
func (b *hydraBrowser) signIn(ctx context.Context, h *hydra.Client, subject string) {
	b.t.Helper()
	ch := b.authorize(ctx)
	r, err := h.AcceptLoginRequest(ctx, ch, hydra.AcceptLoginRequest{Subject: subject, Remember: true, RememberFor: hydraRemember})
	if err != nil {
		b.t.Fatalf("accept login: %v", err)
	}
	b.finishConsent(ctx, h, r.RedirectTo, true)
}

// finishConsent follows the login verifier redirect to the consent challenge, accepts it, and follows on to the client.
func (b *hydraBrowser) finishConsent(ctx context.Context, h *hydra.Client, verifierURL string, remember bool) {
	b.t.Helper()
	cc := b.hop(ctx, verifierURL).Query().Get("consent_challenge")
	if cc == "" {
		b.t.Fatal("login verifier did not lead to a consent challenge")
	}
	r, err := h.AcceptConsentRequest(ctx, cc, hydra.AcceptConsentRequest{GrantScope: []string{"openid"}, Remember: remember, RememberFor: hydraRemember})
	if err != nil {
		b.t.Fatalf("accept consent: %v", err)
	}
	if end := b.hop(ctx, r.RedirectTo); end.Query().Get("code") == "" {
		b.t.Fatalf("consent verifier ended at %s, want a code", end)
	}
}

// nextLogin starts a new authorization request from this browser and reports what Hydra says about the login: skip=true means it
// would sign the browser in with no password.
func (b *hydraBrowser) nextLogin(ctx context.Context, h *hydra.Client) *hydra.LoginRequest {
	b.t.Helper()
	lr, err := h.GetLoginRequest(ctx, b.authorize(ctx))
	if err != nil {
		b.t.Fatalf("get login request: %v", err)
	}
	return lr
}

func createHydraClient(ctx context.Context, t *testing.T, admin string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"client_id": hydraClientID, "grant_types": []string{"authorization_code"}, "response_types": []string{"code"},
		"scope": "openid", "redirect_uris": []string{hydraRedirectURI}, "token_endpoint_auth_method": "none",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, admin+"/admin/clients", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create client: status %d", resp.StatusCode)
	}
}

// TestHydraRevokeForgetsRememberedLogin runs the live-Hydra bug (a signed-out browser was signed straight back in, skip=true,
// because Hydra remembers the login for 24 h) against a real Hydra. The first assertion is the control: without a revoke Hydra
// does skip, so the test can tell "forgotten" from "never remembered". Then the revoke calls the logout and secure-account
// paths make (hydra.Client.RevokeLoginSessions / RevokeConsentSessions), and the next login must be a fresh one.
func TestHydraRevokeForgetsRememberedLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	public, admin := startHydra(ctx, t)
	createHydraClient(ctx, t, admin)
	h, err := hydra.New(hydra.Config{AdminURL: admin})
	if err != nil {
		t.Fatal(err)
	}
	const alice, bob = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

	aliceBrowser, aliceLaptop, bobBrowser := newHydraBrowser(t, public), newHydraBrowser(t, public), newHydraBrowser(t, public)
	aliceBrowser.signIn(ctx, h, alice)
	aliceLaptop.signIn(ctx, h, alice)
	bobBrowser.signIn(ctx, h, bob)

	t.Run("control: a remembered browser is skipped", func(t *testing.T) {
		for name, b := range map[string]*hydraBrowser{"alice": aliceBrowser, "bob": bobBrowser} {
			if lr := b.nextLogin(ctx, h); !lr.Skip {
				t.Fatalf("%s: skip=false before any revoke; the test would pass without the fix", name)
			}
		}
	})

	t.Run("revoking the login sessions forces a fresh login", func(t *testing.T) {
		if err := h.RevokeLoginSessions(ctx, alice); err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string]*hydraBrowser{"the signed-out browser": aliceBrowser, "the other device": aliceLaptop} {
			if lr := b.nextLogin(ctx, h); lr.Skip {
				t.Fatalf("%s: skip=true after RevokeLoginSessions; it would be signed in with no password", name)
			}
		}
	})

	t.Run("another user is not affected", func(t *testing.T) {
		if lr := bobBrowser.nextLogin(ctx, h); !lr.Skip {
			t.Fatal("bob's remembered login was forgotten by revoking alice's")
		}
	})

	t.Run("revoking twice, or an unknown subject, is not an error", func(t *testing.T) {
		if err := h.RevokeLoginSessions(ctx, alice); err != nil {
			t.Fatalf("second revoke: %v", err)
		}
		if err := h.RevokeLoginSessions(ctx, "33333333-3333-3333-3333-333333333333"); err != nil {
			t.Fatalf("unknown subject: %v", err)
		}
	})

	t.Run("revoking consent forces a fresh consent", func(t *testing.T) {
		// bob is still remembered for login; his consent is remembered too, so the consent challenge is skipped until revoked.
		consentSkip := func() bool {
			lr := bobBrowser.nextLogin(ctx, h)
			r, err := h.AcceptLoginRequest(ctx, lr.Challenge, hydra.AcceptLoginRequest{Subject: bob, Remember: true, RememberFor: hydraRemember})
			if err != nil {
				t.Fatalf("accept login: %v", err)
			}
			cc := bobBrowser.hop(ctx, r.RedirectTo).Query().Get("consent_challenge")
			cr, err := h.GetConsentRequest(ctx, cc)
			if err != nil {
				t.Fatalf("get consent: %v", err)
			}
			if !cr.Skip {
				// Finish the flow so the browser is left in a clean state.
				bobBrowser.finishConsentChallenge(ctx, h, cc)
			}
			return cr.Skip
		}
		if !consentSkip() {
			t.Fatal("control: consent not skipped before the revoke")
		}
		if err := h.RevokeConsentSessions(ctx, bob); err != nil {
			t.Fatal(err)
		}
		if consentSkip() {
			t.Fatal("consent skipped after RevokeConsentSessions")
		}
	})
}

// finishConsentChallenge accepts an already-fetched consent challenge and follows the redirect to the client.
func (b *hydraBrowser) finishConsentChallenge(ctx context.Context, h *hydra.Client, cc string) {
	b.t.Helper()
	r, err := h.AcceptConsentRequest(ctx, cc, hydra.AcceptConsentRequest{GrantScope: []string{"openid"}, Remember: true, RememberFor: hydraRemember})
	if err != nil {
		b.t.Fatalf("accept consent: %v", err)
	}
	b.hop(ctx, r.RedirectTo)
}
