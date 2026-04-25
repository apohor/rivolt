// Package flags owns the DB-backed operational flag table
// (migration 0003_flags.sql). It exposes one real flag today
// — rivian_upstream_paused, the kill switch wired into every
// Rivian-facing code path — but the shape generalises: add a new
// row, add a new accessor.
//
// # Design
//
// The hot path must never block on Postgres. A Store holds an
// in-process snapshot of the flag row and refreshes it on a
// ticker (default 10s). Every caller reads the snapshot, which
// is a plain atomic load — no DB round-trip, no mutex for readers.
//
// Operator writes (HTTP admin endpoint, future CLI) go straight
// to Postgres and immediately refresh the snapshot so the writer
// sees their own effect. Other replicas see the change on their
// next poll (p99 ≤ poll interval). That gap is intentional: a
// kill switch is a "within ~10 seconds the storm stops" contract,
// not a "within 10ms" one — the architecture doc sizes it that
// way on purpose.
//
// # Why not LISTEN/NOTIFY
//
// LISTEN/NOTIFY would close the cross-replica propagation gap to
// sub-second. It also doubles our Postgres connection assumptions
// (every pod needs a dedicated listen connection) and our failure
// surface (reconnect logic, missed notifies). 10s polling at
// 3-8 pods is one indexed SELECT per pod per 10s — cheap enough
// that the simplicity wins. Revisit when a pod count gets into
// the dozens.
package flags

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// KillSwitchName is the flags.name value for the Rivian upstream
// kill switch. Exported so tests and admin handlers can reference
// the same string the poller does.
const KillSwitchName = "rivian_upstream_paused"

// DefaultPollInterval is how often the Store refreshes its
// in-memory snapshot from Postgres. Chosen so a flipped flag
// takes effect in roughly the time a human can notice — on the
// same order as a Prometheus scrape interval.
const DefaultPollInterval = 10 * time.Second

// KillSwitchState is the JSONB payload stored in
// flags.value for rivian_upstream_paused. The extra fields let
// us track who flipped it and why without a schema migration per
// axis.
type KillSwitchState struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
	Actor  string `json:"actor,omitempty"`
}

// Store is the runtime flag cache. Start() kicks off the background
// refresh; callers read via KillSwitch(). The type is safe for
// concurrent use: all readers hit atomic.Value, only the refresh
// goroutine mutates.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
	poll   time.Duration

	// snapshot is the latest KillSwitchState, read by every
	// Rivian-facing code path. Stored as *KillSwitchState so we
	// can atomically swap the pointer on refresh.
	snapshot atomic.Pointer[KillSwitchState]

	startOnce sync.Once
}

// OpenStore builds a Store against the shared Postgres pool and
// primes the snapshot with a blocking read so the first
// KillSwitch() call after New never returns a zero value. A DB
// outage at startup is non-fatal — the snapshot defaults to
// "not paused" and the background loop keeps trying.
func OpenStore(ctx context.Context, d *sql.DB, logger *slog.Logger) (*Store, error) {
	if d == nil {
		return nil, errors.New("flags: nil db")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{db: d, logger: logger, poll: DefaultPollInterval}
	// Initial read. If it fails we still return a usable Store
	// with the default "not paused" state so the server boots.
	if err := s.refresh(ctx); err != nil {
		logger.Warn("flags: initial refresh failed; defaulting to not-paused",
			"err", err.Error())
		s.snapshot.Store(&KillSwitchState{Paused: false})
	}
	return s, nil
}

// Start launches the refresh loop. Safe to call multiple times;
// only the first invocation takes effect. The loop exits when
// ctx is cancelled.
func (s *Store) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		go s.loop(ctx)
	})
}

func (s *Store) loop(ctx context.Context) {
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.refresh(ctx); err != nil {
				s.logger.Warn("flags refresh failed", "err", err.Error())
			}
		}
	}
}

// refresh reads the kill-switch row from Postgres and swaps it
// into the snapshot pointer.
func (s *Store) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM flags WHERE name = $1`, KillSwitchName).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Migration didn't seed (or was rolled back); fall
			// back to not-paused rather than stranding the poller.
			s.snapshot.Store(&KillSwitchState{Paused: false})
			return nil
		}
		return fmt.Errorf("select flag %q: %w", KillSwitchName, err)
	}
	var st KillSwitchState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("decode flag %q: %w", KillSwitchName, err)
	}
	s.snapshot.Store(&st)
	return nil
}

// KillSwitch returns the current cached kill-switch state. Cheap
// — one atomic load. Safe to call on every outbound request.
// Returns the zero value (Paused: false) when the Store was
// opened against a DB where the migration hasn't been applied.
func (s *Store) KillSwitch() KillSwitchState {
	p := s.snapshot.Load()
	if p == nil {
		return KillSwitchState{}
	}
	return *p
}

// SetKillSwitch persists a new kill-switch state and immediately
// refreshes the local snapshot so the caller observes their own
// write. `actor` is stored on the row for audit trails; pass the
// username of whoever flipped the switch (or "system:reason" for
// automated trips).
func (s *Store) SetKillSwitch(ctx context.Context, paused bool, reason, actor string) error {
	st := KillSwitchState{Paused: paused, Reason: reason, Actor: actor}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO flags (name, value, updated_at, updated_by)
		VALUES ($1, $2, now(), $3)
		ON CONFLICT (name) DO UPDATE
			SET value = EXCLUDED.value,
			    updated_at = EXCLUDED.updated_at,
			    updated_by = EXCLUDED.updated_by
	`, KillSwitchName, raw, actor)
	if err != nil {
		return fmt.Errorf("upsert flag %q: %w", KillSwitchName, err)
	}
	s.snapshot.Store(&st)
	return nil
}
