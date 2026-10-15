package dcfcrates

import (
	"encoding/json"
	"os"
	"testing"
)

// The Go side of the shared vectors (the TS side is ux's vitest).
func TestPinVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/choose_pin_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		NetworkKey []struct {
			Parts []*string `json:"parts"`
			Want  *string   `json:"want"`
		} `json:"network_key"`
		DisplayNetwork []struct {
			In   string `json:"in"`
			Want string `json:"want"`
		} `json:"display_network"`
		ChoosePin []struct {
			Name  string    `json:"name"`
			POI   *PoiLike  `json:"poi"`
			Known *string   `json:"known"`
			IsDC  bool      `json:"isDC"`
			Want  PinChoice `json:"want"`
		} `json:"choose_pin"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.NetworkKey) == 0 || len(v.ChoosePin) == 0 {
		t.Fatal("no vectors")
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, c := range v.NetworkKey {
		parts := make([]string, len(c.Parts))
		for i, p := range c.Parts {
			parts[i] = str(p)
		}
		if got := NetworkKey(parts...); got != str(c.Want) {
			t.Errorf("NetworkKey(%q) = %q, want %q", parts, got, str(c.Want))
		}
	}
	for _, c := range v.DisplayNetwork {
		if got := DisplayNetwork(c.In); got != c.Want {
			t.Errorf("DisplayNetwork(%q) = %q, want %q", c.In, got, c.Want)
		}
	}
	for _, c := range v.ChoosePin {
		if got := ChoosePin(c.POI, str(c.Known), c.IsDC); got != c.Want {
			t.Errorf("%s: ChoosePin = %+v, want %+v", c.Name, got, c.Want)
		}
	}
}
