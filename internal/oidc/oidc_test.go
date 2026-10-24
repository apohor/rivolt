package oidc

import (
	"reflect"
	"strings"
	"testing"
)

// resolveIdentity is the only piece of identity-mapping logic
// that's pure and testable without a real IdP. The rules it
// encodes (verified email > preferred_username > unverified email
// > iss+sub) are load-bearing — they decide whether a user
// signing in by Google and a user signing in by password with the
// same email land on the same UUIDv5. Tightly pin the table.
func TestResolveIdentityKey(t *testing.T) {
	const iss = "https://idp.example.com"
	synth := strings.ToLower(iss + ":abc-123")
	cases := []struct {
		name      string
		sub       string
		email     string
		verified  bool
		preferred string
		trust     bool // RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL
		bareOK    bool // single provider, or this one opted in

		wantUsername   string
		wantEmail      string
		wantCandidates []string
	}{
		// 1. verified email
		{name: "verified email wins", sub: "abc-123", email: "Alice@Example.com", verified: true, preferred: "alice-rivian", bareOK: true,
			wantUsername: "alice@example.com", wantEmail: "alice@example.com"},
		{name: "verified email beats an email-shaped preferred", sub: "abc-123", email: "me@example.com", verified: true, preferred: "victim@example.com",
			wantUsername: "me@example.com", wantEmail: "me@example.com"},
		{name: "whitespace and case are normalised", sub: " ", email: "  ALICE@EXAMPLE.COM  ", verified: true,
			wantUsername: "alice@example.com", wantEmail: "alice@example.com"},
		// 2. bare preferred_username, only for IdPs that sent no email
		{name: "no email, bare preferred, allowed: keys on it", sub: "abc-123", preferred: "Alice", bareOK: true,
			wantUsername: "alice"},
		{name: "no email, bare preferred, multi-IdP without opt-in: candidate, iss:sub", sub: "abc-123", preferred: "Alice",
			wantUsername: synth, wantCandidates: []string{"alice"}},
		{name: "email present: a bare preferred (Hydra local part, login, display name) never keys", sub: "abc-123",
			email: "alex@a.com", preferred: "alex", bareOK: true,
			wantUsername: synth, wantEmail: "alex@a.com", wantCandidates: []string{"alex", "alex@a.com"}},
		// 3. unverified candidates
		{name: "unverified email only: iss:sub, candidate flagged", sub: "abc-123", email: "Alice@Example.com", bareOK: true,
			wantUsername: synth, wantEmail: "alice@example.com", wantCandidates: []string{"alice@example.com"}},
		{name: "email-shaped preferred is not a key", sub: "abc-123", preferred: "Victim@Example.com", bareOK: true,
			wantUsername: synth, wantCandidates: []string{"victim@example.com"}},
		{name: "email-shaped preferred and an unverified email: both flagged", sub: "abc-123", email: "me@example.com", preferred: "victim@example.com",
			wantUsername: synth, wantEmail: "me@example.com", wantCandidates: []string{"victim@example.com", "me@example.com"}},
		// trusted (RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL): today's keying
		{name: "trusted: unverified email keys", sub: "abc-123", email: "Alice@Example.com", trust: true, bareOK: true,
			wantUsername: "alice@example.com", wantEmail: "alice@example.com"},
		{name: "trusted: bare preferred beats unverified email (old order)", sub: "abc-123", email: "alice@example.com", preferred: "alice-r", trust: true, bareOK: true,
			wantUsername: "alice-r", wantEmail: "alice@example.com"},
		{name: "trusted: email-shaped preferred keys", sub: "abc-123", preferred: "victim@example.com", trust: true,
			wantUsername: "victim@example.com"},
		{name: "trusted but bare name not allowed (multi-IdP): falls to the email", sub: "abc-123", email: "a@example.com", preferred: "alice", trust: true,
			wantUsername: "a@example.com", wantEmail: "a@example.com"},
		// 4. floor
		{name: "no email, no preferred: iss:sub, nothing flagged", sub: "abc-123", wantUsername: synth},
		{name: "nothing at all: empty", wantUsername: ""},
		{name: "no sub with an unverified email: no key, candidate flagged", email: "a@example.com",
			wantUsername: "", wantEmail: "a@example.com", wantCandidates: []string{"a@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveIdentityKey(iss, tc.sub, tc.email, tc.verified, tc.preferred, tc.trust, tc.bareOK)
			if got.Username != tc.wantUsername || got.Email != tc.wantEmail || !reflect.DeepEqual(got.UnverifiedCandidates, tc.wantCandidates) {
				t.Fatalf("got %+v, want {%q %q %v}", got, tc.wantUsername, tc.wantEmail, tc.wantCandidates)
			}
		})
	}
}

// PKCE: encoding is fixed by RFC 7636 §4.2 to BASE64URL(SHA256(v)),
// no padding. The IdP rejects challenges that don't match,
// silently — so this is one of those "wrong by one byte and login
// just stops working with no useful error" cases. Pin it.
func TestPKCEChallenge(t *testing.T) {
	// Test vector from RFC 7636 appendix B.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	got := pkceChallenge(verifier)
	if got != want {
		t.Fatalf("pkceChallenge: want %q got %q", want, got)
	}
}

// State cookie roundtrip: any tampering of the cookie value
// must produce a decode error so the callback rejects it.
func TestStateRoundtrip(t *testing.T) {
	in := flowState{
		Provider: "google",
		State:    "abc123",
		Nonce:    "abc123",
		Verifier: "verifier-here",
		Return:   "/dashboard",
	}
	enc, err := encodeState(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodeState(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: want %+v got %+v", in, out)
	}
	// Truncated cookie should fail to decode.
	if _, err := decodeState(enc[:len(enc)-4]); err == nil {
		t.Fatal("decode should fail on truncation")
	}
	// Garbage cookie should fail to decode.
	if _, err := decodeState("not-base64!!"); err == nil {
		t.Fatal("decode should fail on garbage")
	}
}

// ParseProvidersFromEnv: the env-soup parser is the operator-
// facing surface. Misreading this wrong-foots a deploy with no
// useful error, so test the happy path and the validation cases.
func TestParseProvidersFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	t.Run("empty list disables OIDC", func(t *testing.T) {
		got, err := ParseProvidersFromEnv(env(nil), "https://example.com")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected zero providers, got %d", len(got))
		}
	})
	t.Run("happy path builds redirect URL", func(t *testing.T) {
		m := map[string]string{
			"RIVOLT_OIDC_PROVIDERS":            "google",
			"RIVOLT_OIDC_GOOGLE_ISSUER":        "https://accounts.google.com",
			"RIVOLT_OIDC_GOOGLE_CLIENT_ID":     "id",
			"RIVOLT_OIDC_GOOGLE_CLIENT_SECRET": "secret",
			"RIVOLT_OIDC_GOOGLE_DISPLAY_NAME":  "Sign in with Google",
		}
		got, err := ParseProvidersFromEnv(env(m), "https://rivolt.example.com/")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(got))
		}
		want := "https://rivolt.example.com/api/auth/oidc/google/callback"
		if got[0].RedirectURL != want {
			t.Fatalf("redirect: want %q got %q", want, got[0].RedirectURL)
		}
		if got[0].DisplayName != "Sign in with Google" {
			t.Fatalf("display: %q", got[0].DisplayName)
		}
	})
	t.Run("missing base URL is a deploy-time error", func(t *testing.T) {
		m := map[string]string{"RIVOLT_OIDC_PROVIDERS": "google"}
		if _, err := ParseProvidersFromEnv(env(m), ""); err == nil {
			t.Fatal("expected error for missing RIVOLT_BASE_URL")
		}
	})
	t.Run("missing per-provider env is an error", func(t *testing.T) {
		m := map[string]string{"RIVOLT_OIDC_PROVIDERS": "google"}
		_, err := ParseProvidersFromEnv(env(m), "https://x")
		if err == nil || !strings.Contains(err.Error(), "ISSUER") {
			t.Fatalf("expected ISSUER error, got %v", err)
		}
	})
	t.Run("duplicate provider name is an error", func(t *testing.T) {
		m := map[string]string{
			"RIVOLT_OIDC_PROVIDERS":        "google,google",
			"RIVOLT_OIDC_GOOGLE_ISSUER":    "https://x",
			"RIVOLT_OIDC_GOOGLE_CLIENT_ID": "id",
		}
		_, err := ParseProvidersFromEnv(env(m), "https://x")
		if err == nil || !strings.Contains(err.Error(), "twice") {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})
	t.Run("scopes are parsed", func(t *testing.T) {
		m := map[string]string{
			"RIVOLT_OIDC_PROVIDERS":   "x",
			"RIVOLT_OIDC_X_ISSUER":    "https://x",
			"RIVOLT_OIDC_X_CLIENT_ID": "id",
			"RIVOLT_OIDC_X_SCOPES":    "openid, email , profile, groups ",
		}
		got, err := ParseProvidersFromEnv(env(m), "https://x")
		if err != nil {
			t.Fatal(err)
		}
		if len(got[0].Scopes) != 4 || got[0].Scopes[3] != "groups" {
			t.Fatalf("scopes: %v", got[0].Scopes)
		}
	})
}

func TestUsernameKeyOptInAndProviderCount(t *testing.T) {
	env := map[string]string{
		"RIVOLT_OIDC_PROVIDERS":         "google,corp",
		"RIVOLT_BASE_URL":               "https://rivolt.example.com",
		"RIVOLT_OIDC_GOOGLE_ISSUER":     "https://accounts.google.com",
		"RIVOLT_OIDC_GOOGLE_CLIENT_ID":  "g",
		"RIVOLT_OIDC_CORP_ISSUER":       "https://sso.corp.example.com",
		"RIVOLT_OIDC_CORP_CLIENT_ID":    "c",
		"RIVOLT_OIDC_CORP_USERNAME_KEY": "true",
	}
	provs, err := ParseProvidersFromEnv(func(k string) string { return env[k] }, env["RIVOLT_BASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 2 || provs[0].UsernameKey || !provs[1].UsernameKey {
		t.Fatalf("UsernameKey parse: %+v", provs)
	}
	multi := &Service{providerCount: 2}
	if multi.bareUsernameKeyOK(provs[0]) {
		t.Error("multi-provider install: a provider without the opt-in must not key on a bare username")
	}
	if !multi.bareUsernameKeyOK(provs[1]) {
		t.Error("multi-provider install: an opted-in provider may key on a bare username")
	}
	single := &Service{providerCount: 1}
	if !single.bareUsernameKeyOK(provs[0]) {
		t.Error("single provider keeps today's behaviour")
	}
}

// First-party Hydra derives preferred_username from the email local
// part. With RIVOLT_TRUST_FIRST_PARTY_EMAILS=false its email is
// unverified; two people whose addresses share a local part must not
// land on one account, on a single-provider install either.
func TestResolveIdentityKey_HydraLocalPartTwinsGetSeparateAccounts(t *testing.T) {
	const iss = "https://auth.example.com"
	a := resolveIdentityKey(iss, "sub-a", "alex@a.com", false, "alex", false, true)
	b := resolveIdentityKey(iss, "sub-b", "alex@b.com", false, "alex", false, true)
	for _, k := range []identityKey{a, b} {
		if k.Username == "alex" || strings.Contains(k.Username, "@") || !strings.HasPrefix(k.Username, iss+":") {
			t.Fatalf("must key on iss:sub, got %q", k.Username)
		}
	}
	if a.Username == b.Username {
		t.Fatalf("local-part twins share a key: %q", a.Username)
	}
}

// The sign-in buttons keep one order (Email, Google, GitHub, then others by name), however the registry map happens to iterate. The Email
// button's registry name is "hydra" (display name "Email"): qa found the first version ranked it last because it only knew "email".
func TestProvidersAreInAFixedOrder(t *testing.T) {
	names := func(l []ProviderListing) string {
		var out []string
		for _, p := range l {
			out = append(out, p.Name)
		}
		return strings.Join(out, ",")
	}
	real := []ProviderListing{
		{Name: "google", DisplayName: "Google"}, {Name: "github", DisplayName: "GitHub"}, {Name: "hydra", DisplayName: "Email"},
	}
	for i := 0; i < 30; i++ {
		l := append([]ProviderListing(nil), real...)
		k := i % len(l)
		l = append(l[k:], l[:k]...)
		sortProviderListings(l)
		if got := names(l); got != "hydra,google,github" {
			t.Fatalf("real names: order %q", got)
		}
	}
	// Other spellings of the same buttons, and an unknown provider, which goes last by name.
	l := []ProviderListing{{Name: "zeta", DisplayName: "Zeta"}, {Name: "github", DisplayName: "GitHub"}, {Name: "email", DisplayName: "Email"}, {Name: "alpha", DisplayName: "Alpha"}, {Name: "x", DisplayName: "Google"}}
	sortProviderListings(l)
	if got := names(l); got != "email,x,github,alpha,zeta" {
		t.Fatalf("order %q", got)
	}
	// The registry itself lists them in that order on every call.
	s := &Service{providers: map[string]*provider{
		"github": {cfg: ProviderConfig{Name: "github", DisplayName: "GitHub"}},
		"google": {cfg: ProviderConfig{Name: "google", DisplayName: "Google"}},
		"hydra":  {cfg: ProviderConfig{Name: "hydra", DisplayName: "Email"}},
	}}
	for i := 0; i < 50; i++ {
		if got := names(s.Providers()); got != "hydra,google,github" {
			t.Fatalf("Providers() order %q on call %d", got, i)
		}
	}
}
