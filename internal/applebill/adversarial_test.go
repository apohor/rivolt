package applebill

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Pro 30: hostile input to the StoreKit endpoints, written by someone other than the author. Every case here is a value that is VALID JSON and
// often validly Apple-shaped (a test chain stands in for Apple, whose keys are not ours): what must hold is that it is refused (or, for a replay,
// changes nothing) and never panics, hangs or grants Pro. Cases that need rows (replays, ordering, an expired or revoked transaction against
// the database) are in adversarial_integration_test.go.

// forge signs payload with the chain's leaf under header, then lets mutate change the raw 64-byte signature.
func forge(t *testing.T, c *testChain, header map[string]any, payload []byte, mutate func([]byte) []byte) string {
	t.Helper()
	hb, _ := json.Marshal(header)
	input := b64(hb) + "." + b64(payload)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, c.leafKey, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	if mutate != nil {
		sig = mutate(sig)
	}
	return input + "." + b64(sig)
}

func goodHeader(c *testChain) map[string]any {
	return map[string]any{"alg": "ES256", "x5c": []string{std(c.leaf), std(c.inter), std(c.root)}}
}

func TestAdversarialJWS(t *testing.T) {
	c := newChain(t, chainOpts{})
	pl := []byte(`{"n":"x"}`)
	hdr := goodHeader(c)
	if err := c.verifier.Verify(forge(t, c, hdr, pl, nil), &probe{}); err != nil {
		t.Fatalf("the control (a well-formed JWS from this chain) was refused: %v", err)
	}

	big := make([]byte, 70<<10)
	for i := range big {
		big[i] = 'a'
	}
	bigPayload, _ := json.Marshal(map[string]string{"n": string(big)})

	withExtra := goodHeader(c)
	withExtra["jku"], withExtra["x5u"], withExtra["kid"], withExtra["crit"] = "https://evil.invalid/jwks", "https://evil.invalid/cert", "k1", []string{"exp"}

	badChainWithURL := map[string]any{"alg": "ES256", "x5u": "https://evil.invalid/cert", "jku": "https://evil.invalid/jwks", "x5c": []string{"AAAA", "AAAA", "AAAA"}}

	p384 := newChain(t, chainOpts{leafP384: true})
	early := newChain(t, chainOpts{})
	notYet := &Verifier{roots: early.verifier.roots, Now: func() time.Time { return time.Now().Add(-72 * time.Hour) }}
	interShort := newChain(t, chainOpts{interNotAfter: time.Now().Add(2 * time.Hour)})
	interShort.verifier.Now = func() time.Time { return time.Now().Add(3 * time.Hour) } // the leaf (200 days) is fine, the intermediate is not

	cases := []struct {
		name string
		v    *Verifier
		jws  string
		want error
	}{
		{"over the 64 KiB cap", c.verifier, forge(t, c, hdr, bigPayload, nil), ErrMalformed},
		{"signature one byte short", c.verifier, forge(t, c, hdr, pl, func(s []byte) []byte { return s[:63] }), ErrSignature},
		{"signature one byte long", c.verifier, forge(t, c, hdr, pl, func(s []byte) []byte { return append(s, 0) }), ErrSignature},
		{"signature all zeros (r = s = 0)", c.verifier, forge(t, c, hdr, pl, func(s []byte) []byte { return make([]byte, 64) }), ErrSignature},
		{"signature with a flipped bit", c.verifier, forge(t, c, hdr, pl, func(s []byte) []byte { s[10] ^= 1; return s }), ErrSignature},
		{"leaf key is P-384, not P-256", p384.verifier, p384.sign(t, probe{N: "x"}), ErrChain},
		{"chain not yet valid (clock before it)", notYet, early.sign(t, probe{N: "x"}), ErrChain},
		{"intermediate expired, leaf not", interShort.verifier, interShort.sign(t, probe{N: "x"}), ErrChain},
		{"leaf repeated three times", c.verifier, forge(t, c, map[string]any{"alg": "ES256", "x5c": []string{std(c.leaf), std(c.leaf), std(c.leaf)}}, pl, nil), ErrChain},
		{"chain order reversed (root, intermediate, leaf)", c.verifier, forge(t, c, map[string]any{"alg": "ES256", "x5c": []string{std(c.root), std(c.inter), std(c.leaf)}}, pl, nil), ErrChain},
		{"intermediate repeated as the leaf", c.verifier, forge(t, c, map[string]any{"alg": "ES256", "x5c": []string{std(c.inter), std(c.inter), std(c.root)}}, pl, nil), ErrChain},
		{"no x5c at all", c.verifier, forge(t, c, map[string]any{"alg": "ES256"}, pl, nil), ErrChain},
		{"x5c is not a list", c.verifier, forge(t, c, map[string]any{"alg": "ES256", "x5c": "abc"}, pl, nil), ErrMalformed},
		{"alg in lower case", c.verifier, forge(t, c, map[string]any{"alg": "es256", "x5c": hdr["x5c"]}, pl, nil), ErrAlg},
		{"alg with a space", c.verifier, forge(t, c, map[string]any{"alg": "ES256 ", "x5c": hdr["x5c"]}, pl, nil), ErrAlg},
		{"jku/x5u pointing away are not followed (the chain is still judged)", c.verifier, forge(t, c, badChainWithURL, pl, nil), ErrChain},
		{"payload is not JSON", c.verifier, forge(t, c, hdr, []byte("not json"), nil), ErrMalformed},
		{"payload is a JSON array", c.verifier, forge(t, c, hdr, []byte(`[1,2,3]`), nil), ErrMalformed},
		{"payload field of the wrong type", c.verifier, forge(t, c, hdr, []byte(`{"n":["x"]}`), nil), ErrMalformed},
		{"empty payload part", c.verifier, forge(t, c, hdr, nil, nil), ErrMalformed},
		{"header is not JSON", c.verifier, b64([]byte("nope")) + "." + b64(pl) + "." + b64(make([]byte, 64)), ErrMalformed},
		{"header part with base64 padding", c.verifier, strings.Replace(forge(t, c, hdr, pl, nil), ".", "=.", 1), ErrMalformed},
		{"four parts", c.verifier, forge(t, c, hdr, pl, nil) + ".x", ErrMalformed},
		{"std-alphabet signature characters", c.verifier, forge(t, c, hdr, pl, nil)[:len(forge(t, c, hdr, pl, nil))-4] + "+/+/", ErrSignature},
	}
	for _, tc := range cases {
		err := tc.v.Verify(tc.jws, &probe{})
		if tc.name == "std-alphabet signature characters" {
			// a fresh forge is a different signature each time, so only the family is checked: refused, never accepted
			if err == nil {
				t.Errorf("%s: accepted", tc.name)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	// Unknown extra header fields (jku, x5u, kid, crit) are inert: a good chain and signature still verify and nothing is fetched.
	if err := c.verifier.Verify(forge(t, c, withExtra, pl, nil), &probe{}); err != nil {
		t.Errorf("extra header fields changed the verdict: %v", err)
	}
}

// A hostile body must be refused with a status code, quickly, without panicking.
func TestAdversarialNotificationBodies(t *testing.T) {
	c := newChain(t, chainOpts{})
	var outcomes []string
	h := &Handler{Verifier: c.verifier, Config: testConfig(), Observe: func(o string) { outcomes = append(outcomes, o) }}
	exp := time.Now().Add(30 * 24 * time.Hour)
	tok := uuid.NewString()

	nonSub := ti("rv-test-orig-1", exp, tok)
	nonSub.Type = "Consumable"
	nonRenew := ti("rv-test-orig-1", exp, tok)
	nonRenew.Type = "Non-Renewing Subscription"
	emptyEnv := ti("rv-test-orig-1", exp, tok)
	emptyEnv.Environment = ""
	lowerEnv := ti("rv-test-orig-1", exp, tok)
	lowerEnv.Environment = "production"
	noMarkers := newChain(t, chainOpts{noLeafMarker: true})

	payloadWith := func(extra map[string]any) string {
		p := map[string]any{"notificationType": "DID_RENEW", "notificationUUID": uuid.NewString(), "version": "2.0", "signedDate": time.Now().UnixMilli(),
			"data": map[string]any{"environment": EnvProduction, "bundleId": testBundle}}
		for k, v := range extra {
			if v == nil {
				delete(p, k)
			} else {
				p[k] = v
			}
		}
		b, _ := json.Marshal(map[string]string{"signedPayload": c.sign(t, p)})
		return string(b)
	}
	long := strings.Repeat("u", 256)
	huge := strings.Repeat("A", 70<<10)

	for name, body := range map[string]string{
		"a consumable transaction":                   notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), nonSub),
		"a non-renewing subscription transaction":    notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), nonRenew),
		"an empty environment":                       notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), emptyEnv),
		"an environment in the wrong case":           notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), lowerEnv),
		"a leaf without Apple's marker":              notification(noMarkers, t, "DID_RENEW", "", uuid.NewString(), time.Now(), ti("rv-test-orig-1", exp, tok)),
		"a notification uuid over 255 characters":    payloadWith(map[string]any{"notificationUUID": long}),
		"no notification type":                       payloadWith(map[string]any{"notificationType": ""}),
		"a notification type of the wrong JSON type": payloadWith(map[string]any{"notificationType": 7}),
		"signedPayload as a number":                  `{"signedPayload": 7}`,
		"signedPayload as an object":                 `{"signedPayload": {"a":1}}`,
		"signedPayload over the 64 KiB cap":          `{"signedPayload": "` + huge + `.` + huge + `.` + huge + `"}`,
		"a JSON array":                               `[]`,
		"JSON null":                                  `null`,
		"an empty body":                              ``,
		"only whitespace":                            "   \n",
		"a bundle in data that is not ours":          payloadWith(map[string]any{"data": map[string]any{"environment": EnvProduction, "bundleId": "dev.other.test"}}),
		"a data object of the wrong type":            payloadWith(map[string]any{"data": "x"}),
	} {
		outcomes = nil
		rec := post(h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}

	// A body over the 1 MiB cap is refused (400) without reading it all or counting an outcome.
	outcomes = nil
	over := `{"signedPayload":"` + strings.Repeat("a", MaxBodyBytes+10) + `"}`
	if rec := post(h, over); rec.Code != http.StatusBadRequest {
		t.Errorf("a body over the cap: %d, want 400", rec.Code)
	}
	if len(outcomes) != 0 {
		t.Errorf("a body over the cap counted outcomes %v", outcomes)
	}
}

func TestAdversarialTransactionBodies(t *testing.T) {
	c := newChain(t, chainOpts{})
	uid := uuid.New()
	h := &Transaction{Verifier: c.verifier, Config: testConfig()}
	exp := time.Now().Add(30 * 24 * time.Hour)
	call := func(body string) (code int) {
		// a request that passes every check reaches the database, which this unit test does not have: that panic means "accepted"
		defer func() {
			if recover() != nil {
				code = -1
			}
		}()
		rec := httptest.NewRecorder()
		h.Serve(uid, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(body)))
		return rec.Code
	}
	signed := func(x TransactionInfo) string {
		b, _ := json.Marshal(map[string]string{"signedTransaction": c.sign(t, x)})
		return string(b)
	}
	with := func(f func(*TransactionInfo)) TransactionInfo {
		x := ti("rv-test-orig-1", exp, uid.String())
		f(&x)
		return x
	}
	for name, body := range map[string]string{
		"the sandbox transaction on a production deployment": signed(with(func(x *TransactionInfo) { x.Environment = EnvSandbox })),
		"a product that is not sold":                         signed(with(func(x *TransactionInfo) { x.ProductID = "pro.other" })),
		"a consumable":                                       signed(with(func(x *TransactionInfo) { x.Type = "Consumable" })),
		"a non-consumable":                                   signed(with(func(x *TransactionInfo) { x.Type = "Non-Consumable" })),
		"a non-renewing subscription":                        signed(with(func(x *TransactionInfo) { x.Type = "Non-Renewing Subscription" })),
		"the nil uuid as the token":                          signed(with(func(x *TransactionInfo) { x.AppAccountToken = uuid.Nil.String() })),
		"the token with a space":                             signed(with(func(x *TransactionInfo) { x.AppAccountToken = " " + uid.String() })),
		"the token with a newline":                           signed(with(func(x *TransactionInfo) { x.AppAccountToken = uid.String() + "\n" })),
		"half the token":                                     signed(with(func(x *TransactionInfo) { x.AppAccountToken = uid.String()[:18] })),
		"another account's token, upper case":                signed(with(func(x *TransactionInfo) { x.AppAccountToken = strings.ToUpper(uuid.NewString()) })),
		"an empty bundle":                                    signed(with(func(x *TransactionInfo) { x.BundleID = "" })),
		"a bundle with different case":                       signed(with(func(x *TransactionInfo) { x.BundleID = strings.ToUpper(testBundle) })),
		"signedTransaction as a number":                      `{"signedTransaction": 7}`,
		"signedTransaction as an object":                     `{"signedTransaction": {}}`,
		"signedTransaction as null":                          `{"signedTransaction": null}`,
		"an empty signedTransaction":                         `{"signedTransaction": ""}`,
		"a JSON array":                                       `[]`,
		"an empty body":                                      ``,
		"a signedTransaction over the 16 KiB cap":            `{"signedTransaction": "` + strings.Repeat("a", MaxTransactionBytes+10) + `"}`,
		"a 16 KiB body of padding before the field":          `{"x": "` + strings.Repeat("a", MaxTransactionBytes) + `", "signedTransaction": "a.b.c"}`,
	} {
		if code := call(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
}
