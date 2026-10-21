package push

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The charging Live Activity: Apple shows it on the lock screen and the server
// updates it through the activity's own push token. Unlike the alert push, a Live
// Activity update carries its content in the payload (there is no extension hook
// for it), so the payload is a fixed struct of three numbers and nothing else:
// no strings, no location, no names. DA's conditions, pinned by test.

// LiveState is the whole content of a Live Activity update.
type LiveState struct {
	SoCPct          int     `json:"soc_pct"`
	PowerKW         float64 `json:"power_kw"`
	MinutesToTarget int     `json:"minutes_to_target"`
}

// NewLiveState clamps the figures to what the lock screen can show: charge 0-100,
// power at least 0 with one decimal, minutes at least 0.
func NewLiveState(socPct, powerKW, minutesToTarget float64) LiveState {
	return LiveState{
		SoCPct:          int(math.Round(math.Min(math.Max(socPct, 0), 100))),
		PowerKW:         math.Round(math.Max(powerKW, 0)*10) / 10,
		MinutesToTarget: int(math.Round(math.Max(minutesToTarget, 0))),
	}
}

// Live Activity events.
const (
	LiveUpdate = "update"
	LiveEnd    = "end"
)

// liveTopicSuffix is appended to the app's bundle id for Live Activity pushes.
const liveTopicSuffix = ".push-type.liveactivity"

// LiveMinInterval is the least time between two routine updates to one activity
// (Apple budgets Live Activity updates; at most 12 an hour per activity). The start
// and the end are not routine and bypass it.
const LiveMinInterval = 5 * time.Minute

// liveAPS and liveEnvelope are the whole payload: aps with a timestamp, the event
// and the content state. Nothing else is added.
type liveAPS struct {
	Timestamp    int64     `json:"timestamp"`
	Event        string    `json:"event"`
	ContentState LiveState `json:"content-state"`
}

type liveEnvelope struct {
	APS liveAPS `json:"aps"`
}

func liveBody(event string, at time.Time, st LiveState) []byte {
	b, _ := json.Marshal(liveEnvelope{APS: liveAPS{Timestamp: at.Unix(), Event: event, ContentState: st}})
	return b
}

// SendLive delivers one Live Activity update (event LiveUpdate or LiveEnd) to one
// activity token. Routine updates go at low priority and the end at high, as Apple
// advises. An *APNsError comes back for Apple's refusals; Dead() says the token is
// gone.
func (a *APNs) SendLive(ctx context.Context, env, activityToken, event string, st LiveState) error {
	if !ValidActivityToken(activityToken) {
		return &APNsError{Status: http.StatusBadRequest, Reason: "BadDeviceToken"}
	}
	if event != LiveUpdate && event != LiveEnd {
		return errors.New("apns: unknown live event")
	}
	prio := "5"
	if event == LiveEnd {
		prio = "10"
	}
	return a.deliver(ctx, env, activityToken, apnsRequest{
		pushType: "liveactivity", topic: a.cfg.Topic + liveTopicSuffix, priority: prio,
		body: liveBody(event, a.now(), st), ttl: time.Hour,
	})
}

// WithLive turns on Live Activity updates (RIVOLT_APNS_LIVE). Off by default; it
// needs APNs to be on as well. Chainable.
func (s *Service) WithLive(on bool) *Service {
	s.live = on
	return s
}

// LiveEnabled reports whether Live Activity updates are sent.
func (s *Service) LiveEnabled() bool { return s != nil && s.apns != nil && s.live }

// UpdateLive sends a routine update to the user's running activities on the
// vehicle whose device switch is on, at most one per LiveMinInterval each, and
// returns how many Apple accepted. A dead token is forgotten.
func (s *Service) UpdateLive(ctx context.Context, store *Store, vehicleID uuid.UUID, st LiveState) int {
	return s.sendLive(ctx, store, vehicleID, LiveUpdate, st)
}

// EndLive sends the final state with the end event to the vehicle's running
// activities and forgets them, whatever Apple answers (the charge is over).
func (s *Service) EndLive(ctx context.Context, store *Store, vehicleID uuid.UUID, st LiveState) int {
	return s.sendLive(ctx, store, vehicleID, LiveEnd, st)
}

func (s *Service) sendLive(ctx context.Context, store *Store, vehicleID uuid.UUID, event string, st LiveState) int {
	if !s.LiveEnabled() || store == nil {
		return 0
	}
	acts, err := store.ListLiveActivities(ctx, vehicleID)
	if err != nil {
		s.logger.Warn("push: list live activities failed")
		return 0
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for _, a := range acts {
		if a.BundleID != s.apns.Topic() {
			continue
		}
		if event == LiveUpdate {
			claimed, err := store.claimActivityUpdate(ctx, a.ID, LiveMinInterval)
			if err != nil || !claimed {
				continue
			}
		}
		wg.Add(1)
		go func(a Activity) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			err := s.apns.SendLive(sctx, a.Env, a.Token, event, st)
			if event == LiveEnd {
				_ = store.deleteActivityByID(ctx, a.ID)
			}
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
				return
			}
			var ae *APNsError
			if errors.As(err, &ae) {
				if ae.Dead() && event != LiveEnd {
					_ = store.deleteActivityByID(ctx, a.ID)
				}
				s.logger.Warn("push: apns live refused", "status", ae.Status, "reason", ae.Reason, "activity", a.ID.String(), "event", event)
				return
			}
			s.logger.Warn("push: apns live send failed", "activity", a.ID.String(), "event", event)
		}(a)
	}
	wg.Wait()
	return ok
}
