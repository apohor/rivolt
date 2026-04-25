package oidc

import (
	"strings"
	"testing"
)

// resolveIdentity is the only piece of identity-mapping logic
// that's pure and testable without a real IdP. The rules it
// encodes (verified email > preferred_username > unverified email
// > iss+sub) are load-bearing — they decide whether a user
// signing in by Google and a user signing in by password with the
// same email land on the same UUIDv5. Tightly pin the table.
func TestResolveIdentity(t *testing.T) {
	const iss = "https://accounts.google.com"
	cases := []struct {
		name         string
		sub          string
		email        string
		verified     bool
		preferred    string
		wantUsername string
		wantEmail    string
	}{
		{
			name:         "verified email wins",
			sub:          "abc",
			email:        "Alice@Example.com",
			verified:     true,
			preferred:    "alice-rivian",
			wantUsername: "alice@example.com",
			wantEmail:    "alice@example.com",
		},
		{
			name:         "unverified email loses to preferred_username",
			sub:          "abc",
			email:        "Alice@Example.com",
			verified:     false,
			preferred:    "Alice-Rivian",
			wantUsername: "alice-rivian",
			wantEmail:    "alice@example.com",
		},
		{
			name:         "unverified email is the floor when no preferred",
			sub:          "abc",
			email:        "Alice@Example.com",
			verified:     false,
			preferred:    "",
			wantUsername: "alice@example.com",
			wantEmail:    "alice@example.com",
		},
		{
			name:         "iss+sub fallback when nothing else",
			sub:          "abc-123",
			email:        "",
			verified:     false,
			preferred:    "",
			wantUsername: strings.ToLower(iss + ":abc-123"),
			wantEmail:    "",
		},
		{
			name:         "no sub, no email, no preferred → empty",
			sub:          "",
			email:        "",
			verified:     false,
			preferred:    "",
			wantUsername: "",
			wantEmail:    "",
		},
		{
			name:         "whitespace and case are normalised",
			sub:          " ",
			email:        "  ALICE@EXAMPLE.COM  ",
			verified:     true,
			preferred:    "",
			wantUsername: "alice@example.com",
			wantEmail:    "alice@example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotU, gotE := resolveIdentity(iss, tc.sub, tc.email, tc.verified, tc.preferred)
			if gotU != tc.wantUsername {
				t.Fatalf("username: want %q got %q", tc.wantUsername, gotU)
			}
			if gotE != tc.wantEmail {
				t.Fatalf("email: want %q got %q", tc.wantEmail, gotE)
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
			"RIVOLT_OIDC_PROVIDERS":           "google",
			"RIVOLT_OIDC_GOOGLE_ISSUER":       "https://accounts.google.com",
			"RIVOLT_OIDC_GOOGLE_CLIENT_ID":    "id",
			"RIVOLT_OIDC_GOOGLE_CLIENT_SECRET": "secret",
			"RIVOLT_OIDC_GOOGLE_DISPLAY_NAME": "Sign in with Google",
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
			"RIVOLT_OIDC_PROVIDERS":            "google,google",
			"RIVOLT_OIDC_GOOGLE_ISSUER":        "https://x",
			"RIVOLT_OIDC_GOOGLE_CLIENT_ID":     "id",
		}
		_, err := ParseProvidersFromEnv(env(m), "https://x")
		if err == nil || !strings.Contains(err.Error(), "twice") {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})
	t.Run("scopes are parsed", func(t *testing.T) {
		m := map[string]string{
			"RIVOLT_OIDC_PROVIDERS":         "x",
			"RIVOLT_OIDC_X_ISSUER":          "https://x",
			"RIVOLT_OIDC_X_CLIENT_ID":       "id",
			"RIVOLT_OIDC_X_SCOPES":          "openid, email , profile, groups ",
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
