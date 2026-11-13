package api

import (
	"os"
	"strings"
	"testing"
)

// Both operator-key LLM routes must carry the taste middleware and map its errors; a route added later without it would spend
// the operator key for free.
func TestLLMRoutesUseTheTasteMiddleware(t *testing.T) {
	api, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{`"/drives/{id}/efficiency"`, `"/trips/plan/advice"`} {
		i := strings.Index(string(api), ".Post("+route)
		if i < 0 {
			t.Fatalf("route %s not found", route)
		}
		start := strings.LastIndex(string(api)[:i], "\n") + 1
		line := string(api)[start:i]
		if !strings.Contains(line, "aiTaste") {
			t.Errorf("%s is not behind the taste middleware: %s", route, line)
		}
	}
	for _, f := range []string{"drive_efficiency.go", "tripplan.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "writeAITasteError(") {
			t.Errorf("%s does not map taste errors", f)
		}
	}
}
