//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
		if err := st.Upsert(ctx, push.Subscription{Endpoint: ep, P256dh: "k", Auth: "a", OnChargingDone: true, OnAnomaly: true}); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.SetOptIns(ctx, ep, &plug, &mile); err != nil || !ok {
			t.Fatalf("opt-ins: %v %v", ok, err)
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

// Only the opt-in path turns the plug-in reminder and milestones on: the legacy
// subscribe endpoint ignores both flags, however it is called, and a
// re-subscribe leaves what is stored.
func TestPushOptInsOnlyThroughOptIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "pd-legacy", "pd-legacy@x.test", "pd-legacy", true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUserVerified(ctx, pool, "pd-other", "pd-other@x.test", "pd-other", true)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := push.OpenStore(pool, uid)
	ost, _ := push.OpenStore(pool, other)
	call := func(h http.HandlerFunc, u uuid.UUID, body string, imp bool) int {
		c := auth.WithUser(ctx, u)
		if imp {
			c = auth.WithImpersonation(c, uuid.New(), u)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/x", bytes.NewBufferString(body)).WithContext(c))
		return rec.Code
	}
	flags := func(ep string) (plug, mile bool) {
		s, err := st.Get(ctx, ep)
		if err != nil || s == nil {
			t.Fatalf("get %s: %v %v", ep, s, err)
		}
		return s.OnPlugInReminder, s.OnMilestones
	}
	const ep = "https://push.example/legacy"
	legacy := `{"endpoint":"` + ep + `","keys":{"p256dh":"k","auth":"a"},"preferences":{"on_charging_done":true,"on_plug_in_reminder":true,"on_anomaly":true,"on_software_update":true,"on_milestones":true}}`

	// A stale tab subscribing with the reminder on: it is off.
	if code := call(handlePushSubscribe(st), uid, legacy, false); code != 200 {
		t.Fatalf("subscribe: %d", code)
	}
	if p, m := flags(ep); p || m {
		t.Errorf("legacy subscribe set plug=%v milestones=%v, want both off", p, m)
	}
	// The opt-in path turns them on; a later legacy re-subscribe (true or false) leaves them.
	if code := call(handlePushOptIn(st), uid, `{"endpoint":"`+ep+`","on_plug_in_reminder":true}`, false); code != 200 {
		t.Fatalf("optin: %d", code)
	}
	if p, m := flags(ep); !p || m {
		t.Errorf("after opt-in plug=%v milestones=%v, want true/false", p, m)
	}
	call(handlePushSubscribe(st), uid, legacy, false)
	if p, _ := flags(ep); !p {
		t.Error("a legacy re-subscribe changed the stored switch")
	}
	off := `{"endpoint":"` + ep + `","keys":{"p256dh":"k","auth":"a"},"preferences":{"on_plug_in_reminder":false,"on_milestones":false}}`
	call(handlePushSubscribe(st), uid, off, false)
	if p, _ := flags(ep); !p {
		t.Error("a legacy re-subscribe with false changed the stored switch")
	}
	// Opt-in guards: the endpoint must be the caller's, something must be set, no view-as.
	if code := call(handlePushOptIn(ost), other, `{"endpoint":"`+ep+`","on_milestones":true}`, false); code != 404 {
		t.Errorf("another account's endpoint: %d, want 404", code)
	}
	if code := call(handlePushOptIn(st), uid, `{"endpoint":"`+ep+`"}`, false); code != 400 {
		t.Errorf("no switch: %d, want 400", code)
	}
	if code := call(handlePushOptIn(st), uid, `{"endpoint":"`+ep+`","on_milestones":true}`, true); code != http.StatusForbidden {
		t.Errorf("view-as: %d, want 403", code)
	}
	if _, m := flags(ep); m {
		t.Error("view-as turned milestones on")
	}
	// The migration's reset: every switch off for every row.
	if _, err := pool.ExecContext(ctx, `UPDATE push_subscriptions SET on_plug_in_reminder = TRUE, on_milestones = TRUE`); err != nil {
		t.Fatal(err)
	}
	mig, err := os.ReadFile("../db/migrations/0068_push_optins_reset.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(mig)); err != nil {
		t.Fatal(err)
	}
	var on int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE on_plug_in_reminder OR on_milestones`).Scan(&on); err != nil || on != 0 {
		t.Errorf("%d rows still on after the reset (%v)", on, err)
	}
}
