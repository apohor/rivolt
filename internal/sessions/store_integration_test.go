//go:build integration

package sessions_test

import (
	"bytes"
	"context"
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
