package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
)

type memClaims struct {
	engage.Store
	mu   sync.Mutex
	rows map[string]string
	err  error
}

func (c *memClaims) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return false, false, c.err
	}
	k := m.User.String() + "/" + m.Kind + "/" + m.Key
	if _, ok := c.rows[k]; ok {
		return false, false, nil
	}
	if c.rows == nil {
		c.rows = map[string]string{}
	}
	c.rows[k] = "sending"
	return true, false, nil
}

func (c *memClaims) Mark(_ context.Context, uid uuid.UUID, kind, key, status string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[uid.String()+"/"+kind+"/"+key] = status
	return nil
}

// A charge's confirm push goes out once however many pods run its close hook: the first claim sends and records it, the second does not.
func TestNetworkConfirmPushIsClaimedOncePerCharge(t *testing.T) {
	ctx := context.Background()
	claims := &memClaims{}
	uid := uuid.New()
	now := time.Now()

	send, mark := claimNetworkConfirm(ctx, claims, uid, "live_rv-test-1_c_1", now)
	if !send {
		t.Fatal("the first pod may not send")
	}
	mark("sent")
	if again, _ := claimNetworkConfirm(ctx, claims, uid, "live_rv-test-1_c_1", now); again {
		t.Error("a second pod sent the same charge's push")
	}
	if got := claims.rows[uid.String()+"/"+networkConfirmKind+"/live_rv-test-1_c_1"]; got != "sent" {
		t.Errorf("claim status %q, want sent", got)
	}
	if other, _ := claimNetworkConfirm(ctx, claims, uid, "live_rv-test-1_c_2", now); !other {
		t.Error("another charge's push was blocked")
	}
	// No store, no charge id, or a claim that fails: send, as before the claim existed.
	if s, _ := claimNetworkConfirm(ctx, nil, uid, "c", now); !s {
		t.Error("no store: not sent")
	}
	if s, _ := claimNetworkConfirm(ctx, &memClaims{err: errors.New("db down")}, uid, "c", now); !s {
		t.Error("a failing claim: not sent")
	}
}
