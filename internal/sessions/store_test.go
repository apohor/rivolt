package sessions

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
)

// Unit tests for the pure, DB-free surface: token encoding,
// peppered hashing, constant-time compare, and IP sanitization.
// The DB-touching methods (Create / Lookup / Revoke / List /
// PurgeExpired) are covered by runtime smoke-testing against the
// live Postgres; adding a testcontainer here would be the
// project's first and drag the whole test story along with it.

func TestEncodeDecodeTokenRoundTrip(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, tokenBytes)
	encoded := encodeToken(raw)
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("token must be base64url-no-pad (cookie-safe), got %q", encoded)
	}
	got, err := decodeToken(encoded)
	if err != nil {
		t.Fatalf("decodeToken: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("round-trip mismatch: %x vs %x", got, raw)
	}
}

func TestDecodeTokenRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"bad_base64", "not_base64_!!!"},
		{"wrong_length_short", encodeToken(bytes.Repeat([]byte{1}, tokenBytes-1))},
		{"wrong_length_long", encodeToken(bytes.Repeat([]byte{1}, tokenBytes+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeToken(tc.in); err == nil {
				t.Fatalf("decodeToken(%q) = nil, want error", tc.in)
			}
		})
	}
}

func TestHashDeterministicAndPeppered(t *testing.T) {
	pepper1 := bytes.Repeat([]byte{0x11}, 32)
	pepper2 := bytes.Repeat([]byte{0x22}, 32)
	s1 := &Store{pepper: pepper1}
	s1b := &Store{pepper: pepper1}
	s2 := &Store{pepper: pepper2}

	raw := []byte("session-token-raw-bytes")
	h1 := s1.hash(raw)
	h1b := s1b.hash(raw)
	h2 := s2.hash(raw)

	// Same pepper + same input → identical hash. Without this
	// Lookup's `WHERE token_hash = $1` can never match.
	if !bytes.Equal(h1, h1b) {
		t.Fatalf("hash not deterministic for same pepper")
	}
	// Different pepper → different hash. Without this the
	// pepper provides no defense against DB dumps.
	if bytes.Equal(h1, h2) {
		t.Fatalf("hash must vary with pepper")
	}
	if len(h1) != 32 {
		t.Fatalf("expected 32-byte SHA-256 output, got %d", len(h1))
	}
}

func TestCompareConstantTime(t *testing.T) {
	s := &Store{pepper: bytes.Repeat([]byte{0xAA}, 32)}
	raw := []byte("hello")
	h := s.hash(raw)
	if !s.Compare(raw, h) {
		t.Fatalf("Compare must accept matching pair")
	}
	if s.Compare([]byte("hellp"), h) {
		t.Fatalf("Compare must reject mismatched input")
	}
}

func TestNewRejectsShortPepper(t *testing.T) {
	// A pepper shorter than the HMAC block size provides less
	// than one-block of entropy to an attacker who sees the
	// output; 32 bytes is the project-wide floor (same as
	// cookie secret, sealer DEK wrap key).
	if _, err := New(&sql.DB{}, bytes.Repeat([]byte{0x01}, 31)); err == nil {
		t.Fatalf("New must reject pepper < 32 bytes")
	}
}

func TestNewDefensivelyCopiesPepper(t *testing.T) {
	// Mutating the caller's slice after New must not change
	// the store's pepper; otherwise a careless caller can
	// quietly poison every in-flight Lookup.
	pepper := bytes.Repeat([]byte{0x33}, 32)
	s, err := New(&sql.DB{}, pepper)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pre := s.hash([]byte("x"))
	pepper[0] = 0xFF // mutate caller's buffer
	post := s.hash([]byte("x"))
	if !bytes.Equal(pre, post) {
		t.Fatalf("pepper wasn't defensively copied")
	}
}

func TestSanitizeIP(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"ipv4_with_port", "192.0.2.10:54321", "192.0.2.10"},
		{"ipv4_bare", "192.0.2.10", "192.0.2.10"},
		{"ipv6_with_port", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv6_bare", "2001:db8::1", "2001:db8::1"},
		{"garbage_rejected", "not-an-ip", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeIP(tc.in); got != tc.want {
				t.Fatalf("SanitizeIP(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
