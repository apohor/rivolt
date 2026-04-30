// Package tracing wires the OpenTelemetry trace SDK with an OTLP/HTTP
// exporter pointed at Grafana Tempo.
//
// Configuration is env-only — there's exactly one production
// deployment and one happy path, so a config struct would be
// ceremony:
//
//	RIVOLT_OTEL_ENABLED      = "true" | "false"   (default: false)
//	RIVOLT_OTEL_ENDPOINT     = host:port           (e.g. tempo.observability:4318)
//	RIVOLT_OTEL_INSECURE     = "true" | "false"   (default: true — in-cluster)
//	RIVOLT_OTEL_SAMPLE_RATIO = float in [0,1]      (default: 1.0 at low scale)
//	RIVOLT_OTEL_SERVICE_NAME = string              (default: "rivolt")
//
// At 1000 vehicles we'll dial the sample ratio down; today every
// trace lands in Tempo, which makes the "is the wiring even right?"
// question trivially answerable in Grafana.
//
// Init is a no-op (returns a no-op shutdown) when disabled, so the
// rest of the codebase can call otel.Tracer(...) unconditionally —
// spans created against the global no-op TracerProvider are free.
package tracing

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes pending spans and tears down the exporter.
// Always non-nil; safe to call when tracing is disabled.
type ShutdownFunc func(ctx context.Context) error

// Init reads RIVOLT_OTEL_* env vars and, if enabled, installs a
// global TracerProvider with an OTLP/HTTP exporter. Returns a
// shutdown closure the caller MUST defer-call before process exit
// so buffered spans aren't dropped.
//
// Returns (noopShutdown, nil) when RIVOLT_OTEL_ENABLED is unset or
// false — the global provider stays the no-op default.
func Init(ctx context.Context, version string) (ShutdownFunc, error) {
	if !envBool("RIVOLT_OTEL_ENABLED", false) {
		return func(context.Context) error { return nil }, nil
	}

	endpoint := os.Getenv("RIVOLT_OTEL_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("tracing: RIVOLT_OTEL_ENABLED=true but RIVOLT_OTEL_ENDPOINT is unset")
	}

	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if envBool("RIVOLT_OTEL_INSECURE", true) {
		// In-cluster traffic to Tempo's OTLP receiver is plaintext
		// by default — there's no Ingress in front. Flip to false
		// once we move Tempo behind a service mesh with mTLS.
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("tracing: build exporter: %w", err)
	}

	serviceName := os.Getenv("RIVOLT_OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "rivolt"
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES if set
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		// Resource construction failures are usually env-attr parse
		// errors — fall back to a minimal resource rather than
		// nuking the boot path.
		res = resource.NewSchemaless(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		)
	}

	ratio := envFloat("RIVOLT_OTEL_SAMPLE_RATIO", 1.0)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			// Batch small + flush often: 1000 vehicles is low scale
			// and operator UX of "did my trace show up?" is way
			// more valuable than throughput today.
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	// W3C TraceContext for cross-service propagation, Baggage so
	// per-request user/vehicle attributes propagate through outbound
	// HTTP. Both must be set explicitly — otel.GetTextMapPropagator
	// defaults to a no-op composite which silently drops headers.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(sctx context.Context) error {
		// Force a final flush before Shutdown — Shutdown alone has
		// best-effort semantics on the batcher.
		_ = tp.ForceFlush(sctx)
		return tp.Shutdown(sctx)
	}
	return shutdown, nil
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
