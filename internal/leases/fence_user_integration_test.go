//go:build integration

package leases_test

import (
	"context"
	"testing"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
)

// FenceUser maps a user to their group's token: both members of a shared-vehicle group get the
// group's token on the owning pod, any other pod is Lost, a user with no vehicle has no group, and
// with lead mode off it is Off.
func TestFenceUser_GroupTokenForEveryMember(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "fu-1", "fu-x", "fu-shared")
	seedUserVehicles(ctx, t, pool, "fu-2", "fu-shared")
	for _, u := range []string{"fu-1", "fu-2"} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
			SELECT id, 'rivian.session', '\x00'::bytea, 'k' FROM users WHERE username = $1`, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.EnsureUser(ctx, pool, "fu-novehicle"); err != nil {
		t.Fatal(err)
	}
	uid := map[string]string{}
	rows, err := pool.QueryContext(ctx, `SELECT username, id::text FROM users WHERE username LIKE 'fu-%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n, id string
		if err := rows.Scan(&n, &id); err != nil {
			t.Fatal(err)
		}
		uid[n] = id
	}
	_ = rows.Close()
	group := uid["fu-1"]
	if uid["fu-2"] < group {
		group = uid["fu-2"]
	}

	a, b := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b")
	if _, err := a.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"fu-1", "fu-2"} {
		tok, st, err := a.FenceUser(ctx, uid[u])
		if err != nil || st != fence.Held || tok.Group != group || tok.Pod != "pod-a" || tok.Epoch == 0 {
			t.Errorf("owner, %s: %+v %v %v; want Held with group %s", u, tok, st, err, group)
		}
		if _, st, err := b.FenceUser(ctx, uid[u]); err != nil || st != fence.Lost {
			t.Errorf("other pod, %s: %v %v; want Lost", u, st, err)
		}
	}
	if _, st, err := a.FenceUser(ctx, uid["fu-novehicle"]); err != nil || st != fence.Lost {
		t.Errorf("user with no vehicle: %v %v; want Lost (no group)", st, err)
	}
	off := leadStore(t, pool, "pod-off")
	off.SetGroupLead(false)
	if _, st, err := off.FenceUser(ctx, uid["fu-1"]); err != nil || st != fence.Off {
		t.Errorf("lead mode off: %v %v; want Off", st, err)
	}
}
