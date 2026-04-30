// Package logging provides a context-aware slog handler and helpers
// for stamping per-request fields (request_id, user_id, vehicle_id,
// trace_id) onto every log line emitted while serving a request,
// without requiring callers to thread the logger through.
//
// Mechanism: ContextHandler wraps any underlying slog.Handler and on
// every Handle() pulls registered values out of context.Context. The
// fields are added as top-level attributes on the record, so the
// existing slog.JSONHandler output gains them transparently.
//
// HTTP middleware sets request_id (from chi) and is followed by the
// auth middleware (user_id) and vehicle-ownership middleware
// (vehicle_id). Background goroutines pass nil/Background context
// and simply emit log lines without those fields — no harm.
package logging

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

// ctxKey is unexported so callers must use the With* / *FromContext
// helpers below to interact with values.
type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyUserID
	keyVehicleID
	keyTraceID
)

// WithRequestID returns a context that carries the given request ID.
// Empty values are not stored (avoids polluting logs with empty keys).
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, keyRequestID, id)
}

// WithUserID returns a context that carries the given user UUID.
// uuid.Nil is treated as unset.
func WithUserID(ctx context.Context, uid uuid.UUID) context.Context {
	if uid == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, keyUserID, uid)
}

// WithVehicleID returns a context that carries the given Rivian
// vehicle ID. Empty values are not stored.
func WithVehicleID(ctx context.Context, vid string) context.Context {
	if vid == "" {
		return ctx
	}
	return context.WithValue(ctx, keyVehicleID, vid)
}

// WithTraceID returns a context that carries the given trace ID.
// Reserved for OpenTelemetry wire-up later; currently nothing sets
// it but ContextHandler will surface it whenever it appears.
func WithTraceID(ctx context.Context, tid string) context.Context {
	if tid == "" {
		return ctx
	}
	return context.WithValue(ctx, keyTraceID, tid)
}

// RequestIDFromContext returns the request ID, or "" if unset.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(keyRequestID).(string); ok {
		return v
	}
	return ""
}

// UserIDFromContext returns the user ID, or uuid.Nil if unset.
func UserIDFromContext(ctx context.Context) uuid.UUID {
	if v, ok := ctx.Value(keyUserID).(uuid.UUID); ok {
		return v
	}
	return uuid.Nil
}

// VehicleIDFromContext returns the vehicle ID, or "" if unset.
func VehicleIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(keyVehicleID).(string); ok {
		return v
	}
	return ""
}

// TraceIDFromContext returns the trace ID, or "" if unset.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(keyTraceID).(string); ok {
		return v
	}
	return ""
}

// ContextHandler is a slog.Handler that decorates each record with
// request-scoped fields pulled from context.Context. It delegates
// formatting and IO to an inner handler.
type ContextHandler struct {
	inner slog.Handler
}

// NewContextHandler wraps the given handler. Pass slog.NewJSONHandler
// or slog.NewTextHandler as inner depending on RIVOLT_LOG_FORMAT.
func NewContextHandler(inner slog.Handler) *ContextHandler {
	return &ContextHandler{inner: inner}
}

// Enabled defers to the inner handler.
func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle decorates the record with context-scoped attributes and
// then delegates to the inner handler.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if rid := RequestIDFromContext(ctx); rid != "" {
		r.AddAttrs(slog.String("request_id", rid))
	}
	if uid := UserIDFromContext(ctx); uid != uuid.Nil {
		r.AddAttrs(slog.String("user_id", uid.String()))
	}
	if vid := VehicleIDFromContext(ctx); vid != "" {
		r.AddAttrs(slog.String("vehicle_id", vid))
	}
	// trace_id resolution order: explicit WithTraceID value first
	// (callers that synthesise a trace ID without OTel can still
	// stamp it), then the active OTel span. Either way the trace_id
	// in Loki matches the trace_id in Tempo so Grafana's
	// "log → trace" jump works without translation.
	if tid := TraceIDFromContext(ctx); tid != "" {
		r.AddAttrs(slog.String("trace_id", tid))
	} else if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
		r.AddAttrs(slog.String("span_id", sc.SpanID().String()))
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new ContextHandler whose inner handler has the
// given attrs pre-applied.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup returns a new ContextHandler whose inner handler is in
// the given group. Note: context-derived attrs are still emitted at
// the record's top level, not under the group — request_id and
// friends should always be addressable by exact key in Loki/Grafana
// regardless of how nested the call site is.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{inner: h.inner.WithGroup(name)}
}
