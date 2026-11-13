package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func nudgeFor(group string, hasGroup, held *atomic.Bool, local, peer *atomic.Int32) *signInNudge {
	return &signInNudge{
		groupOf: func(context.Context, string) (string, bool, error) { return group, hasGroup.Load(), nil },
		held:    func(context.Context, uuid.UUID) (bool, bool) { return held.Load(), true },
		local:   func(uuid.UUID) { local.Add(1) },
		peer:    func(context.Context, string, string) bool { peer.Add(1); return true },
		tries:   3, every: time.Millisecond,
	}
}

// The owner's own sign-in nudges the local job; a sign-in on another pod pushes to the owner; a user with no group is left alone.
func TestSignInNudgeGoesToTheOwner(t *testing.T) {
	var hasGroup, held atomic.Bool
	var local, peer atomic.Int32
	n := nudgeFor("g1", &hasGroup, &held, &local, &peer)
	uid := uuid.New()

	if n.once(context.Background(), uid) || local.Load()+peer.Load() != 0 {
		t.Fatal("a user with no group was nudged")
	}
	hasGroup.Store(true)
	held.Store(true)
	if !n.once(context.Background(), uid) || local.Load() != 1 || peer.Load() != 0 {
		t.Fatalf("held: local %d peer %d, want 1 and 0", local.Load(), peer.Load())
	}
	held.Store(false)
	if !n.once(context.Background(), uid) || local.Load() != 1 || peer.Load() != 1 {
		t.Fatalf("not held: local %d peer %d, want 1 and 1", local.Load(), peer.Load())
	}
}

// A newly linked user's group has no owner until the next lead pass: the nudge asks again until there is one.
func TestSignInNudgeRetriesUntilTheGroupHasAnOwner(t *testing.T) {
	var hasGroup, held atomic.Bool
	var local, peer atomic.Int32
	hasGroup.Store(true)
	var asks atomic.Int32
	n := nudgeFor("g1", &hasGroup, &held, &local, &peer)
	n.peer = func(context.Context, string, string) bool { return asks.Add(1) == 3 }
	n.run(uuid.New())
	if asks.Load() != 3 {
		t.Errorf("asked the owner %d times, want 3 (the third succeeds)", asks.Load())
	}
	asks.Store(0)
	n.peer = func(context.Context, string, string) bool { asks.Add(1); return false }
	n.run(uuid.New())
	if asks.Load() != 3 {
		t.Errorf("without an owner it asked %d times, want the 3 tries and no more", asks.Load())
	}
}

// run reports a nudge that never reached an owner (NudgeNotify turns that into the lost callback).
func TestSignInNudgeRunReportsALostNudge(t *testing.T) {
	var hasGroup, held atomic.Bool
	var local, peer atomic.Int32
	hasGroup.Store(true)
	n := nudgeFor("g1", &hasGroup, &held, &local, &peer)
	n.tries, n.every = 2, time.Millisecond
	n.peer = func(context.Context, string, string) bool { return false }
	if n.run(uuid.New()) {
		t.Fatal("run reported delivery without an owner")
	}
	n.peer = func(context.Context, string, string) bool { return true }
	if !n.run(uuid.New()) {
		t.Fatal("run reported no delivery with an owner")
	}
}
