package rivian

import (
	"context"
	"net"
	"testing"
	"time"
)

// A dial to a gateway that accepts the TCP connection and never answers must give
// up after wsDialTimeout, not sit on the mux lock until the caller's context or
// the OS ends it: the lock is the user's, so every one of their subscriptions
// would wait behind it (review §4.2).
func TestWSDialToABlackholeTimesOutAndReleasesTheMuxLock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }() // held open, never answered
		}
	}()

	old := wsDialTimeout
	wsDialTimeout = 300 * time.Millisecond
	defer func() { wsDialTimeout = old }()

	c := NewLive()
	c.userSessionToken = "u"
	c.wsEndpointOverride = "ws://" + ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ { // the second call proves the lock was released
		start := time.Now()
		_, err := c.acquireMux(ctx)
		if err == nil {
			t.Fatal("acquireMux succeeded against a gateway that never answers")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("attempt %d took %v, want it bounded by the dial timeout", i+1, d)
		}
	}
}
