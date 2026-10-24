package api

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The iPhone's fixture for the sleep-health answer is the server's own shape: it decodes into the server's type with no field unknown,
// and the type's JSON has no field the fixture lacks (generated_at aside, which the app doesn't read).
func TestSleepHealthMatchesTheIOSFixture(t *testing.T) {
	raw, err := os.ReadFile("../../ios/RivoltTests/Fixtures/sleep_health.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var h sleepHealthResponse
	if err := dec.Decode(&h); err != nil {
		t.Fatalf("the fixture is not the server's shape: %v", err)
	}
	if h.State != "more" || h.ThisWeek.FailedSleeps != 9 || h.MoreBy == nil || *h.MoreBy != 4 || h.UsualFailedSleeps == nil || *h.UsualFailedSleeps != 5 {
		t.Errorf("%+v", h)
	}
	// Every key the server can send for a "more" week is in the fixture.
	out, _ := json.Marshal(h)
	var got, want map[string]any
	if json.Unmarshal(out, &got) != nil || json.Unmarshal(raw, &want) != nil {
		t.Fatal("not json")
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("the server sends %q and the fixture lacks it", k)
		}
	}
}
