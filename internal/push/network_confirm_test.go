package push

import (
	"strings"
	"testing"
	"time"
)

func TestNetworkConfirmPayloadWordings(t *testing.T) {
	cases := []struct {
		name string
		e    NetworkConfirm
		want string
	}{
		{"estimated", NetworkConfirm{ChargeID: "c1", Network: "Tesla Supercharger", Cost: 16.2, Rate: 0.4, Matched: true, State: "estimated"},
			"Fast charge at Tesla Supercharger priced $16.20 ($0.40/kWh). Confirm or change"},
		{"colocated", NetworkConfirm{ChargeID: "c1", Network: "Tesla Supercharger", Other: "Electrify America", Cost: 16.2, Matched: true, State: "needs_confirmation"},
			"Fast charge priced $16.20 as Tesla Supercharger; Electrify America is here too. Confirm or change"},
		{"unmatched", NetworkConfirm{ChargeID: "c1", KWh: 41.6},
			"Fast charge (42 kWh) couldn't be matched to a network. Pick one"},
	}
	for _, c := range cases {
		p := NetworkConfirmPayload(c.e)
		if p.Body != c.want || p.URL != "/charges/c1#pricing" || p.Kind != "network_confirm" {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
}

func TestConfirmLimiter(t *testing.T) {
	l := newConfirmLimiter()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < confirmPerWindow; i++ {
		if send, _ := l.take("u"); !send {
			t.Fatalf("send %d denied", i)
		}
	}
	// First overflow yields the summary; later ones are silent.
	if send, sum := l.take("u"); send || !sum {
		t.Errorf("overflow 1 = %v %v, want summary", send, sum)
	}
	if send, sum := l.take("u"); send || sum {
		t.Errorf("overflow 2 = %v %v, want silent", send, sum)
	}
	// Other users are independent; the window rolls over.
	if send, _ := l.take("v"); !send {
		t.Error("other user limited")
	}
	now = now.Add(confirmWindow + time.Minute)
	if send, _ := l.take("u"); !send {
		t.Error("window did not roll over")
	}
}

func TestCandidateOther(t *testing.T) {
	if got := CandidateOther("Tesla Supercharger", []string{"tesla supercharger", " Electrify America "}); got != "Electrify America" {
		t.Errorf("got %q", got)
	}
	if got := CandidateOther("A", []string{"a"}); got != "" || strings.Contains(got, "a") {
		t.Errorf("got %q", got)
	}
}
