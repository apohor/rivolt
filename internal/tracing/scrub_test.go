package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// A server span reaches the exporter with none of the visitor's address, port or user agent; a client span (a call to Rivian) is
// untouched. Run through the real otelhttp handler, so a version bump that renames the attributes fails here.
func TestServerSpansLoseTheVisitorsAddressAndUserAgent(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(scrubServerSpans{mem}))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(upstream.Close)
	client := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(tp))}
	h := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := client.Get(upstream.URL) // a client span, as the Rivian calls make
		if err == nil {
			resp.Body.Close()
		}
		w.WriteHeader(http.StatusNoContent)
	}), "http.server", otelhttp.WithTracerProvider(tp))

	req := httptest.NewRequest(http.MethodGet, "http://rivolt.example/api/state", nil)
	req.RemoteAddr = "203.0.113.77:51234"
	req.Header.Set("User-Agent", "Mozilla/5.0 (a visitor's browser)")
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var servers, clients int
	for _, sp := range mem.GetSpans() {
		switch sp.SpanKind {
		case trace.SpanKindServer:
			servers++
			for _, a := range sp.Attributes {
				if personalServerAttrs[a.Key] || a.Value.String() == "203.0.113.77" || a.Value.String() == "Mozilla/5.0 (a visitor's browser)" {
					t.Errorf("server span still carries %s=%s", a.Key, a.Value.String())
				}
			}
			if len(sp.Attributes) == 0 {
				t.Error("the server span lost all its attributes: only the personal ones may go")
			}
		case trace.SpanKindClient:
			clients++
			if len(sp.Attributes) == 0 {
				t.Error("a client span lost its attributes")
			}
		}
	}
	if servers != 1 || clients != 1 {
		t.Fatalf("got %d server and %d client spans, want 1 and 1", servers, clients)
	}
}

// The control: without the wrapper, otelhttp does put the address and user agent on the server span, so the test above can fail.
func TestServerSpansCarryThemWithoutTheWrapper(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(mem))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	h := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }), "http.server", otelhttp.WithTracerProvider(tp))
	req := httptest.NewRequest(http.MethodGet, "http://rivolt.example/api/state", nil)
	req.RemoteAddr = "203.0.113.77:51234"
	req.Header.Set("User-Agent", "Mozilla/5.0 (a visitor's browser)")
	h.ServeHTTP(httptest.NewRecorder(), req)
	found := false
	for _, sp := range mem.GetSpans() {
		for _, a := range sp.Attributes {
			if personalServerAttrs[a.Key] {
				found = true
			}
		}
	}
	if !found {
		t.Error("control failed: otelhttp no longer sets any personal attribute on a server span (update personalServerAttrs, or drop the scrub)")
	}
}
