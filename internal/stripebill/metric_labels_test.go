package stripebill

import "testing"

// The webhook's metric labels are a fixed set: a type Rivolt does not handle is "other", a delivery rejected before its type is known is
// "none", and every handled type is itself.
func TestMetricType(t *testing.T) {
	for in, want := range map[string]string{
		"": "none", "checkout.session.completed": "checkout.session.completed", "charge.refunded": "charge.refunded",
		"customer.subscription.updated": "customer.subscription.updated", "invoice.payment_failed": "other", "anything.else": "other",
	} {
		if got := MetricType(in); got != want {
			t.Errorf("MetricType(%q) = %q, want %q", in, got, want)
		}
	}
	for _, o := range []string{OutcomeApplied, OutcomeDuplicate, OutcomeIgnored, OutcomeUnmapped, OutcomeStale, OutcomeRejected} {
		found := false
		for _, m := range MetricOutcomes {
			found = found || m == o
		}
		if !found {
			t.Errorf("outcome %q is missing from MetricOutcomes", o)
		}
	}
}
