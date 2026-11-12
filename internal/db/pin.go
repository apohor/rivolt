package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ErrPinGateFull is Pin's answer when the pool has no connection to spare for another long-held one.
var ErrPinGateFull = errors.New("db: every connection that may be pinned is in use")

// pinReserve is how many connections of a pool Pin never hands out: boot (settings.NewManager and the readiness checks) and the
// requests of the role need them while the maintenance jobs hold theirs.
const pinReserve = 2

// pinGate counts the slots in use out of cap for one pool.
type pinGate struct {
	mu        sync.Mutex
	used, cap int
}

func (g *pinGate) take(n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used+n > g.cap {
		return false
	}
	g.used += n
	return true
}

func (g *pinGate) give(n int) {
	g.mu.Lock()
	g.used -= n
	g.mu.Unlock()
}

var pinGates sync.Map // *sql.DB -> *pinGate

func gateOf(d *sql.DB) *pinGate {
	if g, ok := pinGates.Load(d); ok {
		return g.(*pinGate)
	}
	n := d.Stats().MaxOpenConnections
	if n <= 0 {
		return nil // an unlimited pool cannot be starved
	}
	g, _ := pinGates.LoadOrStore(d, &pinGate{cap: max(1, n-pinReserve)})
	return g.(*pinGate)
}

var (
	pinObserver atomic.Pointer[func(job string)]
	pinLogged   sync.Map // job -> time.Time of the last Info line
)

// SetPinObserver tells f the job of every run Pin refused, for rivolt_db_pin_gate_full_total. Set once at boot.
func SetPinObserver(f func(job string)) { pinObserver.Store(&f) }

func pinRefused(ctx context.Context, job string) {
	if f := pinObserver.Load(); f != nil {
		(*f)(job)
	}
	// A refused tick is a job that did not run, so it is visible; once a minute per job keeps the leader election's retries quiet.
	if last, ok := pinLogged.Load(job); !ok || time.Since(last.(time.Time)) > time.Minute {
		pinLogged.Store(job, time.Now())
		slog.InfoContext(ctx, "maintenance job skipped: the pool has no connection to spare for it", "job", job)
	}
}

// Pin is d.Conn for a caller that keeps the connection for a whole job, as the ones holding a session advisory lock do (retention,
// thinning, archiving, the drain leader, singleton jobs). weight is how many pool connections the job uses at once: 1 when all its work
// runs on the pinned connection, 2 when it also queries the pool while it holds the lock (packhealth, vehicle discovery). At most pool
// size minus pinReserve (at least 1) are counted at once; past that Pin returns ErrPinGateFull at once, counts it and logs it, and the
// caller skips the run as it would for a lock another replica holds. Without that cap a small pool is held entirely by jobs and a
// booting pod waits for a connection (rivolt#1998). done returns the connection to the pool and frees the slots; call it once, after
// the lock is released.
func Pin(ctx context.Context, d *sql.DB, job string, weight int) (conn *sql.Conn, done func(), err error) {
	g := gateOf(d)
	if g != nil {
		weight = min(max(weight, 1), g.cap)
		if !g.take(weight) {
			pinRefused(ctx, job)
			return nil, nil, ErrPinGateFull
		}
	}
	conn, err = d.Conn(ctx)
	if err != nil {
		if g != nil {
			g.give(weight)
		}
		return nil, nil, err
	}
	var once sync.Once
	return conn, func() {
		once.Do(func() {
			_ = conn.Close()
			if g != nil {
				g.give(weight)
			}
		})
	}, nil
}
