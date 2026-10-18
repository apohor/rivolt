package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every write route in the admin block of api.go carries the audit
// middleware, so a new admin write cannot ship unrecorded. The raw Rivian
// probes (disabled by default in a separate change) and the AI ping are the
// only exemptions; they write nothing here.
func TestEveryAdminWriteRouteIsAudited(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, `r.Route("/admin"`)
	if start < 0 {
		t.Fatal("admin route block not found")
	}
	block := text[start:]
	if end := strings.Index(block, "\n\t\t\t})\n"); end > 0 {
		block = block[:end]
	}
	exempt := map[string]bool{"/trips/plan/raw": true, "/gql/raw": true, "/ai/ping": true}
	// A write route is `.Put("/x"`, `.Post("/x"` or `.Delete("/x"`; audited when the
	// statement starts with r.With(audit(.
	re := regexp.MustCompile(`(?m)^(\s*)(r\.With\(audit\([\s\S]*?\)\)\.\s*|r\.)(Put|Post|Delete)\("([^"]+)"`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		path, audited := m[4], strings.Contains(m[2], "audit(")
		found++
		if !audited && !exempt[path] {
			t.Errorf("admin write route %s %s is not audited", m[3], path)
		}
	}
	if found < 18 {
		t.Errorf("only %d admin write routes found; the scan is broken", found)
	}
}
