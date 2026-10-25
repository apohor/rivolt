package api

import (
	"database/sql"
	"testing"
)

// NewRequestStores is what main and the pool-on tests share: every store is there, and Secrets exists only with a sealer.
func TestNewRequestStoresBuildsEveryStore(t *testing.T) {
	req, sys := new(sql.DB), new(sql.DB)
	rs := NewRequestStores(req, sys, nil)
	if rs.Resolver == nil || rs.Drives == nil || rs.Charges == nil || rs.Samples == nil || rs.Settings == nil || rs.Push == nil ||
		rs.Trips == nil || rs.PackHealth == nil || rs.AIBudget == nil {
		t.Fatalf("a request-side store is missing: %+v", rs)
	}
	if rs.Secrets != nil {
		t.Error("Secrets without a sealer must be nil (the Rivian sign-in surface is read-only then)")
	}
	if rs.Push.DB() != req {
		t.Error("the push factory's own pool must be the request pool")
	}
}
