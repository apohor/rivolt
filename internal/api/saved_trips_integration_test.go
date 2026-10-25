//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
	"github.com/apohor/rivolt/internal/trips"
)

// What a client sees at the edges of the saved-trip routes, against real tables: a taken name is 409 name_taken, the (cap+1)th is 422
// trip_limit, both as JSON {error, class}.
func TestSavedTripsEdgesAreJSON(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "saved-edges")
	store, err := trips.OpenStore(pool, uid)
	if err != nil {
		t.Fatal(err)
	}
	post := func(name string) (int, struct{ Error, Class string }) {
		rec := httptest.NewRecorder()
		body := fmt.Sprintf(`{"name":%q,"inputs":{"o":"x"}}`, name)
		handleSavedTripCreate(store)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/saved", strings.NewReader(body)))
		var out struct{ Error, Class string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := post("first"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if code, out := post("first"); code != http.StatusConflict || out.Class != "name_taken" || out.Error == "" {
		t.Errorf("duplicate name: %d %+v, want 409 name_taken", code, out)
	}
	for i := 1; i < trips.MaxPerUser; i++ {
		if code, _ := post(fmt.Sprintf("filler %03d", i)); code != http.StatusCreated {
			t.Fatalf("filler %d: %d", i, code)
		}
	}
	if code, out := post("over"); code != http.StatusUnprocessableEntity || out.Class != "trip_limit" || out.Error == "" {
		t.Errorf("over the cap: %d %+v, want 422 trip_limit", code, out)
	}
}
