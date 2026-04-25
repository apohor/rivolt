package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
)

// testKEK is a deterministic 32-byte KEK used in tests so the same
// env-string across tests opens into the same Sealer without
// anyone having to care about base64 formatting.
func testKEK(id string, fill byte) string {
	raw := bytes.Repeat([]byte{fill}, kekLen)
	return id + ":" + base64.StdEncoding.EncodeToString(raw)
}

// TestRoundTrip is the basic "same bytes come back out" check.
// Covers the happy path end-to-end: Seal generates a fresh DEK,
// wraps it under the KEK, assembles the wire format; Open picks
// it apart and returns the original plaintext.
func TestRoundTrip(t *testing.T) {
	t.Setenv("RIVOLT_KEK", testKEK("v1", 0xAA))
	s, err := NewEnvSealerFromEnv("RIVOLT_KEK")
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	user := uuid.New()
	ctx := context.Background()
	plaintext := []byte(`{"refresh_token":"abc","access_token":"xyz"}`)

	blob, err := s.Seal(ctx, user, plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// The blob must NOT contain the plaintext — that's the
	// whole point.
	if bytes.Contains(blob, plaintext) {
		t.Fatalf("blob leaks plaintext: %q", blob)
	}
	got, err := s.Open(ctx, user, blob)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(plaintext, got) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

// TestCrossUserRejected pins the AAD-binding guarantee: a blob
// sealed for user A must not open against user B, even with the
// same KEK. This is what prevents the "row-swap" attack in a
// shared Postgres where an attacker has UPDATE but not the KEK.
func TestCrossUserRejected(t *testing.T) {
	t.Setenv("RIVOLT_KEK", testKEK("v1", 0xAA))
	s, err := NewEnvSealerFromEnv("RIVOLT_KEK")
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	a := uuid.New()
	b := uuid.New()
	ctx := context.Background()

	blob, err := s.Seal(ctx, a, []byte("alice-only"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := s.Open(ctx, b, blob); err != ErrSealedBlob {
		t.Fatalf("want ErrSealedBlob on cross-user open, got %v", err)
	}
}

// TestTamperRejected pins AEAD integrity: any single bit flipped
// in the ciphertext must fail Open. We flip bytes at several
// representative offsets (magic prefix, kek id, nonce, ciphertext
// tail) rather than hammer every byte — each hit exercises a
// different part of the header / AES-GCM path.
func TestTamperRejected(t *testing.T) {
	t.Setenv("RIVOLT_KEK", testKEK("v1", 0xAA))
	s, err := NewEnvSealerFromEnv("RIVOLT_KEK")
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	user := uuid.New()
	ctx := context.Background()
	blob, err := s.Seal(ctx, user, []byte("secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	offsets := []int{0, 4, 10, len(blob) / 2, len(blob) - 1}
	for _, off := range offsets {
		corrupt := make([]byte, len(blob))
		copy(corrupt, blob)
		corrupt[off] ^= 0x01
		if _, err := s.Open(ctx, user, corrupt); err != ErrSealedBlob {
			t.Errorf("flipped byte %d: want ErrSealedBlob, got %v", off, err)
		}
	}
}

// TestKEKIDStamped confirms the kek id used for Seal is stored
// in the blob and reported by KEKID. Callers rely on this to
// fill user_secrets.kek_id for audit + rotation queries.
func TestKEKIDStamped(t *testing.T) {
	t.Setenv("RIVOLT_KEK", testKEK("v7", 0xCC))
	s, err := NewEnvSealerFromEnv("RIVOLT_KEK")
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	if got := s.KEKID(); got != "v7" {
		t.Fatalf("KEKID() = %q, want v7", got)
	}
	blob, err := s.Seal(context.Background(), uuid.New(), []byte("x"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	hdr, err := decodeBlob(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hdr.kekID != "v7" {
		t.Errorf("blob kekID = %q, want v7", hdr.kekID)
	}
}

// TestRotation covers the real reason the wire format carries a
// kek id: during rotation the primary KEK changes but rows
// written under the previous KEK must still open. We seal with
// v1, rotate to v2 as primary (v1 kept as rotation fallback),
// and check both existing blobs open and new blobs stamp v2.
func TestRotation(t *testing.T) {
	t.Setenv("RIVOLT_KEK_V1", testKEK("v1", 0x11))
	v1, err := NewEnvSealerFromEnv("RIVOLT_KEK_V1")
	if err != nil {
		t.Fatalf("new v1: %v", err)
	}
	user := uuid.New()
	ctx := context.Background()
	oldBlob, err := v1.Seal(ctx, user, []byte("old row"))
	if err != nil {
		t.Fatalf("seal v1: %v", err)
	}

	// Rotate: v2 primary, v1 still in rotation list.
	t.Setenv("RIVOLT_KEK", testKEK("v2", 0x22))
	t.Setenv("RIVOLT_KEK_OLD", testKEK("v1", 0x11))
	rotated, err := NewEnvSealerFromEnv("RIVOLT_KEK", "RIVOLT_KEK_OLD")
	if err != nil {
		t.Fatalf("new rotated: %v", err)
	}
	if rotated.KEKID() != "v2" {
		t.Fatalf("rotated KEKID = %q, want v2", rotated.KEKID())
	}
	// Old blob still opens.
	got, err := rotated.Open(ctx, user, oldBlob)
	if err != nil {
		t.Fatalf("open old blob under rotated sealer: %v", err)
	}
	if !bytes.Equal(got, []byte("old row")) {
		t.Fatalf("old blob round-trip: got %q", got)
	}
	// New writes stamp v2 and round-trip.
	newBlob, err := rotated.Seal(ctx, user, []byte("new row"))
	if err != nil {
		t.Fatalf("seal v2: %v", err)
	}
	hdr, err := decodeBlob(newBlob)
	if err != nil {
		t.Fatalf("decode new: %v", err)
	}
	if hdr.kekID != "v2" {
		t.Errorf("new blob kekID = %q, want v2", hdr.kekID)
	}
}

// TestRotationMissingOldKEKFailsClosed exercises the less-happy
// path: a blob sealed under v1 is asked to open against a sealer
// that only has v2 configured. Must return ErrSealedBlob, not
// panic, not succeed.
func TestRotationMissingOldKEKFailsClosed(t *testing.T) {
	t.Setenv("RIVOLT_KEK_V1", testKEK("v1", 0x11))
	v1, err := NewEnvSealerFromEnv("RIVOLT_KEK_V1")
	if err != nil {
		t.Fatalf("new v1: %v", err)
	}
	user := uuid.New()
	ctx := context.Background()
	oldBlob, err := v1.Seal(ctx, user, []byte("old"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	t.Setenv("RIVOLT_KEK", testKEK("v2", 0x22))
	v2, err := NewEnvSealerFromEnv("RIVOLT_KEK")
	if err != nil {
		t.Fatalf("new v2: %v", err)
	}
	if _, err := v2.Open(ctx, user, oldBlob); err != ErrSealedBlob {
		t.Fatalf("want ErrSealedBlob for unknown KEK, got %v", err)
	}
}

// TestMissingEnvFailsStartup. The whole point of phase-1 EnvSealer
// is that it is startup-hard: if operators forget to set the KEK
// we refuse to boot rather than run with everyone's secrets at
// the mercy of DB-at-rest only. Reflect that in the constructor
// contract.
func TestMissingEnvFailsStartup(t *testing.T) {
	// Explicitly unset — t.Setenv restores after the test so
	// other tests still see their own values.
	t.Setenv("RIVOLT_KEK_NOT_SET", "")
	if _, err := NewEnvSealerFromEnv("RIVOLT_KEK_NOT_SET"); err == nil {
		t.Fatalf("want error when KEK env is empty, got nil")
	}
}

// TestParseKEKEnvRejectsShortKey. AES-128 is not offered; anyone
// providing a 16-byte KEK has made a mistake, and we'd rather
// fail loudly than silently weaken the scheme.
func TestParseKEKEnvRejectsShortKey(t *testing.T) {
	short := bytes.Repeat([]byte{0xAA}, 16)
	t.Setenv("RIVOLT_KEK", "v1:"+base64.StdEncoding.EncodeToString(short))
	if _, err := NewEnvSealerFromEnv("RIVOLT_KEK"); err == nil {
		t.Fatalf("want error on short KEK, got nil")
	}
}

// TestNoopSealerIsIdentity protects tests that use NoopSealer
// from accidentally depending on the envelope format. Also
// serves as live documentation that NoopSealer really does
// nothing — a reader of the source can check the assertion here.
func TestNoopSealerIsIdentity(t *testing.T) {
	s := NoopSealer{}
	ctx := context.Background()
	u := uuid.New()
	in := []byte("plaintext")
	out, err := s.Seal(ctx, u, in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Errorf("seal: got %q, want %q", out, in)
	}
	roundTrip, err := s.Open(ctx, u, out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, roundTrip) {
		t.Errorf("open: got %q, want %q", roundTrip, in)
	}
	if s.KEKID() != "noop" {
		t.Errorf("KEKID = %q, want noop", s.KEKID())
	}
}
