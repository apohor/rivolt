//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// A replay account (preview) is linked and served from its stored row, which nothing refreshes: a row stored before the pictures were
// configured must still pick them up, with the demo vehicle's id and no order id.
func TestReplayVehicleImagesReachTheStoredRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid := acceptedUser(ctx, t, pool, "replay-images")

	feed := rivian.NewSampleReplayFeed(nil, time.Hour, nil)
	imgs, err := rivian.ParseReplayImages(`[{"url":"https://img.test/int.webp","placement":"interior"},{"url":"https://img.test/3q.webp","placement":"exterior-3qfront-driver"}]`)
	if err != nil {
		t.Fatal(err)
	}
	// The row as an earlier sign-in left it: no pictures, not synced.
	old := rivian.NewReplayClient(feed, uid)
	if err := old.Login(ctx, rivian.Credentials{Email: "x@y", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	ovs, _ := old.Vehicles(ctx)
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: ovs[0].ID, Name: ovs[0].Name, Model: ovs[0].Model, Info: ovs[0].StoredInfo()}); err != nil {
		t.Fatal(err)
	}

	list := func(c *rivian.ReplayClient) []rivian.Vehicle {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
		handleVehicles(c, nil, pool, nil, nil, nil, &vehiclesFromDB{account: linkedAccount, ownerDiscovery: true})(rec, req)
		var vs []rivian.Vehicle
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &vs) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return vs
	}
	login := func() *rivian.ReplayClient {
		c := rivian.NewReplayClient(feed, uid)
		if err := c.Login(ctx, rivian.Credentials{Email: "x@y", Password: "p"}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if vs := list(login()); len(vs) != 1 || len(vs[0].Images) != 0 {
		t.Fatalf("no pictures configured: %+v", vs)
	}
	feed.SetImages(imgs)
	for i := 0; i < 2; i++ { // the second call is the stored answer
		vs := list(login())
		if len(vs) != 1 || vs[0].ImageURL != "https://img.test/3q.webp" || len(vs[0].Images) != 2 {
			t.Fatalf("call %d: %+v", i, vs)
		}
		for _, img := range vs[0].Images {
			if img.VehicleID != vs[0].ID || img.OrderID != "" {
				t.Errorf("ids: %+v", img)
			}
		}
	}
	// A list whose pictures already match the row does not write it again (a write makes a new row version, so xmin moves).
	rowVersion := func() string {
		var x string
		if err := pool.QueryRowContext(ctx, `SELECT xmin::text FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id LIKE 'demo-%'`, uid).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	before := rowVersion()
	list(login())
	if after := rowVersion(); after != before {
		t.Errorf("unchanged pictures rewrote the row: xmin %s -> %s", before, after)
	}
}
