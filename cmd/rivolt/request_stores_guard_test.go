package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The request-side stores are built in one place, api.NewRequestStores, which the pool-on integration tests use too. A store built
// by hand on requestDB in main would be one CI never exercises the way production wires it (this already drifted once: push, secrets,
// pack health and the AI budget). The one hand-built exception is the telemetry archive reader, which needs the archive config.
func TestRequestStoresAreBuiltInOnePlace(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "api.NewRequestStores(requestDB, pgPool, sealer)") {
		t.Error("main.go must build the request-side stores with api.NewRequestStores(requestDB, pgPool, sealer)")
	}
	byHand := regexp.MustCompile(`\b\w+\.(New\w*)\(requestDB\b`)
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		if m := byHand.FindString(line); m != "" && !strings.Contains(m, "archive.NewReader") && !strings.Contains(m, "api.NewRequestStores") {
			t.Errorf("main.go builds a store on requestDB by hand (%q): add it to api.NewRequestStores so CI exercises what production wires", strings.TrimSpace(m))
		}
	}
}
