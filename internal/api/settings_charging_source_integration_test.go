//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

// The rate's source: a rate nobody marked reads as the user's, a state average stays marked until the user writes a rate, and the
// web's full overwrite with the same rate does not un-mark it.
func TestChargingHomePriceSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "price-source")
	store := settings.NewFactory(pool).For(uid)
	get := func() settings.ChargingConfig {
		t.Helper()
		c, err := settings.GetChargingConfig(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	f := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }

	if c := get(); c.HomePriceSource != "" {
		t.Errorf("no rate: source %q, want empty", c.HomePriceSource)
	}
	// A rate stored the old way (no source key) is the user's.
	if err := store.SetMany(ctx, map[string]string{"charging.home_price_per_kwh": "0.2"}); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != settings.PriceSourceUser {
		t.Errorf("legacy rate: source %q, want user", c.HomePriceSource)
	}
	// The setup's default.
	if err := settings.PatchChargingConfigSource(ctx, store, f(0.17), str(settings.PriceSourceStateAverage), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePricePerKWh != 0.17 || c.HomePriceSource != settings.PriceSourceStateAverage {
		t.Errorf("state average: %+v", c)
	}
	// A patch of another field leaves the source alone.
	if err := settings.PatchChargingConfig(ctx, store, nil, str("USD"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != settings.PriceSourceStateAverage {
		t.Errorf("currency patch changed the source: %q", c.HomePriceSource)
	}
	// The web's overwrite with the same rate and no source keeps the mark; a different rate is the user's.
	cur := get()
	cur.HomePriceSource = ""
	if err := settings.SetChargingConfig(ctx, store, cur); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != settings.PriceSourceStateAverage {
		t.Errorf("round trip un-marked the estimate: %q", c.HomePriceSource)
	}
	cur.HomePricePerKWh = 0.31
	if err := settings.SetChargingConfig(ctx, store, cur); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != settings.PriceSourceUser {
		t.Errorf("a typed rate: source %q, want user", c.HomePriceSource)
	}
	// A rate patched without a source is the user's; clearing the rate clears the source.
	if err := settings.PatchChargingConfigSource(ctx, store, f(0.17), str(settings.PriceSourceStateAverage), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := settings.PatchChargingConfig(ctx, store, f(0.17), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != settings.PriceSourceUser {
		t.Errorf("patched rate without a source: %q, want user", c.HomePriceSource)
	}
	if err := settings.PatchChargingConfig(ctx, store, f(0), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.HomePriceSource != "" {
		t.Errorf("rate cleared: source %q, want empty", c.HomePriceSource)
	}
}
