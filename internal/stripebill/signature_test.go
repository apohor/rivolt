package stripebill

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const testSecret = "whsec_test_secret_for_unit_tests"

var t0 = time.Unix(1790000000, 0)

func TestVerifySignatureAcceptsStripesScheme(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"ping"}`)
	if err := VerifySignature(body, Sign(body, testSecret, t0), testSecret, t0, SignatureTolerance); err != nil {
		t.Fatalf("a correct signature was refused: %v", err)
	}
	// Within the tolerance, either side of now.
	for _, d := range []time.Duration{-4 * time.Minute, 4 * time.Minute} {
		if err := VerifySignature(body, Sign(body, testSecret, t0), testSecret, t0.Add(d), SignatureTolerance); err != nil {
			t.Errorf("signature %v from now refused: %v", d, err)
		}
	}
	// While a secret is rolled Stripe sends two v1 signatures; either may match. A v0 (test-mode scheme) is never accepted on its own.
	good := Sign(body, testSecret, t0)
	rolled := "t=1790000000,v1=" + strings.Repeat("ab", 32) + "," + strings.SplitN(good, ",", 2)[1]
	if err := VerifySignature(body, rolled, testSecret, t0, SignatureTolerance); err != nil {
		t.Errorf("a header carrying the right v1 among others was refused: %v", err)
	}
	v0 := "t=1790000000,v0=" + strings.SplitN(good, "v1=", 2)[1]
	if err := VerifySignature(body, v0, testSecret, t0, SignatureTolerance); !errors.Is(err, ErrBadHeader) {
		t.Errorf("a v0-only header: %v, want ErrBadHeader", err)
	}
}

func TestVerifySignatureRefuses(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"customer.subscription.updated"}`)
	good := Sign(body, testSecret, t0)
	for _, c := range []struct {
		name   string
		body   []byte
		header string
		secret string
		now    time.Time
		want   error
	}{
		{"a tampered body", []byte(`{"id":"evt_1","type":"customer.subscription.deleted"}`), good, testSecret, t0, ErrNoValidSignature},
		{"the wrong secret", body, Sign(body, "whsec_other", t0), testSecret, t0, ErrNoValidSignature},
		{"a replay six minutes later", body, good, testSecret, t0.Add(6 * time.Minute), ErrStaleTimestamp},
		{"a timestamp in the future", body, good, testSecret, t0.Add(-6 * time.Minute), ErrStaleTimestamp},
		{"no secret configured", body, good, "", t0, ErrNoSecret},
		{"an empty header", body, "", testSecret, t0, ErrBadHeader},
		{"no signature", body, "t=1790000000", testSecret, t0, ErrBadHeader},
		{"no timestamp", body, "v1=" + strings.SplitN(good, "v1=", 2)[1], testSecret, t0, ErrBadHeader},
		{"a non-hex signature", body, "t=1790000000,v1=zzzz", testSecret, t0, ErrBadHeader},
		{"a non-numeric timestamp", body, "t=abc,v1=00", testSecret, t0, ErrBadHeader},
		{"a part with no equals sign", body, "t=1790000000,garbage", testSecret, t0, ErrBadHeader},
		{"the timestamp altered, signature kept", body, "t=1790000100," + strings.SplitN(good, ",", 2)[1], testSecret, t0, ErrNoValidSignature},
	} {
		if err := VerifySignature(c.body, c.header, c.secret, c.now, SignatureTolerance); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func fixture(t *testing.T, name string) Event {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var ev Event
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return ev
}

func TestSubscriptionFixturesParse(t *testing.T) {
	for _, c := range []struct {
		file     string
		status   string
		ok       bool
		cancels  bool
		endUnix  int64
		hasItems bool
	}{
		{"subscription_created_active.json", "active", true, false, 1821536000, false},
		{"subscription_created_trialing.json", "active", true, false, 1791209600, false},
		{"subscription_updated_cancel.json", "active", true, true, 1821536000, false},
		{"subscription_updated_past_due.json", "past_due", true, false, 1821536000, false},
		{"subscription_updated_items_end.json", "active", true, false, 1821536000, true}, // newer API versions: the end is on the item
		{"subscription_deleted.json", "ended", true, false, 1821536000, false},
		{"subscription_incomplete.json", "", false, false, 1821536000, false},
	} {
		var sub Subscription
		if err := json.Unmarshal(fixture(t, c.file).Data.Object, &sub); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		status, ok := EntitlementStatus(sub.Status)
		if ok != c.ok || (ok && status != c.status) {
			t.Errorf("%s: status %q ok=%v, want %q ok=%v", c.file, status, ok, c.status, c.ok)
		}
		if sub.WillCancel() != c.cancels {
			t.Errorf("%s: WillCancel = %v, want %v", c.file, sub.WillCancel(), c.cancels)
		}
		if got := sub.PeriodEnd().Unix(); got != c.endUnix {
			t.Errorf("%s: period end %d, want %d", c.file, got, c.endUnix)
		}
		if (len(sub.Items.Data) > 0 && sub.Items.Data[0].CurrentPeriodEnd != 0) != c.hasItems {
			t.Errorf("%s: item-level end = %v, want %v", c.file, !c.hasItems, c.hasItems)
		}
	}
	// cancel_at (the other way Stripe says "stops at a date") also counts.
	if !(Subscription{CancelAt: 1821536000}).WillCancel() {
		t.Error("a subscription with cancel_at set is not 'will cancel'")
	}
	if (Subscription{}).PeriodEnd().IsZero() == false {
		t.Error("a subscription with no end reads a time")
	}
}
