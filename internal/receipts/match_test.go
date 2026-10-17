package receipts

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)

func chg(startMin, endMin int, kwh float64) ChargeRef {
	return ChargeRef{ID: uuid.New(), StartedAt: t0.Add(time.Duration(startMin) * time.Minute), EndedAt: t0.Add(time.Duration(endMin) * time.Minute), EnergyKWh: kwh}
}

func rcpt(startMin, endMin int, kwh float64) Receipt {
	r := Receipt{ID: uuid.New(), StartedAt: t0.Add(time.Duration(startMin) * time.Minute), EndedAt: t0.Add(time.Duration(endMin) * time.Minute)}
	if kwh > 0 {
		r.EnergyKWh = &kwh
	}
	return r
}

func TestMatchWindowAndEnergy(t *testing.T) {
	c := chg(0, 60, 40)
	cases := []struct {
		name string
		r    Receipt
		want bool
	}{
		{"same window and energy", rcpt(1, 59, 40.4), true},
		{"edge of the 5 minute window", rcpt(5, 65, 41), true},
		{"just outside on both ends", rcpt(6, 67, 40), false},
		{"times match, energy off by 20 percent", rcpt(0, 60, 48), false},
		{"times match, no receipt energy", rcpt(0, 60, 0), true},
		{"only the start matches but energy agrees (long trickle)", rcpt(2, 90, 40), true},
		{"only the start matches and energy is unknown", rcpt(2, 90, 0), false},
		{"a different day", rcpt(60*24, 60*25, 40), false},
	}
	for _, tc := range cases {
		got := len(Match([]ChargeRef{c}, []Receipt{tc.r})) == 1
		if got != tc.want {
			t.Errorf("%s: matched = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Two back-to-back sessions: each receipt pairs with the closer charge,
// one to one, whichever order they are given in.
func TestMatchIsOneToOneAndPicksTheClosest(t *testing.T) {
	c1, c2 := chg(0, 30, 10), chg(34, 60, 10)
	r1, r2 := rcpt(1, 31, 10), rcpt(35, 59, 10)
	pairs := Match([]ChargeRef{c2, c1}, []Receipt{r2, r1})
	got := map[uuid.UUID]uuid.UUID{}
	for _, p := range pairs {
		got[p.ReceiptID] = p.ChargeID
	}
	if len(pairs) != 2 || got[r1.ID] != c1.ID || got[r2.ID] != c2.ID {
		t.Fatalf("pairs = %+v", pairs)
	}
	// One charge, two receipts in range: only the closer pairs.
	pairs = Match([]ChargeRef{c1}, []Receipt{rcpt(4, 28, 10), rcpt(1, 31, 10)})
	if len(pairs) != 1 {
		t.Fatalf("one charge matched %d receipts", len(pairs))
	}
}

func TestMatchSkipsAlreadyMatchedReceipts(t *testing.T) {
	c := chg(0, 60, 40)
	r := rcpt(0, 60, 40)
	id := uuid.New()
	r.ChargeID = &id
	if got := Match([]ChargeRef{c}, []Receipt{r}); len(got) != 0 {
		t.Fatalf("matched an already matched receipt: %+v", got)
	}
}
