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
	"github.com/apohor/rivolt/internal/settings"
)

// A change keeps the home it replaces; restore swaps back (and restoring again returns), with nothing to restore a 404.
func TestHomeLocationKeepsThePreviousHomeAndRestoresIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "home-restore", "home-restore@x.test", "home-restore", true)
	if err != nil {
		t.Fatal(err)
	}
	store := settings.NewFactory(pool).For(uid)

	put := func(body string) homeLocationResponse {
		w := httptest.NewRecorder()
		handleHomeLocationPut(store, nil, nil)(w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("put %s: %d %s", body, w.Code, w.Body.String())
		}
		var out homeLocationResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	restore := func() (int, homeLocationResponse) {
		w := httptest.NewRecorder()
		handleHomeLocationRestore(store, nil, nil)(w, httptest.NewRequest("POST", "/", nil))
		var out homeLocationResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	if code, _ := restore(); code != http.StatusNotFound {
		t.Fatalf("nothing to restore: %d", code)
	}
	first := put(`{"set":true,"latitude":32.78,"longitude":-96.8,"label":"Home","address":"Dallas, Texas","source":"city"}`)
	if first.PreviousSet || first.Source != "city" {
		t.Fatalf("first save: %+v", first)
	}
	second := put(`{"set":true,"latitude":32.776665,"longitude":-96.796989,"label":"Home","source":"coordinates"}`)
	if !second.PreviousSet || second.Latitude != 32.776665 || second.Source != "coordinates" {
		t.Fatalf("second save: %+v", second)
	}
	code, back := restore()
	if code != http.StatusOK || back.Latitude != 32.78 || back.Address != "Dallas, Texas" || back.Source != "city" {
		t.Fatalf("restore: %d %+v", code, back)
	}
	_, again := restore()
	if again.Latitude != 32.776665 || again.Source != "coordinates" {
		t.Fatalf("restore twice: %+v", again)
	}
}
