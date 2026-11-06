package stripebill

import (
	"encoding/json"
	"testing"
)

func TestSubscriptionPeriodStart(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want int64
	}{
		{"on the subscription (older API versions)", `{"current_period_start": 1821536000}`, 1821536000},
		{"on the item (newer API versions)", `{"items":{"data":[{"current_period_start": 1821536000}]}}`, 1821536000},
		{"the subscription's own wins", `{"current_period_start": 5, "items":{"data":[{"current_period_start": 9}]}}`, 5},
		{"absent", `{}`, 0},
	} {
		var sub Subscription
		if err := json.Unmarshal([]byte(c.body), &sub); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := sub.PeriodStart()
		if c.want == 0 && !got.IsZero() || c.want != 0 && got.Unix() != c.want {
			t.Errorf("%s: PeriodStart = %v, want %d", c.name, got, c.want)
		}
	}
}
