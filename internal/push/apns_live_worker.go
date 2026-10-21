package push

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// LiveSource is where a charging truck's figures come from: the recorder's latest
// vehicle state. Charging reports the charge in progress (charge percent, power in
// kW, minutes to the charge target) and ok=false when the truck is not charging or
// the figures are stale.
type LiveSource interface {
	Charging(uid uuid.UUID, rivianVehicleID string) (socPct, powerKW, minutesToTarget float64, ok bool)
}

// LiveSourceFunc adapts a function to LiveSource.
type LiveSourceFunc func(uid uuid.UUID, rivianVehicleID string) (float64, float64, float64, bool)

// Charging implements LiveSource.
func (f LiveSourceFunc) Charging(uid uuid.UUID, rivianVehicleID string) (float64, float64, float64, bool) {
	return f(uid, rivianVehicleID)
}

// LiveInterval is how often the worker looks for charging trucks that have a
// running Live Activity. A send still waits for LiveMinInterval per activity.
const LiveInterval = time.Minute

type liveTarget struct {
	userID    uuid.UUID
	vehicleID uuid.UUID
	rivianID  string
}

// liveTargets lists the (user, vehicle) pairs that have a registered activity.
// Nothing is listed until some iPhone has turned its Live Activity switch on and
// started one, so a server without them does no work.
func liveTargets(ctx context.Context, db *sql.DB) ([]liveTarget, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT a.user_id, a.vehicle_id, v.rivian_vehicle_id
		  FROM push_apns_activities a JOIN vehicles v ON v.id = a.vehicle_id
		 WHERE a.started_at > now() - make_interval(secs => $1)`, ActivityTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []liveTarget
	for rows.Next() {
		var t liveTarget
		if err := rows.Scan(&t.userID, &t.vehicleID, &t.rivianID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TickLive sends one round of routine updates: for each truck with a running
// activity that is charging now, the current figures go to its activities (each at
// most once per LiveMinInterval). It returns how many Apple accepted.
func (s *Service) TickLive(ctx context.Context, db *sql.DB, f *Factory, src LiveSource) int {
	if !s.LiveEnabled() || db == nil || f == nil || src == nil {
		return 0
	}
	targets, err := liveTargets(ctx, db)
	if err != nil {
		s.logger.Warn("push: live targets failed")
		return 0
	}
	n := 0
	for _, t := range targets {
		soc, kw, mins, ok := src.Charging(t.userID, t.rivianID)
		if !ok {
			continue
		}
		n += s.UpdateLive(ctx, f.For(t.userID), t.vehicleID, NewLiveState(soc, kw, mins))
	}
	return n
}

// RunLive calls TickLive every `every` until ctx ends.
func (s *Service) RunLive(ctx context.Context, db *sql.DB, f *Factory, src LiveSource, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			s.TickLive(tctx, db, f, src)
			cancel()
		}
	}
}

// EndLiveForCharge is the charge-closed hook: it sends the final state with the
// end event to the truck's running activities and forgets them. The figures are the
// charge's end percent, no power and no time left.
func (s *Service) EndLiveForCharge(ctx context.Context, f *Factory, userID uuid.UUID, rivianVehicleID string, endSoCPct float64) int {
	if !s.LiveEnabled() || f == nil {
		return 0
	}
	store := f.For(userID)
	if store == nil {
		return 0
	}
	vid, err := store.vehicleUUID(ctx, rivianVehicleID)
	if err != nil || vid == uuid.Nil {
		return 0
	}
	return s.EndLive(ctx, store, vid, NewLiveState(endSoCPct, 0, 0))
}
