package db

import "testing"

func TestSystemPoolMaxConns(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k != "RIVOLT_DB_MAX_CONNS" {
				t.Fatalf("read %s", k)
			}
			return v
		}
	}
	for _, c := range []struct {
		in   string
		want int
	}{
		{"", 10}, // the default is 10 per pod, no longer 20
		{"6", 6},
		{" 12 ", 12},
		{"100", 100},
		{"0", 10},
		{"101", 10},
		{"-3", 10},
		{"many", 10},
	} {
		if got := SystemPoolMaxConns(env(c.in)); got != c.want {
			t.Errorf("RIVOLT_DB_MAX_CONNS=%q -> %d, want %d", c.in, got, c.want)
		}
	}
	if DefaultSystemMaxConns != 10 {
		t.Errorf("DefaultSystemMaxConns = %d, want 10", DefaultSystemMaxConns)
	}
}
