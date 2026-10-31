//go:build integration

package singleton_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/singleton"
	"github.com/apohor/rivolt/internal/testpg"
)

// Two replicas (two pools on one database) racing for a job: one runs it while the other skips, each outcome is counted, the lock is
// free again afterwards and a failing job still releases it.
func TestRunIsOneReplicaAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dsn := testpg.DSN(ctx, t)
	a, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var mu sync.Mutex
	counts := map[string]int{}
	singleton.SetObserver(func(job, outcome string) { mu.Lock(); counts[job+"/"+outcome]++; mu.Unlock() })
	t.Cleanup(func() { singleton.SetObserver(func(string, string) {}) })

	for name, key := range map[string]singleton.Key{"named": {Name: "test:named"}, "id": {ID: 0x7465737431}} {
		inside, release := make(chan struct{}), make(chan struct{})
		done := make(chan bool)
		go func() {
			ran, err := singleton.Run(ctx, a, name, key, func(context.Context) error { close(inside); <-release; return nil })
			if err != nil {
				t.Error(err)
			}
			done <- ran
		}()
		<-inside // replica A is in the job
		if ran, err := singleton.Run(ctx, b, name, key, func(context.Context) error { t.Error("job ran on two replicas"); return nil }); ran || err != nil {
			t.Errorf("%s: second replica ran=%v err=%v, want skipped", name, ran, err)
		}
		close(release)
		if !<-done {
			t.Errorf("%s: first replica did not run", name)
		}
		// Free again; a job that fails still releases the lock.
		boom := errors.New("boom")
		if ran, err := singleton.Run(ctx, b, name, key, func(context.Context) error { return boom }); !ran || !errors.Is(err, boom) {
			t.Errorf("%s: after release ran=%v err=%v, want ran with the job's error", name, ran, err)
		}
		if ran, _ := singleton.Run(ctx, a, name, key, func(context.Context) error { return nil }); !ran {
			t.Errorf("%s: a failed job kept the lock", name)
		}
		mu.Lock()
		if counts[name+"/ran"] != 3 || counts[name+"/skipped"] != 1 {
			t.Errorf("%s: counted %v, want 3 ran and 1 skipped", name, counts)
		}
		mu.Unlock()
	}
}
