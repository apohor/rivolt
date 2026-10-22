package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/packhealth"
)

func TestNewestSamples(t *testing.T) {
	mk := func(n int) []packhealth.Sample {
		var out []packhealth.Sample
		for i := 0; i < n; i++ {
			out = append(out, packhealth.Sample{At: time.Unix(int64(1000+i), 0), PackKWhEffective: float64(i)})
		}
		return out
	}
	for _, c := range []struct{ have, limit, want int }{{5, 0, 5}, {5, 2, 2}, {5, 5, 5}, {5, 9, 5}, {0, 3, 0}} {
		got := newestSamples(mk(c.have), c.limit)
		if got == nil || len(got) != c.want {
			t.Errorf("newestSamples(%d, %d) = %d rows (nil=%v), want %d", c.have, c.limit, len(got), got == nil, c.want)
		}
		if c.limit > 0 && c.have > c.limit && (got[0].PackKWhEffective != float64(c.have-c.limit) || got[len(got)-1].PackKWhEffective != float64(c.have-1)) {
			t.Errorf("newestSamples(%d, %d) did not keep the newest, oldest first: %v", c.have, c.limit, got)
		}
	}
	if got := newestSamples(nil, 0); got == nil || len(got) != 0 {
		t.Errorf("nil in = %v, want an empty non-nil slice", got)
	}
}

// ?limit is checked before anything is read: a bad one is a 400 whatever else is
// missing, and a good one (or none) reaches the next check (here, no database: 503).
func TestPackHealthLimitIsValidated(t *testing.T) {
	call := func(query string) int {
		r := chi.NewRouter()
		r.Get("/api/vehicles/{vehicleID}/pack-health", func(w http.ResponseWriter, r *http.Request) {
			handlePackHealthGet(nil, nil)(uuid.New(), w, r)
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/vehicles/veh-fixture/pack-health"+query, nil)
		r.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), uuid.New())))
		return w.Code
	}
	for _, q := range []string{"?limit=0", "?limit=-3", "?limit=abc", "?limit=1.5", "?limit=5001", "?limit=" + strings.Repeat("9", 30)} {
		if code := call(q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	for _, q := range []string{"", "?limit=1", "?limit=5000"} {
		if code := call(q); code != http.StatusServiceUnavailable {
			t.Errorf("%q: %d, want 503 (past the limit check, no database)", q, code)
		}
	}
}

// Through the real router with full URLs: the route takes ?limit as written.
func TestPackHealthLimitThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	get := func(query string) int {
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/vehicles/veh-fixture/pack-health"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), uid))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for _, q := range []string{"?limit=0", "?limit=abc", "?limit=5001"} {
		if code := get(q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	if code := get("?limit=3"); code != http.StatusServiceUnavailable {
		t.Errorf("?limit=3: %d, want 503 (no database in this router)", code)
	}
}
