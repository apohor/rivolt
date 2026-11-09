//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// Single-owner M4-3: a needs_reauth raise carries the group's epoch (and, for RaiseNeedsReauthIfCurrent, the session version). A pod that holds the
// group raises; one that lost it is refused (fence.ErrFenced) and nothing is written; once raises are owner-only a raise with no token is refused
// (db.ErrNotOwner); a stale session version raises nothing. The clear stays a user and operator action, and SetNeedsReauth no longer raises.
func TestNeedsReauthRaiseCarriesTheEpoch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	t.Cleanup(func() { db.SetReauthRequiresOwner(false) })

	newUser := func(name string) (uuid.UUID, time.Time) {
		uid, err := db.EnsureUser(ctx, pool, name+"-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'k')`, uid); err != nil {
			t.Fatal(err)
		}
		var ver time.Time
		if err := pool.QueryRowContext(ctx, `SELECT updated_at FROM user_secrets WHERE user_id = $1 AND name = 'rivian.session'`, uid).Scan(&ver); err != nil {
			t.Fatal(err)
		}
		return uid, ver
	}
	lease := func(uid uuid.UUID, pod string, live bool) fence.Token {
		exp := "now() + interval '30 seconds'"
		if !live {
			exp = "now() - interval '1 minute'"
		}
		var epoch int64
		if err := pool.QueryRowContext(ctx, `INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
			VALUES ($1, $2, nextval('account_lease_epoch'), now(), now(), `+exp+`) RETURNING epoch`, uid, pod).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		return fence.Token{Group: uid.String(), Pod: pod, Epoch: epoch}
	}
	flagged := func(uid uuid.UUID) bool {
		needs, _, err := db.GetNeedsReauth(ctx, pool, uid)
		if err != nil {
			t.Fatal(err)
		}
		return needs
	}

	// The owner raises, with the version of the session it used.
	owner, ver := newUser("reauth-owner")
	tok := lease(owner, "pod-a", true)
	if tr, err := db.RaiseNeedsReauthIfCurrent(fence.With(ctx, tok), pool, owner, "rejected", ver); err != nil || !tr || !flagged(owner) {
		t.Fatalf("owner: transitioned=%v err=%v flagged=%v, want a raise", tr, err, flagged(owner))
	}
	if err := db.SetNeedsReauth(ctx, pool, owner, ""); err != nil || flagged(owner) {
		t.Fatalf("clear: err=%v flagged=%v", err, flagged(owner))
	}
	// A stale session version, even from the owner, raises nothing and is not a fence failure.
	if tr, err := db.RaiseNeedsReauthIfCurrent(fence.With(ctx, tok), pool, owner, "rejected", ver.Add(-time.Hour)); err != nil || tr || flagged(owner) {
		t.Errorf("stale version: transitioned=%v err=%v flagged=%v, want nothing", tr, err, flagged(owner))
	}

	// A pod that lost the group (its row expired) is refused and writes nothing, for both raises.
	lost, lver := newUser("reauth-lost")
	stale := lease(lost, "pod-gone", false)
	if _, err := db.RaiseNeedsReauthIfCurrent(fence.With(ctx, stale), pool, lost, "rejected", lver); !errors.Is(err, fence.ErrFenced) || flagged(lost) {
		t.Errorf("lost group (IfCurrent): err=%v flagged=%v, want fence.ErrFenced and no flag", err, flagged(lost))
	}
	if _, err := db.RaiseNeedsReauth(fence.With(ctx, stale), pool, lost, "rejected"); !errors.Is(err, fence.ErrFenced) || flagged(lost) {
		t.Errorf("lost group (plain): err=%v flagged=%v, want fence.ErrFenced and no flag", err, flagged(lost))
	}

	// No token: unguarded as before while raises are not owner-only; refused once they are.
	free, fver := newUser("reauth-free")
	if tr, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, free, "rejected", fver); err != nil || !tr {
		t.Fatalf("no token, flag off: transitioned=%v err=%v, want the raise to go through as before", tr, err)
	}
	if err := db.SetNeedsReauth(ctx, pool, free, ""); err != nil {
		t.Fatal(err)
	}
	db.SetReauthRequiresOwner(true)
	if _, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, free, "rejected", fver); !errors.Is(err, db.ErrNotOwner) || flagged(free) {
		t.Errorf("no token, owner-only (IfCurrent): err=%v flagged=%v, want db.ErrNotOwner", err, flagged(free))
	}
	if _, err := db.RaiseNeedsReauth(ctx, pool, free, "rejected"); !errors.Is(err, db.ErrNotOwner) || flagged(free) {
		t.Errorf("no token, owner-only (plain): err=%v flagged=%v, want db.ErrNotOwner", err, flagged(free))
	}
	// The owner still raises when raises are owner-only.
	if tr, err := db.RaiseNeedsReauth(fence.With(ctx, lease(free, "pod-b", true)), pool, free, "rejected"); err != nil || !tr {
		t.Errorf("owner under owner-only: transitioned=%v err=%v", tr, err)
	}
	// A user with no group has no owner to wait for: marked fence.Unowned, the raise goes through even when raises are owner-only (the session
	// version still guards it).
	none, nver := newUser("reauth-ungrouped")
	if tr, err := db.RaiseNeedsReauthIfCurrent(fence.Unowned(ctx), pool, none, "rejected", nver.Add(-time.Hour)); err != nil || tr || flagged(none) {
		t.Errorf("ungrouped, stale version: transitioned=%v err=%v flagged=%v, want nothing", tr, err, flagged(none))
	}
	if tr, err := db.RaiseNeedsReauthIfCurrent(fence.Unowned(ctx), pool, none, "rejected", nver); err != nil || !tr || !flagged(none) {
		t.Errorf("ungrouped, owner-only: transitioned=%v err=%v flagged=%v, want the raise", tr, err, flagged(none))
	}
	// SetNeedsReauth no longer raises.
	if err := db.SetNeedsReauth(ctx, pool, free, "some reason"); err == nil {
		t.Error("SetNeedsReauth accepted a reason: it must only clear")
	}
}
