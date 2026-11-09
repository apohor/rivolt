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

// Prior is how a group came free before this pod took it: why, and (for a release) when the previous holder released it, which is the
// start of the hand-off gap. Released is zero for an expired takeover.
type Prior struct {
	Reason   string
	Released time.Time
	taken    time.Time // when this pod took the group
}

// priorFresh is how long after taking a group its prior still describes a vehicle acquired from it.
const priorFresh = 2 * time.Minute

// notePrior records how a just-acquired group came free. Caller holds groupMu.
func (s *Store) notePrior(group string, expired bool, released time.Time, reason string) {
	if s.priorOf == nil {
		s.priorOf = map[string]Prior{}
	}
	switch {
	case expired:
		s.priorOf[group] = Prior{Reason: ReasonExpired, taken: time.Now()}
	case !released.IsZero():
		s.priorOf[group] = Prior{Reason: reason, Released: released, taken: time.Now()}
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
