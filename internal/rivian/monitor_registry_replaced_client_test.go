package rivian

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Nov 24 incident: a lost group wipes the user's client (ReleaseUnheld drops it from the account registry and zeroes it). When the group
// came back, EnsureSubscribed found the old monitor, still holding the wiped client, and subscribed on it: parked at "awaiting auth"
// forever, counted as streaming. It must restart the monitor on the current client, signed in again from the stored session.
func TestEnsureOwnedRestartsAMonitorWhoseClientWasReplaced(t *testing.T) {
	uid := uuid.New()
	accounts := NewLiveAccountRegistry(func(uuid.UUID) *LiveClient { return NewLive() })
	reg := NewMonitorRegistry(nil, accounts, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg.SetParent(context.Background())

	old := accounts.For(uid).(*LiveClient)
	oldMon := NewStateMonitor(old, nil)
	reg.monitors[uid] = &monitorEntry{monitor: oldMon, cancel: func() {}}

	// What ReleaseUnheld does when the group is lost.
	accounts.(*liveAccountRegistry).forget(uid)
	old.Zero()

	rehydrated := 0
	reg.rehydrate = func(_ context.Context, u uuid.UUID) bool {
		rehydrated++
		c := accounts.For(u).(*LiveClient)
		c.mu.Lock()
		c.userSessionToken = "u"
		c.mu.Unlock()
		return true
	}

	reg.ensureOwned("v1", uid)

	cur := reg.For(uid)
	if cur == nil || cur == oldMon {
		t.Fatalf("still the old monitor (nil=%v): the vehicle subscribes on a wiped client", cur == nil)
	}
	if rehydrated != 1 {
		t.Errorf("session restored %d times, want 1", rehydrated)
	}
	if cur.client != accounts.For(uid).(*LiveClient) || !cur.client.Authenticated() {
		t.Error("the new monitor is not on the current, signed-in client")
	}

	// A monitor on the current client is left alone.
	reg.ensureOwned("v1", uid)
	if reg.For(uid) != cur || rehydrated != 1 {
		t.Error("a monitor on the current client was restarted")
	}
}

// A run parked at "awaiting auth" past one reconcile is not streaming: the lease heal and the unstreamed gauge must see it.
func TestStreamingIsFalseForARunParkedAwaitingAuth(t *testing.T) {
	r := &MonitorRegistry{monitors: map[uuid.UUID]*monitorEntry{}}
	mon := NewStateMonitor(NewLive(), nil)
	r.monitors[uuid.New()] = &monitorEntry{monitor: mon}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.mu.Lock()
	mon.active["v1"] = cancel
	mon.mu.Unlock()

	now := time.Unix(1_000_000, 0)
	mon.now = func() time.Time { return now }
	done := make(chan bool)
	go func() { done <- mon.waitAuthReady(ctx, "v1") }() // the client is not signed in: it parks
	for i := 0; ; i++ {
		mon.mu.RLock()
		_, parked := mon.authParked["v1"]
		mon.mu.RUnlock()
		if parked {
			break
		}
		if i > 2000 {
			t.Fatal("run never parked")
		}
		time.Sleep(time.Millisecond)
	}
	if !r.Streaming("v1") {
		t.Error("a run that just began waiting already reads as not streaming")
	}
	mon.mu.Lock()
	now = now.Add(authParkedGrace + time.Second)
	mon.mu.Unlock()
	if r.Streaming("v1") {
		t.Error("a run parked at awaiting auth past one reconcile still reads as streaming (the gauge stays 0)")
	}
	// A user flagged needs_reauth is correctly not streaming and must not count as unstreamed (no gauge, no heal re-fire).
	mon.client.SetNeedsReauth(true, "expired")
	if !r.Streaming("v1") {
		t.Error("a needs_reauth account reads as unstreamed: the 5-minute alert would page for it")
	}
	mon.client.SetNeedsReauth(false, "")
	if r.Streaming("v1") {
		t.Error("clearing needs_reauth did not bring the parked run back into the count")
	}
	cancel()
	<-done
	if mon.ParkedForAuth("v1", 0) {
		t.Error("parked mark left behind after the run gave up")
	}
}
