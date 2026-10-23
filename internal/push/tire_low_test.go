package push

import (
	"strings"
	"testing"
)

func TestJoinWheels(t *testing.T) {
	for in, want := range map[string]string{
		"fl":          "front left",
		"rr":          "rear right",
		"fl,rr":       "front left and rear right",
		"rr,fl":       "front left and rear right", // listed in a fixed order whatever the input
		"fl,fr,rr":    "front left, front right and rear right",
		"fl,fr,rl,rr": "front left, front right, rear left and rear right",
		"":            "a tire",
		"spare,x":     "a tire", // not a wheel we name
		"fl,fl":       "front left",
	} {
		var pos []string
		if in != "" {
			pos = strings.Split(in, ",")
		}
		if got := JoinWheels(pos); got != want {
			t.Errorf("JoinWheels(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTireLowPayloadRepeatsTheTruckAndNothingElse(t *testing.T) {
	p := TireLowPayload("R1S", []string{"fl"})
	if p.Title != "Low tire pressure" || p.Body != "R1S reports low tire pressure: front left." ||
		p.Kind != "tire_low" || p.URL != "/" || p.Tag != "rivolt-tire-low" {
		t.Errorf("payload = %+v", p)
	}
	if b := TireLowPayload("", []string{"fl", "rr"}).Body; b != "Your vehicle reports low tire pressure: front left and rear right." {
		t.Errorf("no model: %q", b)
	}
	// No figure, no place, no verdict and no advice, whatever the wheels.
	for _, w := range [][]string{{"fl"}, {"fl", "fr", "rl", "rr"}, nil} {
		b := strings.ToLower(TireLowPayload("Truck", w).Body + " " + TireLowPayload("Truck", w).Title)
		for _, bad := range []string{"safe", "unsafe", "danger", "psi", "bar", "inflate", "check your", "should", "home", "location", "flat"} {
			if strings.Contains(b, bad) {
				t.Errorf("%q contains %q", b, bad)
			}
		}
		for _, digit := range "0123456789" {
			if strings.ContainsRune(b, digit) {
				t.Errorf("%q carries a number", b)
			}
		}
	}
}
