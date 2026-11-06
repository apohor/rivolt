package stripebill

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The launch pre-flight's unsigned POST names itself and is counted as "probe", so a few pre-flights an hour do not trip RivoltStripeWebhookRejected
// (three refused in an hour with none applied). Everything that could be a real delivery, or a failure to verify one, is still "rejected".
func TestPreflightProbeIsCountedApartAndNothingElseIs(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		headers map[string]string
		want    string
	}{
		"the pre-flight's probe": {map[string]string{ProbeHeader: ProbeValue}, OutcomeProbe},
		"no header at all (a scanner, or a proxy that strips the signature)": {nil, OutcomeRejected},
		"a different probe value":                      {map[string]string{ProbeHeader: "other"}, OutcomeRejected},
		"the probe header with a signature that fails": {map[string]string{ProbeHeader: ProbeValue, "Stripe-Signature": "t=1,v1=bad"}, OutcomeRejected},
		"a failing signature alone":                    {map[string]string{"Stripe-Signature": "t=1,v1=bad"}, OutcomeRejected},
	} {
		var typ, outcome string
		var calls int
		h := &Handler{Secret: testSecret, Now: func() time.Time { return t0 }, Observe: func(ty, o string) { typ, outcome = ty, o; calls++ }}
		req := httptest.NewRequest(http.MethodPost, "/api/billing/stripe/webhook", strings.NewReader("{}"))
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (the pre-flight must still see a refusal)", name, rec.Code)
		}
		if calls != 1 || outcome != c.want || typ != "" {
			t.Errorf("%s: observed %d x (%q, %q), want one (\"\", %q)", name, calls, typ, outcome, c.want)
		}
	}
	found := false
	for _, o := range MetricOutcomes {
		if o == OutcomeProbe {
			found = true
		}
	}
	if !found {
		t.Error("MetricOutcomes lacks probe: its series would not exist at 0 at boot")
	}
}
