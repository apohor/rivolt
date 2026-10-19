package main

import (
	"io"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSecondSignalExitsAtOnce(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 1)
	go exitOnSecondSignal(sigs, func(code int) { exited <- code }, slog.New(slog.NewTextHandler(io.Discard, nil)))

	select {
	case <-exited:
		t.Fatal("exited before any second signal")
	case <-time.After(50 * time.Millisecond):
	}
	sigs <- syscall.SIGTERM
	select {
	case code := <-exited:
		if code == 0 {
			t.Errorf("exit code %d, want non-zero for an aborted shutdown", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second SIGTERM did not end the process")
	}
}

func TestSecondSignalWatcherStopsWhenTheChannelCloses(t *testing.T) {
	sigs := make(chan os.Signal)
	done := make(chan struct{})
	go func() {
		exitOnSecondSignal(sigs, func(int) { t.Error("exit called without a signal") }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	close(sigs)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not return")
	}
}

// The worst case of the shutdown steps must fit what is left of the pod's grace
// period after the preStop sleep. (The steps run one after another: the drain
// flush, the lease release, the HTTP drain, the wait for background work.)
func TestShutdownBudgetFitsTheGracePeriod(t *testing.T) {
	total := preStopSleep + drainFlushTimeout + leaseReleaseTimeout + httpDrainTimeout + bgShutdownWait
	if total > gracePeriod {
		t.Fatalf("shutdown can take %v (preStop %v + flush %v + leases %v + HTTP %v + background %v), over the %v grace period",
			total, preStopSleep, drainFlushTimeout, leaseReleaseTimeout, httpDrainTimeout, bgShutdownWait, gracePeriod)
	}
}
