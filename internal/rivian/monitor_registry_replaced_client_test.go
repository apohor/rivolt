package rivian

import (
	"context"
	"io"
	"log/slog"
	"testing"

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
