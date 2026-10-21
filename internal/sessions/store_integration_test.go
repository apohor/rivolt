//go:build integration

package sessions_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/testpg"
)

// The device list: kinds are listed, a user revokes only their own sessions, and
// "everywhere else" keeps the caller's.
func TestDeviceListRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	st, err := sessions.New(pool, bytes.Repeat([]byte("p"), 32))
	if err != nil {
		t.Fatal(err)
	}
	me, _ := db.EnsureUser(ctx, pool, "sess-list-me")
	other, _ := db.EnsureUser(ctx, pool, "sess-list-other")
	mk := func(uid [16]byte, kind, label string) (sessions.Session, string) {
		s, raw, err := st.Create(ctx, uid, time.Hour, sessions.CreateOpts{Kind: kind, DeviceLabel: label, UserAgent: "UA", IPAddress: "203.0.113.5"})
		if err != nil {
			t.Fatal(err)
		}
		return s, raw
	}
	web, webTok := mk(me, sessions.KindWeb, "")
	app1, _ := mk(me, sessions.KindApp, "Sam's iPhone")
	app2, app2Tok := mk(me, sessions.KindApp, "iPad")
	foreign, _ := mk(other, sessions.KindWeb, "")

	list, err := st.List(ctx, me)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	kinds := map[string]int{}
	for _, s := range list {
		kinds[s.Kind]++
	}
	if kinds[sessions.KindWeb] != 1 || kinds[sessions.KindApp] != 2 {
		t.Errorf("kinds = %v", kinds)
	}

	// Own session: revoked once; a second time, someone else's, and an unknown id all read false.
	if ok, err := st.RevokeOwned(ctx, me, app1.ID); err != nil || !ok {
		t.Fatalf("revoke own = %v, %v", ok, err)
	}
	if ok, _ := st.RevokeOwned(ctx, me, app1.ID); ok {
		t.Error("revoking twice reported true")
	}
	if ok, _ := st.RevokeOwned(ctx, me, foreign.ID); ok {
		t.Error("revoked another user's session")
	}
	if _, err := st.Lookup(ctx, webTok, sessions.KindWeb); err != nil {
		t.Errorf("an untouched session stopped working: %v", err)
	}

	// Everywhere else: keeps the caller's, revokes the rest of theirs only.
	n, err := st.RevokeOthers(ctx, me, app2.ID)
	if err != nil || n != 1 {
		t.Fatalf("revoke others = %d, %v (want 1: the web session; app1 was already revoked)", n, err)
	}
	if _, err := st.Lookup(ctx, webTok, sessions.KindWeb); err == nil {
		t.Error("the web session survived sign-out everywhere else")
	}
	if _, err := st.Lookup(ctx, app2Tok, sessions.KindApp); err != nil {
		t.Errorf("the caller's own session was revoked: %v", err)
	}
	if l, _ := st.List(ctx, other); len(l) != 1 || l[0].ID != foreign.ID {
		t.Error("another user's sessions were touched")
	}
	_ = web
}

// Revoking a session, by any path, deletes the push devices registered with it
// (the privacy page says so); the user's other devices stay.
func TestRevokeDeletesPushDevices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	st, err := sessions.New(pool, bytes.Repeat([]byte("q"), 32))
	if err != nil {
		t.Fatal(err)
	}
	me, _ := db.EnsureUser(ctx, pool, "sess-push-me")
	n := 0
	// mk makes an app session with one push device registered to it.
	mk := func() (sessions.Session, string) {
		s, raw, err := st.Create(ctx, me, time.Hour, sessions.CreateOpts{Kind: sessions.KindApp, DeviceLabel: "iPhone"})
		if err != nil {
			t.Fatal(err)
		}
		n++
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO push_apns_devices (user_id, session_id, token, env, bundle_id) VALUES ($1, $2, $3, 'sandbox', 'dev.rivolt.app')`,
			me, s.ID, strings.Repeat(string(rune('a'+n)), 64)); err != nil {
			t.Fatal(err)
		}
		return s, raw
	}
	devices := func(s sessions.Session) int {
		var c int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM push_apns_devices WHERE session_id = $1`, s.ID).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	keep, _ := mk()
	byID, _ := mk()
	byTok, rawTok := mk()
	owned, _ := mk()
	others1, _ := mk()
	others2, _ := mk()

	if err := st.Revoke(ctx, byID.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeByToken(ctx, rawTok); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RevokeOwned(ctx, me, owned.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for name, s := range map[string]sessions.Session{"Revoke": byID, "RevokeByToken": byTok, "RevokeOwned": owned} {
		if c := devices(s); c != 0 {
			t.Errorf("%s left %d device rows", name, c)
		}
	}
	if devices(keep) != 1 || devices(others1) != 1 {
		t.Fatal("revoking one session removed another's device")
	}
	if _, err := st.RevokeOthers(ctx, me, keep.ID); err != nil {
		t.Fatal(err)
	}
	if devices(others1) != 0 || devices(others2) != 0 {
		t.Error("RevokeOthers left device rows")
	}
	if devices(keep) != 1 {
		t.Error("RevokeOthers removed the caller's own device")
	}
	// RevokeAllExcept (the second path that revokes many).
	extra, _ := mk()
	if err := st.RevokeAllExcept(ctx, me, keep.ID); err != nil {
		t.Fatal(err)
	}
	if devices(extra) != 0 || devices(keep) != 1 {
		t.Errorf("RevokeAllExcept: extra=%d keep=%d", devices(extra), devices(keep))
	}
}
