//go:build integration

package push

import (
	"testing"

	"github.com/apohor/rivolt/internal/db"
)

// The left-open switch is off for every new device and every re-registration, and only its own setter (for the
// owner) turns it on; one-tap off clears it on every device.
func TestLeftOpenSwitchIsOptInAndOwnerOnly(t *testing.T) {
	ctx, pool := apnsDB(t)
	uid, _ := db.EnsureUser(ctx, pool, "left-open-a")
	other, _ := db.EnsureUser(ctx, pool, "left-open-b")
	st, _ := OpenStore(pool, uid)
	stOther, _ := OpenStore(pool, other)

	// The iPhone.
	sess := session(t, ctx, pool, uid)
	if err := st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: tok(0), Env: EnvSandbox, BundleID: "me.apoh.rivolt"}); err != nil {
		t.Fatal(err)
	}
	devs, _ := st.ListLiveAPNsDevices(ctx)
	if len(devs) != 1 || devs[0].Prefs.OnLeftOpen {
		t.Fatalf("a new device starts with the alert off: %+v", devs)
	}
	if ok, _ := stOther.SetAPNsLeftOpen(ctx, tok(0), true); ok {
		t.Error("another user changed a device's left-open switch")
	}
	if ok, err := st.SetAPNsLeftOpen(ctx, tok(0), true); !ok || err != nil {
		t.Fatalf("owner could not set it: %v %v", ok, err)
	}
	_ = st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: tok(0), Env: EnvProduction, BundleID: "me.apoh.rivolt"}) // re-register
	devs, _ = st.ListLiveAPNsDevices(ctx)
	if !devs[0].Prefs.OnLeftOpen {
		t.Error("re-registering switched it off (or on): it must keep what was stored")
	}
	p, ok, _ := st.APNsPrefs(ctx, tok(0))
	if !ok || !p.OnLeftOpen {
		t.Errorf("APNsPrefs = %+v %v", p, ok)
	}

	// The browser.
	if err := st.Upsert(ctx, Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/x", P256dh: "p", Auth: "a", OnChargingDone: true}); err != nil {
		t.Fatal(err)
	}
	subs, _ := st.List(ctx)
	if len(subs) != 1 || subs[0].OnLeftOpen {
		t.Fatalf("a new subscription starts with the alert off: %+v", subs)
	}
	if ok, _ := stOther.SetLeftOpen(ctx, subs[0].Endpoint, true); ok {
		t.Error("another user changed a subscription's left-open switch")
	}
	if ok, _ := st.SetLeftOpen(ctx, subs[0].Endpoint, true); !ok {
		t.Fatal("owner could not set it")
	}
	_ = st.Upsert(ctx, Subscription{Endpoint: subs[0].Endpoint, P256dh: "p", Auth: "a", OnChargingDone: true}) // a stale client re-subscribes
	if got, _ := st.Get(ctx, subs[0].Endpoint); got == nil || !got.OnLeftOpen {
		t.Errorf("re-subscribing changed the switch: %+v", got)
	}
	// One-tap off clears it on the browser (the iPhone has its own control).
	if n, err := st.SwitchOffAll(ctx, "left_open"); err != nil || n != 1 {
		t.Fatalf("SwitchOffAll = %d, %v", n, err)
	}
	if got, _ := st.Get(ctx, subs[0].Endpoint); got.OnLeftOpen {
		t.Error("one-tap off left it on")
	}
}
