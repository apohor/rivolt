package weather

import "testing"

func TestHeadwindSignsAgainstBearing(t *testing.T) {
	// Wind direction is where the wind comes FROM: a north wind (0) on a south-bound drive (180) pushes from behind.
	if hw := Headwind(20, 0, 180); hw > -19.9 {
		t.Errorf("north wind, south-bound = %.1f, want about -20 (tailwind)", hw)
	}
	if hw := Headwind(20, 0, 0); hw < 19.9 {
		t.Errorf("north wind, north-bound = %.1f, want about +20 (headwind)", hw)
	}
	if hw := Headwind(20, 90, 0); hw > 0.01 || hw < -0.01 {
		t.Errorf("east wind, north-bound = %.3f, want about 0 (crosswind)", hw)
	}
}

func TestWindRelation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hw, wind float64
		label    string
		wantKPH  float64
	}{
		{"north wind south-bound", Headwind(30, 0, 180), 30, "tailwind", 30},
		{"south wind south-bound", Headwind(30, 180, 180), 30, "headwind", 30},
		{"east wind north-bound", Headwind(30, 90, 0), 30, "crosswind", 0},
		{"slightly off square", Headwind(30, 100, 0), 30, "crosswind", 0},
		{"light breeze along track", 2, 2, "crosswind", 0},
		{"45 degrees", Headwind(30, 45, 0), 30, "headwind", 21.2},
	} {
		label, kph := WindRelation(tc.hw, tc.wind)
		if label != tc.label || kph < tc.wantKPH-0.1 || kph > tc.wantKPH+0.1 {
			t.Errorf("%s: got %s %.1f, want %s %.1f", tc.name, label, kph, tc.label, tc.wantKPH)
		}
	}
}
