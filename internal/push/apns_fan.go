package push

import (
	"context"
	"errors"
	"sync"
	"time"
)

// WithAPNs turns on delivery to the iPhone app's installs. Chainable; a nil
// sender leaves it off.
func (s *Service) WithAPNs(a *APNs) *Service {
	s.apns = a
	return s
}

// APNsEnabled reports whether the iPhone app's push is on.
func (s *Service) APNsEnabled() bool { return s != nil && s.apns != nil }

// apnsSelector says whether a device's switches want a payload of the given kind.
func apnsSelector(kind string, p Preferences) bool {
	switch kind {
	case "charging_done":
		return p.OnChargingDone
	case "plug_in_reminder":
		return p.OnPlugInReminder
	case "anomaly":
		return p.OnAnomaly
	case "software_update":
		return p.OnSoftwareUpdate
	case "milestone":
		return p.OnMilestones
	}
	return false // test pings and anything new stay on the web until opted in here
}

// fanAPNs delivers p to the user's live app installs that have its kind on and
// returns how many were accepted by Apple. The text is stored once
// (push_notifications) and fetched by the app; Apple is sent only its id. A device
// Apple reports dead is removed. Failures are logged by reason, never by token, and
// never affect the web sends. Does nothing when APNs is off or no device wants the kind.
func (s *Service) fanAPNs(ctx context.Context, store *Store, p Payload) int {
	if s == nil || s.apns == nil || store == nil {
		return 0
	}
	devs, err := store.ListLiveAPNsDevices(ctx)
	if err != nil {
		s.logger.Warn("push: list app devices failed")
		return 0
	}
	var want []APNsDevice
	for _, d := range devs {
		if d.BundleID == s.apns.Topic() && apnsSelector(p.Kind, d.Prefs) {
			want = append(want, d)
		}
	}
	if len(want) == 0 {
		return 0
	}
	id, err := store.CreateNotification(ctx, p.Kind, p.Title, p.Body, p.URL)
	if err != nil {
		s.logger.Warn("push: store notification failed", "kind", p.Kind)
		return 0
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for _, d := range want {
		wg.Add(1)
		go func(d APNsDevice) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			err := s.apns.Send(sctx, d.Env, d.Token, id.String())
			if err == nil {
				store.touchAPNsDevice(ctx, d.ID)
				mu.Lock()
				ok++
				mu.Unlock()
				return
			}
			var ae *APNsError
			if errors.As(err, &ae) {
				if ae.Dead() {
					_ = store.deleteAPNsDeviceByID(ctx, d.ID)
				}
				s.logger.Warn("push: apns refused", "status", ae.Status, "reason", ae.Reason, "device", d.ID.String(), "dead", ae.Dead())
				return
			}
			s.logger.Warn("push: apns send failed", "device", d.ID.String())
		}(d)
	}
	wg.Wait()
	return ok
}
