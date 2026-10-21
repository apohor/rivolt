//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/push"
)

func TestAPNsEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	a, err := push.NewAPNs(push.APNsConfig{KeyID: "KEY123ABCD", TeamID: "TEAM123456", Topic: "dev.rivolt.app",
		KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	if err != nil {
		t.Fatal(err)
	}
	on := (&push.Service{}).WithAPNs(a)
	off := &push.Service{}

	mkUser := func(name string) (uuid.UUID, *push.Store, uuid.UUID) {
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		st, err := push.OpenStore(pool, uid)
		if err != nil {
			t.Fatal(err)
		}
		sid := uuid.New()
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, token_hash, kind, expires_at) VALUES ($1, $2, $3, 'app', now() + interval '1 day')`,
			sid, uid, []byte(sid.String())); err != nil {
			t.Fatal(err)
		}
		return uid, st, sid
	}
	alice, as, asid := mkUser("apns-api-alice")
	bob, bs, _ := mkUser("apns-api-bob")

	const (
		tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	call := func(h http.HandlerFunc, uid, sid uuid.UUID, bearer bool, body string) (int, map[string]any) {
		c := auth.WithUser(ctx, uid)
		if bearer {
			c = logging.WithAuthMethod(c, logging.AuthBearer)
			c = auth.WithSession(c, auth.SessionRef{ID: sid, Kind: "app"})
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/x", bytes.NewBufferString(body)).WithContext(c))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	reg := func(token string) string {
		return `{"token":"` + token + `","env":"sandbox","bundle_id":"dev.rivolt.app"}`
	}

	// A browser session, or a server with APNs off, never registers a device.
	if code, _ := call(handleAPNsRegister(on, as), alice, asid, false, reg(tokA)); code != 403 {
		t.Errorf("cookie register = %d, want 403", code)
	}
	if code, _ := call(handleAPNsRegister(off, as), alice, asid, true, reg(tokA)); code != 404 {
		t.Errorf("apns off register = %d, want 404", code)
	}
	// Malformed devices.
	for _, body := range []string{
		`{"token":"zz","env":"sandbox"}`,
		`{"token":"` + tokA + `","env":"staging"}`,
		`{"token":"` + tokA + `","env":"sandbox","bundle_id":"com.other.app"}`,
		`{"token":"` + tokA + `/../x","env":"sandbox"}`,
	} {
		if code, _ := call(handleAPNsRegister(on, as), alice, asid, true, body); code != 400 {
			t.Errorf("register %s = %d, want 400", body, code)
		}
	}

	// Register: the web's defaults come back, and the device is tied to the session.
	code, out := call(handleAPNsRegister(on, as), alice, asid, true, reg(tokA))
	p, _ := out["preferences"].(map[string]any)
	if code != 200 || p["on_charging_done"] != true || p["on_plug_in_reminder"] != false || p["on_milestones"] != false {
		t.Fatalf("register = %d %v", code, out)
	}

	// Preferences: all five switches work; another user's token is a 404.
	if code, _ := call(handleAPNsPreferences(on, as), alice, asid, true, `{"token":"`+tokA+`","on_milestones":true,"on_anomaly":false}`); code != 200 {
		t.Errorf("preferences = %d", code)
	}
	got, ok, err := as.APNsPrefs(ctx, tokA)
	if err != nil || !ok || !got.OnMilestones || got.OnAnomaly || got.OnPlugInReminder || !got.OnChargingDone {
		t.Errorf("prefs after = %+v %v %v", got, ok, err)
	}
	if code, _ := call(handleAPNsPreferences(on, bs), bob, uuid.New(), true, `{"token":"`+tokA+`","on_anomaly":true}`); code != 404 {
		t.Errorf("another user's token = %d, want 404", code)
	}
	if code, _ := call(handleAPNsPreferences(on, as), alice, asid, true, `{"token":"`+tokA+`"}`); code != 400 {
		t.Errorf("no switch = %d, want 400", code)
	}

	// Unregister removes only the caller's device.
	if code, _ := call(handleAPNsUnregister(on, bs), bob, uuid.New(), true, `{"token":"`+tokA+`"}`); code != 200 {
		t.Errorf("bob unregister = %d", code)
	}
	if _, ok, _ := as.APNsPrefs(ctx, tokA); !ok {
		t.Error("bob's unregister removed alice's device")
	}
	if code, _ := call(handleAPNsUnregister(on, as), alice, asid, true, `{"token":"`+tokA+`"}`); code != 200 {
		t.Errorf("alice unregister = %d", code)
	}
	if _, ok, _ := as.APNsPrefs(ctx, tokA); ok {
		t.Error("device still there after unregister")
	}
	_ = tokB

	// Notification text: the owner reads it, another user gets a 404, a bad id a 404.
	nid, err := as.CreateNotification(ctx, "charging_done", "Charge complete", "Your truck is at 80%.", "/")
	if err != nil {
		t.Fatal(err)
	}
	get := func(uid uuid.UUID, id string) (int, map[string]any) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		c := context.WithValue(auth.WithUser(ctx, uid), chi.RouteCtxKey, rctx)
		st, _ := push.OpenStore(pool, uid)
		rec := httptest.NewRecorder()
		handlePushNotification(st)(rec, httptest.NewRequest("GET", "/x", nil).WithContext(c))
		var o map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &o)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("notification response is cacheable")
		}
		return rec.Code, o
	}
	if code, o := get(alice, nid.String()); code != 200 || o["title"] != "Charge complete" || o["id"] != nid.String() {
		t.Errorf("owner get = %d %v", code, o)
	}
	if code, _ := get(bob, nid.String()); code != 404 {
		t.Errorf("other user get = %d, want 404", code)
	}
	if code, _ := get(alice, "not-a-uuid"); code != 404 {
		t.Errorf("bad id = %d, want 404", code)
	}

	// /api/app/config reports the Live Activity capability only when the flag is on.
	liveOn := (&push.Service{}).WithAPNs(a).WithLive(true)
	for svc, want := range map[*push.Service]bool{liveOn: true, on: false, off: false} {
		rec := httptest.NewRecorder()
		handleAppConfig(Deps{PushService: svc})(rec, httptest.NewRequest("GET", "/api/app/config", nil))
		if !strings.Contains(rec.Body.String(), `"live":`+map[bool]string{true: "true", false: "false"}[want]) {
			t.Errorf("app config = %s, want live=%v", rec.Body.String(), want)
		}
	}

	// /api/app/config reports the capability.
	for svc, want := range map[*push.Service]bool{on: true, off: false} {
		rec := httptest.NewRecorder()
		handleAppConfig(Deps{PushService: svc})(rec, httptest.NewRequest("GET", "/api/app/config", nil))
		if !strings.Contains(rec.Body.String(), `"apns":`+map[bool]string{true: "true", false: "false"}[want]) {
			t.Errorf("app config = %s, want apns=%v", rec.Body.String(), want)
		}
	}
}

func TestAPNsLiveActivityEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	a, err := push.NewAPNs(push.APNsConfig{KeyID: "KEY123ABCD", TeamID: "TEAM123456", Topic: "dev.rivolt.app",
		KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	if err != nil {
		t.Fatal(err)
	}
	on := (&push.Service{}).WithAPNs(a)
	mk := func(name string) (uuid.UUID, *push.Store, uuid.UUID, uuid.UUID) {
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := push.OpenStore(pool, uid)
		sid := uuid.New()
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, token_hash, kind, expires_at) VALUES ($1, $2, $3, 'app', now() + interval '1 day')`,
			sid, uid, []byte(sid.String())); err != nil {
			t.Fatal(err)
		}
		var vid uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, name+"-v").Scan(&vid); err != nil {
			t.Fatal(err)
		}
		return uid, st, sid, vid
	}
	alice, as, asid, avid := mk("live-api-alice")
	bob, bs, bsid, bvid := mk("live-api-bob")
	const (
		dev  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		actT = "0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	call := func(h http.HandlerFunc, uid, sid uuid.UUID, bearer bool, body string) (int, map[string]any) {
		c := auth.WithUser(ctx, uid)
		if bearer {
			c = logging.WithAuthMethod(c, logging.AuthBearer)
			c = auth.WithSession(c, auth.SessionRef{ID: sid, Kind: "app"})
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/x", bytes.NewBufferString(body)).WithContext(c))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	reqA := `{"token":"` + dev + `","activity_token":"` + actT + `","vehicle_id":"` + avid.String() + `"}`

	if code, _ := call(handleAPNsRegister(on, as), alice, asid, true, `{"token":"`+dev+`","env":"sandbox"}`); code != 200 {
		t.Fatalf("register device = %d", code)
	}
	// Register returns the switch, off.
	_, out := call(handleAPNsRegister(on, as), alice, asid, true, `{"token":"`+dev+`","env":"sandbox"}`)
	if p, _ := out["preferences"].(map[string]any); p["on_charging_live"] != false {
		t.Errorf("register preferences = %v, want on_charging_live false", out["preferences"])
	}
	if code, _ := call(handleAPNsActivity(on, as), alice, asid, false, reqA); code != 403 {
		t.Errorf("cookie = %d, want 403", code)
	}
	if code, _ := call(handleAPNsActivity(on, as), alice, asid, true, reqA); code != 409 {
		t.Errorf("switch off = %d, want 409", code)
	}
	if code, _ := call(handleAPNsPreferences(on, as), alice, asid, true, `{"token":"`+dev+`","on_charging_live":true}`); code != 200 {
		t.Fatalf("turn on = %d", code)
	}
	if code, _ := call(handleAPNsActivity(on, as), alice, asid, true, reqA); code != 200 {
		t.Errorf("register = %d, want 200", code)
	}
	if n, _ := as.ActivityCount(ctx); n != 1 {
		t.Errorf("rows = %d", n)
	}
	for name, body := range map[string]string{
		"bad activity token": `{"token":"` + dev + `","activity_token":"zz","vehicle_id":"` + avid.String() + `"}`,
		"bad vehicle id":     `{"token":"` + dev + `","activity_token":"` + actT + `","vehicle_id":"nope"}`,
	} {
		if code, _ := call(handleAPNsActivity(on, as), alice, asid, true, body); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := call(handleAPNsActivity(on, as), alice, asid, true,
		`{"token":"`+dev+`","activity_token":"`+actT+`","vehicle_id":"`+bvid.String()+`"}`); code != 404 {
		t.Errorf("another user's vehicle = %d, want 404", code)
	}
	// Bob can't end Alice's activity; Alice can; ending twice is fine.
	_, _, _ = bob, bs, bsid
	if code, _ := call(handleAPNsActivityEnd(on, bs), bob, bsid, true, `{"activity_token":"`+actT+`"}`); code != 200 {
		t.Errorf("bob end = %d", code)
	}
	if n, _ := as.ActivityCount(ctx); n != 1 {
		t.Error("bob's end removed alice's activity")
	}
	for i := 0; i < 2; i++ {
		if code, _ := call(handleAPNsActivityEnd(on, as), alice, asid, true, `{"activity_token":"`+actT+`"}`); code != 200 {
			t.Errorf("alice end = %d", code)
		}
	}
	if n, _ := as.ActivityCount(ctx); n != 0 {
		t.Errorf("rows after end = %d", n)
	}
}
