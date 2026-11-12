//go:build integration

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// Opting in queues one receipts sync for the user; opting out and a failed save queue none. The hook only queues: the handler has no Rivian
// client to call.
func TestRivianReceiptsOptInQueuesOneSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "optin-sync")
	if err != nil {
		t.Fatal(err)
	}
	var asked []uuid.UUID
	h := handleRivianReceiptsPut(nil, settings.NewFactory(pool), nil, func(u uuid.UUID) { asked = append(asked, u) })
	put := func(body string) int {
		rec := httptest.NewRecorder()
		h(uid, rec, httptest.NewRequest("PUT", "/api/settings/rivian-receipts", strings.NewReader(body)))
		return rec.Code
	}
	if code := put(`{"opt_in":true}`); code != 200 || len(asked) != 1 || asked[0] != uid {
		t.Errorf("opt in: status %d, asked %v", code, asked)
	}
	if code := put(`{"opt_in":false}`); code != 200 || len(asked) != 1 {
		t.Errorf("opt out: status %d, asked %v, want no new ask", code, asked)
	}
	if code := put(`{bad`); code != 400 || len(asked) != 1 {
		t.Errorf("bad body: status %d, asked %v", code, asked)
	}
}
