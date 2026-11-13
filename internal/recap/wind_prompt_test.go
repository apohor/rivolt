package recap

import (
	"strings"
	"testing"
)

// The model is handed a signed, labelled wind, never raw direction maths alone.
func TestPromptsLabelWindAgainstTheDrive(t *testing.T) {
	w := func(hw float64) *Weather {
		return &Weather{WindKPH: 30, HasWind: true, WindDirDeg: 0, HeadwindKPH: hw, HasHeadwind: true}
	}
	for _, tc := range []struct {
		name         string
		hw           float64
		recapWant    string
		effWant      string
		effNotWanted string
	}{
		{"tailwind", -30, "tailwind_kph: 30", "19 mph tailwind", "headwind"},
		{"headwind", 20, "headwind_kph: 20", "12 mph headwind", "tailwind"},
		{"crosswind", 1, "wind_vs_drive: crosswind", "crosswind", "headwind"},
	} {
		_, user := buildPrompt(Inputs{Weather: w(tc.hw)})
		if !strings.Contains(user, tc.recapWant) || !strings.Contains(user, "wind_from_deg: 0") {
			t.Errorf("recap %s: want %q and the raw direction in:\n%s", tc.name, tc.recapWant, user)
		}
		_, user = buildEfficiencyPrompt(EfficiencyInputs{Weather: w(tc.hw)})
		if !strings.Contains(user, tc.effWant) || strings.Contains(user, tc.effNotWanted) {
			t.Errorf("efficiency %s: want %q, not %q in:\n%s", tc.name, tc.effWant, tc.effNotWanted, user)
		}
	}
}

// Calm air names no direction, even when it lies along the road.
func TestPromptsCallLightWindLight(t *testing.T) {
	w := &Weather{WindKPH: 2, HasWind: true, HeadwindKPH: 2, HasHeadwind: true}
	_, user := buildPrompt(Inputs{Weather: w})
	if !strings.Contains(user, "wind_vs_drive: light") || strings.Contains(user, "crosswind") || strings.Contains(user, "headwind") {
		t.Errorf("recap:\n%s", user)
	}
	_, user = buildEfficiencyPrompt(EfficiencyInputs{Weather: w})
	if strings.Contains(user, "crosswind") || strings.Contains(user, "headwind") || strings.Contains(user, "tailwind") {
		t.Errorf("efficiency:\n%s", user)
	}
}
