package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestOpenRequestSide(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sys := new(sql.DB)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	// Unset: the request pool IS the system pool, and closing it is a no-op.
	got, closeFn, err := openRequestSide(context.Background(), env(nil), sys, nil, log)
	if err != nil || got != sys || closeFn == nil {
		t.Fatalf("unset: %v %v %v", got == sys, closeFn != nil, err)
	}
	closeFn()

	// A URL without the role it connects as is refused, never a quiet fall back.
	got, _, err = openRequestSide(context.Background(), env(map[string]string{"RIVOLT_REQUEST_DATABASE_URL": "postgres://x"}), sys, nil, log)
	if err == nil || got != nil || !strings.Contains(err.Error(), "RIVOLT_APP_ROLE") {
		t.Fatalf("url without role: %v %v", got, err)
	}
}
