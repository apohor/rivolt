package forward

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/leases"
)

// LocalLeases is what the forwarder asks of this pod's lease store (*leases.Store satisfies it). The forwarder only reads.
type LocalLeases interface {
	GroupTokens() []leases.GroupToken
	PastLocalDeadline() bool
}

// leaseCacheTTL is how long a lease row read is reused for a group.
const leaseCacheTTL = 2 * time.Second

// PGLeaseView is the LeaseView over Postgres and the local lease store. The lease row is read on the SYSTEM pool (C3): row-level
// security on a user pool could hide the row, and a hidden row reads as "no owner", which is the wrong answer to fail toward.
type PGLeaseView struct {
	db    *sql.DB
	local LocalLeases
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]cachedOwner
}

type cachedOwner struct {
	owner LeaseOwner
	at    time.Time
}

// NewPGLeaseView returns a view over db (the system pool) and local (this pod's lease store).
func NewPGLeaseView(db *sql.DB, local LocalLeases) *PGLeaseView {
	return &PGLeaseView{db: db, local: local, now: time.Now, cache: map[string]cachedOwner{}}
}

// Owner reads the group's lease row (cached for leaseCacheTTL). A group with no row, or whose row is released or expired, has no
// owner (Found false). The pod and epoch of the row are the holder as the database sees it.
func (v *PGLeaseView) Owner(ctx context.Context, group string) (LeaseOwner, error) {
	v.mu.Lock()
	if c, ok := v.cache[group]; ok && v.now().Sub(c.at) < leaseCacheTTL {
		v.mu.Unlock()
		return c.owner, nil
	}
	v.mu.Unlock()
	var pod sql.NullString
	var epoch int64
	var exp sql.NullTime
	var live bool
	// The database decides whether the row is still live, with its own clock: pods never compare wall clocks.
	err := v.db.QueryRowContext(ctx, `SELECT pod_id, epoch, expires_at, expires_at > statement_timestamp() FROM account_leases WHERE group_id = $1`, group).Scan(&pod, &epoch, &exp, &live)
	var o LeaseOwner
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return LeaseOwner{}, err
	case pod.Valid && pod.String != "" && exp.Valid && live:
		// A released or expired row is no owner. This is advisory: the owner re-checks its own lease before it runs anything.
		o = LeaseOwner{Found: true, Pod: pod.String, Epoch: epoch, ExpiresAt: exp.Time}
	}
	v.mu.Lock()
	v.cache[group] = cachedOwner{o, v.now()}
	if len(v.cache) > 4096 { // groups are few; a runaway cache is a bug, not a feature
		v.cache = map[string]cachedOwner{group: {o, v.now()}}
	}
	v.mu.Unlock()
	return o, nil
}

// Held reports whether this pod holds the group, and at which epoch, as of its last renew.
func (v *PGLeaseView) Held(group string) (int64, bool) {
	for _, t := range v.local.GroupTokens() {
		if t.Group == group {
			return t.Epoch, true
		}
	}
	return 0, false
}

// PastDeadline reports whether this pod's local lease deadline has passed.
func (v *PGLeaseView) PastDeadline() bool { return v.local.PastLocalDeadline() }

var _ LeaseView = (*PGLeaseView)(nil)
