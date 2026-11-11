package vehiclediscovery

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// The sign-in nudge reaches a job that is already running: the queue is made before Run (OwnerMode), so a nudge sent while Run starts is neither a
// data race nor dropped on a nil channel. Run with -race.
func TestNudgeReachesARunningOwnerJob(t *testing.T) {
	var asked atomic.Int32
	j := &Job{DB: &sql.DB{}, Lookup: nil}
	j.Lookup = func(context.Context, uuid.UUID) ([]rivian.Vehicle, error) { return nil, nil }
	j.OwnerMode(func(context.Context) ([]OwnedUser, error) { asked.Add(1); return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); j.Run(ctx) }()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); j.NudgeUser(uuid.New()) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if got := asked.Load(); got != 8 {
		t.Errorf("owner job handled %d of 8 nudges", got)
	}
}
