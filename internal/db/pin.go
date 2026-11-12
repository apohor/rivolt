package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// ErrPinGateFull is Pin's answer when the pool has no connection to spare for another long-held one.
var ErrPinGateFull = errors.New("db: every connection that may be pinned is in use")

// pinReserve is how many connections of a pool Pin never hands out: boot (settings.NewManager and the readiness checks) and the
// requests of the role need them while the maintenance jobs hold theirs.
const pinReserve = 2

var pinGates sync.Map // *sql.DB -> chan struct{}

func pinGate(d *sql.DB) chan struct{} {
	if g, ok := pinGates.Load(d); ok {
		return g.(chan struct{})
	}
	n := d.Stats().MaxOpenConnections
	if n <= 0 {
		return nil // an unlimited pool cannot be starved
	}
	g, _ := pinGates.LoadOrStore(d, make(chan struct{}, max(1, n-pinReserve)))
	return g.(chan struct{})
}

// Pin is d.Conn for a caller that keeps the connection for a whole job, as the ones holding a session advisory lock do (retention,
// thinning, archiving, the drain leader, singleton jobs). At most pool size minus pinReserve (at least 1) connections of d are pinned at
// once; past that it returns ErrPinGateFull at once, and the caller skips the run as it would for a lock another replica holds.
// Without that cap a small pool is held entirely by jobs and a booting pod waits for a connection (rivolt#1998). done returns the
// connection to the pool and frees the slot; call it exactly once, after the lock is released.
func Pin(ctx context.Context, d *sql.DB) (conn *sql.Conn, done func(), err error) {
	g := pinGate(d)
	if g != nil {
		select {
		case g <- struct{}{}:
		default:
			return nil, nil, ErrPinGateFull
		}
	}
	conn, err = d.Conn(ctx)
	if err != nil {
		if g != nil {
			<-g
		}
		return nil, nil, err
	}
	var once sync.Once
	return conn, func() {
		once.Do(func() {
			_ = conn.Close()
			if g != nil {
				<-g
			}
		})
	}, nil
}
