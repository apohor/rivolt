package leases

import "time"

// Why a group changed hands, for rivolt_rollout_handoffs_total. The release row does not say who let go of it or why, so the reason is
// inferred when it is taken: a peer that is draining means a drain; a group released while no peer drains is a rebalance (fair-share shed
// or a retired candidate); a group taken from a holder that never released it (its lease expired) is expired.
const (
	ReasonDrain     = "drain"
	ReasonExpired   = "expired"
	ReasonRebalance = "rebalance"
	ReasonOther     = "other"
)

// maxHandoffWait: a group released longer ago than this was not handed off, it was a candidate that came back. It is counted (reason other)
// but has no wait or gap, or one such group would wreck the percentile.
const maxHandoffWait = 10 * time.Minute

// Prior is how a group came free before this pod took it: why, and (for a release) how long it sat free, measured on the database's clock:
// the wait between the previous holder's release and this acquire. The gap the plan gates on is that wait plus the time from the acquire to
// the first frame (this pod's clock), two same-clock parts.
type Prior struct {
	Reason  string
	Wait    time.Duration
	HasWait bool
	taken   time.Time // when this pod took the group
}

// priorFresh is how long after taking a group its prior still describes a vehicle acquired from it.
const priorFresh = 2 * time.Minute

// notePrior records how a just-acquired group came free. Caller holds groupMu.
func (s *Store) notePrior(group string, expired bool, hasWait bool, wait time.Duration, reason string) {
	if s.priorOf == nil {
		s.priorOf = map[string]Prior{}
	}
	switch {
	case expired:
		s.priorOf[group] = Prior{Reason: ReasonExpired, taken: time.Now()}
	case hasWait && wait > maxHandoffWait:
		s.priorOf[group] = Prior{Reason: ReasonOther, taken: time.Now()}
	case hasWait:
		s.priorOf[group] = Prior{Reason: reason, Wait: wait, HasWait: true, taken: time.Now()}
	default:
		delete(s.priorOf, group) // a group nobody held before (first acquire): not a hand-off
	}
}

// TakePrior returns, once, how the vehicle's group came free when this pod took it. ok is false when it was not a hand-off or was
// already taken.
func (s *Store) TakePrior(vehicleID string) (Prior, bool) {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	g, ok := s.vehicleGroup[vehicleID]
	if !ok {
		return Prior{}, false
	}
	p, ok := s.priorOf[g]
	if ok {
		delete(s.priorOf, g)
	}
	if ok && time.Since(p.taken) > priorFresh {
		return Prior{}, false
	}
	return p, ok
}
