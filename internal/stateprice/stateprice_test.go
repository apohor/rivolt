package stateprice

import (
	"math"
	"testing"
	"time"
)

func TestTableShape(t *testing.T) {
	if Len() != 51 {
		t.Fatalf("%d entries, want 50 states and DC", Len())
	}
	names := map[string]bool{}
	for code, e := range byCode {
		if len(code) != 2 || code != toUpper(code) {
			t.Errorf("bad code %q", code)
		}
		if names[e.name] {
			t.Errorf("duplicate name %q", e.name)
		}
		names[e.name] = true
		// A residential average between 8 and 60 cents catches a dropped decimal point or a column mix-up.
		if e.cents < 8 || e.cents > 60 {
			t.Errorf("%s: %v cents is out of range", code, e.cents)
		}
	}
	for _, d := range []string{Table.Released, Table.Checked} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			t.Errorf("date %q: %v", d, err)
		}
	}
	if Table.Period == "" || Table.URL == "" || Table.Name == "" {
		t.Error("the source is not fully described")
	}
}

func toUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestLookup(t *testing.T) {
	for _, in := range []string{"CA", "ca", " California ", "CALIFORNIA"} {
		p, ok := Lookup(in)
		if !ok || p.State != "CA" || p.Name != "California" || p.Currency != "USD" || math.Abs(p.PricePerKWh-0.3258) > 1e-9 {
			t.Errorf("%q: %+v %v", in, p, ok)
		}
	}
	if p, ok := Lookup("District of Columbia"); !ok || p.State != "DC" {
		t.Errorf("DC: %+v %v", p, ok)
	}
	for _, in := range []string{"", " ", "ON", "Ontario", "Puerto Rico", "XX", "C"} {
		if _, ok := Lookup(in); ok {
			t.Errorf("%q should have no default", in)
		}
	}
}
