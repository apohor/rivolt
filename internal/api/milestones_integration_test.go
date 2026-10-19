//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/milestones"
)

func TestMilestonesStoreAndCards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string) uuid.UUID {
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	alice, bob := mk("ms-alice"), mk("ms-bob")
	veh := func(uid uuid.UUID, rid string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2) RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	av, bv := veh(alice, "rv-ms-a"), veh(bob, "rv-ms-b")
	store := milestones.NewStore(pool)
	now := time.Now()

	// Ready flips once.
	if ok, _ := store.Ready(ctx); ok {
		t.Fatal("ready before the backfill")
	}
	if err := store.MarkReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReady(ctx); err != nil {
		t.Fatalf("second MarkReady: %v", err)
	}
	if ok, _ := store.Ready(ctx); !ok {
		t.Fatal("not ready after MarkReady")
	}

	ins := func(uid, vid uuid.UUID, rid, key, status string, ago time.Duration) {
		t.Helper()
		ok, err := store.Insert(ctx, milestones.Row{User: uid, Vehicle: vid, RivianID: rid, Key: key, Status: status, AchievedAt: now.Add(-ago),
			Detail: milestones.Detail{Miles: 1080, MiPerKWh: 2.5}})
		if err != nil || !ok {
			t.Fatalf("insert %s: %v %v", key, ok, err)
		}
	}
	ins(alice, av, "rv-ms-a", milestones.KindMiles, milestones.StatusAchieved, time.Hour)
	ins(alice, av, "rv-ms-a", milestones.KindCharges, milestones.StatusBackfilled, time.Hour) // never shown
	ins(alice, av, "rv-ms-a", "best_eff:2026-08", milestones.StatusAchieved, 40*24*time.Hour) // older than 30 days
	ins(bob, bv, "rv-ms-b", milestones.KindMiles, milestones.StatusAchieved, time.Hour)
	if ok, _ := store.Insert(ctx, milestones.Row{User: alice, Vehicle: av, Key: milestones.KindMiles, Status: milestones.StatusAchieved, AchievedAt: now}); ok {
		t.Error("the same milestone inserted twice")
	}

	api := &milestonesAPI{store: store, now: time.Now}
	call := func(h func(uuid.UUID, http.ResponseWriter, *http.Request), uid uuid.UUID, method, body string, imp bool) (int, map[string]any) {
		c := auth.WithUser(ctx, uid)
		if imp {
			c = auth.WithImpersonation(c, uuid.New(), uid)
		}
		rec := httptest.NewRecorder()
		h(uid, rec, httptest.NewRequest(method, "/x", bytes.NewBufferString(body)).WithContext(c))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	cards := func(uid uuid.UUID) []any {
		code, out := call(api.list, uid, "GET", "", false)
		if code != 200 {
			t.Fatalf("list: %d", code)
		}
		c, _ := out["milestones"].([]any)
		return c
	}

	// Alice sees her one fresh achieved card, with the text; not the backfilled or the old one, nor Bob's.
	got := cards(alice)
	if len(got) != 1 {
		t.Fatalf("alice's cards: %v", got)
	}
	card := got[0].(map[string]any)
	if card["key"] != milestones.KindMiles || card["vehicle_id"] != "rv-ms-a" || card["title"] != "1,000 miles" ||
		card["body"] != "Your first 1,000 miles in Rivolt: 2.50 mi/kWh." {
		t.Errorf("card %v", card)
	}
	if len(cards(bob)) != 1 {
		t.Error("bob's card missing")
	}

	// Dismiss: view-as is refused, another account's card is a 404, the owner's works and is idempotent.
	body := `{"vehicle_id":"rv-ms-a","key":"miles_1000"}`
	if code, _ := call(api.dismiss, alice, "POST", body, true); code != http.StatusForbidden {
		t.Errorf("view-as dismiss: %d", code)
	}
	if code, _ := call(api.dismiss, bob, "POST", body, false); code != http.StatusNotFound {
		t.Errorf("another account's card: %d, want 404", code)
	}
	if code, _ := call(api.dismiss, alice, "POST", `{"vehicle_id":"rv-ms-a"}`, false); code != http.StatusBadRequest {
		t.Errorf("no key: %d", code)
	}
	for i := 0; i < 2; i++ {
		if code, _ := call(api.dismiss, alice, "POST", body, false); code != 200 {
			t.Errorf("dismiss %d: %d", i, code)
		}
	}
	if len(cards(alice)) != 0 || len(cards(bob)) != 1 {
		t.Errorf("after dismiss: alice %d bob %d", len(cards(alice)), len(cards(bob)))
	}

	// The vehicle lookups the checker uses.
	if id, ok, err := store.Vehicle(ctx, alice, "rv-ms-a"); err != nil || !ok || id != av {
		t.Errorf("Vehicle: %v %v %v", id, ok, err)
	}
	if _, ok, _ := store.Vehicle(ctx, alice, "rv-ms-b"); ok {
		t.Error("alice resolved bob's vehicle")
	}
	vs, err := store.Vehicles(ctx, time.Time{})
	if err != nil || len(vs) < 2 {
		t.Errorf("Vehicles: %v %v", vs, err)
	}
	if active, _ := store.Vehicles(ctx, now.Add(-time.Hour)); len(active) != 0 {
		t.Errorf("vehicles with no recent drive or charge listed as active: %v", active)
	}
}
