// Package bg tracks the process's fire-and-forget goroutines (a re-auth email, a
// close hook's push, a detached write) so shutdown can wait for them, bounded,
// instead of closing the database under them.
package bg

import (
	"sync"
	"time"
)

// Group counts running goroutines.
type Group struct {
	wg sync.WaitGroup
}

// Default is the process-wide group; main waits on it at shutdown.
var Default = &Group{}

// Go runs f in a goroutine the group waits for.
func (g *Group) Go(f func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		f()
	}()
}

// Wait blocks until every goroutine started with Go has returned, or timeout
// passes. It reports whether they all finished. (A timed-out wait leaves its
// helper goroutine behind; the process is exiting.)
func (g *Group) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// Go runs f on the Default group.
func Go(f func()) { Default.Go(f) }
