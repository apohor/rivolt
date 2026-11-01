//go:build integration

package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// ReauthRaisedAt: not flagged, flagged (the time of the raise), cleared, flagged again (a new time).
func TestReauthRaisedAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := db.EnsureUser(ctx, d, "raised-at")
	if err != nil {
		t.Fatal(err)
	}
	if _, flagged, err := db.ReauthRaisedAt(ctx, d, uid); err != nil || flagged {
		t.Fatalf("a new user: flagged=%v err=%v", flagged, err)
	}
	if _, err := db.RaiseNeedsReauth(ctx, d, uid, "session expired"); err != nil {
		t.Fatal(err)
	}
	first, flagged, err := db.ReauthRaisedAt(ctx, d, uid)
	if err != nil || !flagged || first.IsZero() || time.Since(first) > time.Minute {
		t.Fatalf("after a raise: at=%v flagged=%v err=%v", first, flagged, err)
	}
	if err := db.SetNeedsReauth(ctx, d, uid, ""); err != nil {
		t.Fatal(err)
	}
	if _, flagged, err := db.ReauthRaisedAt(ctx, d, uid); err != nil || flagged {
		t.Fatalf("after the clear: flagged=%v err=%v", flagged, err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := db.RaiseNeedsReauth(ctx, d, uid, "session expired"); err != nil {
		t.Fatal(err)
	}
	second, flagged, err := db.ReauthRaisedAt(ctx, d, uid)
	if err != nil || !flagged || !second.After(first) {
		t.Fatalf("raised again: at=%v (first %v) flagged=%v err=%v", second, first, flagged, err)
	}
	// An unknown user is not flagged.
	if _, flagged, err := db.ReauthRaisedAt(ctx, d, [16]byte{9}); err != nil || flagged {
		t.Fatalf("an unknown user: flagged=%v err=%v", flagged, err)
	}
}
