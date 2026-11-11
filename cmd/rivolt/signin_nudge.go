package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
)

// signInNudge is the hook a successful Rivian sign-in calls (api.Deps.SignInNudge) so owner discovery looks the user up now instead of at its
// next 5-minute pass or after a 24 h "none" backoff. The pod that took the sign-in is not necessarily the owner of the user's group:
//   - it holds the group: nudge the local discovery job (Forwarder.Nudge refuses when the owner is self);
//   - another pod owns it: push the adopt nudge to that pod;
//   - nobody owns it yet: a newly linked, vehicle-less user is a group of one that the lead pass acquires up to a few seconds later, so ask
//     again a few times and then leave it to the owner's own pass.
//
// Best effort and detached from the request.
type signInNudge struct {
	groupOf func(ctx context.Context, uid string) (string, bool, error)
	held    func(ctx context.Context, uid uuid.UUID) (held, known bool)
	local   func(uuid.UUID)
	peer    func(ctx context.Context, uid, group string) bool
	log     *slog.Logger

	// tries and every bound the retry for a group nobody owns yet; the lead pass runs every 5 s.
	tries int
	every time.Duration
}

const (
	signInNudgeTries = 4
	signInNudgeEvery = 3 * time.Second
)

// newSignInNudge wires the nudge to the lease store, the hold predicate, this pod's discovery job and the forwarder (nil without RIVOLT_FORWARD:
// then only the local path works).
func newSignInNudge(groups *leases.Store, held func(context.Context, uuid.UUID) (bool, bool), fw *forward.Forwarder, log *slog.Logger) *signInNudge {
	n := &signInNudge{groupOf: groups.GroupOfUser, held: held, log: log,
		local: func(uid uuid.UUID) { ownerDiscoveryJob.Load().NudgeUser(uid) },
		peer:  func(context.Context, string, string) bool { return false }}
	if fw != nil {
		n.peer = func(ctx context.Context, uid, group string) bool { return fw.Nudge(ctx, uid, group) }
	}
	return n
}

func (n *signInNudge) Nudge(_ context.Context, uid uuid.UUID) {
	go n.run(uid)
}

func (n *signInNudge) run(uid uuid.UUID) {
	tries, every := n.tries, n.every
	if tries <= 0 {
		tries = signInNudgeTries
	}
	if every <= 0 {
		every = signInNudgeEvery
	}
	for i := 0; i < tries; i++ {
		if i > 0 {
			time.Sleep(every)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		done := n.once(ctx, uid)
		cancel()
		if done {
			return
		}
	}
	if n.log != nil {
		n.log.Debug("sign-in nudge: no owner for the user's group yet; the owner's own pass will look them up")
	}
}

// once reports whether the nudge reached an owner (this pod's job or a peer).
func (n *signInNudge) once(ctx context.Context, uid uuid.UUID) bool {
	group, ok, err := n.groupOf(ctx, uid.String())
	if err != nil || !ok {
		return false
	}
	if held, _ := n.held(ctx, uid); held {
		n.local(uid)
		return true
	}
	return n.peer(ctx, uid.String(), group)
}
