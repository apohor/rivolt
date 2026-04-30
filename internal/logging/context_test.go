package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestContextHandler_StampsFieldsFromContext is the load-bearing
// contract for this package: every record produced while a context
// carries request-scoped values gets those values as top-level
// attributes — so Loki/Grafana can filter by `user_id` etc. without
// any callsite changes in internal/* packages.
func TestContextHandler_StampsFieldsFromContext(t *testing.T) {
	var buf bytes.Buffer
	h := NewContextHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	uid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-abc")
	ctx = WithUserID(ctx, uid)
	ctx = WithVehicleID(ctx, "VIN1234")
	ctx = WithTraceID(ctx, "trace-xyz")

	logger.InfoContext(ctx, "hello", "k", "v")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nout=%s", err, buf.String())
	}
	if got["request_id"] != "req-abc" {
		t.Errorf("request_id: %v", got["request_id"])
	}
	if got["user_id"] != uid.String() {
		t.Errorf("user_id: %v", got["user_id"])
	}
	if got["vehicle_id"] != "VIN1234" {
		t.Errorf("vehicle_id: %v", got["vehicle_id"])
	}
	if got["trace_id"] != "trace-xyz" {
		t.Errorf("trace_id: %v", got["trace_id"])
	}
	if got["msg"] != "hello" {
		t.Errorf("msg: %v", got["msg"])
	}
	if got["k"] != "v" {
		t.Errorf("k: %v", got["k"])
	}
}

// TestContextHandler_OmitsUnsetFields guards against logs filling
// up with empty-string keys when fields are absent (e.g. background
// goroutines, pre-auth requests).
func TestContextHandler_OmitsUnsetFields(t *testing.T) {
	var buf bytes.Buffer
	h := NewContextHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	logger.InfoContext(context.Background(), "no-context")

	out := buf.String()
	for _, k := range []string{`"request_id"`, `"user_id"`, `"vehicle_id"`, `"trace_id"`} {
		if strings.Contains(out, k) {
			t.Errorf("expected %s absent, got: %s", k, out)
		}
	}
}

// TestContextHandler_NilUUIDIsUnset is a regression test for the
// uuid.Nil sentinel handling in WithUserID. A pre-auth handler
// helpfully calling auth.WithUser(ctx, uuid.Nil) (e.g. when the
// session lookup returns no user) must not emit `"user_id":""` —
// that would corrupt log filtering.
func TestContextHandler_NilUUIDIsUnset(t *testing.T) {
	ctx := WithUserID(context.Background(), uuid.Nil)
	if uid := UserIDFromContext(ctx); uid != uuid.Nil {
		t.Errorf("expected nil uuid, got %s", uid)
	}
}
