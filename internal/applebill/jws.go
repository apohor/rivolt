// Package applebill is the server side of StoreKit 2 (Rivolt Pro, Sprint 14, item 5): it verifies what Apple and the app send about a
// subscription and turns it into the account's `apple` entitlement row. Nothing here trusts a claim before a signature has been checked
// against Apple's pinned root, and nothing here calls Apple: it is inert unless configured (config.go).
package applebill

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// appleRootCAG3PEM is Apple's "Apple Root CA - G3" certificate, the root every App Store Server Notification and signed transaction chains
// to. Source: https://www.apple.com/certificateauthority/AppleRootCA-G3.cer (downloaded as DER, converted to PEM). Pinned two ways: it is
// embedded here, and TestAppleRootIsThePublishedOne checks its SHA-256 fingerprint against the value Apple publishes on
// https://www.apple.com/certificateauthority/ (AppleRootCA-G3SHA-256 63:34:3A:BF:B8:9A:6A:03:EB:B5:7E:9B:3F:5F:A7:BE:7C:4F:5C:75:6F:30:17:B3:A8:C4:88:C3:65:3E:91:79).
//
// Verification is offline on purpose: no OCSP or CRL fetch is made, so the endpoint never makes an outbound call to a host a request names
// and keeps working when Apple's responders are down. The cost is that a revoked Apple signing certificate would still verify until it expires.
//
//go:embed AppleRootCA-G3.pem
var appleRootCAG3PEM []byte

// AppleRootG3Fingerprint is the published SHA-256 of the root, lower-case hex.
const AppleRootG3Fingerprint = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"

// Apple's marker extensions: the leaf of a signing chain carries the first, its intermediate the second.
var (
	oidLeafMarker         = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
	oidIntermediateMarker = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}
)

// Verifier checks Apple-signed JWS values.
type Verifier struct {
	roots *x509.CertPool
	// Now is the time certificates are checked at (nil: time.Now).
	Now func() time.Time
}

// NewVerifier pins Apple's root.
func NewVerifier() (*Verifier, error) {
	root, err := parseRootPEM(appleRootCAG3PEM)
	if err != nil {
		return nil, err
	}
	return NewVerifierWithRoot(root), nil
}

// NewVerifierWithRoot pins another root (tests build their own chain with Apple's marker extensions).
func NewVerifierWithRoot(root *x509.Certificate) *Verifier {
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return &Verifier{roots: pool}
}

func parseRootPEM(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("applebill: no certificate in the embedded root")
	}
	return x509.ParseCertificate(block.Bytes)
}

// RootFingerprint is the SHA-256 of the embedded root, lower-case hex.
func RootFingerprint() (string, error) {
	root, err := parseRootPEM(appleRootCAG3PEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(root.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Errors, so callers and tests can tell a rejection from a malformed value.
var (
	ErrMalformed = errors.New("applebill: not a compact JWS")
	ErrAlg       = errors.New("applebill: the JWS algorithm is not ES256")
	ErrChain     = errors.New("applebill: the certificate chain is not Apple's")
	ErrSignature = errors.New("applebill: bad signature")
)

type jwsHeader struct {
	Alg string   `json:"alg"`
	X5C []string `json:"x5c"`
}

// Verify checks a compact JWS and unmarshals its payload into out. Order matters: the algorithm and the chain are checked, then the
// signature, and only then is the payload parsed.
func (v *Verifier) Verify(compact string, out any) error {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || len(compact) > 64<<10 {
		return ErrMalformed
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrMalformed
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return ErrMalformed
	}
	if h.Alg != "ES256" {
		return ErrAlg
	}
	leaf, err := v.verifyChain(h.X5C)
	if err != nil {
		return err
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return ErrChain
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return ErrSignature
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return ErrSignature
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrMalformed
	}
	if err := json.Unmarshal(pb, out); err != nil {
		return fmt.Errorf("%w: payload", ErrMalformed)
	}
	return nil
}

// verifyChain checks the x5c header: exactly three certificates (leaf, intermediate, root), the leaf and the intermediate carrying Apple's
// marker extensions, the whole chain valid now and ending at the pinned root. It returns the leaf.
func (v *Verifier) verifyChain(x5c []string) (*x509.Certificate, error) {
	if len(x5c) != 3 {
		return nil, ErrChain
	}
	certs := make([]*x509.Certificate, 3)
	for i, s := range x5c {
		der, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, ErrChain
		}
		if certs[i], err = x509.ParseCertificate(der); err != nil {
			return nil, ErrChain
		}
	}
	leaf, inter := certs[0], certs[1]
	if !hasExtension(leaf.Extensions, oidLeafMarker) || !hasExtension(inter.Extensions, oidIntermediateMarker) {
		return nil, ErrChain
	}
	inters := x509.NewCertPool()
	inters.AddCert(inter)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: v.roots, Intermediates: inters, CurrentTime: v.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChain, err)
	}
	return leaf, nil
}

func hasExtension(exts []pkix.Extension, oid asn1.ObjectIdentifier) bool {
	for _, e := range exts {
		if e.Id.Equal(oid) {
			return true
		}
	}
	return false
}
