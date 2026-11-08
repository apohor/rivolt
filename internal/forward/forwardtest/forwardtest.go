// Package forwardtest holds the fakes the forwarder's tests inject: a lease view, a runner that records its calls, and a Redis clock
// that a test can set apart from the wall clock.
package forwardtest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/forward"
)

// LeaseView is a settable forward.LeaseView.
type LeaseView struct {
	mu       sync.Mutex
	Owners   map[string]forward.LeaseOwner
	HeldAt   map[string]int64
	Past     bool
	OwnerErr error
}

// SetOwner sets the lease row a group shows; DeleteOwner removes it; SetPast sets the local deadline verdict. They are safe to call
// while an Owner reads the view.
func (l *LeaseView) SetOwner(group string, o forward.LeaseOwner) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Owners == nil {
		l.Owners = map[string]forward.LeaseOwner{}
	}
	l.Owners[group] = o
}

func (l *LeaseView) DeleteOwner(group string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.Owners, group)
}

func (l *LeaseView) SetPast(p bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Past = p
}

// Forget is a no-op (the fake caches nothing); it lets the fake stand in for a caching view.
func (l *LeaseView) Forget(string) {}

func (l *LeaseView) Owner(_ context.Context, group string) (forward.LeaseOwner, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.OwnerErr != nil {
		return forward.LeaseOwner{}, l.OwnerErr
	}
	return l.Owners[group], nil
}

func (l *LeaseView) Held(group string) (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.HeldAt[group]
	return e, ok
}

func (l *LeaseView) PastDeadline() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Past
}

// Call is one recorded Runner call.
type Call struct {
	Kind        forward.Kind
	User, Group string
	Args        any
}

// Runner records its calls and answers with Reply (200 and an empty object by default) or Err.
type Runner struct {
	mu    sync.Mutex
	calls []Call
	Reply forward.Result
	Err   error
	// Gate, when set, holds every call (after it is recorded) until it is closed or the call's context ends.
	Gate chan struct{}
}

func (r *Runner) Run(ctx context.Context, kind forward.Kind, user, group string, args any) (forward.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, Call{kind, user, group, args})
	gate := r.Gate
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return forward.Result{}, r.Err
	}
	if r.Reply.Status == 0 {
		return forward.Result{Status: 200, Body: json.RawMessage(`{}`)}, nil
	}
	return r.Reply, nil
}

// Calls returns a copy of the recorded calls.
func (r *Runner) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// Clock is a Redis clock a test sets.
type Clock struct {
	mu  sync.Mutex
	ms  int64
	Err error
}

// NewClock starts at ms.
func NewClock(ms int64) *Clock { return &Clock{ms: ms} }

func (c *Clock) RedisTimeMS(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ms, c.Err
}

// Advance moves the clock.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.ms += d.Milliseconds()
	c.mu.Unlock()
}

var (
	_ forward.LeaseView = (*LeaseView)(nil)
	_ forward.Runner    = (*Runner)(nil)
	_ forward.Clock     = (*Clock)(nil)
)
