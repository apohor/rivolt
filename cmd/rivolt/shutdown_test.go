package main

import (
	"context"
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

// The drain wait sits in front of the shutdown steps, so the grace period has to cover it too (P2b): 75 s + cap + the wait's slack.
func TestShutdownBudgetWithTheDriveWait(t *testing.T) {
	total := preStopSleep + (15*time.Minute + leaseReleaseTimeout) + drainFlushTimeout + leaseReleaseTimeout + httpDrainTimeout + bgShutdownWait
	if got := graceWithDriveWait(15 * time.Minute); got != gracePeriod+15*time.Minute+leaseReleaseTimeout || total > got {
		t.Fatalf("grace with a 15 min cap = %v (steps sum to %v)", got, total)
	}
	if got := graceWithDriveWait(0); got != gracePeriod {
		t.Errorf("no cap: grace %v, want %v", got, gracePeriod)
	}
	t.Setenv("RIVOLT_INGEST_DRIVE_WAIT_CAP", "15m")
	if got := ingestDriveWaitCap(); got != 15*time.Minute {
		t.Errorf("cap = %v", got)
	}
	t.Setenv("RIVOLT_INGEST_DRIVE_WAIT_CAP", "bogus")
	if got := ingestDriveWaitCap(); got != 0 {
		t.Errorf("bad cap = %v, want 0", got)
	}
}

type fakeWaiter struct{ during func() }

func (f fakeWaiter) DrainWait(context.Context) { f.during() }

// The monitors and the forwarder run under workCtx and must stay up for the whole drain wait, whatever the signal context does; they are
// stopped only after it (F1: frames keep being recorded for a group kept for its drive).
func TestDrainThenStopKeepsTheWorkUpThroughTheWait(t *testing.T) {
	sig, stopSig := context.WithCancel(context.Background())
	work, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	stopSig() // SIGTERM has arrived
	var aliveDuring bool
	drainThenStop(fakeWaiter{func() { aliveDuring = work.Err() == nil }}, stopWork)
	if sig.Err() == nil || !aliveDuring {
		t.Fatalf("work context alive during the wait = %v (signal ctx err %v)", aliveDuring, sig.Err())
	}
	if work.Err() == nil {
		t.Error("work context still alive after the drain")
	}
	// No coordinator: still stops the work.
	w2, c2 := context.WithCancel(context.Background())
	drainThenStop(nil, c2)
	if w2.Err() == nil {
		t.Error("no coordinator: work not stopped")
	}
}
