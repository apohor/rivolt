package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/trips"
)

// TripRecapKind is the engage kind of the saved-trip recap notification.
const TripRecapKind = "trip_recap"

const (
	// tripRecapMaxTrips bounds how many of the user's saved trips (most recently updated first) are
	// compared after a drive; the account cap is 100.
	tripRecapMaxTrips = 25
	// tripRecapFresh is how recent the arrival at the plan's destination must be.
	tripRecapFresh = 4 * time.Hour
)

// TripRecap sends the "You drove a saved trip" notification (Settings, Trip recap; off until the user
// turns it on). After a drive closes it compares the user's saved trips with what was driven (the
// plan-vs-actual matcher) and, for an outing that reached the plan's destination in the last few hours,
// sends one push with the planned and actual arrival charge and cost. It goes through engage: the
// account's toggle, quiet hours (09:00 to 20:00 local), the 4-per-30-days budget, and one claim per
// (saved trip, day, outing), so each outing is announced at most once across replicas and restarts. A
// recap held back by quiet hours or the budget is tried again only if a later drive closes within the
// freshness window. The trip's name never goes in the push; the tap opens its day-by-day.
type TripRecap struct {
	pa      *planActualAPI
	db      *sql.DB
	list    func(ctx context.Context, uid uuid.UUID) ([]trips.SavedTrip, error)
	enabled func(ctx context.Context, uid uuid.UUID) (bool, error)
	engage  *engage.Engage
	deliver func(ctx context.Context, uid uuid.UUID, p push.Payload) error
	now     func() time.Time
	log     *slog.Logger
}

// NewTripRecap wires the notifier over the system pool. deliver sends one payload to the user's devices.
func NewTripRecap(pool *sql.DB, tf *trips.Factory, sf *settings.Factory, e *engage.Engage,
	deliver func(ctx context.Context, uid uuid.UUID, p push.Payload) error, log *slog.Logger) *TripRecap {
	if log == nil {
		log = slog.Default()
	}
	return &TripRecap{
		pa: newPlanActualAPI(pool, tf, sf), db: pool, engage: e, deliver: deliver, now: time.Now, log: log,
		list: func(ctx context.Context, uid uuid.UUID) ([]trips.SavedTrip, error) { return tf.For(uid).List(ctx) },
		enabled: func(ctx context.Context, uid uuid.UUID) (bool, error) {
			var on bool
			err := pool.QueryRowContext(ctx, `SELECT trip_recap FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&on)
			if err == sql.ErrNoRows {
				return false, nil
			}
			return on, err
		},
	}
}

// OnDriveClose is the drive-close hook. Errors are logged, never returned: this is best effort.
func (n *TripRecap) OnDriveClose(ctx context.Context, uid uuid.UUID) {
	on, err := n.enabled(ctx, uid)
	if err != nil || !on {
		if err != nil {
			n.log.Warn("trip recap: read toggle failed", "err", err.Error())
		}
		return
	}
	saved, err := n.list(ctx, uid)
	if err != nil {
		n.log.Warn("trip recap: list saved trips failed", "err", err.Error())
		return
	}
	if len(saved) > tripRecapMaxTrips {
		saved = saved[:tripRecapMaxTrips]
	}
	for i := range saved {
		st := &saved[i]
		var subs []planSubject
		if days, multi := savedMultiday(st.Plan); multi {
			for d := 1; d <= len(days); d++ {
				if sub, ok := subjectForDay(days, d); ok {
					subs = append(subs, sub)
				}
			}
		} else {
			subs = append(subs, planSubject{inputs: st.Inputs, plan: st.Plan})
		}
		for _, sub := range subs {
			n.consider(ctx, uid, st, sub)
		}
	}
}

func (n *TripRecap) consider(ctx context.Context, uid uuid.UUID, st *trips.SavedTrip, sub planSubject) {
	out, err := n.pa.build(ctx, uid, st, sub)
	if err != nil || out.Comparison == nil || out.Comparison.Status != "complete" {
		return
	}
	// A linked outing, or a suggestion the matcher is confident about (build only compares a
	// suggestion when its band is high).
	var outing string
	switch {
	case out.Status == "linked" && out.Link != nil && !out.Link.LinkStale:
		outing = out.Link.TripID
	case out.Status == "suggested" && len(out.Candidates) > 0:
		outing = out.Candidates[0].TripID
	default:
		return
	}
	if age := n.now().Sub(out.Comparison.Leg.ArrivedAt); age < -time.Hour || age > tripRecapFresh {
		return
	}
	body, ok := TripRecapBody(*out.Comparison)
	if !ok {
		return
	}
	key := fmt.Sprintf("trip_recap:%s:%d:%s", st.ID, sub.day, outing)
	if len(key) > 100 {
		key = key[:100]
	}
	_, _ = n.engage.Send(ctx, engage.Msg{
		User: uid, Kind: TripRecapKind, Key: key, Channel: engage.Push,
		Toggle: func(ctx context.Context) (bool, error) { return n.enabled(ctx, uid) },
	}, func(ctx context.Context) error {
		return n.deliver(ctx, uid, push.TripRecapPayload(body, st.ID.String()))
	})
}

// TripRecapBody is the notification text: the arrival charge and the cost, planned against actual,
// both marked as estimates, or whichever of the two is known. It reports false when neither is.
func TripRecapBody(c ledger.PlanComparison) (string, bool) {
	metric := func(name string) *ledger.PlanMetric {
		for i := range c.Metrics {
			if c.Metrics[i].Metric == name {
				return &c.Metrics[i]
			}
		}
		return nil
	}
	var soc, cost string
	if m := metric("arrival_soc"); m != nil && m.Planned != nil && m.Actual != nil {
		soc = fmt.Sprintf("Arrived at %.0f%% vs %.0f%% planned", *m.Actual, *m.Planned)
	}
	if m := metric("cost"); m != nil && m.Planned != nil && m.Actual != nil && (m.ActualComplete == nil || *m.ActualComplete) {
		cost = fmt.Sprintf("%s vs %s", statement.Money(*m.Actual, c.Currency), statement.Money(*m.Planned, c.Currency))
	}
	switch {
	case soc != "" && cost != "":
		return soc + " · " + cost + " (estimates). Tap for the day-by-day.", true
	case soc != "":
		return soc + ". Tap for the day-by-day.", true
	case cost != "":
		return "Cost " + cost + " planned (estimates). Tap for the day-by-day.", true
	}
	return "", false
}
