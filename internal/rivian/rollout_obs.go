package rivian

import (
	"context"
	"time"
)

// What a deploy does to an open drive or charge is measured here, not guessed. When a pod takes a vehicle over (a roll, a crash, a
// rebalance) its recorder starts with no sessions and rehydrates them: from the Redis snapshot, for a charge also from its open DB row. If
// the vehicle is mid-session on the first frame and neither gave anything back, the recorder opens a NEW session and the trip or charge is
// split in two. Two events come out, through the observer main wires to the rivolt_session_* counters:
//
//	rehydrate: an open drive or charge was found at takeover, source snapshot or db;
//	rehydrate fresh + split: the vehicle was mid-session at takeover, nothing was found, and the previous row of that kind ended less than
//	                         splitGap before the new one began and no more than acquireSlack before the acquire (so it is the same trip or charge).
//
// A drive or charge that genuinely starts right after a takeover, with no previous row nearby or one that ended well before the hand-off, counts as nothing.
const (
	// takeoverWindow is how long after this pod acquired the vehicle's group a fresh session opening still counts as a takeover.
	takeoverWindow = 3 * time.Minute
	// splitGap is how close a previous row's end must be to the new session's start for it to count as a split of it.
	splitGap = 5 * time.Minute
	// acquireSlack: the previous row must also have ended no earlier than this before the acquire. A stop that ended well before the
	// hand-off (a coffee stop, then the roll) is a new drive, not a lost one.
	acquireSlack = 2 * time.Minute
)

// Rollout events and sources, the label values of the counters.
const (
	RolloutRehydrate = "rehydrate"
	RolloutSplit     = "split"
	KindDrive        = "drive"
	KindCharge       = "charge"
	SourceSnapshot   = "snapshot"
	SourceDB         = "db"
	SourceFresh      = "fresh"
)

// takeoverTrack is what is left to decide about one ownership of a vehicle: it lives on the ownership's liveSessions.
type takeoverTrack struct {
	at                       time.Time // when this pod acquired the vehicle (EnsureSubscribed), else its first frame
	driveSrc, chargeSrc      string    // set at rehydrate when the snapshot held the session
	driveDecided, chargeDone bool
}

// SetRolloutObserver wires the takeover counters: fn(event, kind, source) with event RolloutRehydrate (source snapshot, db or fresh) or
// RolloutSplit (source empty). Safe before Start.
func (m *StateMonitor) SetRolloutObserver(fn func(event, kind, source string)) { m.onRollout = fn }

func (m *StateMonitor) noteRollout(event, kind, source string) {
	if m.onRollout != nil {
		m.onRollout(event, kind, source)
	}
}

// acquiredAtOr is when this pod last began streaming the vehicle (EnsureSubscribed starts a subscription only on an acquire, so a vehicle
// that returns to this pod later in the same roll gets a new time), or now when it was never subscribed here (a frame pushed in a test).
func (m *StateMonitor) acquiredAtOr(vehicleID string) time.Time {
	m.mu.RLock()
	at, ok := m.acquiredAt[vehicleID]
	m.mu.RUnlock()
	if ok {
		return at
	}
	return m.clock()
}

// noteAcquired records the start of an ownership. Caller holds m.mu.
func (m *StateMonitor) noteAcquiredLocked(vehicleID string) {
	if m.acquiredAt == nil {
		m.acquiredAt = make(map[string]time.Time)
	}
	m.acquiredAt[vehicleID] = m.clock()
}

// beginTakeover starts tracking an ownership: called on the liveSessions that maybeRehydrate just returned, with a shared store wired.
func (m *StateMonitor) beginTakeover(vehicleID string, sess *liveSessions) {
	if sess == nil || m.liveStateStore == nil {
		return
	}
	t := &takeoverTrack{at: m.acquiredAtOr(vehicleID)}
	if sess.drive != nil {
		t.driveSrc = SourceSnapshot
	}
	if sess.charge != nil {
		t.chargeSrc = SourceSnapshot
	}
	sess.takeover = t
}

// noteTakeover runs after each lifecycle pass (caller holds sessMu) with the drive and charge as they were before it. At the first active
// pass of each kind in the takeover window it counts how the session came back, or hands a fresh opening to the split check.
func (m *StateMonitor) noteTakeover(vehicleID string, sess *liveSessions, driveBefore *liveDrive, chargeBefore *liveCharge) {
	t := sess.takeover
	if t == nil {
		return
	}
	if m.clock().Sub(t.at) > takeoverWindow || (t.driveDecided && t.chargeDone) {
		sess.takeover = nil
		return
	}
	if !t.driveDecided && sess.drive != nil {
		t.driveDecided = true
		switch {
		case t.driveSrc != "":
			m.noteRollout(RolloutRehydrate, KindDrive, t.driveSrc)
		case sess.drive != driveBefore:
			go m.classifyFresh(vehicleID, KindDrive, sess.drive.id, sess.drive.startedAt, t.at)
		}
	}
	if !t.chargeDone && sess.charge != nil {
		t.chargeDone = true
		switch {
		case t.chargeSrc != "":
			m.noteRollout(RolloutRehydrate, KindCharge, t.chargeSrc)
		case sess.charge != chargeBefore && sess.charge.resumes > 0:
			m.noteRollout(RolloutRehydrate, KindCharge, SourceDB)
		case sess.charge != chargeBefore:
			go m.classifyFresh(vehicleID, KindCharge, sess.charge.id, sess.charge.startedAt, t.at)
		}
	}
}

// classifyFresh counts a session opened at takeover with nothing to resume: a split when the previous row of that kind ended within splitGap
// of its start, else nothing (a genuinely new drive or charge). It reads the store, so it runs off the frame path.
func (m *StateMonitor) classifyFresh(vehicleID, kind, id string, startedAt, acquired time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	end, ok := m.previousSessionEnd(ctx, vehicleID, kind, id)
	if !ok || startedAt.Sub(end) > splitGap || end.Sub(startedAt) > splitGap || end.Before(acquired.Add(-acquireSlack)) {
		return
	}
	m.noteRollout(RolloutRehydrate, kind, SourceFresh)
	m.noteRollout(RolloutSplit, kind, "")
}

// previousSessionEnd is the end of the newest stored row of kind for the vehicle other than the session with the given id.
func (m *StateMonitor) previousSessionEnd(ctx context.Context, vehicleID, kind, exclude string) (time.Time, bool) {
	if m.prevEnd != nil {
		return m.prevEnd(ctx, vehicleID, kind, exclude)
	}
	switch kind {
	case KindDrive:
		if m.drivesStore == nil {
			return time.Time{}, false
		}
		rows, err := m.drivesStore.ListRecentForVehicle(ctx, vehicleID, 3, false)
		if err != nil {
			return time.Time{}, false
		}
		for _, r := range rows {
			if r.ID != exclude {
				return r.EndedAt, true
			}
		}
	case KindCharge:
		if m.chargesStore == nil {
			return time.Time{}, false
		}
		rows, err := m.chargesStore.ListRecentForVehicle(ctx, vehicleID, 3)
		if err != nil {
			return time.Time{}, false
		}
		for _, r := range rows {
			if r.ID != exclude {
				return r.EndedAt, true
			}
		}
	}
	return time.Time{}, false
}
