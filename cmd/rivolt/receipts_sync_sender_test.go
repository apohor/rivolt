package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type senderCalls struct{ local, peer int }

func testSender(held, known bool, groupOK bool, opted bool, c *senderCalls) *receiptsSyncSender {
	return &receiptsSyncSender{
		start:   func(_ string, fn func()) bool { fn(); return true },
		groupOf: func(context.Context, string) (string, bool, error) { return "g1", groupOK, nil },
		held:    func(context.Context, uuid.UUID) (bool, bool) { return held, known },
		local:   func(context.Context, uuid.UUID) { c.local++ },
		peer:    func(context.Context, string, string) bool { c.peer++; return true },
		optedIn: func(uuid.UUID) bool { return opted },
	}
}

// The pass runs here when this pod holds the group, goes to the owner otherwise, and goes nowhere when the user has no group yet.
func TestReceiptsSyncSenderRoutes(t *testing.T) {
	uid := uuid.New()
	for name, tc := range map[string]struct {
		held, group bool
		wantLocal   int
		wantPeer    int
	}{
		"holder runs it":  {held: true, group: true, wantLocal: 1},
		"non-owner sends": {group: true, wantPeer: 1},
		"no group":        {},
	} {
		var c senderCalls
		testSender(tc.held, true, tc.group, true, &c).run(uid)
		if c.local != tc.wantLocal || c.peer != tc.wantPeer {
			t.Errorf("%s: local %d peer %d, want %d and %d", name, c.local, c.peer, tc.wantLocal, tc.wantPeer)
		}
	}
}

// A sign-in asks only for a user who is already opted in.
func TestReceiptsSyncSenderSignedInNeedsOptIn(t *testing.T) {
	for _, opted := range []bool{false, true} {
		var c senderCalls
		s := testSender(false, true, true, opted, &c)
		s.SignedIn(context.Background(), uuid.New())
		if got := c.peer == 1; got != opted {
			t.Errorf("opted %v: peer calls %d", opted, c.peer)
		}
	}
}

// The send window decides before anything is looked up or pushed: once claimed, further requests for the user do nothing.
func TestReceiptsSyncSenderHonoursTheSendWindow(t *testing.T) {
	var c senderCalls
	s := testSender(false, true, true, true, &c)
	claimed := false
	s.once = func(context.Context, uuid.UUID) bool { won := !claimed; claimed = true; return won }
	uid := uuid.New()
	for i := 0; i < 5; i++ {
		s.run(uid)
	}
	if c.peer != 1 {
		t.Errorf("peer pushes %d, want 1", c.peer)
	}
}
