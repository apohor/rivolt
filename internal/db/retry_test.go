package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryUntilSucceedsAfterTransientFailures(t *testing.T) {
	var slept []time.Duration
	var logged int
	calls := 0
	err := retryUntil(context.Background(), 60*time.Second,
		func(d time.Duration) { slept = append(slept, d) },
		func(int, error, time.Duration) { logged++ },
		func() error {
			calls++
			if calls < 4 {
				return errors.New("dial tcp: connect")
			}
			return nil
		})
	if err != nil || calls != 4 || logged != 3 {
		t.Fatalf("err=%v calls=%d logged=%d", err, calls, logged)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, w := range want {
		if slept[i] != w {
			t.Fatalf("backoff %v, want %v", slept, want)
		}
	}
}

func TestRetryUntilGivesUpAfterBudget(t *testing.T) {
	var total time.Duration
	calls := 0
	boom := errors.New("down")
	err := retryUntil(context.Background(), 60*time.Second,
		func(d time.Duration) { total += d }, func(int, error, time.Duration) {},
		func() error { calls++; return boom })
	if !errors.Is(err, boom) || total > 60*time.Second || total < 50*time.Second {
		t.Fatalf("err=%v waited=%v calls=%d", err, total, calls)
	}
}

func TestRetryUntilDoesNotRetryPermanentOrCancelled(t *testing.T) {
	calls := 0
	err := retryUntil(context.Background(), time.Minute, func(time.Duration) { t.Fatal("slept") },
		func(int, error, time.Duration) {}, func() error { calls++; return errPermanent })
	if !errors.Is(err, errPermanent) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_ = retryUntil(ctx, time.Minute, func(time.Duration) { t.Fatal("slept") },
		func(int, error, time.Duration) {}, func() error { calls++; return errors.New("x") })
	if calls != 1 {
		t.Fatalf("calls=%d after cancel", calls)
	}
}
