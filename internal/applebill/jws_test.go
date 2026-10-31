package applebill

import (
	"crypto/elliptic"
	"errors"
	"strings"
	"testing"
	"time"
)

// The embedded root is Apple's published one: its SHA-256 is the fingerprint Apple lists on https://www.apple.com/certificateauthority/.
func TestAppleRootIsThePublishedOne(t *testing.T) {
	got, err := RootFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got != AppleRootG3Fingerprint {
		t.Fatalf("embedded root fingerprint %s, want Apple's published %s", got, AppleRootG3Fingerprint)
	}
	if _, err := NewVerifier(); err != nil {
		t.Fatal(err)
	}
}

type probe struct {
	N string `json:"n"`
}

func TestVerifyAcceptsAnAppleShapedChain(t *testing.T) {
	c := newChain(t, chainOpts{})
	var p probe
	if err := c.verifier.Verify(c.sign(t, probe{N: "ok"}), &p); err != nil || p.N != "ok" {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestVerifyRejects(t *testing.T) {
	good := newChain(t, chainOpts{})
	other := newChain(t, chainOpts{}) // a different root: not the one pinned
	x5c := []string{std(good.leaf), std(good.inter), std(good.root)}
	jws := good.sign(t, probe{N: "x"})
	parts := strings.Split(jws, ".")
	tampered := parts[0] + "." + b64([]byte(`{"n":"evil"}`)) + "." + parts[2]

	cases := map[string]struct {
		v    *Verifier
		jws  string
		want error
	}{
		"payload tampered":           {good.verifier, tampered, ErrSignature},
		"signed by another key":      {good.verifier, good.signWith(t, probe{}, "ES256", x5c, mustKey(t, elliptic.P256())), ErrSignature},
		"alg none":                   {good.verifier, good.signWith(t, probe{}, "none", x5c, good.leafKey), ErrAlg},
		"alg HS256":                  {good.verifier, good.signWith(t, probe{}, "HS256", x5c, good.leafKey), ErrAlg},
		"alg RS256":                  {good.verifier, good.signWith(t, probe{}, "RS256", x5c, good.leafKey), ErrAlg},
		"chain of two":               {good.verifier, good.signWith(t, probe{}, "ES256", x5c[:2], good.leafKey), ErrChain},
		"chain of four":              {good.verifier, good.signWith(t, probe{}, "ES256", append(append([]string{}, x5c...), x5c[2]), good.leafKey), ErrChain},
		"chain to another root":      {good.verifier, other.sign(t, probe{}), ErrChain},
		"verifier pins another root": {other.verifier, jws, ErrChain},
		"not base64 in x5c":          {good.verifier, good.signWith(t, probe{}, "ES256", []string{"!!", "!!", "!!"}, good.leafKey), ErrChain},
		"empty":                      {good.verifier, "", ErrMalformed},
		"two parts":                  {good.verifier, "a.b", ErrMalformed},
		"garbage header":             {good.verifier, "e30.e30.e30", ErrAlg},
	}
	for name, c := range cases {
		if err := c.v.Verify(c.jws, &probe{}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}

	// The leaf and intermediate must carry Apple's marker extensions.
	for name, o := range map[string]chainOpts{"no leaf marker": {noLeafMarker: true}, "no intermediate marker": {noInterMarker: true}} {
		c := newChain(t, o)
		if err := c.verifier.Verify(c.sign(t, probe{}), &probe{}); !errors.Is(err, ErrChain) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A leaf past its validity is refused.
	exp := newChain(t, chainOpts{leafNotAfter: time.Now().Add(time.Hour)})
	exp.verifier.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if err := exp.verifier.Verify(exp.sign(t, probe{}), &probe{}); !errors.Is(err, ErrChain) {
		t.Errorf("expired leaf: %v", err)
	}
}
