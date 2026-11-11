//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

type countingVehiclesClient struct {
	rivian.Client
	calls atomic.Int32
}

func (c *countingVehiclesClient) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	c.calls.Add(1)
	return []rivian.Vehicle{{ID: "should-not-be-asked"}}, nil
}

// The discovery endpoint's states, with DA's precedence (single-owner M4-2b): unlinked, then reauth (the sign-in prompt, never "looking"), then
// found, then what the owner's lookup recorded: none; or looking when nothing is recorded or the last lookup failed.
func TestVehicleDiscoveryStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "disc-api-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	state := func(linked, reauth bool) string {
		t.Helper()
		account := func(context.Context) (bool, bool, bool) { return linked, reauth, true }
		rec := httptest.NewRecorder()
		handleVehicleDiscovery(account, pool)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles/discovery", nil).WithContext(auth.WithUser(ctx, uid)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["state"]
	}
	if got := state(false, false); got != "unlinked" {
		t.Errorf("not linked: %q", got)
	}
	if got := state(true, false); got != "looking" {
		t.Errorf("linked, nothing recorded: %q, want looking", got)
	}
	if err := db.RecordDiscovery(ctx, pool, uid, db.DiscoveryError, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := state(true, false); got != "looking" {
		t.Errorf("last lookup failed: %q, want looking (it is retried)", got)
	}
	if err := db.RecordDiscovery(ctx, pool, uid, db.DiscoveryNone, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := state(true, false); got != "none" {
		t.Errorf("Rivian said no vehicles: %q, want none", got)
	}
	if got := state(true, true); got != "reauth" {
		t.Errorf("flagged for sign-in: %q, want reauth to take precedence over none", got)
	}
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-disc-api-" + uuid.NewString()[:6]}); err != nil {
		t.Fatal(err)
	}
	if got := state(true, false); got != "found" {
		t.Errorf("a vehicle is stored: %q, want found", got)
	}
	if got := state(true, true); got != "reauth" {
		t.Errorf("flagged with a vehicle: %q, want reauth", got)
	}
}

// A database hiccup while reading the account state is a 503 with Retry-After, never "unlinked" (which would tell a linked user to link again).
func TestVehicleDiscoveryUnreadableAccountIs503(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "disc-unreadable-"+uuid.NewString()[:8])
	rec := httptest.NewRecorder()
	handleVehicleDiscovery(func(context.Context) (bool, bool, bool) { return false, false, false }, pool)(rec,
		httptest.NewRequest(http.MethodGet, "/api/vehicles/discovery", nil).WithContext(auth.WithUser(ctx, uid)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), "unlinked") {
		t.Errorf("status %d Retry-After %q body %q, want 503 with Retry-After and no \"unlinked\"", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
}

// With owner discovery /api/vehicles never asks Rivian: a linked account with nothing stored gets an empty list at once, and a flagged one still
// gets the sign-in error.
func TestVehiclesWithOwnerDiscoveryNeverAsksRivian(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "disc-list-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	c := &countingVehiclesClient{}
	list := func(reauth bool) (int, string) {
		fromDB := &vehiclesFromDB{ownerDiscovery: true, account: func(context.Context) (bool, bool, bool) { return true, reauth, true }}
		rec := httptest.NewRecorder()
		handleVehicles(c, nil, pool, nil, nil, nil, fromDB)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid)))
		return rec.Code, rec.Body.String()
	}
	if code, body := list(false); code != 200 || body != "[]\n" || c.calls.Load() != 0 {
		t.Fatalf("linked, nothing stored: %d %q, %d Rivian calls; want 200, [] and none", code, body, c.calls.Load())
	}
	if code, _ := list(true); code != http.StatusUnauthorized || c.calls.Load() != 0 {
		t.Errorf("flagged: status %d with %d Rivian calls, want 401 and none", code, c.calls.Load())
	}
	// Stored but not yet synced by the owner: served as stored, still no Rivian call.
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-disc-list-" + uuid.NewString()[:6], Name: "Truck"}); err != nil {
		t.Fatal(err)
	}
	code, body := list(false)
	var vs []rivian.Vehicle
	if err := json.Unmarshal([]byte(body), &vs); err != nil || code != 200 || len(vs) != 1 || c.calls.Load() != 0 {
		t.Errorf("stored, unsynced: %d %q (%v), %d Rivian calls; want the one vehicle and no call", code, body, err, c.calls.Load())
	}
}

type noSyncerRegistry struct{ rivian.AccountRegistry }

// A registry without a syncer (replay, mock, stub) still has a users row: a user with no stored session is "unlinked" and a flagged one needs
// sign-in, never an account state that "could not be read" for ever.
func TestVehiclesAccountStateWithoutAnAccountSyncer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "disc-nosync-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: pool, Accounts: noSyncerRegistry{}}
	linked, reauth, ok := d.vehiclesAccountState(uid)(ctx)
	if !ok || linked || reauth {
		t.Fatalf("no session: linked=%v reauth=%v ok=%v, want unlinked and readable", linked, reauth, ok)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET needs_reauth = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, reauth, ok = d.vehiclesAccountState(uid)(ctx); !ok || !reauth {
		t.Fatalf("flagged: reauth=%v ok=%v, want reauth and readable", reauth, ok)
	}
}
