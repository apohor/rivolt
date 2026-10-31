//go:build integration

package demo

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/singleton"
	"github.com/apohor/rivolt/internal/testpg"
)

// Scenario 10 of the N-instance harness, for the demo loops: three pods tick each loop at once and the pass is never running on two of
// them at the same time (a pass that takes a moment, so an overlap would show); at least one pod runs it and a pod that found it
// taken skipped rather than waited.
func TestDemoLoopsRunOnOnePodAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := testpg.DSN(ctx, t)
	var pools []*sql.DB
	for i := 0; i < 3; i++ {
		p, err := db.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		pools = append(pools, p)
	}
	// The helper counts every attempt: a pod that finds the pass taken must be counted as skipped, not left waiting for it.
	var mu sync.Mutex
	outcomes := map[string]int{}
	singleton.SetObserver(func(job, outcome string) { mu.Lock(); outcomes[job+"/"+outcome]++; mu.Unlock() })
	t.Cleanup(func() { singleton.SetObserver(func(string, string) {}) })
	for _, job := range []string{"pricing", "weather", "plans"} {
		var inFlight, maxInFlight, ran atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, p := range pools {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				onOneReplica(ctx, p, job, func() {
					n := inFlight.Add(1)
					for {
						m := maxInFlight.Load()
						if n <= m || maxInFlight.CompareAndSwap(m, n) {
							break
						}
					}
					ran.Add(1)
					time.Sleep(300 * time.Millisecond)
					inFlight.Add(-1)
				})
			}()
		}
		close(start)
		wg.Wait()
		if maxInFlight.Load() != 1 {
			t.Errorf("%s: %d pods ran the pass at the same time, want 1", job, maxInFlight.Load())
		}
		if ran.Load() < 1 || ran.Load() > 3 {
			t.Errorf("%s: the pass ran %d times across 3 pods", job, ran.Load())
		}
		mu.Lock()
		counted, skipped, failed := outcomes["demo_"+job+"/ran"], outcomes["demo_"+job+"/skipped"], outcomes["demo_"+job+"/error"]
		mu.Unlock()
		if counted != int(ran.Load()) || counted+skipped != 3 || failed != 0 || skipped < 1 {
			t.Errorf("%s: counted %d ran, %d skipped, %d errors for %d runs on 3 pods; want every attempt counted and a pod that found the pass taken skipping it", job, counted, skipped, failed, ran.Load())
		}
	}
}
