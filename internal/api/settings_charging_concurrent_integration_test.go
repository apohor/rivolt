//go:build integration

package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

// A phone PATCH racing a web PUT (and two PATCHes racing each other) must not lose a
// write or leave a mix of two writers' values: each write is one statement on the
// settings row. A reader watching throughout sees only whole pairs.
func TestChargingRateWritesAreAtomic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "rate-atomic")
	store := settings.NewFactory(pool).For(uid)
	seed := settings.ChargingConfig{HomePricePerKWh: 0.20, HomeCurrency: "GBP", GasPricePerGallon: 5.5, ComparisonMPG: 25}
	if err := settings.SetChargingConfig(ctx, store, seed); err != nil {
		t.Fatal(err)
	}

	type pair struct {
		price float64
		cur   string
	}
	valid := map[pair]bool{{0.20, "GBP"}: true, {1.1, "USD"}: true, {2.2, "EUR"}: true}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var mu sync.Mutex
	var bad []pair
	reads := 0
	wg.Add(1)
	go func() { // the reader
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			c, err := settings.GetChargingConfig(ctx, store)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			reads++
			if p := (pair{c.HomePricePerKWh, c.HomeCurrency}); !valid[p] {
				bad = append(bad, p)
			}
			if c.GasPricePerGallon != 5.5 || c.ComparisonMPG != 25 {
				t.Errorf("a rate write touched the gas price or MPG: %+v", c)
			}
			mu.Unlock()
		}
	}()
	var writers sync.WaitGroup
	const rounds = 150
	writers.Add(2)
	go func() { // the web: a full overwrite of the same values each time
		defer writers.Done()
		for i := 0; i < rounds; i++ {
			if err := settings.SetChargingConfig(ctx, store, settings.ChargingConfig{HomePricePerKWh: 1.1, HomeCurrency: "USD", GasPricePerGallon: 5.5, ComparisonMPG: 25}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() { // the phone: both rate fields in one patch
		defer writers.Done()
		price, cur := 2.2, "EUR"
		for i := 0; i < rounds; i++ {
			if err := settings.PatchChargingConfig(ctx, store, &price, &cur, nil, nil); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	writers.Wait()
	close(stop)
	wg.Wait()
	if len(bad) > 0 {
		t.Errorf("%d of %d reads saw a mix of two writers' values, e.g. %+v", len(bad), reads, bad[0])
	}
	if reads < 20 {
		t.Errorf("the reader only ran %d times", reads)
	}
	if c, _ := settings.GetChargingConfig(ctx, store); !valid[pair{c.HomePricePerKWh, c.HomeCurrency}] {
		t.Errorf("final state is a mix: %+v", c)
	}

	// Two PATCHes of different fields, run together, both land.
	for i := 0; i < 50; i++ {
		price, cur := float64(i%5)+0.5, []string{"USD", "CAD", "EUR", "GBP", "AUD"}[i%5]
		var w sync.WaitGroup
		w.Add(2)
		go func() { defer w.Done(); _ = settings.PatchChargingConfig(ctx, store, &price, nil, nil, nil) }()
		go func() { defer w.Done(); _ = settings.PatchChargingConfig(ctx, store, nil, &cur, nil, nil) }()
		w.Wait()
		c, _ := settings.GetChargingConfig(ctx, store)
		if c.HomePricePerKWh != price || c.HomeCurrency != cur {
			t.Fatalf("round %d: lost a write: got %+v, want %v %s", i, c, price, cur)
		}
	}

	// A patch of only the gas price and MPG lands and leaves the rate alone.
	before, _ := settings.GetChargingConfig(ctx, store)
	gas, mpg := 6.25, 31.0
	if err := settings.PatchChargingConfig(ctx, store, nil, nil, &gas, &mpg); err != nil {
		t.Fatal(err)
	}
	if c, _ := settings.GetChargingConfig(ctx, store); c.GasPricePerGallon != gas || c.ComparisonMPG != mpg || c.HomePricePerKWh != before.HomePricePerKWh || c.HomeCurrency != before.HomeCurrency {
		t.Errorf("gas/MPG patch: got %+v, want gas %v mpg %v with the rate %v %s unchanged", c, gas, mpg, before.HomePricePerKWh, before.HomeCurrency)
	}
}
