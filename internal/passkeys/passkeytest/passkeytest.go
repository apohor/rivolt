// Package passkeytest is a software authenticator for tests: enough of one to make attestations ("none") and assertions that
// the WebAuthn library accepts, with the flags, origin and counter under the test's control. Not for production use.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// SoftAuth is a software authenticator: enough of one to make attestations ("none") and assertions that the library accepts,
// with the flags, origin and counter under the test's control.
type SoftAuth struct {
	t          *testing.T
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
}

// CredentialID is the authenticator's credential id; SetCredentialID replaces it (to forge a signature over another id).
func (a *SoftAuth) CredentialID() []byte { return a.credID }

// SetCredentialID sets the credential id.
func (a *SoftAuth) SetCredentialID(id []byte) { a.credID = id }

// UserHandle is the handle the last registration carried (what a sign-in must send back).
func (a *SoftAuth) UserHandle() []byte { return a.userHandle }

// New makes an authenticator with a fresh key and credential id.
func New(t *testing.T) *SoftAuth {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &SoftAuth{t: t, key: key, credID: id}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagAT = 0x40
)

func (a *SoftAuth) coseKey() []byte {
	pub, err := a.key.PublicKey.Bytes() // 0x04, X, Y
	if err != nil {
		a.t.Fatal(err)
	}
	x, y := pub[1:33], pub[33:65]
	raw, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func rpHash(rpID string) []byte { h := sha256.Sum256([]byte(rpID)); return h[:] }

// Create answers a registration: rpID and origin as the (possibly wrong) caller claims, uv whether it sets the UV flag.
func (a *SoftAuth) Create(opts *protocol.CredentialCreation, rpID, origin string, uv bool) []byte {
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": opts.Response.Challenge.String(), "origin": origin, "crossOrigin": false})
	switch v := opts.Response.User.ID.(type) { // a struct from the library, or the same options after a JSON round trip
	case protocol.URLEncodedBase64:
		a.userHandle = v
	case []byte:
		a.userHandle = v
	case string:
		a.userHandle, _ = base64.RawURLEncoding.DecodeString(v)
	}
	flags := byte(flagUP | flagAT | flagBE)
	if uv {
		flags |= flagUV
	}
	auth := append(rpHash(rpID), flags, 0, 0, 0, 0)
	auth = append(auth, make([]byte, 16)...) // AAGUID
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(a.credID)))
	auth = append(auth, a.credID...)
	auth = append(auth, a.coseKey()...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		a.t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(cd), "attestationObject": b64(att)},
	})
	return out
}

// Sign answers a sign-in.
func (a *SoftAuth) Sign(opts *protocol.CredentialAssertion, rpID, origin string, uv bool, signCount uint32, userHandle []byte) []byte {
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": opts.Response.Challenge.String(), "origin": origin, "crossOrigin": false})
	flags := byte(flagUP | flagBE)
	if uv {
		flags |= flagUV
	}
	auth := append(rpHash(rpID), flags)
	auth = binary.BigEndian.AppendUint32(auth, signCount)
	cdh := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, auth...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(cd), "authenticatorData": b64(auth), "signature": b64(sig), "userHandle": b64(userHandle)},
	})
	return out
}
