package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// The Settings toggle for the Rivian expiry reminders exists only where the reminders are
// sent: with the sender off it must 404 (the web hides the toggle on a 404), or it would show
// "email me before the connection expires" switched on where nothing is sent.
func TestExpiryReminderSettingsRoutesExistOnlyWhenTheSenderRuns(t *testing.T) {
	lazy, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none") // nothing connects
	if err != nil {
		t.Fatal(err)
	}
	defer lazy.Close()
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	status := func(sender bool, method string) int {
		router := New(Deps{Auth: svc, AuthEnforced: true, DB: lazy, RivianExpiryEmails: sender})
		req := httptest.NewRequest(method, "https://rivolt.dev/api/settings/rivian-expiry-email", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithUser(req.Context(), uuid.New())
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if c := status(false, m); c != http.StatusNotFound {
			t.Errorf("%s with the sender off: %d, want 404", m, c)
		}
		if c := status(true, m); c == http.StatusNotFound {
			t.Errorf("%s with the sender on: 404, want the route mounted", m)
		}
	}
}
