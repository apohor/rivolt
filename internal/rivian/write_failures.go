package rivian

import (
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// RecorderWriteFailureObserver, when set (once, at startup, before serving), is
// told about every failed recorder write by kind ("drive_upsert",
// "charge_upsert", "stale_charge"); main counts them. Never given an id.
var RecorderWriteFailureObserver func(kind string)

// writeFailureLogEvery bounds the Warn lines for one (kind, vehicle): a database
// outage fails on every frame, and a line per frame would drown the log.
const writeFailureLogEvery = time.Minute

type writeFailureLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// noteWriteFailure counts a failed live write and logs it at Warn, at most once
// a minute per kind and vehicle (the rest at Debug). These were Debug only, so a
// database outage lost drives and charges without a trace (review §4.2).
func (m *StateMonitor) noteWriteFailure(kind, vehicleID, id string, err error) {
	if f := RecorderWriteFailureObserver; f != nil {
		f(kind)
	}
	key := kind + "|" + vehicleID
	now := time.Now()
	m.writeFails.mu.Lock()
	if m.writeFails.last == nil {
		m.writeFails.last = make(map[string]time.Time)
	}
	loud := now.Sub(m.writeFails.last[key]) >= writeFailureLogEvery
	if loud {
		m.writeFails.last[key] = now
	}
	m.writeFails.mu.Unlock()
	why := writeFailureReason(err)
	if loud {
		m.logger.Warn("live write failed", "kind", kind, "vehicle", vehicleID, "id", id, "err", why)
		return
	}
	m.logger.Debug("live write failed", "kind", kind, "vehicle", vehicleID, "id", id, "err", why)
}

// writeFailureReason is what is safe to log about a failed write. A Postgres
// error's DETAIL can quote the whole failing row (coordinates included), so only
// its SQLSTATE, table and constraint name are kept; other errors (connection refused,
// timeouts) carry no row data.
func writeFailureReason(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		r := "sqlstate " + pg.Code
		if pg.TableName != "" {
			r += " table " + pg.TableName
		}
		if pg.ConstraintName != "" {
			r += " constraint " + pg.ConstraintName
		}
		return r
	}
	return err.Error()
}
