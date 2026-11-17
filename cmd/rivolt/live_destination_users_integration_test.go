//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/testpg"
)

// The command on a real database: show counts, a dry run writes nothing, --apply writes the flag row (updated_by ops-cli) and one finished
// audit row, add is idempotent, remove undoes it, and no output carries an id.
func TestLiveDestinationUsersOnADatabase(t *testing.T) {
	ctx := context.Background()
	pool, err := sql.Open("pgx", testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const a, b = "3f2b8c1e-5a4d-4e6f-9a0b-1c2d3e4f5a6b", "9d1c7e20-0b7a-4f0e-8a55-2f6c1f0a7b11"
	run := func(op, id string, apply bool) string {
		t.Helper()
		var out bytes.Buffer
		if err := changeLiveDestinationUsers(ctx, pool, op, id, apply, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), a) || strings.Contains(out.String(), b) {
			t.Fatalf("output carries an id: %q", out.String())
		}
		return out.String()
	}
	flagUsers := func() (int, string) {
		var raw []byte
		var by string
		if err := pool.QueryRowContext(ctx, `SELECT value, updated_by FROM flags WHERE name = 'live_destination'`).Scan(&raw, &by); err == sql.ErrNoRows {
			return 0, ""
		} else if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Users []string `json:"users"`
			All   bool     `json:"all"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		if st.All {
			t.Fatal("the flag was turned on for everyone")
		}
		return len(st.Users), by
	}
	auditRows := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM admin_audit WHERE action = 'flag.live_destination' AND outcome = 'ok' AND details->>'actor' = 'ops-cli'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if out := run("", "", false); !strings.Contains(out, "0 users") {
		t.Fatalf("show: %q", out)
	}
	if out := run("add", a, false); !strings.Contains(out, "dry run") {
		t.Fatalf("dry run: %q", out)
	}
	if n, _ := flagUsers(); n != 0 || auditRows() != 0 {
		t.Fatal("a dry run wrote")
	}
	run("add", a, true)
	if n, by := flagUsers(); n != 1 || by != "ops-cli" || auditRows() != 1 {
		t.Fatalf("after add: %d users by %q, %d audit rows", n, by, auditRows())
	}
	if out := run("add", a, true); !strings.Contains(out, "no change") || auditRows() != 1 {
		t.Fatalf("a repeat add: %q, %d audit rows", out, auditRows())
	}
	run("add", b, true)
	if n, _ := flagUsers(); n != 2 || auditRows() != 2 {
		t.Fatalf("after second add: %d users, %d audit rows", n, auditRows())
	}
	run("remove", a, true)
	if n, _ := flagUsers(); n != 1 || auditRows() != 3 {
		t.Fatalf("after remove: %d users, %d audit rows", n, auditRows())
	}
	if out := run("", "", false); !strings.Contains(out, "1 users") {
		t.Fatalf("show: %q", out)
	}
}
