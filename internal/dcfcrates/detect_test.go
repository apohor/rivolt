package dcfcrates

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tesla := func(d, kw float64) Candidate {
		return Candidate{Name: "Tesla Supercharger Some Town", Network: "TESLA", DistM: d, MaxPowerKW: kw}
	}
	ran := func(d, kw float64) Candidate {
		return Candidate{Name: "Rivian Adventure Network - Some Town", Network: "RIVIAN_ADVENTURE", DistM: d, MaxPowerKW: kw}
	}
	ea := func(d, kw float64) Candidate {
		return Candidate{Name: "Walmart 1234", Network: "Electrify America", Operator: "https://www.electrifyamerica.com", DistM: d, MaxPowerKW: kw}
	}
	cases := []struct {
		name    string
		in      DetectInput
		network string
		state   string
		conf    float64
		source  string
	}{
		{"isRivianCharger wins, even at a Supercharger", DetectInput{RivianCharger: true, Candidates: []Candidate{tesla(20, 250)}},
			"Rivian Adventure Network", StateEstimated, 0.95, "rivian_charger"},
		{"the user's own verified charge at this site", DetectInput{Site: &SiteMemory{Network: "Electrify America", DistM: 40}, Candidates: []Candidate{tesla(30, 250), ea(90, 350)}},
			"Electrify America", StateEstimated, 0.9, "site_memory"},
		{"site memory too far away doesn't count", DetectInput{Site: &SiteMemory{Network: "Electrify America", DistM: 140}, Candidates: []Candidate{tesla(30, 250)}},
			"Tesla Supercharger", StateEstimated, 0.9, "archive"},
		{"one network close by", DetectInput{Candidates: []Candidate{tesla(38, 250), tesla(60, 250)}},
			"Tesla Supercharger", StateEstimated, 0.9, "archive"},
		{"one network, farther", DetectInput{Candidates: []Candidate{ea(200, 350)}},
			"Electrify America", StateNeedsConfirmation, 0.7, "archive"},
		{"co-located: nearest, asked", DetectInput{Candidates: []Candidate{tesla(112, 250), ran(40, 0)}},
			"Rivian Adventure Network", StateNeedsConfirmation, 0.4, "archive"},
		{"co-located: a peak only one could deliver picks it, still asked", DetectInput{Candidates: []Candidate{tesla(40, 250), ea(120, 350)}, PeakKW: 330},
			"Electrify America", StateNeedsConfirmation, 0.6, "archive"},
		{"co-located: unknown maxima never rule a network out", DetectInput{Candidates: []Candidate{tesla(40, 0), ea(120, 350)}, PeakKW: 330},
			"Tesla Supercharger", StateNeedsConfirmation, 0.4, "archive"},
		{"unrecognised network", DetectInput{Candidates: []Candidate{{Name: "Blink DC", Network: "Blink Network", DistM: 30}}},
			"Unknown DCFC", StateNeedsConfirmation, 0.5, "archive"},
		{"node whose name names another network", DetectInput{Candidates: []Candidate{{Name: "Tesla Supercharger", Network: "ChargePoint Network", DistM: 30}}},
			"", StateNeedsConfirmation, 0.6, "archive"},
		{"nothing near", DetectInput{Candidates: []Candidate{tesla(400, 250)}},
			"", StateNeedsConfirmation, 0, "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Detect(c.in, "open")
			if (c.network != "" && d.Network != c.network) || d.State != c.state || d.Confidence != c.conf {
				t.Errorf("got %q %s %.2f, want %q %s %.2f", d.Network, d.State, d.Confidence, c.network, c.state, c.conf)
			}
			var ev struct {
				V          int    `json:"v"`
				Source     string `json:"source"`
				Pos        string `json:"pos"`
				Candidates []struct {
					Network string  `json:"network"`
					DistM   float64 `json:"dist_m"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal(d.Evidence, &ev); err != nil || ev.V != 1 || ev.Source != c.source || ev.Pos != "open" {
				t.Errorf("evidence %s (%v), want source %s", d.Evidence, err, c.source)
			}
			if c.source == "none" && d.Query != "" {
				t.Errorf("nothing matched but query %q", d.Query)
			}
			if c.source != "none" && d.Query == "" {
				t.Errorf("no query to price with")
			}
		})
	}
}

// The query prices at the detected network: the user's overrides and
// memberships apply through ResolveRate as before.
func TestDetectQueryPricesTheDetectedNetwork(t *testing.T) {
	d := Detect(DetectInput{Candidates: []Candidate{{Name: "Some Plaza", Network: "SHELL_RECHARGE", DistM: 20}}}, "open")
	if rr := ResolveRate(d.Query, nil); rr.Slug != "shell_recharge" || d.State != StateEstimated {
		t.Fatalf("query %q → %s, state %s (%s)", d.Query, rr.Slug, d.State, d.Evidence)
	}
	if !strings.Contains(string(d.Evidence), `"agree":true`) {
		t.Fatalf("NREL code read as a conflict: %s", d.Evidence)
	}
}
