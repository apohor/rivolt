package entitlements

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

// With billing off a Stripe subscription that is not over keeps the "manage" signal (the Customer Portal still works), and nothing else
// does: not an ended one, not another source, not billing on, not a server without Stripe configured.
func TestManageSubscription(t *testing.T) {
	uid := uuid.New()
	svc := func(billing, stripe bool, rows ...Row) *Service {
		s := New(&sql.DB{}, Config{Billing: false})
		s.billing = billing // New turns billing off without a working pool; the cache stands in for the table below
		s.cache[uid] = cached{st: rows, at: time.Now().Add(time.Hour)}
		s.stripeManage.Store(stripe)
		return s
	}
	end := time.Now().Add(30 * 24 * time.Hour)
	live := Row{Source: SourceStripe, Status: StatusActive, PeriodEnd: &end}
	for name, c := range map[string]struct {
		s    *Service
		want bool
	}{
		"billing off, live stripe sub":       {svc(false, true, live), true},
		"billing off, past due stripe sub":   {svc(false, true, Row{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: &end}), true},
		"billing off, cancelling stripe":     {svc(false, true, Row{Source: SourceStripe, Status: StatusActive, PeriodEnd: &end, CancelAtPeriodEnd: true}), true},
		"billing off, ended stripe sub":      {svc(false, true, Row{Source: SourceStripe, Status: StatusEnded, PeriodEnd: &end}), false},
		"billing off, only an apple sub":     {svc(false, true, Row{Source: SourceApple, Status: StatusActive, PeriodEnd: &end}), false},
		"billing off, only founding":         {svc(false, true, Row{Source: SourceFounding, Status: StatusActive, PeriodEnd: &end}), false},
		"billing off, no rows":               {svc(false, true), false},
		"billing on: the plan says it all":   {svc(true, true, live), false},
		"billing off, Stripe not configured": {svc(false, false, live), false},
	} {
		if got := c.s.ManageSubscription(context.Background(), uid); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	var nilSvc *Service
	if nilSvc.ManageSubscription(context.Background(), uid) {
		t.Error("a nil service reported a subscription")
	}
}
