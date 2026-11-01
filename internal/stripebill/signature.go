// Package stripebill is Rivolt's side of Stripe: verifying and applying webhook events (this file and webhook.go) and, next to them, the
// hosted Checkout and Customer Portal sessions. Card data never touches Rivolt: Stripe's hosted pages take it. Everything here is built
// and tested against Stripe's documented event shapes and its documented signature scheme, offline; nothing needs the Stripe account.
package stripebill

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureTolerance is how far a signed timestamp may be from now (Stripe's libraries default to 5 minutes): a captured delivery cannot
// be replayed later.
const SignatureTolerance = 5 * time.Minute

// Errors VerifySignature returns. They are for logs; the response to Stripe is the same for all of them.
var (
	ErrNoSecret         = errors.New("stripe: webhook secret not configured")
	ErrBadHeader        = errors.New("stripe: malformed Stripe-Signature header")
	ErrNoValidSignature = errors.New("stripe: no valid signature for the payload")
	ErrStaleTimestamp   = errors.New("stripe: signature timestamp outside the tolerance")
)

// VerifySignature checks a Stripe-Signature header against the raw request body, as Stripe documents it: the header is
// "t=<unix>,v1=<hex>[,v1=<hex>...][,v0=...]"; the signed payload is "<t>.<body>"; each v1 is HMAC-SHA256 of it with the endpoint secret,
// hex-encoded. Any v1 matching passes (several are sent while a secret is being rolled); v0 (the test-mode scheme) is never accepted.
// The comparison is constant-time, the timestamp must be within tolerance of now, and the body must be the exact bytes received.
func VerifySignature(body []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	if secret == "" {
		return ErrNoSecret
	}
	var ts int64
	var haveTS bool
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrBadHeader
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrBadHeader
			}
			ts, haveTS = n, true
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil {
				return ErrBadHeader
			}
			sigs = append(sigs, b)
		}
	}
	if !haveTS || len(sigs) == 0 {
		return ErrBadHeader
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	matched := false
	for _, s := range sigs {
		if hmac.Equal(s, want) { // constant-time
			matched = true
		}
	}
	if !matched {
		return ErrNoValidSignature
	}
	// In whole seconds, not a Duration: a Duration holds about 292 years, and a timestamp further out than that wraps around to "fresh".
	n, tol := now.Unix(), int64(tolerance/time.Second)
	if ts > n+tol || ts < n-tol {
		return ErrStaleTimestamp
	}
	return nil
}

// Sign builds a Stripe-Signature header for a body (tests, and the local fake Stripe).
func Sign(body []byte, secret string, at time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	ts := strconv.FormatInt(at.Unix(), 10)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
