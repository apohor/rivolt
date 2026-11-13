// Package tripshare builds and stores public read-only trip links: a frozen, sanitized copy of a saved trip's plan behind an
// unguessable token. The design is _shared/share-trip-design.md (approved by DA, conditions S1-S9).
package tripshare

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
)

// tokenBytes is the token's entropy: 128 bits, 22 base64url characters.
const tokenBytes = 16

var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// NewToken returns a fresh token and the hash that is stored in its place. The token is shown to the owner once.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// ValidToken reports whether s has the shape of a token (so a malformed body is a 400 and never reaches the database).
func ValidToken(s string) bool { return tokenRE.MatchString(s) }

// HashToken is the SHA-256 of the token string, what trip_shares.token_hash holds.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
