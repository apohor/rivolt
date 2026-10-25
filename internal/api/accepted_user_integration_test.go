//go:build integration

package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// acceptedUser makes (or finds) the account called name and records that it has accepted the current Terms.
//
// Use it for every integration test that signs a user in through the router and is not about the Terms. The server-side Terms
// gate answers 403 terms_required on every route behind a session to a fresh account that has not accepted, so a test that
// builds its user with db.EnsureUser alone fails with a confusing 403. Tests of the Terms themselves (terms_integration_test.go)
// build their own users on purpose.
func acceptedUser(ctx context.Context, t *testing.T, pool *sql.DB, name string) uuid.UUID {
	t.Helper()
	uid, err := db.EnsureUser(ctx, pool, name)
	if err != nil {
		t.Fatalf("ensure user %q: %v", name, err)
	}
	if err := db.AcceptTerms(ctx, pool, uid, currentTermsVersion); err != nil {
		t.Fatalf("accept the Terms for %q: %v", name, err)
	}
	return uid
}
