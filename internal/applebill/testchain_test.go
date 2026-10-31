package applebill

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
	"time"
)

// testChain is a signing chain shaped like Apple's (root, intermediate carrying Apple's marker extension, leaf carrying its own), built
// here because Apple's private keys are not ours: tests pin this root instead of Apple's and sign what Apple would sign.
type testChain struct {
	root, inter, leaf *x509.Certificate
	leafKey           *ecdsa.PrivateKey
	verifier          *Verifier
}

func mustKey(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func issue(t testing.TB, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type chainOpts struct {
	noLeafMarker, noInterMarker bool
	leafNotAfter                time.Time
}

func newChain(t testing.TB, o chainOpts) *testChain {
	t.Helper()
	now := time.Now()
	rootKey, interKey, leafKey := mustKey(t, elliptic.P384()), mustKey(t, elliptic.P384()), mustKey(t, elliptic.P256())
	rootT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root CA - G3"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour * 365),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	root := issue(t, rootT, rootT, &rootKey.PublicKey, rootKey)
	interT := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test Worldwide Developer Relations"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour * 300),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	if !o.noInterMarker {
		interT.ExtraExtensions = []pkix.Extension{{Id: oidIntermediateMarker, Value: []byte{5, 0}}}
	}
	inter := issue(t, interT, root, &interKey.PublicKey, rootKey)
	leafEnd := o.leafNotAfter
	if leafEnd.IsZero() {
		leafEnd = now.Add(24 * time.Hour * 200)
	}
	leafT := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Test Prod ECC Mac App Store Receipt Signing"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: leafEnd,
		KeyUsage: x509.KeyUsageDigitalSignature}
	if !o.noLeafMarker {
		leafT.ExtraExtensions = []pkix.Extension{{Id: oidLeafMarker, Value: []byte{5, 0}}}
	}
	leaf := issue(t, leafT, inter, &leafKey.PublicKey, interKey)
	return &testChain{root: root, inter: inter, leaf: leaf, leafKey: leafKey, verifier: NewVerifierWithRoot(root)}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// sign builds a compact JWS of payload with this chain's leaf, ES256, x5c = leaf, intermediate, root.
func (c *testChain) sign(t testing.TB, payload any) string {
	t.Helper()
	return c.signWith(t, payload, "ES256", []string{std(c.leaf), std(c.inter), std(c.root)}, c.leafKey)
}

func std(c *x509.Certificate) string { return base64.StdEncoding.EncodeToString(c.Raw) }

func (c *testChain) signWith(t testing.TB, payload any, alg string, x5c []string, key *ecdsa.PrivateKey) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]any{"alg": alg, "x5c": x5c})
	pl, _ := json.Marshal(payload)
	input := b64(hdr) + "." + b64(pl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig)
}
