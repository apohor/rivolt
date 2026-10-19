package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
)

type fakeRestorer struct {
	err    error
	gotDry bool
	ctxErr error
}

func (f *fakeRestorer) Restore(ctx context.Context, uid, vid uuid.UUID, month time.Time, dryRun, wait bool) (archive.RestoreResult, error) {
	f.gotDry = dryRun
	f.ctxErr = ctx.Err()
	return archive.RestoreResult{InObject: 3, DryRun: dryRun}, f.err
}

// The admin restore validates its body and maps the archive's refusals to
// statuses; the archive being off is a 503.
func TestAdminArchiveRestore(t *testing.T) {
	good := `{"user_id":"` + uuid.NewString() + `","vehicle_id":"` + uuid.NewString() + `","month":"2026-05","dry_run":true}`
	for _, tc := range []struct {
		name string
		rs   ArchiveRestorer
		body string
		want int
	}{
		{"archive off", nil, good, http.StatusServiceUnavailable},
		{"bad body", &fakeRestorer{}, `{"user_id":"x","vehicle_id":"y","month":"May"}`, http.StatusBadRequest},
		{"busy", &fakeRestorer{err: archive.ErrBusy}, good, http.StatusConflict},
		{"no object", &fakeRestorer{err: archive.ErrNoObject}, good, http.StatusNotFound},
		{"ok", &fakeRestorer{}, good, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handleAdminArchiveRestore(tc.rs)(rec, httptest.NewRequest(http.MethodPost, "/api/admin/archive/restore", strings.NewReader(tc.body)))
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if f, ok := tc.rs.(*fakeRestorer); ok && tc.want == http.StatusOK && (!f.gotDry || !strings.Contains(rec.Body.String(), `"in_object":3`)) {
				t.Errorf("dry_run not passed or result not returned: %s", rec.Body.String())
			}
		})
	}
}
