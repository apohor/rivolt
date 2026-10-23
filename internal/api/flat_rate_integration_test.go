//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// The choice is the user's own row: off by default, on after a write, in the settings
// export, never another user's, and turning it off (or on) leaves the stored schedule alone.
func TestFlatRateRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, err := db.EnsureUser(ctx, pool, "flat-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, pool, "flat-b")
	if err != nil {
		t.Fatal(err)
	}
	sf := settings.NewFactory(pool)
	sa, sb := sf.For(a), sf.For(b)
	sch, e := offpeak.Normalize(offpeak.Schedule{TZ: "America/Chicago", Windows: []offpeak.Window{{Days: "weekdays", Start: "21:00", End: "06:00", Rate: 0.09}}})
	if e != nil {
		t.Fatal(e)
	}
	if err := settings.SetOffpeak(ctx, sa, sch); err != nil {
		t.Fatal(err)
	}
	put := func(s *settings.Store, body string) int {
		rec := httptest.NewRecorder()
		handleFlatRatePut(s)(rec, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(body)))
		return rec.Code
	}
	view := func(s *settings.Store) offpeakResponse {
		rec := httptest.NewRecorder()
		handleOffpeakGet(s)(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		var out offpeakResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if v := view(sa); v.FlatRate || !v.Configured {
		t.Fatalf("default: %+v", v)
	}
	if c := put(sa, `{"flat_rate":true}`); c != 200 {
		t.Fatalf("set: %d", c)
	}
	if v := view(sa); !v.FlatRate || !v.Configured || len(v.Windows) != 1 {
		t.Errorf("on: %+v (the schedule must stay stored)", v)
	}
	if v := view(sb); v.FlatRate {
		t.Error("another user's choice changed")
	}
	all, _ := sa.GetAll(ctx)
	if _, ok := exportSettings(all)["charging.flat_rate"]; !ok {
		t.Error("the choice is not in the settings export")
	}
	if c := put(sa, `{"flat_rate":false}`); c != 200 {
		t.Fatalf("clear: %d", c)
	}
	if v := view(sa); v.FlatRate || !v.Configured || len(v.Windows) != 1 {
		t.Errorf("off: %+v (the schedule must still be stored)", v)
	}
	if all, _ = sa.GetAll(ctx); all["charging.flat_rate"] != "" {
		t.Error("turning it off left the key behind")
	}
}
