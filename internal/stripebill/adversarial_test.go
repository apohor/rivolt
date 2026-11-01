package stripebill

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Pro 29: adversarial inputs to the Stripe webhook that never reach the database (a signature, a size, a shape). Test data only: a made-up
// secret, made-up ids; no real account or money. The handler under test has a NIL database, so a delivery that got past these checks would
// panic: each case below must be refused before anything is applied.

// refused posts body with header to a handler that must refuse it without touching a database.
func refused(t *testing.T, name string, h *Handler, body []byte, header string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/billing/stripe/webhook", strings.NewReader(string(body)))
	if header != "" {
		req.Header.Set("Stripe-Signature", header)
	}
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: the delivery got past the checks and reached the database: %v", name, r)
		}
	}()
	h.ServeHTTP(rec, req)
	return rec
}

func nilDBHandler() *Handler {
	return &Handler{Secret: testSecret, Now: func() time.Time { return t0 }}
}

// A timestamp far enough in the future that its distance from now overflows a Duration must be refused as stale, not slip through the
// freshness window. (Only the holder of the secret can sign one, so this is no forgery; it is the freshness check being exact.)
func TestSignatureTimestampsOutsideADurationAreStale(t *testing.T) {
	body := []byte(`{"id":"evt_far","type":"ping"}`)
	for _, ts := range []int64{math.MaxInt64, math.MaxInt64 / 2, 1_000_000_000_000, 100_000_000_000, -1_000_000_000_000, math.MinInt64 / 2, math.MinInt64} {
		hdr := Sign(body, testSecret, time.Unix(ts, 0))
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); !errors.Is(err, ErrStaleTimestamp) {
			t.Errorf("timestamp %d: %v, want ErrStaleTimestamp", ts, err)
		}
	}
	// And the edge of the window itself: exactly at the tolerance is accepted, one second past it is not.
	for _, c := range []struct {
		off  time.Duration
		want error
	}{{SignatureTolerance, nil}, {-SignatureTolerance, nil}, {SignatureTolerance + time.Second, ErrStaleTimestamp}, {-SignatureTolerance - time.Second, ErrStaleTimestamp}} {
		hdr := Sign(body, testSecret, t0.Add(c.off))
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("offset %v: %v, want %v", c.off, err, c.want)
		}
	}
}

// A captured delivery's signature cannot be moved to a fresh timestamp by repeating the t parameter: the signed time is the one the header
// ends up with, and the signature covers it.
func TestACapturedSignatureCannotBeRetimedByRepeatingT(t *testing.T) {
	body := []byte(`{"id":"evt_cap","type":"customer.subscription.deleted"}`)
	old := t0.Add(-time.Hour)
	captured := Sign(body, testSecret, old) // t=<old>,v1=<sig over old.body>
	sig := strings.SplitN(captured, ",v1=", 2)[1]
	now := strconv.FormatInt(t0.Unix(), 10)
	for name, hdr := range map[string]string{
		"a fresh t after the old one":  captured + ",t=" + now,
		"a fresh t before the old one": "t=" + now + "," + captured,
		"a fresh t only, the old sig":  "t=" + now + ",v1=" + sig,
	} {
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); err == nil {
			t.Errorf("%s: the replay was accepted", name)
		}
	}
}

// A header crafted to be expensive or odd: thousands of signatures, huge values, stray whitespace, empty parts.
func TestOddSignatureHeadersAreRefusedQuicklyOrAcceptedOnlyWhenRight(t *testing.T) {
	body := []byte(`{"id":"evt_odd","type":"ping"}`)
	good := Sign(body, testSecret, t0)
	var many strings.Builder
	many.WriteString("t=" + strconv.FormatInt(t0.Unix(), 10))
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&many, ",v1=%064x", i)
	}
	start := time.Now()
	if err := VerifySignature(body, many.String(), testSecret, t0, SignatureTolerance); !errors.Is(err, ErrNoValidSignature) {
		t.Errorf("20,000 wrong signatures: %v, want ErrNoValidSignature", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("20,000 signatures took %v", d)
	}
	// The right one among many is still found.
	withRight := many.String() + "," + strings.SplitN(good, ",", 2)[1]
	if err := VerifySignature(body, withRight, testSecret, t0, SignatureTolerance); err != nil {
		t.Errorf("the right signature among 20,000 wrong ones: %v", err)
	}
	for name, hdr := range map[string]string{
		"an empty part":           good + ",",
		"an empty value":          "t=" + strconv.FormatInt(t0.Unix(), 10) + ",v1=",
		"a timestamp too large":   "t=99999999999999999999999,v1=00",
		"a negative timestamp":    "t=-5,v1=" + strings.SplitN(good, "v1=", 2)[1],
		"a signature with 0x":     "t=" + strconv.FormatInt(t0.Unix(), 10) + ",v1=0x" + strings.SplitN(good, "v1=", 2)[1],
		"an odd-length signature": "t=" + strconv.FormatInt(t0.Unix(), 10) + ",v1=abc",
		"an unknown scheme only":  "t=" + strconv.FormatInt(t0.Unix(), 10) + ",v2=" + strings.SplitN(good, "v1=", 2)[1],
		"a signature, no equals":  strings.Replace(good, "v1=", "v1", 1),
	} {
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Whitespace around the parts is harmless, as Stripe's own libraries allow.
	spaced := strings.ReplaceAll(good, ",", " , ")
	if err := VerifySignature(body, spaced, testSecret, t0, SignatureTolerance); err != nil {
		t.Errorf("a header with spaces around its parts: %v", err)
	}
}

// With no secret configured the endpoint refuses everything, even a delivery signed with an empty secret (fail closed).
func TestWithoutASecretEveryDeliveryIsRefused(t *testing.T) {
	h := &Handler{Secret: "", Now: func() time.Time { return t0 }}
	body := []byte(`{"id":"evt_nosecret","type":"customer.subscription.deleted","created":1790000001,"data":{"object":{}}}`)
	for name, hdr := range map[string]string{"signed with an empty secret": Sign(body, "", t0), "signed with a real one": Sign(body, testSecret, t0), "unsigned": ""} {
		if rec := refused(t, name, h, body, hdr); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
}

// A body over MaxBodyBytes is refused whole, however it is signed, before anything is parsed or applied; the cap is exact.
func TestAnOversizedBodyIsRefusedBeforeAnythingIsApplied(t *testing.T) {
	h := nilDBHandler()
	filler := func(n int) []byte {
		pre := `{"id":"evt_big","type":"ping","created":1790000001,"pad":"`
		suf := `"}`
		return []byte(pre + strings.Repeat("a", n-len(pre)-len(suf)) + suf)
	}
	over := filler(MaxBodyBytes + 1)
	if rec := refused(t, "one byte over, correctly signed", h, over, Sign(over, testSecret, t0)); rec.Code != http.StatusBadRequest {
		t.Errorf("one byte over the cap: %d, want 400", rec.Code)
	}
	huge := filler(8 << 20)
	if rec := refused(t, "8 MiB, correctly signed", h, huge, Sign(huge, testSecret, t0)); rec.Code != http.StatusBadRequest {
		t.Errorf("8 MiB: %d, want 400", rec.Code)
	}
	// The cap is on the bytes read, not on a Content-Length the sender states.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(over)))
	req.ContentLength = 10
	req.Header.Set("Stripe-Signature", Sign(over, testSecret, t0))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("a lying Content-Length reached the database: %v", r)
			}
		}()
		h.ServeHTTP(rec, req)
	}()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a lying Content-Length: %d, want 400", rec.Code)
	}
}

// A correctly signed body that is not an event is refused as a bad event (400, so Stripe does not retry it for days), before the database.
func TestSignedBodiesThatAreNotEventsAreRefused(t *testing.T) {
	h := nilDBHandler()
	for name, body := range map[string]string{
		"an empty body":                 ``,
		"null":                          `null`,
		"an array":                      `[{"id":"evt_1","type":"x"}]`,
		"a string":                      `"evt_1"`,
		"no id":                         `{"type":"customer.subscription.deleted"}`,
		"no type":                       `{"id":"evt_1"}`,
		"an empty id":                   `{"id":"","type":"customer.subscription.deleted"}`,
		"a number for the id":           `{"id":5,"type":"customer.subscription.deleted"}`,
		"truncated JSON":                `{"id":"evt_1","type":"customer.subscr`,
		"trailing garbage after a JSON": `{"id":"evt_1","type":"x"} {"id":"evt_2"`,
	} {
		b := []byte(body)
		if rec := refused(t, name, h, b, Sign(b, testSecret, t0)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
}
