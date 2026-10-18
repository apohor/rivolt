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
	"github.com/apohor/rivolt/internal/push"
)

func TestPushDeviceAndOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string) (uuid.UUID, *push.Store) {
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		st, err := push.OpenStore(pool, uid)
		if err != nil {
			t.Fatal(err)
		}
		return uid, st
	}
	alice, as := mk("pd-alice")
	bob, bs := mk("pd-bob")
	sub := func(st *push.Store, ep string, plug, mile bool) {
		if err := st.Upsert(ctx, push.Subscription{Endpoint: ep, P256dh: "k", Auth: "a", OnChargingDone: true, OnAnomaly: true,
			OnPlugInReminder: plug, OnMilestones: mile}); err != nil {
			t.Fatal(err)
		}
	}
	sub(as, "https://push.example/a1", true, true)
	sub(as, "https://push.example/a2", true, false)
	sub(bs, "https://push.example/b1", true, true)

	call := func(h http.HandlerFunc, uid uuid.UUID, body string, imp bool) (int, map[string]any) {
		c := auth.WithUser(ctx, uid)
		if imp {
			c = auth.WithImpersonation(c, uuid.New(), uid)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/x", bytes.NewBufferString(body)).WithContext(c))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// device: this user's endpoint returns its stored switches; another's is a 404.
	code, out := call(handlePushDevice(as), alice, `{"endpoint":"https://push.example/a2"}`, false)
	p, _ := out["preferences"].(map[string]any)
	if code != 200 || p["on_plug_in_reminder"] != true || p["on_milestones"] != false || p["on_charging_done"] != true {
		t.Errorf("device: %d %v", code, out)
	}
	if code, _ := call(handlePushDevice(as), alice, `{"endpoint":"https://push.example/b1"}`, false); code != 404 {
		t.Errorf("another account's endpoint: %d, want 404", code)
	}
	if code, _ := call(handlePushDevice(as), alice, `{}`, false); code != 400 {
		t.Errorf("no endpoint: %d", code)
	}

	// off: every device of the caller, nobody else's; milestones untouched.
	if code, out := call(handlePushOff(as), alice, `{"kind":"plug_in_reminder"}`, false); code != 200 || out["devices_changed"] != float64(2) {
		t.Fatalf("off: %d %v", code, out)
	}
	subs, _ := as.List(ctx)
	for _, s := range subs {
		if s.OnPlugInReminder {
			t.Errorf("%s still has the reminder on", s.Endpoint)
		}
	}
	if !subs[0].OnMilestones {
		t.Error("turning off the reminder turned off milestones")
	}
	if bsubs, _ := bs.List(ctx); !bsubs[0].OnPlugInReminder {
		t.Error("another account's switch was turned off")
	}
	if code, out := call(handlePushOff(as), alice, `{"kind":"milestones"}`, false); code != 200 || out["devices_changed"] != float64(1) {
		t.Errorf("milestones off: %d %v", code, out)
	}
	if code, out := call(handlePushOff(as), alice, `{"kind":"plug_in_reminder"}`, false); code != 200 || out["devices_changed"] != float64(0) {
		t.Errorf("idempotent: %d %v", code, out)
	}
	for _, bad := range []string{`{"kind":"charging_done"}`, `{"kind":""}`, `{`} {
		if code, _ := call(handlePushOff(bs), bob, bad, false); code != 400 {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if code, _ := call(handlePushOff(bs), bob, `{"kind":"plug_in_reminder"}`, true); code != http.StatusForbidden {
		t.Errorf("view-as: %d, want 403", code)
	}
	if bsubs, _ := bs.List(ctx); !bsubs[0].OnPlugInReminder {
		t.Error("view-as changed the switch")
	}
}
