package fence

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestRowJSON(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	doc, err := RowJSON(
		[]string{"a", "b", "c", "d", "e", "f", "g"},
		[]any{sql.NullFloat64{Float64: 1.5, Valid: true}, sql.NullFloat64{}, sql.NullString{String: "x", Valid: true}, []byte(`{"v":1}`), at, true, nil})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatalf("not JSON: %v: %s", err, doc)
	}
	if got["a"] != 1.5 || got["b"] != nil || got["c"] != "x" || got["f"] != true || got["g"] != nil {
		t.Errorf("values: %v", got)
	}
	if m, ok := got["d"].(map[string]any); !ok || m["v"] != float64(1) {
		t.Errorf("a []byte must embed as a JSON document, got %v", got["d"])
	}
	if got["e"] != "2026-10-30T12:00:00Z" {
		t.Errorf("time: %v", got["e"])
	}
	if _, err := RowJSON([]string{"a"}, nil); err == nil {
		t.Error("a column/value count mismatch must fail")
	}
}
