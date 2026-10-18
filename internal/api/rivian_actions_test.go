package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestRivianActionsEnabled(t *testing.T) {
	for _, c := range []struct {
		client, forbidden string
		want              bool
	}{
		{"", "", true}, // unset is the live client
		{"live", "", true},
		{"mock", "", true},
		{"MOCK", "", true},
		{"stub", "", false},
		{" Stub ", "", false},
		{"replay", "", false},
		{"live", "true", false}, // live forbidden wins
		{"mock", "1", false},
		{"", "yes", false},
		{"live", "false", true},
	} {
		t.Setenv("RIVIAN_CLIENT", c.client)
		t.Setenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN", c.forbidden)
		if got := rivianActionsEnabled(); got != c.want {
			t.Errorf("RIVIAN_CLIENT=%q LIVE_FORBIDDEN=%q = %v, want %v", c.client, c.forbidden, got, c.want)
		}
	}
}

func TestConfigAdvertisesRivianActions(t *testing.T) {
	get := func() any {
		rec := httptest.NewRecorder()
		handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var out struct {
			Features map[string]any `json:"features"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Features["rivian_actions"]
	}
	t.Setenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN", "")
	t.Setenv("RIVIAN_CLIENT", "stub")
	if got := get(); got != false {
		t.Errorf("stub: features.rivian_actions = %v, want false", got)
	}
	t.Setenv("RIVIAN_CLIENT", "live")
	if got := get(); got != true {
		t.Errorf("live: features.rivian_actions = %v, want true", got)
	}
	t.Setenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN", "true")
	if got := get(); got != false {
		t.Errorf("live forbidden: features.rivian_actions = %v, want false", got)
	}
}

// The two admin actions that call Rivian say so plainly (409) where Rivian is
// off, before touching the registry or the database, and behave as before
// where it is on.
func TestAdminRivianActionsRefuseWhereRivianIsOff(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/users/{id}/sync-rivian", handleAdminUserSyncRivian(nil, nil, nil))
	r.Post("/users/{id}/refresh-rivian-session", handleAdminUserRefreshRivianSession(nil, nil, nil, nil))
	post := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		return rec.Code, rec.Body.String()
	}
	id := uuid.NewString()
	for name, env := range map[string][2]string{
		"stub":           {"stub", ""},
		"replay":         {"replay", ""},
		"live forbidden": {"live", "true"},
	} {
		t.Setenv("RIVIAN_CLIENT", env[0])
		t.Setenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN", env[1])
		for _, p := range []string{"/users/" + id + "/sync-rivian", "/users/" + id + "/refresh-rivian-session"} {
			code, body := post(p)
			if code != http.StatusConflict || !strings.Contains(body, "Rivian is disabled on this host") {
				t.Errorf("%s %s = %d %q, want 409 with the explanation", name, p, code, body)
			}
		}
	}
	// With Rivian on, the handlers get past the gate (here to their own
	// "unavailable" answer, since no registry is wired in this test).
	t.Setenv("RIVIAN_CLIENT", "live")
	t.Setenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN", "")
	for _, p := range []string{"/users/" + id + "/sync-rivian", "/users/" + id + "/refresh-rivian-session"} {
		if code, _ := post(p); code != http.StatusServiceUnavailable {
			t.Errorf("live %s = %d, want the handler's own 503", p, code)
		}
	}
}
