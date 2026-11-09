package main

import (
	"os"
	"strings"
	"testing"
)

// The HTTP drain starts before the flush and the lease release and is waited for after them, so a request in flight does not delay the
// hand-off of the vehicles; and the readiness probe fails from the start of the drain. Read from main.go, like the registry wiring test.
func TestMainDrainsHTTPAlongsideTheLeaseRelease(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	idx := func(s string) int {
		i := strings.Index(src, s)
		if i < 0 {
			t.Fatalf("main.go no longer contains %q: update this test with the shutdown code", s)
		}
		return i
	}
	start, flush, lease, wait := idx("httpDone <- srv.Shutdown(shutdownCtx)"), idx("monitorRegistry.FlushPending(fctx)"), idx("leaseCoordinator.Shutdown(lctx)"), idx("<-httpDone")
	if !(start < flush && start < lease && lease < wait) {
		t.Errorf("shutdown order: the HTTP drain must start (%d) before the flush (%d) and the lease release (%d) and be waited for (%d) after them", start, flush, lease, wait)
	}
	if i := idx("api.StartDrain()"); i < 0 {
		t.Error("api.StartDrain is not called")
	}
	if strings.Count(src, "api.StartDrain()") < 2 {
		t.Error("api.StartDrain must be called both from the preStop drain hook and at SIGTERM")
	}
}
