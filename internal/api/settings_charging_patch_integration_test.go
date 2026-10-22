//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
)

// PATCH /api/settings/charging against Postgres: only the fields sent change (the
// others, the gas price and comparison MPG included, keep their stored values), another
// user's settings are never touched, and the audit line names the user, the source, the
// group and the fields, never a value.
func TestChargingRatePatchMerges(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	// No time attribute: its digits could contain a value the test looks for.
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "rate-patch-a")
	b, _ := db.EnsureUser(ctx, pool, "rate-patch-b")
	sf := settings.NewFactory(pool)
	seed := settings.ChargingConfig{HomePricePerKWh: 0.20, HomeCurrency: "GBP", GasPricePerGallon: 5.5, ComparisonMPG: 25}
	for _, u := range []uuid.UUID{a, b} {
		if err := settings.SetChargingConfig(ctx, sf.For(u), seed); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(uid uuid.UUID, kind, body string) (int, settings.ChargingConfig) {
		req := httptest.NewRequest(http.MethodPatch, "/api/settings/charging", strings.NewReader(body))
		c := auth.WithUser(ctx, uid)
		c = auth.WithSession(c, auth.SessionRef{ID: uuid.New(), Kind: kind})
		rec := httptest.NewRecorder()
		handleChargingSettingsPatch(sf.For(uid))(rec, req.WithContext(c))
		var out settings.ChargingConfig
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, got := patch(a, sessions.KindApp, `{"home_price_per_kwh": 0.31}`)
	if code != 200 || got.HomePricePerKWh != 0.31 || got.HomeCurrency != "GBP" || got.GasPricePerGallon != 5.5 || got.ComparisonMPG != 25 {
		t.Fatalf("rate only: %d %+v", code, got)
	}
	code, got = patch(a, sessions.KindApp, `{"home_currency": "cad"}`)
	if code != 200 || got.HomePricePerKWh != 0.31 || got.HomeCurrency != "CAD" || got.GasPricePerGallon != 5.5 || got.ComparisonMPG != 25 {
		t.Fatalf("currency only: %d %+v", code, got)
	}
	code, got = patch(a, sessions.KindWeb, `{"home_price_per_kwh": 0, "home_currency": "EUR"}`)
	if code != 200 || got.HomePricePerKWh != 0 || got.HomeCurrency != "EUR" || got.GasPricePerGallon != 5.5 {
		t.Fatalf("both (a rate of 0 is allowed): %d %+v", code, got)
	}
	// A refused body changes nothing.
	if code, _ := patch(a, sessions.KindApp, `{"home_price_per_kwh": 7, "home_currency": "USD"}`); code != 400 {
		t.Fatalf("over the cap: %d", code)
	}
	after, _ := settings.GetChargingConfig(ctx, sf.For(a))
	if after.HomeCurrency != "EUR" || after.HomePricePerKWh != 0 {
		t.Errorf("a refused patch changed the settings: %+v", after)
	}
	// The other user's settings are untouched.
	other, _ := settings.GetChargingConfig(ctx, sf.For(b))
	if other != seed {
		t.Errorf("another user's settings changed: %+v", other)
	}
	// The audit lines: who, from where, which group and fields; no values.
	out := logs.String()
	for _, want := range []string{`"msg":"settings write"`, `"source":"app"`, `"source":"web"`, `"group":"charging.home_rate"`, `"fields":["home_price_per_kwh"]`, `"fields":["home_currency"]`, a.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %s:\n%s", want, out)
		}
	}
	for _, secret := range []string{"0.31", "CAD", "EUR", `"cad"`} {
		if strings.Contains(out, secret) {
			t.Errorf("the audit log carries a value %q:\n%s", secret, out)
		}
	}
	if strings.Contains(out, b.String()) {
		t.Error("the log mentions the other user")
	}
}
