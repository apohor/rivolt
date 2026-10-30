package rivian

import (
	"testing"

	"github.com/google/uuid"
)

// A subscription that could not start (no monitor for its owner yet) is remembered, handed to that
// owner's monitor when it starts, left alone for other owners, and forgotten when released.
func TestWantedSubscriptions(t *testing.T) {
	r := &MonitorRegistry{logger: NewStateMonitor(nil, nil).logger}
	a, b := uuid.New(), uuid.New()
	r.want("v-a", a)
	r.want("v-b", b)
	r.want("v-gone", a)
	r.Unsubscribe("v-gone")
	if _, ok := r.wanted["v-gone"]; ok {
		t.Error("an unsubscribed vehicle is still wanted")
	}
	r.mu.Lock()
	r.runWanted(a, NewStateMonitor(nil, nil))
	r.mu.Unlock()
	if _, ok := r.wanted["v-a"]; ok {
		t.Error("v-a still wanted after its owner's monitor started")
	}
	if u, ok := r.wanted["v-b"]; !ok || u != b {
		t.Error("v-b (another owner) was touched")
	}
}
