package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/receipts"
)

// receiptsSyncSender asks the owner of a user's group for one receipts summaries pass right now (first-screen design §4), so card B fills in
// minutes instead of waiting for the owner's 30-minute sweep. It runs only with RIVOLT_FIRST_SCREEN=1. Senders: the opt-in handler, and a
// Rivian sign-in by a user who is already opted in.
//   - this pod holds the group: run the pass here (SyncOnRequest, summaries only);
//   - another pod owns it: push `receipts_sync` to that pod, no reply awaited;
//   - nobody owns it, or the push is lost: nothing is retried; the owner's sweep picks the user up.
//
// The opt-in handler never calls Rivian: the pass is detached from the request. However often a user toggles, the Postgres claim in the
// syncer caps Rivian at one summaries call per user per 30 minutes.
type receiptsSyncSender struct {
	start   func(name string, fn func()) bool // role.start: runs the pass detached (startAlways)
	groupOf func(ctx context.Context, uid string) (string, bool, error)
	held    func(ctx context.Context, uid uuid.UUID) (held, known bool)
	local   func(ctx context.Context, uid uuid.UUID)
	peer    func(ctx context.Context, uid, group string) bool
	optedIn func(uuid.UUID) bool
	log     *slog.Logger
}

// receiptsSyncTimeout bounds one sender run; it matches the receiver's budget for a summaries-only pass.
const receiptsSyncTimeout = 25 * time.Second

// newReceiptsSyncSender wires the sender to the lease store, the hold predicate, this pod's syncer and the forwarder (nil without
// RIVOLT_FORWARD: then only the local path works).
func newReceiptsSyncSender(role rolePlan, groups *leases.Store, held func(context.Context, uuid.UUID) (bool, bool), fw *forward.Forwarder, syncer *receipts.Syncer, optedIn func(uuid.UUID) bool, log *slog.Logger) *receiptsSyncSender {
	s := &receiptsSyncSender{start: role.start, groupOf: groups.GroupOfUser, held: held, optedIn: optedIn, log: log,
		local: func(ctx context.Context, uid uuid.UUID) {
			//rivolt:transient the owner pod's one summaries pass for this user, claim-capped; detached from the request that asked
			if _, err := syncer.SyncOnRequest(ctx, uid); err != nil && log != nil {
				log.Warn("receipts sync on request failed", "user_id", uid.String(), "err", err.Error())
			}
		},
		peer: func(context.Context, string, string) bool { return false }}
	if fw != nil {
		s.peer = func(ctx context.Context, uid, group string) bool { return fw.ReceiptsSync(ctx, uid, group) }
	}
	return s
}

// Request is the opt-in handler's hook (api.Deps.ReceiptsSyncNow).
func (s *receiptsSyncSender) Request(uid uuid.UUID) {
	s.start("receipts_sync_push", func() { s.run(uid) })
}

// SignedIn is the sign-in hook: it asks only for a user who is already opted in.
func (s *receiptsSyncSender) SignedIn(_ context.Context, uid uuid.UUID) {
	if s.optedIn != nil && s.optedIn(uid) {
		s.Request(uid)
	}
}

func (s *receiptsSyncSender) run(uid uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), receiptsSyncTimeout)
	defer cancel()
	group, ok, err := s.groupOf(ctx, uid.String())
	if err != nil || !ok {
		return
	}
	if held, _ := s.held(ctx, uid); held {
		s.local(ctx, uid)
		return
	}
	s.peer(ctx, uid.String(), group)
}
