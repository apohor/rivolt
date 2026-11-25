package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/logging"
)

// While a version is in App Review the iPhone app sees the purchase UI (billing_enabled) only for the review account named in the values AND a
// build with the purchase UI; everyone else on the app keeps the paywall hidden until RIVOLT_BILLING_IOS goes on. The web is untouched, and
// with the flag off nobody is special.
func TestAuthMeBillingEnabledForReviewAccount(t *testing.T) {
	db := sql.OpenDB(meNoDB{})
	defer db.Close()
	demo := uuid.New() // the demo account answers billing on without a lookup
	ent := entitlements.New(db, entitlements.Config{Billing: true, Demo: demo})
	cfg := func(accept bool, accounts ...uuid.UUID) *applebill.Config {
		m := map[uuid.UUID]bool{}
		for _, a := range accounts {
			m[a] = true
		}
		return &applebill.Config{AcceptSandbox: accept, SandboxMinBuild: 100, SandboxAccounts: m}
	}
	for _, c := range []struct {
		name       string
		review     *applebill.Config
		bearer     bool
		build      string
		iosBilling bool
		want       bool
	}{
		{"review account, new build, switch off", cfg(true, demo), true, "100", false, true},
		{"review account, build 1.4.3 (older than the purchase UI)", cfg(true, demo), true, "99", false, false},
		{"review account, no build header", cfg(true, demo), true, "", false, false},
		{"review account, flag off", cfg(false, demo), true, "100", false, false},
		{"no review config", nil, true, "100", false, false},
		{"other account, new build, switch off", cfg(true, uuid.New()), true, "100", false, false},
		{"other account, new build, switch on", cfg(true, uuid.New()), true, "100", true, true},
		{"web", cfg(true, uuid.New()), false, "", false, true},
	} {
		ctx := auth.WithUser(context.Background(), demo)
		if c.bearer {
			ctx = logging.WithAuthMethod(ctx, logging.AuthBearer)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil).WithContext(ctx)
		if c.build != "" {
			req.Header.Set("X-Rivolt-App-Build", c.build)
		}
		rec := httptest.NewRecorder()
		handleMeEnrichedPlan(newMeAuth(t), nil, false, nil, false, ent, c.iosBilling, c.review)(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v: %s", c.name, err, rec.Body.String())
		}
		if body["billing_enabled"] != c.want {
			t.Errorf("%s: billing_enabled %v, want %v", c.name, body["billing_enabled"], c.want)
		}
	}
}
