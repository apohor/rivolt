package rivian

import (
	"testing"
	"time"
)

func TestReconnectBackoff(t *testing.T) {
	now := time.Unix(0, 0)
	b := reconnectBackoff{now: func() time.Time { return now }}
	// Fast failures escalate 1, 2, 4, … s (+<1 s jitter), capped at
	// 5 min, and stay sane after a year's worth of reconnects.
	for i := 0; i < 200_000; i++ {
		b.start()
		now = now.Add(time.Second)
		w := b.next()
		if w <= 0 || w > maxReconnectWait {
			t.Fatalf("attempt %d: wait %v out of (0, 5m]", i, w)
		}
		if i < 8 {
			if lo := time.Duration(1<<i) * time.Second; w < lo || w >= lo+time.Second {
				t.Fatalf("attempt %d: wait %v, want [%v, %v)", i, w, lo, lo+time.Second)
			}
		}
	}
	if b.attempt != maxAttempt {
		t.Errorf("attempt = %d, want capped at %d", b.attempt, maxAttempt)
	}
	// A healthy session (Rivian's ~3-min TTL close) resets: ~1 s.
	b.start()
	now = now.Add(178 * time.Second)
	if w := b.next(); w >= 2*time.Second {
		t.Errorf("after a healthy session: wait %v, want ~1 s", w)
	}
	// The real clock path works without a hook.
	var real reconnectBackoff
	real.start()
	if w := real.next(); w <= 0 || w >= 2*time.Second {
		t.Errorf("first wait %v", w)
	}
}
