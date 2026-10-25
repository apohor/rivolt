package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// personalServerAttrs are the span attributes that identify a visitor: the client's address and port, the peer's address and port, and
// the browser or app string. otelhttp puts them on every SERVER span (the legacy names and the current ones, depending on the
// instrumentation version). The privacy policy does not disclose traces holding visitors' addresses and user agents, and behind some
// proxies these are the real client address, so they never leave the process. Client spans (the calls Rivolt makes to Rivian and
// others) are not touched: their peer is a server, not a visitor.
var personalServerAttrs = map[attribute.Key]bool{
	"client.address":       true,
	"client.port":          true,
	"network.peer.address": true,
	"network.peer.port":    true,
	"user_agent.original":  true,
	// Older semantic conventions, which a version bump of otelhttp could bring back.
	"http.client_ip":     true,
	"http.user_agent":    true,
	"net.peer.ip":        true,
	"net.peer.port":      true,
	"net.sock.peer.addr": true,
	"net.sock.peer.port": true,
}

// scrubServerSpans wraps the span exporter and removes personalServerAttrs from server spans on the way out.
type scrubServerSpans struct{ sdktrace.SpanExporter }

func (s scrubServerSpans) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return s.SpanExporter.ExportSpans(ctx, scrubbed(spans))
}

// scrubbed returns spans with the personal attributes removed from every server span. A span with none is passed through as is.
func scrubbed(spans []sdktrace.ReadOnlySpan) []sdktrace.ReadOnlySpan {
	var stubs tracetest.SpanStubs
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, sp := range spans {
		out[i] = sp
		if sp.SpanKind() != trace.SpanKindServer || !hasPersonal(sp.Attributes()) {
			continue
		}
		stub := tracetest.SpanStubFromReadOnlySpan(sp)
		kept := stub.Attributes[:0:0]
		for _, a := range stub.Attributes {
			if !personalServerAttrs[a.Key] {
				kept = append(kept, a)
			}
		}
		stub.Attributes = kept
		stubs = append(stubs[:0], stub)
		out[i] = stubs.Snapshots()[0]
	}
	return out
}

func hasPersonal(attrs []attribute.KeyValue) bool {
	for _, a := range attrs {
		if personalServerAttrs[a.Key] {
			return true
		}
	}
	return false
}
