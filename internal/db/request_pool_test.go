package db

import "testing"

func TestRequestPoolMaxConns(t *testing.T) {
	for v, want := range map[string]int32{"": DefaultRequestMaxConns, "x": DefaultRequestMaxConns, "0": DefaultRequestMaxConns, "-3": DefaultRequestMaxConns, "101": DefaultRequestMaxConns, "1": 1, " 25 ": 25, "100": 100} {
		if got := RequestPoolMaxConns(func(string) string { return v }); got != want {
			t.Errorf("RIVOLT_REQUEST_DB_MAX_CONNS=%q: %d, want %d", v, got, want)
		}
	}
	if DefaultRequestMaxConns < 1 {
		t.Fatal("default out of range")
	}
}
