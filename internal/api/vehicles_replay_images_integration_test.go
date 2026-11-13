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
	"github.com/google/uuid"
)

// A replay account (preview, the public demo) is served from its stored row, which nothing refreshes. The row must follow the feed both
// ways: pictures set reach it (with the demo vehicle's id and no order id), pictures unset remove them, and a list whose row already
// matches writes nothing.
func TestReplayVehicleImagesFollowTheFeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	imgs, err := rivian.ParseReplayImages(`[{"url":"https://img.test/int.webp","placement":"interior"},{"url":"https://img.test/3q.webp","placement":"exterior-3qfront-driver"}]`)
	if err != nil {
		t.Fatal(err)
	}
	for _, linked := range []bool{true, false} {
		name := "demo (not linked, held client)"
		if linked {
			name = "linked, pod holds nothing"
		}
		t.Run(name, func(t *testing.T) {
			uid := acceptedUser(ctx, t, pool, "replay-images-"+name[:4])
			feed := rivian.NewSampleReplayFeed(nil, time.Hour, nil)
			reg := rivian.NewReplayAccountRegistry(feed, nil).(interface {
				ReplayVehicle(uuid.UUID) (rivian.Vehicle, bool)
			})
			login := func() *rivian.ReplayClient {
				c := rivian.NewReplayClient(feed, uid)
				if err := c.Login(ctx, rivian.Credentials{Email: "x@y", Password: "p"}); err != nil {
					t.Fatal(err)
				}
				return c
			}
			list := func() []rivian.Vehicle {
				t.Helper()
				var c rivian.Client = login()
				from := &vehiclesFromDB{account: func(context.Context) (bool, bool, bool) { return false, false, true }}
				if linked {
					// A pod that does not hold the account lists through the unheld client; the row still follows the feed.
					c = rivian.NewUnheldClient()
					from = &vehiclesFromDB{account: linkedAccount, ownerDiscovery: true, replayVehicle: func() (rivian.Vehicle, bool) { return reg.ReplayVehicle(uid) }}
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
				handleVehicles(c, nil, pool, nil, nil, nil, from)(rec, req)
				var vs []rivian.Vehicle
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &vs) != nil {
					t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
				}
				return vs
			}
			stored := func() (hero string, gallery int) {
				t.Helper()
				rows, err := db.ListUserVehiclesForAPI(ctx, pool, uid)
				if err != nil || len(rows) != 1 {
					t.Fatalf("stored rows: %v %v", rows, err)
				}
				var v rivian.Vehicle
				v.ApplyStoredInfo(rows[0].Info)
				return rows[0].ImageURL, len(v.Images)
			}
			rowVersion := func() string { // a write makes a new row version, so xmin moves
				var x string
				if err := pool.QueryRowContext(ctx, `SELECT xmin::text FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id LIKE 'demo-%'`, uid).Scan(&x); err != nil {
					t.Fatal(err)
				}
				return x
			}
			noPictures := func(step string) {
				t.Helper()
				if hero, n := stored(); hero != "" || n != 0 {
					t.Fatalf("%s: stored row keeps %q and %d gallery images", step, hero, n)
				}
			}

			if vs := list(); len(vs) != 1 || vs[0].ImageURL != "" || len(vs[0].Images) != 0 {
				t.Fatalf("no pictures configured: %+v", vs)
			}
			noPictures("before")
			feed.SetImages(imgs)
			for i := 0; i < 2; i++ {
				vs := list()
				if len(vs) != 1 || vs[0].ImageURL != "https://img.test/3q.webp" || len(vs[0].Images) != 2 {
					t.Fatalf("set, call %d: %+v", i, vs)
				}
				for _, img := range vs[0].Images {
					if img.VehicleID != vs[0].ID || img.OrderID != "" {
						t.Errorf("ids: %+v", img)
					}
				}
			}
			if hero, n := stored(); hero != "https://img.test/3q.webp" || n != 2 {
				t.Fatalf("set: stored %q, %d", hero, n)
			}
			before := rowVersion()
			list()
			list()
			if after := rowVersion(); after != before {
				t.Errorf("unchanged pictures rewrote the row: xmin %s -> %s", before, after)
			}

			// Unset: the key is removed from the install; neither the response nor the row may keep a picture.
			feed.SetImages(nil)
			if vs := list(); len(vs) != 1 || vs[0].ImageURL != "" || len(vs[0].Images) != 0 {
				t.Fatalf("unset: response still has pictures: %+v", vs)
			}
			noPictures("unset")
			if vs := list(); len(vs) != 1 || vs[0].ImageURL != "" || len(vs[0].Images) != 0 {
				t.Fatalf("unset, second call: %+v", vs)
			}
			before = rowVersion()
			list()
			if after := rowVersion(); after != before {
				t.Errorf("cleared row rewritten again: xmin %s -> %s", before, after)
			}
		})
	}
}
