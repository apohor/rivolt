// Package crypto is Rivolt's in-process key-wrapping layer. It
// exists so the rest of the codebase can say "seal this blob" and
// "open this blob" without caring whether the key-encryption key
// (KEK) sits in an env var (phase 1 / 2) or in a cloud KMS
// (phase 3).
//
// # Design
//
// Every sealed blob is **envelope-encrypted**:
//
//  1. A fresh 32-byte data-encryption key (DEK) is generated per
//     Seal call.
//  2. The plaintext is encrypted with AES-256-GCM under the DEK.
//  3. The DEK itself is wrapped with the KEK (also AES-256-GCM).
//  4. The wire format carries a magic prefix, a KEK identifier,
//     the nonce, the wrapped DEK, and the ciphertext.
//
// This shape is chosen so that:
//
//   - **KEK rotation is cheap.** Rotating the KEK only re-wraps
//     the per-row DEKs; the payload ciphertexts don't have to be
//     re-encrypted.
//   - **Phase-3 KMS stays affordable.** A per-request
//     kms:Encrypt/Decrypt for a ~32-byte DEK is the canonical
//     KMS usage pattern; encrypting whole session payloads via
//     KMS would blow through the request/s quota and add 20-50ms
//     per call.
//   - **Multiple KEKs can coexist.** `kek_id` in the header lets
//     old rows unwrap with the old key while new writes use the
//     new one; the rotation job walks rows and re-wraps without
//     downtime.
//   - **Cross-tenant ciphertext swaps fail.** The user ID is
//     bound into GCM's additional-authenticated-data, so a blob
//     stolen from user A's row and pasted into user B's row
//     fails to open.
//
// # What this package does NOT do
//
//   - It does not protect secrets in memory. A process-
//     compromise attacker still sees plaintext in RAM. The
//     threat model here is database-at-rest (DB dump, offsite
//     backup, compromised replica).
//   - It does not store the KEK. That's delivered by the
//     environment (env var in phase 1/2, KMS in phase 3) and
//     swapped in via the Sealer implementation.
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
)

// ErrSealedBlob is returned when a supposedly sealed blob fails
// any validation — wrong magic, truncated header, KEK mismatch,
// authentication failure, or user-ID mismatch. The exact cause is
// intentionally not exposed (it'd help an attacker pin down which
// invariant they're tripping); only "the blob did not open".
var ErrSealedBlob = errors.New("crypto: sealed blob did not open")

// blobMagic is the first four bytes of every sealed payload. Lets
// the decoder fail fast on a random byte string without consuming
// AES time. The value is arbitrary but pinned — changing it would
// break every row already in the DB.
var blobMagic = [4]byte{'R', 'V', 'L', 1}

// kekIDLen caps the on-wire length of the KEK identifier. 32
// bytes is enough for an env-var label like "kek-v1" or a KMS
// key-version ARN suffix; anything longer is almost certainly a
// bug in the caller.
const kekIDLen = 32

// dekLen is the size of a data-encryption key. 32 bytes = AES-256.
const dekLen = 32

// kekLen is the size of a key-encryption key. Same algorithm, same
// size.
const kekLen = 32

// Sealer is the surface the rest of the codebase programs
// against. Implementations differ in where the KEK lives;
// everything above the interface stays the same.
//
// Round-trip contract: for any sealer S, plaintext p, user u, and
// context ctx, S.Open(ctx, u, S.Seal(ctx, u, p)) must return p. A
// different user, a different KEK, or a single flipped ciphertext
// bit must return ErrSealedBlob.
type Sealer interface {
	// Seal encrypts plaintext and returns a self-describing
	// ciphertext blob. The userID is bound into the AEAD
	// additional-authenticated-data so a ciphertext stolen
	// from one user's row cannot be opened against another.
	Seal(ctx context.Context, userID uuid.UUID, plaintext []byte) ([]byte, error)

	// Open reverses Seal. Any failure — bad magic, wrong KEK,
	// wrong user, tampered ciphertext — returns
	// ErrSealedBlob. The original cause is not surfaced.
	Open(ctx context.Context, userID uuid.UUID, ciphertext []byte) ([]byte, error)

	// KEKID identifies the KEK used for new writes. Stored
	// alongside each ciphertext so the rotation job can find
	// rows to re-wrap and so audit queries can report which
	// key protects which secret. Must be stable across
	// restarts for a given KEK.
	KEKID() string
}

// EnvSealer is the phase-1 / phase-2 Sealer. It reads its KEK
// from an environment variable at construction time. The KEK is
// 32 raw bytes, base64-standard-encoded; anything shorter is
// rejected — AES-128 is not offered because there's no legitimate
// reason to pick it over AES-256 in 2026.
//
// Rotation: hold two EnvSealers, one under the old KEK id and one
// under the new, and dispatch Open by reading the blob's kek_id
// header. NewEnvSealerFromEnv supports that by accepting a list
// of env-var names; the first non-empty one wins for Seal, but
// all of them are tried for Open.
type EnvSealer struct {
	kekID string
	kek   []byte // exactly kekLen bytes
	// additional sealers consulted by Open, keyed by kekID.
	// Used to unwrap ciphertexts written under a previous KEK
	// during rotation. The primary (this receiver) is NOT in
	// the map.
	rotation map[string]*EnvSealer
}

// NewEnvSealer builds a Sealer from an explicit kekID + raw 32-
// byte KEK. Most code should call NewEnvSealerFromEnv instead;
// this constructor is here for tests and for the rotation tooling.
func NewEnvSealer(kekID string, rawKEK []byte) (*EnvSealer, error) {
	if kekID == "" {
		return nil, errors.New("crypto: empty kekID")
	}
	if len(kekID) > kekIDLen {
		return nil, fmt.Errorf("crypto: kekID too long (%d > %d)", len(kekID), kekIDLen)
	}
	if len(rawKEK) != kekLen {
		return nil, fmt.Errorf("crypto: KEK must be %d bytes, got %d", kekLen, len(rawKEK))
	}
	// Copy so callers can zero their buffer.
	k := make([]byte, kekLen)
	copy(k, rawKEK)
	return &EnvSealer{kekID: kekID, kek: k, rotation: map[string]*EnvSealer{}}, nil
}

// NewEnvSealerFromEnv builds an EnvSealer by reading KEKs from
// the process environment.
//
// primaryVar is the env var holding the primary KEK
// (used for new Seal calls). rotationVars are earlier KEKs still
// required to Open existing rows during a rotation window; they
// can be empty. Each value is "<kekID>:<base64-KEK>" — the colon
// splits a short operator-meaningful label (e.g. "v1", "v2")
// from the 32 random bytes. The label goes into the blob header;
// the bytes never leave the process.
//
// Startup-hard: a missing or malformed primary is a fatal
// configuration error. The caller is expected to refuse to boot.
func NewEnvSealerFromEnv(primaryVar string, rotationVars ...string) (*EnvSealer, error) {
	primary := os.Getenv(primaryVar)
	if primary == "" {
		return nil, fmt.Errorf("crypto: %s is not set", primaryVar)
	}
	primaryKEK, err := parseKEKEnv(primary)
	if err != nil {
		return nil, fmt.Errorf("crypto: %s: %w", primaryVar, err)
	}
	s, err := NewEnvSealer(primaryKEK.id, primaryKEK.key)
	if err != nil {
		return nil, fmt.Errorf("crypto: %s: %w", primaryVar, err)
	}
	for _, v := range rotationVars {
		raw := os.Getenv(v)
		if raw == "" {
			continue
		}
		k, err := parseKEKEnv(raw)
		if err != nil {
			return nil, fmt.Errorf("crypto: %s: %w", v, err)
		}
		if k.id == s.kekID {
			// Same KEK listed twice is a configuration
			// mistake but not dangerous; skip.
			continue
		}
		prev, err := NewEnvSealer(k.id, k.key)
		if err != nil {
			return nil, fmt.Errorf("crypto: %s: %w", v, err)
		}
		s.rotation[k.id] = prev
	}
	return s, nil
}

// KEKID implements Sealer. Used by the sealer caller to stamp the
// user_secrets row with the KEK that protects it.
func (s *EnvSealer) KEKID() string { return s.kekID }

// Seal implements Sealer.
func (s *EnvSealer) Seal(_ context.Context, userID uuid.UUID, plaintext []byte) ([]byte, error) {
	// 1. Fresh DEK.
	dek := make([]byte, dekLen)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("crypto: rand dek: %w", err)
	}
	// 2. Encrypt plaintext under DEK with user ID as AAD.
	payloadNonce, payloadCT, err := aesGCMSeal(dek, plaintext, userID[:])
	if err != nil {
		return nil, err
	}
	// 3. Wrap DEK under KEK. KEK wrap also uses the user ID as
	// AAD — a stolen wrapped-DEK must be useless in another
	// user's row.
	wrapNonce, wrappedDEK, err := aesGCMSeal(s.kek, dek, userID[:])
	if err != nil {
		return nil, err
	}
	// 4. Assemble wire format.
	return encodeBlob(s.kekID, wrapNonce, wrappedDEK, payloadNonce, payloadCT), nil
}

// Open implements Sealer.
func (s *EnvSealer) Open(_ context.Context, userID uuid.UUID, blob []byte) ([]byte, error) {
	hdr, err := decodeBlob(blob)
	if err != nil {
		return nil, ErrSealedBlob
	}
	// Pick the right KEK for this blob.
	sealer := s
	if subtle.ConstantTimeCompare([]byte(hdr.kekID), []byte(s.kekID)) != 1 {
		prev, ok := s.rotation[hdr.kekID]
		if !ok {
			return nil, ErrSealedBlob
		}
		sealer = prev
	}
	// Unwrap the DEK.
	dek, err := aesGCMOpen(sealer.kek, hdr.wrapNonce, hdr.wrappedDEK, userID[:])
	if err != nil {
		return nil, ErrSealedBlob
	}
	defer zero(dek)
	// Unseal the payload.
	plaintext, err := aesGCMOpen(dek, hdr.payloadNonce, hdr.payloadCT, userID[:])
	if err != nil {
		return nil, ErrSealedBlob
	}
	return plaintext, nil
}

// ---- internal helpers -------------------------------------------

type parsedKEK struct {
	id  string
	key []byte
}

// parseKEKEnv splits "<id>:<base64-32-bytes>" and validates
// lengths. The id must be non-empty and ≤ kekIDLen; the bytes
// must be exactly kekLen after decode. Accepts base64 with or
// without padding.
func parseKEKEnv(raw string) (parsedKEK, error) {
	// Strip common accidents: trailing newline, surrounding
	// whitespace.
	for len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r' || raw[len(raw)-1] == ' ') {
		raw = raw[:len(raw)-1]
	}
	colon := -1
	for i := 0; i < len(raw); i++ {
		if raw[i] == ':' {
			colon = i
			break
		}
	}
	if colon <= 0 || colon >= len(raw)-1 {
		return parsedKEK{}, errors.New("expected format <id>:<base64-KEK>")
	}
	id := raw[:colon]
	if len(id) > kekIDLen {
		return parsedKEK{}, fmt.Errorf("kek id too long (%d > %d)", len(id), kekIDLen)
	}
	// Try padded first, then raw (no padding).
	encoded := raw[colon+1:]
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return parsedKEK{}, fmt.Errorf("kek not valid base64: %w", err)
		}
	}
	if len(key) != kekLen {
		return parsedKEK{}, fmt.Errorf("kek must decode to %d bytes, got %d", kekLen, len(key))
	}
	return parsedKEK{id: id, key: key}, nil
}

// aesGCMSeal encrypts plaintext with a fresh 12-byte nonce and
// returns (nonce, ciphertext). The ciphertext includes GCM's 16-
// byte auth tag appended by Seal; we don't split them.
func aesGCMSeal(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("crypto: rand nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, aad)
	return nonce, ciphertext, nil
}

// aesGCMOpen reverses aesGCMSeal. Returns a generic error on any
// failure; callers translate to ErrSealedBlob.
func aesGCMOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("bad nonce length")
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// zero wipes a byte slice. Go makes no guarantees about this in
// the face of an optimising compiler, but clearing what we can
// clear is still worth doing — a process-memory snapshot taken
// seconds after a Seal shouldn't still have the DEK lying around.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---- wire format -----------------------------------------------
//
// All integers are big-endian, unsigned.
//
//   +----------+-----------------+----------------------+
//   | offset   | size            | field                |
//   +----------+-----------------+----------------------+
//   | 0        | 4               | magic ("RVL" || 1)   |
//   | 4        | 1               | len(kekID)           |
//   | 5        | len(kekID)      | kekID (ASCII)        |
//   | ...      | 1               | len(wrapNonce)       |
//   | ...      | len(wrapNonce)  | wrapNonce            |
//   | ...      | 2               | len(wrappedDEK)      |
//   | ...      | len(wrappedDEK) | wrappedDEK           |
//   | ...      | 1               | len(payloadNonce)    |
//   | ...      | len(payloadNonce)| payloadNonce        |
//   | ...      | 4               | len(payloadCT)       |
//   | ...      | len(payloadCT)  | payloadCT            |
//   +----------+-----------------+----------------------+
//
// A single-byte length on the kekID and the nonces is fine (they
// are tiny and fixed-ish). The wrapped DEK is always 32+16 = 48
// bytes today but a 2-byte length leaves headroom for future
// wrappers that emit more metadata (e.g. a KMS envelope that
// carries its own header). The payload length is 4 bytes so
// individual blobs can exceed 64 KiB without a format bump.

type blobHeader struct {
	kekID        string
	wrapNonce    []byte
	wrappedDEK   []byte
	payloadNonce []byte
	payloadCT    []byte
}

func encodeBlob(kekID string, wrapNonce, wrappedDEK, payloadNonce, payloadCT []byte) []byte {
	size := 4 + 1 + len(kekID) +
		1 + len(wrapNonce) +
		2 + len(wrappedDEK) +
		1 + len(payloadNonce) +
		4 + len(payloadCT)
	out := make([]byte, 0, size)
	out = append(out, blobMagic[:]...)
	out = append(out, byte(len(kekID)))
	out = append(out, kekID...)
	out = append(out, byte(len(wrapNonce)))
	out = append(out, wrapNonce...)
	out = append(out, byte(len(wrappedDEK)>>8), byte(len(wrappedDEK)))
	out = append(out, wrappedDEK...)
	out = append(out, byte(len(payloadNonce)))
	out = append(out, payloadNonce...)
	l := len(payloadCT)
	out = append(out, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	out = append(out, payloadCT...)
	return out
}

func decodeBlob(b []byte) (blobHeader, error) {
	var h blobHeader
	if len(b) < 4 || b[0] != blobMagic[0] || b[1] != blobMagic[1] || b[2] != blobMagic[2] || b[3] != blobMagic[3] {
		return h, errors.New("bad magic")
	}
	i := 4
	if len(b) < i+1 {
		return h, errors.New("truncated")
	}
	kl := int(b[i])
	i++
	if kl == 0 || kl > kekIDLen || len(b) < i+kl {
		return h, errors.New("truncated kekID")
	}
	h.kekID = string(b[i : i+kl])
	i += kl
	if len(b) < i+1 {
		return h, errors.New("truncated")
	}
	wn := int(b[i])
	i++
	if len(b) < i+wn {
		return h, errors.New("truncated wrap nonce")
	}
	h.wrapNonce = b[i : i+wn]
	i += wn
	if len(b) < i+2 {
		return h, errors.New("truncated")
	}
	wd := int(b[i])<<8 | int(b[i+1])
	i += 2
	if len(b) < i+wd {
		return h, errors.New("truncated wrapped dek")
	}
	h.wrappedDEK = b[i : i+wd]
	i += wd
	if len(b) < i+1 {
		return h, errors.New("truncated")
	}
	pn := int(b[i])
	i++
	if len(b) < i+pn {
		return h, errors.New("truncated payload nonce")
	}
	h.payloadNonce = b[i : i+pn]
	i += pn
	if len(b) < i+4 {
		return h, errors.New("truncated")
	}
	pl := int(b[i])<<24 | int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
	i += 4
	if len(b) < i+pl {
		return h, errors.New("truncated payload ct")
	}
	h.payloadCT = b[i : i+pl]
	i += pl
	if i != len(b) {
		return h, errors.New("trailing bytes")
	}
	return h, nil
}

// ---- test seam --------------------------------------------------

// NoopSealer is an identity sealer — Seal returns plaintext
// unchanged, Open returns ciphertext unchanged, KEKID is
// "noop". It exists for tests and for the dev server's stub
// mode where disk encryption is explicitly not required. **Never
// use in production.**
type NoopSealer struct{}

// Seal implements Sealer.
func (NoopSealer) Seal(_ context.Context, _ uuid.UUID, p []byte) ([]byte, error) {
	out := make([]byte, len(p))
	copy(out, p)
	return out, nil
}

// Open implements Sealer.
func (NoopSealer) Open(_ context.Context, _ uuid.UUID, c []byte) ([]byte, error) {
	out := make([]byte, len(c))
	copy(out, c)
	return out, nil
}

// KEKID implements Sealer.
func (NoopSealer) KEKID() string { return "noop" }
