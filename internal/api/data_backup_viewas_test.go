package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// An admin viewing as a user cannot take that user's full backup: 403 before any
// store is touched (the Deps are empty, so reaching them would panic).
func TestBackupRefusedWhileViewingAs(t *testing.T) {
	uid, admin := uuid.New(), uuid.New()
	h := handleDataBackup(Deps{})

	viewAs := httptest.NewRequest(http.MethodGet, "/api/data/backup", nil)
	viewAs = viewAs.WithContext(auth.WithImpersonation(auth.WithUser(viewAs.Context(), uid), admin, uid))
	rec := httptest.NewRecorder()
	h(rec, viewAs)
	if rec.Code != http.StatusForbidden {
		t.Errorf("view-as backup: %d, want 403", rec.Code)
	}

	// Not signed in is still a 401 (the view-as check does not hide it).
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/data/backup", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", rec.Code)
	}
}
