package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The re-auth email waits out a grace and sends only if the user is still flagged with the same raise time (DA's follow-up to the sign-in race
// fix). The flag is a fake with a mutex; the grace is milliseconds.

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeFlag struct {
	mu      sync.Mutex
	flagged bool
	at      time.Time
	err     error
}

func (f *fakeFlag) raise(at time.Time) { f.mu.Lock(); f.flagged, f.at = true, at; f.mu.Unlock() }
func (f *fakeFlag) clear()             { f.mu.Lock(); f.flagged = false; f.mu.Unlock() }
func (f *fakeFlag) read(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at, f.flagged, f.err
}

const testGrace = 60 * time.Millisecond

func runGrace(ctx context.Context, f *fakeFlag, sent *atomic.Int32, done chan<- struct{}) {
	go func() {
		defer close(done)
		sendReauthEmailAfterGrace(ctx, testGrace, f.read, func(context.Context) { sent.Add(1) }, quietLogger, uuid.New())
	}()
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the email goroutine did not finish")
	}
}

func TestAFlagStillRaisedAfterTheGraceSendsExactlyOneEmail(t *testing.T) {
	f := &fakeFlag{}
	f.raise(time.Now())
	var sent atomic.Int32
	done := make(chan struct{})
	runGrace(context.Background(), f, &sent, done)
	waitDone(t, done)
	if n := sent.Load(); n != 1 {
		t.Fatalf("emails = %d, want 1", n)
	}
}

func TestAFlagClearedWithinTheGraceSendsNothing(t *testing.T) {
	f := &fakeFlag{}
	f.raise(time.Now())
	var sent atomic.Int32
	done := make(chan struct{})
	runGrace(context.Background(), f, &sent, done)
	time.Sleep(testGrace / 3)
	f.clear() // the sign-in's save cleared it
	waitDone(t, done)
	if n := sent.Load(); n != 0 {
		t.Fatalf("emails = %d, want 0", n)
	}
}

// A clear followed by a genuine new raise inside the grace: the first raise's goroutine sees a different raise time and sends nothing; the new
// raise's own goroutine sends once. One real raise, one email, and it is for the new one.
func TestAClearThenANewRaiseSendsOnceForTheNewRaise(t *testing.T) {
	f := &fakeFlag{}
	t1 := time.Now()
	f.raise(t1)
	var sentOld, sentNew atomic.Int32
	doneOld, doneNew := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(doneOld)
		sendReauthEmailAfterGrace(context.Background(), testGrace, f.read, func(context.Context) { sentOld.Add(1) }, quietLogger, uuid.New())
	}()
	time.Sleep(testGrace / 3)
	f.clear()
	f.raise(t1.Add(time.Second)) // a new raise: a new time
	go func() {
		defer close(doneNew)
		sendReauthEmailAfterGrace(context.Background(), testGrace, f.read, func(context.Context) { sentNew.Add(1) }, quietLogger, uuid.New())
	}()
	waitDone(t, doneOld)
	waitDone(t, doneNew)
	if sentOld.Load() != 0 || sentNew.Load() != 1 {
		t.Fatalf("emails for the old raise = %d (want 0), for the new one = %d (want 1)", sentOld.Load(), sentNew.Load())
	}
}

func TestAPodThatStopsDuringTheWaitSendsNothingAndReturnsAtOnce(t *testing.T) {
	f := &fakeFlag{}
	f.raise(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	var sent atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		sendReauthEmailAfterGrace(ctx, time.Hour, f.read, func(context.Context) { sent.Add(1) }, quietLogger, uuid.New())
	}()
	time.Sleep(20 * time.Millisecond)
	cancel() // shutdown: the user stays flagged and the nudge sweep emails them after its cooldown
	waitDone(t, done)
	if n := sent.Load(); n != 0 {
		t.Fatalf("emails = %d, want 0", n)
	}
}

func TestAReadThatFailsSendsNothing(t *testing.T) {
	// Fails before the wait.
	f := &fakeFlag{err: errors.New("db down")}
	var sent atomic.Int32
	done := make(chan struct{})
	runGrace(context.Background(), f, &sent, done)
	waitDone(t, done)
	// Fails after the wait.
	g := &fakeFlag{}
	g.raise(time.Now())
	var reads atomic.Int32
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		sendReauthEmailAfterGrace(context.Background(), testGrace, func(c context.Context) (time.Time, bool, error) {
			if reads.Add(1) > 1 {
				return time.Time{}, false, errors.New("db down")
			}
			return g.read(c)
		}, func(context.Context) { sent.Add(1) }, quietLogger, uuid.New())
	}()
	waitDone(t, done2)
	if n := sent.Load(); n != 0 {
		t.Fatalf("emails = %d, want 0", n)
	}
}

func TestAUserWhoIsNotFlaggedAtTheStartSendsNothingAndDoesNotWait(t *testing.T) {
	f := &fakeFlag{}
	var sent atomic.Int32
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		sendReauthEmailAfterGrace(context.Background(), time.Hour, f.read, func(context.Context) { sent.Add(1) }, quietLogger, uuid.New())
	}()
	waitDone(t, done)
	if sent.Load() != 0 || time.Since(start) > time.Second {
		t.Fatalf("emails = %d after %v, want 0 at once", sent.Load(), time.Since(start))
	}
}

func TestTheGraceIsLongerThanASignInRequest(t *testing.T) {
	if reauthEmailGrace < 10*time.Second || reauthEmailGrace > 2*time.Minute {
		t.Fatalf("grace = %v, want between 10 s and 2 min (DA: about 20 s)", reauthEmailGrace)
	}
}
