// Package outbox is the transactional outbox (S19 8; design: zero-downtime-architecture.md section 2.3). A discrete side effect is recorded as a
// row in the same transaction as the change that causes it (Insert takes the caller's tx), and a sender claims, sends and marks it.
//
// Delivery is at-least-once: a sender that crashes between the send and MarkSent leaves a claim that expires and the row is claimed again. Each
// effect therefore keeps its own claim as the dedupe (engagement_sends, left_open_alerts, tire_low_alerts, ota_notices, the live-activity claim),
// and each kind has a MaxAge after which a stale intent is dropped by the sender instead of being sent late.
//
// Slice 1 ships the store only; nothing calls it yet. The table is service-only: the request role has no grant and the repair role cannot read it.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// The kinds of discrete event the outbox carries. The set is fixed so a metric label stays bounded.
const (
	KindCloseHook    = "close_hook"    // a drive or charge closed: weather, recap, engagement
	KindLiveActivity = "live_activity" // a Live Activity start or end (the periodic updates do not go through the outbox)
	KindPush         = "push"
	KindEmail        = "email" // account-level: no vehicle
	KindMilestone    = "milestone"
)

// kinds maps each kind to whether it is account-level (no vehicle; its key must carry the user id, since key is globally unique).
var kinds = map[string]bool{
	KindCloseHook: false, KindLiveActivity: false, KindPush: false, KindEmail: true, KindMilestone: false,
}

// MaxAge is how old an intent may be and still be sent. A sender drops (marks failed) an older one rather than send it late.
var MaxAge = map[string]time.Duration{
	KindCloseHook:    24 * time.Hour,
	KindLiveActivity: 5 * time.Minute,
	KindPush:         time.Hour,
	KindEmail:        24 * time.Hour,
	KindMilestone:    24 * time.Hour,
}

// PendingGrace is how long past its kind's MaxAge a never-consumed pending row stays before Purge expires it.
const PendingGrace = 24 * time.Hour

const (
	// MaxAttempts is how many times a row is claimed before it is set aside as failed.
	MaxAttempts = 8
	// MaxBackoff caps the wait after a failed send; the wait doubles from 30 s.
	MaxBackoff = time.Hour
	// MaxPayload is the payload cap, also a table CHECK.
	MaxPayload = 4096
	maxKey     = 200
)

// ErrNotClaimed is MarkSent's and Release's answer when the row is not claimed by that owner any more (or was already sent): the caller does
// not mark it.
var ErrNotClaimed = errors.New("outbox: row is not claimed by this owner")

// Intent is one side effect to record.
type Intent struct {
	Key       string
	UserID    uuid.UUID
	VehicleID uuid.UUID // uuid.Nil for an account-level kind
	Kind      string
	Payload   json.RawMessage // ids and kinds only: never coordinates, addresses or names; empty means {}
}

// Row is a claimed intent.
type Row struct {
	ID        int64
	Key       string
	UserID    uuid.UUID
	VehicleID uuid.UUID
	Kind      string
	Payload   json.RawMessage
	CreatedAt time.Time
	Attempts  int
}

func (i Intent) validate() error {
	accountLevel, ok := kinds[i.Kind]
	switch {
	case !ok:
		return fmt.Errorf("outbox: unknown kind %q", i.Kind)
	case i.Key == "" || len(i.Key) > maxKey:
		return fmt.Errorf("outbox: key must be 1..%d characters", maxKey)
	case i.UserID == uuid.Nil:
		return errors.New("outbox: user id is required")
	case accountLevel && i.VehicleID != uuid.Nil:
		return fmt.Errorf("outbox: kind %s is account-level and takes no vehicle", i.Kind)
	case accountLevel && !strings.Contains(i.Key, i.UserID.String()):
		return fmt.Errorf("outbox: account-level key must carry the user id")
	case !accountLevel && i.VehicleID == uuid.Nil:
		return fmt.Errorf("outbox: kind %s needs a vehicle", i.Kind)
	case len(i.Payload) > MaxPayload:
		return fmt.Errorf("outbox: payload over %d bytes", MaxPayload)
	case len(i.Payload) > 0 && !json.Valid(i.Payload):
		return errors.New("outbox: payload is not JSON")
	}
	return nil
}

type suppressKey struct{}

// WithSuppressed marks ctx so Insert records nothing: replays and repairs run this way by default (sending needs an explicit, separately acked
// opt-in).
func WithSuppressed(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressKey{}, true)
}

// Store reads and writes the outbox table.
type Store struct {
	db         *sql.DB
	inserted   *prometheus.CounterVec
	suppressed *prometheus.CounterVec
}

// NewStore returns a store on db. reg may be nil (tests).
func NewStore(db *sql.DB, reg prometheus.Registerer) *Store {
	s := &Store{
		db: db,
		inserted: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_outbox_inserted_total",
			Help: "Outbox intents recorded, by kind (a repeated key is not counted)."}, []string{"kind"}),
		suppressed: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_outbox_suppressed_total",
			Help: "Outbox intents not recorded because the run was a replay or repair, by kind."}, []string{"kind"}),
	}
	for k := range kinds {
		s.inserted.WithLabelValues(k).Add(0)
		s.suppressed.WithLabelValues(k).Add(0)
	}
	if reg != nil {
		reg.MustRegister(s.inserted, s.suppressed)
	}
	return s
}

// Insert records in in tx, the caller's transaction (so it rolls back with the change that causes it). It reports whether a row was written:
// false for a key already recorded, and for a suppressed context.
func (s *Store) Insert(ctx context.Context, tx *sql.Tx, in Intent) (bool, error) {
	if err := in.validate(); err != nil {
		return false, err
	}
	if v, _ := ctx.Value(suppressKey{}).(bool); v {
		s.suppressed.WithLabelValues(in.Kind).Inc()
		return false, nil
	}
	payload := []byte(in.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var vehicle any
	if in.VehicleID != uuid.Nil {
		vehicle = in.VehicleID
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO outbox (key, user_id, vehicle_id, kind, payload) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO NOTHING`, in.Key, in.UserID, vehicle, in.Kind, payload)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 1 {
		s.inserted.WithLabelValues(in.Kind).Inc()
	}
	return n == 1, nil
}

// Claim leases up to n due rows to owner for lease. A row is due when it is not sent or failed and its claim is absent or expired (a Release
// pushes claimed_until out by its backoff, so a backed-off row is not due). attempts rises at the claim. A row already claimed MaxAttempts
// times is set aside as failed here and not returned, so a row that crashes its sender cannot come back forever on lease expiry.
func (s *Store) Claim(ctx context.Context, owner string, n int, lease time.Duration) ([]Row, error) {
	return s.claim(ctx, owner, nil, n, lease)
}

// claim leases due rows of kinds (all kinds when nil).
func (s *Store) claim(ctx context.Context, owner string, kinds []string, n int, lease time.Duration) ([]Row, error) {
	if owner == "" || n <= 0 {
		return nil, errors.New("outbox: claim needs an owner and a positive count")
	}
	var kindArg any
	if kinds != nil {
		kindArg = strings.Join(kinds, ",") // kinds are from the fixed set: no comma in any
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH due AS (
			SELECT id FROM outbox
			 WHERE sent_at IS NULL AND failed_at IS NULL AND (claimed_until IS NULL OR claimed_until <= now())
			   AND ($5::text IS NULL OR kind = ANY(string_to_array($5, ',')))
			 ORDER BY id LIMIT $2
			 FOR UPDATE SKIP LOCKED),
		poison AS (
			UPDATE outbox o SET failed_at = now(), claimed_by = NULL, claimed_until = NULL
			  FROM due WHERE o.id = due.id AND o.attempts >= $4
			RETURNING o.id)
		UPDATE outbox o SET claimed_by = $1, claimed_until = now() + make_interval(secs => $3), attempts = o.attempts + 1
		  FROM due WHERE o.id = due.id AND o.attempts < $4
		RETURNING o.id, o.key, o.user_id, o.vehicle_id, o.kind, o.payload, o.created_at, o.attempts`,
		owner, n, lease.Seconds(), MaxAttempts, kindArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var vehicle uuid.NullUUID
		if err := rows.Scan(&r.ID, &r.Key, &r.UserID, &vehicle, &r.Kind, &r.Payload, &r.CreatedAt, &r.Attempts); err != nil {
			return nil, err
		}
		r.VehicleID = vehicle.UUID
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimKinds is Claim restricted to the given kinds. With none it claims nothing and does not query: a sender with no handler registered
// leaves every row alone (not claimed, not counted as an attempt).
func (s *Store) ClaimKinds(ctx context.Context, owner string, kinds []string, n int, lease time.Duration) ([]Row, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	return s.claim(ctx, owner, kinds, n, lease)
}

// MarkSent records the send. It needs the row to be claimed by owner and not yet sent; it does not require the lease to be live, so a send that
// finishes just after its lease expired (and before anyone reclaimed the row) is recorded and not sent again.
func (s *Store) MarkSent(ctx context.Context, id int64, owner string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET sent_at = now(), claimed_until = NULL
		 WHERE id = $1 AND claimed_by = $2 AND sent_at IS NULL AND failed_at IS NULL`, id, owner)
	return one(res, err)
}

// Release gives a row back after a failed send. claimed_by is cleared and claimed_until set to now() + backoff (30 s doubling per attempt,
// capped at MaxBackoff), which the claim query honours, so the row is not due until the backoff passes. A row at MaxAttempts is set aside as failed.
func (s *Store) Release(ctx context.Context, id int64, owner string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET claimed_by = NULL,
		       claimed_until = now() + make_interval(secs => LEAST($3::float8, 30 * power(2, LEAST(GREATEST(attempts - 1, 0), 10)))),
		       failed_at = CASE WHEN attempts >= $4 THEN now() END
		 WHERE id = $1 AND claimed_by = $2 AND sent_at IS NULL AND failed_at IS NULL`, id, owner, MaxBackoff.Seconds(), MaxAttempts)
	return one(res, err)
}

// Drop sets a claimed row aside as failed without a send (a stale intent past its kind's MaxAge).
func (s *Store) Drop(ctx context.Context, id int64, owner string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET failed_at = now(), claimed_by = NULL, claimed_until = NULL
		 WHERE id = $1 AND claimed_by = $2 AND sent_at IS NULL AND failed_at IS NULL`, id, owner)
	return one(res, err)
}

// Purged is what one PurgeBatch removed, by class.
type Purged struct{ Sent, Failed, Expired int64 }

// Total is the rows removed.
func (p Purged) Total() int64 { return p.Sent + p.Failed + p.Expired }

// PurgeBatch deletes at most limit rows, oldest id first: sent before sentBefore, failed before failedBefore, and pending rows (never sent, never
// failed) older than their kind's MaxAge plus PendingGrace, which nothing consumed (a shadow row with no handler, or an intent past any use). A short batch
// (Total < limit) means the table is clear of them; the caller loops with pauses so no statement runs long.
func (s *Store) PurgeBatch(ctx context.Context, sentBefore, failedBefore time.Time, limit int) (Purged, error) {
	var p Purged
	var ks, ss []string
	for k, d := range MaxAge {
		ks = append(ks, k)
		ss = append(ss, strconv.FormatFloat((d+PendingGrace).Seconds(), 'f', 0, 64))
	}
	if limit <= 0 {
		return p, errors.New("outbox: purge needs a positive limit")
	}
	err := s.db.QueryRowContext(ctx, `
		WITH gone AS (
			DELETE FROM outbox WHERE id IN (
				SELECT id FROM outbox
				 WHERE (sent_at IS NOT NULL AND sent_at < $1)
				    OR (failed_at IS NOT NULL AND failed_at < $2)
				    OR (sent_at IS NULL AND failed_at IS NULL AND created_at < now() - make_interval(secs => (
					      SELECT s FROM unnest(string_to_array($4, ',')::text[], string_to_array($5, ',')::float8[]) AS m(k, s) WHERE m.k = kind)))
				 ORDER BY id LIMIT $3)
			RETURNING sent_at, failed_at)
		SELECT count(*) FILTER (WHERE sent_at IS NOT NULL),
		       count(*) FILTER (WHERE failed_at IS NOT NULL),
		       count(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL)
		  FROM gone`, sentBefore.UTC(), failedBefore.UTC(), limit, strings.Join(ks, ","), strings.Join(ss, ",")).Scan(&p.Sent, &p.Failed, &p.Expired)
	return p, err
}

func one(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotClaimed
	}
	return nil
}
