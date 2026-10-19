// Package bg tracks the process's fire-and-forget goroutines (a re-auth email, a
// close hook's push, a detached write) so shutdown can wait for them, bounded,
// instead of closing the database under them.
package bg

import (
	"sync"
	"time"
)

// Group counts running goroutines. It is a counter under a mutex rather than a
// sync.WaitGroup because a hook can start new work while shutdown is already
// waiting, and WaitGroup panics on an Add that races a Wait at zero.
type Group struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n returns to zero, while someone waits
}

// Default is the process-wide group; main waits on it at shutdown.
var Default = &Group{}

// Go runs f in a goroutine the group waits for. Safe to call while Wait runs: the
// new goroutine is tracked too.
func (g *Group) Go(f func()) {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.n--
			if g.n == 0 && g.idle != nil {
				close(g.idle)
				g.idle = nil
			}
			g.mu.Unlock()
		}()
		f()
	}()
}

// Wait blocks until no goroutine started with Go is running, or timeout passes.
// It reports whether the group went idle.
func (g *Group) Wait(timeout time.Duration) bool {
	g.mu.Lock()
	if g.n == 0 {
		g.mu.Unlock()
		return true
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	ch := g.idle
	g.mu.Unlock()

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}

// Go runs f on the Default group.
func Go(f func()) { Default.Go(f) }
