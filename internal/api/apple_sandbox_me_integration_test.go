//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/testpg"
)

// While a version is in App Review the iPhone app sees the purchase UI (billing_enabled) only for the accounts on the operator's allowlist;
// everyone else on the app keeps the paywall hidden until RIVOLT_BILLING_IOS goes on. The web is untouched.
func TestAuthMeBillingEnabledForAllowlistedReviewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	reviewer, _ := db.EnsureUser(ctx, pool, "me-reviewer")
	other, _ := db.EnsureUser(ctx, pool, "me-other")
	if _, err := pool.ExecContext(ctx, `INSERT INTO apple_sandbox_accounts (user_id) VALUES ($1)`, reviewer); err != nil {
		t.Fatal(err)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	for _, c := range []struct {
		name       string
		uid        string
		bearer     bool
		build      string
		iosBilling bool
		want       bool
	}{
		{"reviewer, new build, switch off", reviewer.String(), true, "100", false, true},
		{"reviewer, build 1.4.3 (older than the purchase UI)", reviewer.String(), true, "99", false, false},
		{"reviewer, no build header", reviewer.String(), true, "", false, false},
		{"other account, new build, switch off", other.String(), true, "100", false, false},
		{"other account, new build, switch on", other.String(), true, "100", true, true},
		{"other account on the web", other.String(), false, "", false, true},
	} {
		uid := reviewer
		if c.uid == other.String() {
			uid = other
		}
		rctx := auth.WithUser(ctx, uid)
		if c.bearer {
			rctx = logging.WithAuthMethod(rctx, logging.AuthBearer)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil).WithContext(rctx)
		if c.build != "" {
			req.Header.Set("X-Rivolt-App-Build", c.build)
		}
		rec := httptest.NewRecorder()
		handleMeEnrichedPlan(newMeAuth(t), pool, false, nil, false, ent, c.iosBilling, 100)(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v: %s", c.name, err, rec.Body.String())
		}
		if body["billing_enabled"] != c.want {
			t.Errorf("%s: billing_enabled %v, want %v", c.name, body["billing_enabled"], c.want)
		}
	}
}
