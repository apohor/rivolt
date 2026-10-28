//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/appsettings"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// Three replicas on one Postgres: an operator turning recap weather off (the privacy
// switch for sending drive coordinates to Open-Meteo) on one is seen by the other two
// within the poll bound; so is a row rewritten behind their backs.
func TestRecapWeatherToggleReachesEveryReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	kv, err := appsettings.New(pool, crypto.NoopSealer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Set(ctx, "recap.weather_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	var pods [3]*settings.Manager
	for i := range pods {
		m, err := settings.NewManager(ctx, kv, settings.AIConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if !m.RecapWeatherEnabled() {
			t.Fatalf("replica %d should start with weather on", i)
		}
		pods[i] = m
		go m.Sync(ctx, 100*time.Millisecond)
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ok() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("not within 5 s: %s", what)
	}

	off := false
	if _, err := pods[0].UpdateRecap(ctx, settings.RecapUpdate{WeatherEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	wait("replicas B and C stop recap weather", func() bool {
		return !pods[1].RecapWeatherEnabled() && !pods[2].RecapWeatherEnabled()
	})

	// A row rewritten behind the replicas' backs (an operator in psql) also moves the version.
	if err := kv.Delete(ctx, "recap.weather_enabled"); err != nil {
		t.Fatal(err)
	}
	if err := kv.Set(ctx, "recap.weather_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	wait("all replicas see weather on again", func() bool {
		return pods[0].RecapWeatherEnabled() && pods[1].RecapWeatherEnabled() && pods[2].RecapWeatherEnabled()
	})
}
