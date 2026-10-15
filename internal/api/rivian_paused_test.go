package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/rivian"
)

func statusBody(t *testing.T, paused func() bool, signedIn bool, wired bool) map[string]any {
	t.Helper()
	var reg rivian.AccountRegistry
	if wired {
		reg = rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient { return rivian.NewMock() })
	}
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil)
	if signedIn {
		req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
	}
	rec := httptest.NewRecorder()
	handleRivianStatus(reg, nil, nil, nil, nil, paused)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRivianStatus_PausedFlag(t *testing.T) {
	on, off := func() bool { return true }, func() bool { return false }
	for name, c := range map[string]struct{ signedIn, wired bool }{
		"signed in":          {true, true},
		"signed out":         {false, true},
		"registry not wired": {true, false},
	} {
		m := statusBody(t, on, c.signedIn, c.wired)
		if m["paused"] != true || m["paused_reason"] != "operator_pause" {
			t.Errorf("%s paused: %v", name, m)
		}
		m = statusBody(t, off, c.signedIn, c.wired)
		if m["paused"] != false {
			t.Errorf("%s not paused: %v", name, m)
		}
		if _, has := m["paused_reason"]; has {
			t.Errorf("%s: paused_reason must be omitted when not paused: %v", name, m)
		}
	}
	// nil func (no flag store, e.g. self-host without DB) = not paused.
	if m := statusBody(t, nil, true, true); m["paused"] != false {
		t.Errorf("nil paused func: %v", m)
	}
}

// Nothing the operator typed or their identity can reach the user.
func TestRivianStatus_PausedExposesNoOperatorDetail(t *testing.T) {
	m := statusBody(t, func() bool { return true }, true, true)
	for k := range m {
		switch k {
		case "enabled", "authenticated", "mfa_pending", "email", "needs_reauth", "needs_reauth_reason", "paused", "paused_reason":
		default:
			t.Errorf("unexpected key %q in the status response: %v", k, m)
		}
	}
	for _, bad := range []string{"actor", "reason", "paused_by", "admin"} {
		if _, has := m[bad]; has {
			t.Errorf("response exposes %q", bad)
		}
	}
}
