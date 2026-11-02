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
	const advice = " Check the tire and follow your truck's warning."
	p := TireLowPayload("R1S", []string{"fl"}, nil)
	if p.Title != "Low tire pressure" || p.Body != "R1S reports low tire pressure: front left."+advice ||
		p.Kind != "tire_low" || p.URL != "/" || p.Tag != "rivolt-tire-low" {
		t.Errorf("payload = %+v", p)
	}
	if b := TireLowPayload("", []string{"fl", "rr"}, nil).Body; b != "Your vehicle reports low tire pressure: front left and rear right."+advice {
		t.Errorf("no model: %q", b)
	}
	// A puncture warning is named as one, saying only what the truck reports.
	pp := TireLowPayload("R1S", nil, []string{"fl"})
	if pp.Title != "Possible puncture" || pp.Body != "R1S reports a possible puncture: front left."+advice {
		t.Errorf("puncture = %+v", pp)
	}
	mixed := TireLowPayload("R1S", []string{"rr"}, []string{"fl"})
	if mixed.Title != "Tire warning" || mixed.Body != "R1S reports low tire pressure: rear right. R1S reports a possible puncture: front left."+advice {
		t.Errorf("mixed = %+v", mixed)
	}
	// No figure, no place, no verdict and no advice beyond the truck's own warning, whatever the wheels.
	for _, c := range [][2][]string{{{"fl"}, nil}, {{"fl", "fr", "rl", "rr"}, nil}, {nil, {"fr"}}, {{"fl"}, {"rr"}}, {nil, nil}} {
		pl := TireLowPayload("Truck", c[0], c[1])
		b := strings.ToLower(pl.Body + " " + pl.Title)
		for _, bad := range []string{"safe", "unsafe", "danger", "psi", "bar", "inflate", "drive", "immediately", "home", "location", "flat", "fine", "ok"} {
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
