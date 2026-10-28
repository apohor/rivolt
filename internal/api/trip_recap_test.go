package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/trips"
)

func fp64(v float64) *float64 { return &v }
func bp(v bool) *bool         { return &v }

func recapCmp(arrivedAt time.Time, soc, cost bool) ledger.PlanComparison {
	c := ledger.PlanComparison{Status: "complete", Currency: "USD", Leg: ledger.PlanLeg{ArrivedAt: arrivedAt}}
	if soc {
		c.Metrics = append(c.Metrics, ledger.PlanMetric{Metric: "arrival_soc", Unit: "pct", Planned: fp64(24.4), Actual: fp64(31.6)})
	}
	if cost {
		c.Metrics = append(c.Metrics, ledger.PlanMetric{Metric: "cost", Unit: "USD", Planned: fp64(38.2), Actual: fp64(41.5), ActualComplete: bp(true)})
	}
	return c
}

func TestTripRecapBody(t *testing.T) {
	now := time.Now()
	both, ok := TripRecapBody(recapCmp(now, true, true))
	if !ok || both != "Arrived at 32% vs 24% planned · $41.50 vs $38.20 (estimates). Tap for the day-by-day." {
		t.Errorf("both = %q, %v", both, ok)
	}
	if b, ok := TripRecapBody(recapCmp(now, true, false)); !ok || b != "Arrived at 32% vs 24% planned. Tap for the day-by-day." {
		t.Errorf("soc only = %q, %v", b, ok)
	}
	if b, ok := TripRecapBody(recapCmp(now, false, true)); !ok || b != "Cost $41.50 vs $38.20 planned (estimates). Tap for the day-by-day." {
		t.Errorf("cost only = %q, %v", b, ok)
	}
	if _, ok := TripRecapBody(recapCmp(now, false, false)); ok {
		t.Error("nothing known should send nothing")
	}
	// An actual cost that is only partly known is left out rather than shown as a total.
	c := recapCmp(now, true, true)
	c.Metrics[1].ActualComplete = bp(false)
	if b, _ := TripRecapBody(c); strings.Contains(b, "$") {
		t.Errorf("an incomplete cost reached the push: %q", b)
	}
	// The saved trip's name is never in the push.
	p := push.TripRecapPayload(both, "abc")
	if p.Title != "You drove a saved trip" || p.Kind != "trip_recap" || p.URL != "/plan?trip=abc&tab=pva" {
		t.Errorf("payload = %+v", p)
	}
}

type recapEngageStore struct {
	mu      sync.Mutex
	claimed map[string]bool
}

func (f *recapEngageStore) Timezone(context.Context, uuid.UUID) (string, bool, bool, error) {
	return "UTC", true, true, nil
}
func (f *recapEngageStore) BudgetUsed(context.Context, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (f *recapEngageStore) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	if f.claimed[m.Key] {
		return false, false, nil
	}
	f.claimed[m.Key] = true
	return true, false, nil
}
func (f *recapEngageStore) Mark(context.Context, uuid.UUID, string, string, string, time.Time) error {
	return nil
}
func (f *recapEngageStore) RecordSkip(context.Context, uuid.UUID, string, string, engage.Channel, string, time.Time) (bool, error) {
	return true, nil
}
func (f *recapEngageStore) StuckSending(context.Context, time.Duration, time.Time) (int, error) {
	return 0, nil
}
func (f *recapEngageStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

func newRecapFixture(t *testing.T, cands []ledger.PlanCandidate, cmp ledger.PlanComparison, on bool, now time.Time) (*TripRecap, *[]push.Payload, *recapEngageStore) {
	t.Helper()
	pa, _, _ := newFakePA(cands)
	pa.compare = func(ledger.Plan, ledger.Input, string) (ledger.PlanComparison, bool) { return cmp, true }
	es := &recapEngageStore{}
	var sent []push.Payload
	n := &TripRecap{
		pa:      pa,
		list:    func(context.Context, uuid.UUID) ([]trips.SavedTrip, error) { return []trips.SavedTrip{*paSaved}, nil },
		enabled: func(context.Context, uuid.UUID) (bool, error) { return on, nil },
		engage:  &engage.Engage{Store: es, Now: func() time.Time { return now }},
		deliver: func(_ context.Context, _ uuid.UUID, p push.Payload) error { sent = append(sent, p); return nil },
		now:     func() time.Time { return now },
	}
	n.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return n, &sent, es
}

// The recap goes out once, only to a user who turned it on, only for a confident match that reached
// the destination in the last hours, and only inside the push hours.
func TestTripRecapOnDriveClose(t *testing.T) {
	noon := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	hi := []ledger.PlanCandidate{cand("trip_1", 0.9, "high")}
	uid := uuid.New()

	n, sent, _ := newRecapFixture(t, hi, recapCmp(noon.Add(-30*time.Minute), true, true), true, noon)
	n.OnDriveClose(context.Background(), uid)
	n.OnDriveClose(context.Background(), uid) // the next drive closes: same outing, same claim
	if len(*sent) != 1 || (*sent)[0].Title != "You drove a saved trip" || strings.Contains((*sent)[0].Body, "LA to Vegas") {
		t.Fatalf("sent = %+v", *sent)
	}

	off, sentOff, _ := newRecapFixture(t, hi, recapCmp(noon.Add(-30*time.Minute), true, true), false, noon)
	off.OnDriveClose(context.Background(), uid)
	if len(*sentOff) != 0 {
		t.Errorf("toggle off still sent: %+v", *sentOff)
	}

	old, sentOld, _ := newRecapFixture(t, hi, recapCmp(noon.Add(-9*time.Hour), true, true), true, noon)
	old.OnDriveClose(context.Background(), uid)
	if len(*sentOld) != 0 {
		t.Errorf("an old arrival sent: %+v", *sentOld)
	}

	medium, sentMed, _ := newRecapFixture(t, []ledger.PlanCandidate{cand("trip_1", 0.6, "medium")}, recapCmp(noon, true, true), true, noon)
	medium.OnDriveClose(context.Background(), uid)
	if len(*sentMed) != 0 {
		t.Errorf("a medium match sent: %+v", *sentMed)
	}

	inprog := recapCmp(noon, true, true)
	inprog.Status = "in_progress"
	ip, sentIP, _ := newRecapFixture(t, hi, inprog, true, noon)
	ip.OnDriveClose(context.Background(), uid)
	if len(*sentIP) != 0 {
		t.Errorf("an outing still under way sent: %+v", *sentIP)
	}

	night := time.Date(2026, 10, 20, 22, 30, 0, 0, time.UTC)
	q, sentQ, _ := newRecapFixture(t, hi, recapCmp(night.Add(-10*time.Minute), true, true), true, night)
	q.OnDriveClose(context.Background(), uid)
	if len(*sentQ) != 0 {
		t.Errorf("a push at 22:30 local went out: %+v", *sentQ)
	}
}
